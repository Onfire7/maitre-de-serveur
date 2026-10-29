package convert

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// A Compose file's ${VAR} placeholders (plan C10). Those used only in a
// service's environment become settings, asked at install and filled in by
// the Node; the rest (an image tag, a host folder, a port) are resolved now
// from the file's .env and defaults, since they shape the bundle itself.

// varRe matches $VAR, ${VAR} and ${VAR<op>default}; $$ is a literal dollar.
var varRe = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-?+])([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// variable is one placeholder name as the file uses it.
type variable struct {
	name string
	// def is the default the file gives (${VAR:-def}), if any.
	def    string
	hasDef bool
	// setting: every use is in a service's environment.
	setting bool
}

// scanVars finds every placeholder in a Compose file and where it is used.
func scanVars(raw []byte) (map[string]*variable, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	vars := map[string]*variable{}
	var walk func(n *yaml.Node, path []string)
	walk = func(n *yaml.Node, path []string) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c, path)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				walk(n.Content[i], path) // keys can hold placeholders too
				walk(n.Content[i+1], append(append([]string{}, path...), n.Content[i].Value))
			}
		case yaml.ScalarNode:
			inEnv := len(path) >= 3 && path[0] == "services" && path[2] == "environment"
			for _, m := range varRe.FindAllStringSubmatch(n.Value, -1) {
				if m[0] == "$$" {
					continue
				}
				name := m[1] + m[4]
				v := vars[name]
				if v == nil {
					v = &variable{name: name, setting: true}
					vars[name] = v
				}
				v.setting = v.setting && inEnv
				if (m[2] == "-" || m[2] == ":-") && !v.hasDef {
					v.def, v.hasDef = m[3], true
				}
			}
		}
	}
	walk(&doc, nil)
	return vars, nil
}

// token stands in for a setting while compose-go loads the file, and is
// turned into a template field afterwards.
func token(name string) string { return "__MAITRE_SETTING_" + name + "_END__" }

var tokenRe = regexp.MustCompile(`__MAITRE_SETTING_([A-Za-z_][A-Za-z0-9_]*?)_END__`)

// templated makes a value from the Compose file mean in Quadlet what it
// meant to Compose, and turns setting tokens into {{ .Settings.NAME }}.
// Quadlet copies values into the podman command line of ExecStart as they
// are, where systemd expands $VAR, ${VAR} and %-specifiers; compose-go has
// already turned Compose's $$ into $, so each $ and % is doubled for systemd
// to pass it through. {{ is escaped for the Node's Go template.
func templated(v string) string {
	v = systemdEscape.Replace(v)
	v = strings.ReplaceAll(v, "{{", `{{ "{{" }}`)
	return tokenRe.ReplaceAllString(v, "{{ .Settings.$1 }}")
}

var systemdEscape = strings.NewReplacer("$", "$$", "%", "%%")

// secretName is a setting whose value should be made up, not asked for.
var secretName = regexp.MustCompile(`(?i)pass(word)?|secret|token|(^|_)key($|_)|salt`)
