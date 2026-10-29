// Package convert turns a Compose file into a Catalog bundle (plan C10,
// docs/services-from-compose.md). compose-go loads the file and
// compose2quadlet maps it to Quadlet; this package adapts the result to
// Maître's components, volumes and settings, pins images by digest, checks
// the bundle as a Node would, and reports what it changed and what a person
// must still decide.
package convert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	c2q "github.com/Inoriol/comquad/compose2quadlet"
	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
)

// PodmanVersion is the Podman of the Node image; compose2quadlet uses every
// Quadlet key it has.
const PodmanVersion = "5.8.2"

// Options say what to convert and how.
type Options struct {
	// Compose is a Compose file, or a folder holding one (a CasaOS app).
	Compose string
	// ID is the entry's id; by default the Compose project's name.
	ID string
	// URL and Licence say where the file came from, for the manifest.
	URL, Licence string
	// Resolver pins images; Registry{} by default.
	Resolver Resolver
	// Check renders and checks the bundle as a Node does; services.Check by
	// default.
	Check func(context.Context, *maitrev1.ServiceSpec) (*services.CheckResult, error)
	// Now dates the conversion.
	Now time.Time
}

// Result is a conversion: the entry and its files, or only a report when
// something must be fixed first.
type Result struct {
	Entry *catalog.Manifest
	// Files are the bundle's files by name; nil when Report.MustFix is not
	// empty.
	Files  map[string][]byte
	Report Report
}

// composeNames are the files a folder may hold, in Compose's own order.
var composeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// Convert converts one Compose file. The error is for what stops it being
// read at all; problems in the file go in the report.
func Convert(ctx context.Context, opts Options) (*Result, error) {
	if opts.Resolver == nil {
		opts.Resolver = Registry{}
	}
	if opts.Check == nil {
		opts.Check = services.Check
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	path, err := composePath(opts.Compose)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	c := &conv{opts: opts, raw: raw, dir: dir, res: &Result{}}
	if err := c.load(ctx, path); err != nil {
		return nil, err
	}
	if len(c.res.Report.MustFix) == 0 {
		c.convert(ctx)
	}
	if len(c.res.Report.MustFix) == 0 {
		c.findProject(ctx)
		c.check(ctx)
	}
	if len(c.res.Report.MustFix) == 0 {
		c.files()
	}
	return c.res, nil
}

func composePath(p string) (string, error) {
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return filepath.Abs(p)
	}
	for _, n := range composeNames {
		if _, err := os.Stat(filepath.Join(p, n)); err == nil {
			return filepath.Abs(filepath.Join(p, n))
		}
	}
	return "", fmt.Errorf("%s holds no %s", p, strings.Join(composeNames, " or "))
}

// conv is one conversion in progress.
type conv struct {
	opts    Options
	raw     []byte
	dir     string
	project *types.Project
	vars    map[string]*variable
	dotenv  map[string]string
	casaos  map[string]any
	units   map[string]c2q.QuadletUnit // compose2quadlet's container units, by service
	images  map[string]string          // the image each service runs, before pinning
	res     *Result
	entry   catalog.Manifest
	// volumes: a Service volume per host folder or named volume, by its source.
	volumes   map[string]*volume
	settings  map[string]bool
	tz        bool
	restarted bool
	// hostMounts are the Node's own folders already reported.
	hostMounts map[string]bool
	// secrets: a password written into the file, by value, and the setting
	// that replaces it wherever that value appears.
	secrets map[string]string
}

type volume struct {
	name, path string
	users      map[string]bool
	// shared: a folder the user picks, also used by other Services
	// (decision 175), such as CasaOS's /DATA/Media.
	shared bool
}

// load reads the file with compose-go, its placeholders split into settings
// and values resolved now.
func (c *conv) load(ctx context.Context, path string) error {
	vars, err := scanVars(c.raw)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	c.vars = vars
	c.dotenv = map[string]string{}
	if envPath := filepath.Join(c.dir, ".env"); fileExists(envPath) {
		if c.dotenv, err = dotenv.Read(envPath); err != nil {
			return fmt.Errorf("%s: %w", envPath, err)
		}
	}
	// The .env's values, then a token for each setting in their place.
	vals := map[string]string{}
	for k, v := range c.dotenv {
		vals[k] = v
	}
	for _, v := range vars {
		if v.setting {
			vals[v.name] = token(v.name)
		}
	}
	// CasaOS names each app's folder after it: /DATA/AppData/$AppID/...
	if hasTopKey(c.raw, "x-casaos") {
		if _, ok := vals["AppID"]; !ok {
			id := c.opts.ID
			if id == "" {
				id = entryID(filepath.Base(c.dir))
			}
			vals["AppID"] = id
		}
	}
	env := []string{}
	for _, k := range sortedKeys(vals) {
		env = append(env, k+"="+vals[k])
	}
	po := []cli.ProjectOptionsFn{cli.WithWorkingDirectory(c.dir), cli.WithEnv(env)}
	if id := c.opts.ID; id != "" {
		po = append(po, cli.WithName(id))
	} else if !hasTopKey(c.raw, "name") {
		po = append(po, cli.WithName(entryID(filepath.Base(c.dir))))
	}
	// An env_file the file names but does not ship (a .env the user is told
	// to write) is read as empty, as if it said required: false.
	if fixed, missing := optionalEnvFiles(c.raw, c.dir); len(missing) > 0 {
		for _, m := range missing {
			c.res.Report.note("%s is not beside the Compose file; it is read as empty, and its variables become settings", m)
		}
		tmp, err := os.CreateTemp("", "maitre-compose-*.yaml")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.Write(fixed); err != nil {
			return err
		}
		tmp.Close()
		path = tmp.Name()
	}
	options, err := cli.NewProjectOptions([]string{path}, po...)
	if err != nil {
		return err
	}
	// compose-go warns through logrus (a variable not set, say); its words
	// go in the report instead of on the terminal.
	restore := captureLogs(func(msg string) { c.res.Report.note("Compose: %s", msg) })
	c.project, err = options.LoadProject(ctx)
	restore()
	if err != nil {
		c.res.Report.mustFix("Compose could not load the file: %v", err)
		return nil
	}
	if x, ok := c.project.Extensions["x-casaos"].(map[string]any); ok {
		c.casaos = x
	}
	return nil
}

// convert maps the project to an entry.
func (c *conv) convert(ctx context.Context) {
	r := &c.res.Report
	p := c.project
	c.entry = catalog.Manifest{ID: entryID(p.Name), Exposure: "private"}
	if c.opts.ID != "" {
		c.entry.ID = c.opts.ID
	}
	for _, name := range p.ServiceNames() {
		svc := p.Services[name]
		if svc.Build != nil {
			r.mustFix("%s is built from source (build:); a Catalog entry runs published images only", name)
		}
		if svc.Image == "" {
			r.mustFix("%s names no image", name)
		}
		if m := svc.NetworkMode; strings.HasPrefix(m, types.NetworkModeServicePrefix) || strings.HasPrefix(m, types.NetworkModeContainerPrefix) {
			r.mustFix("%s shares another container's network (network_mode: %s), which Maître's components do not do", name, m)
		}
		if !compRe.MatchString(name) {
			r.mustFix("%s: a service name of lowercase letters, digits and dashes is needed, since it is also the container's host name", name)
		}
	}
	if len(r.MustFix) > 0 {
		return
	}

	// compose2quadlet maps everything Compose says that Quadlet can say.
	version, _ := c2q.ParseVersion(PodmanVersion)
	units, err := c2q.Transpile(p, c2q.WithPodmanVersion(version), c2q.WithoutInstallSection(), c2q.WithoutPrefix(),
		c2q.WithoutSELinux(), c2q.WithWorkingDirectory(c.dir), c2q.WithInfo(func(s string) { r.note("compose2quadlet: %s", strings.TrimPrefix(s, "Warning: ")) }))
	if err != nil {
		r.mustFix("compose2quadlet: %v", err)
		return
	}
	c.units = map[string]c2q.QuadletUnit{}
	c.images = map[string]string{}
	for _, u := range units {
		switch u.Type {
		case c2q.UnitContainer:
			c.units[u.Name] = u
		case c2q.UnitImage:
			for _, s := range u.Sections {
				for _, d := range s.Directives {
					if d.Key == "Image" && len(d.Values) > 0 {
						c.images[u.Name] = d.Values[0]
					}
				}
			}
		}
	}

	c.volumes = map[string]*volume{}
	c.hostMounts = map[string]bool{}
	c.settings = map[string]bool{}
	for _, v := range c.vars {
		if v.setting {
			c.settings[v.name] = true
		}
	}
	internal := c.internalNetwork()
	if internal {
		c.entry.Network = "internal"
		r.changed("Every network the file declares is internal, so the Service's own network has no route out")
	}
	c.literalSecrets()
	web, port, path := c.webPage()
	// Templates come after the volumes, ports and settings they use are known.
	type built struct {
		comp catalog.Component
		body []string
	}
	var comps []built
	for _, name := range p.ServiceNames() {
		svc := p.Services[name]
		image, err := pin(ctx, c.opts.Resolver, c.imageOf(name, svc))
		if err != nil {
			r.mustFix("%s: the image could not be resolved to a digest: %v", name, err)
			continue
		}
		comp := catalog.Component{Name: name, Image: image}
		for _, dep := range sortedKeys(svc.DependsOn) {
			comp.After = append(comp.After, dep)
		}
		if internal && c.reachesOut(svc) {
			comp.Egress = true
			r.changed("%s keeps a route out, as it is also on a network that is not internal", name)
		}
		comps = append(comps, built{comp, c.template(name, svc, name == web, port)})
	}
	if len(r.MustFix) > 0 {
		return
	}
	// Health waits: depends_on with condition: service_healthy.
	healthWaits := map[string]bool{}
	for _, name := range p.ServiceNames() {
		for _, dep := range sortedKeys(p.Services[name].DependsOn) {
			switch p.Services[name].DependsOn[dep].Condition {
			case types.ServiceConditionHealthy:
				if !healthWaits[dep] {
					r.changed("%s waits for %s's health check through Notify=healthy, instead of compose2quadlet's polling loop, which would run on the Node", name, dep)
				}
				healthWaits[dep] = true
			case types.ServiceConditionCompletedSuccessfully:
				r.needsPerson("%s waits for %s to finish (service_completed_successfully); Maître's components keep running, so a one-off job like this needs another way", name, dep)
			}
		}
	}
	if c.restarted {
		r.changed("restart: policies are replaced by Maître's: each container restarts when it fails")
	}

	c.settingsAndVolumes()
	c.describe(web, port, path, len(comps))
	if c.entry.Version == "" {
		// The tag of the image serving the page, or of the only image.
		for _, b := range comps {
			if b.comp.Name == web || len(comps) == 1 {
				c.entry.Version = imageTag(b.comp.Image)
			}
		}
	}
	if len(comps) == 1 {
		// One container: the single-container layout, as the hand-written
		// entries have it.
		only := comps[0]
		c.entry.Image = only.comp.Image
		// Maître's network, unless the file puts it on the host's or none.
		network := "Network={{ .Network }}\n"
		if svc, ok := c.project.Services[only.comp.Name]; ok && (svc.NetworkMode == "host" || svc.NetworkMode == "none") {
			network = ""
		}
		c.entry.Quadlet = c.labels("[Container]\nImage={{ .Image }}\nContainerName={{ .Name }}\n" + network + strings.Join(only.body, "\n") + "\n")
		return
	}
	for _, b := range comps {
		b.comp.WaitHealthy = healthWaits[b.comp.Name]
		b.comp.Quadlet = c.labels("[Container]\nImage={{ .Image }}\n" + strings.Join(b.body, "\n") + "\n")
		c.entry.Components = append(c.entry.Components, b.comp)
	}
	if web != "" {
		c.entry.Web.Component = web
	}
}

var logMu sync.Mutex

// captureLogs sends logrus's warnings to note until restore is called.
func captureLogs(note func(string)) (restore func()) {
	logMu.Lock()
	l := logrus.StandardLogger()
	out, hooks := l.Out, l.ReplaceHooks(logrus.LevelHooks{})
	l.SetOutput(io.Discard)
	l.AddHook(noteHook(note))
	return func() {
		l.SetOutput(out)
		l.ReplaceHooks(hooks)
		logMu.Unlock()
	}
}

type noteHook func(string)

func (noteHook) Levels() []logrus.Level { return []logrus.Level{logrus.WarnLevel, logrus.ErrorLevel} }

func (h noteHook) Fire(e *logrus.Entry) error {
	h(e.Message)
	return nil
}

// optionalEnvFiles returns the file with each env_file entry that does not
// exist beside it marked required: false, and those entries' paths.
func optionalEnvFiles(raw []byte, dir string) ([]byte, []string) {
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil || len(doc.Content) == 0 {
		return raw, nil
	}
	var missing []string
	services := mapValue(doc.Content[0], "services")
	if services == nil {
		return raw, nil
	}
	for i := 1; i < len(services.Content); i += 2 {
		ef := mapValue(services.Content[i], "env_file")
		if ef == nil {
			continue
		}
		items := []*yaml.Node{ef}
		if ef.Kind == yaml.SequenceNode {
			items = ef.Content
		}
		for _, it := range items {
			p := it.Value
			if it.Kind == yaml.MappingNode {
				if pn := mapValue(it, "path"); pn != nil {
					p = pn.Value
				}
			}
			full := p
			if !filepath.IsAbs(full) {
				full = filepath.Join(dir, p)
			}
			if p == "" || fileExists(full) {
				continue
			}
			missing = append(missing, p)
			// Absolute, since the edited file is read from elsewhere.
			*it = yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Value: "path"}, {Kind: yaml.ScalarNode, Value: full},
				{Kind: yaml.ScalarNode, Value: "required"}, {Kind: yaml.ScalarNode, Value: "false", Tag: "!!bool"},
			}}
		}
	}
	if len(missing) == 0 {
		return raw, nil
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return raw, nil
	}
	return out, missing
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// placeholder is a value the file expects the user to replace by hand.
var placeholder = regexp.MustCompile(`(?i)\[your[_ -]?[a-z_ -]*\]|<your[_ -]?[a-z_ -]*>|change[-_ ]?me`)

// switchValue is a yes or no, such as MARIADB_RANDOM_ROOT_PASSWORD=true:
// a switch with a password-like name, not a password.
var switchValue = regexp.MustCompile(`(?i)^(true|false|yes|no|on|off|0|1)$`)

// literalSecrets turns passwords written into the file into settings made
// up at install, when the file itself shows every place that uses one: the
// same value under password-like keys in two or more places (a database and
// the app that logs in to it). A password written once is left as written
// for a person to look at, since an app may rely on it through a default of
// its own that the file does not show, unless it is a placeholder such as
// change-me, which nothing relies on.
func (c *conv) literalSecrets() {
	r := &c.res.Report
	c.secrets = map[string]string{}
	type use struct{ svc, key string }
	uses := map[string][]use{}
	var order []string
	for _, name := range c.project.ServiceNames() {
		env := c.project.Services[name].Environment
		for _, k := range sortedKeys(env) {
			v := env[k]
			if v == nil || *v == "" || tokenRe.MatchString(*v) || switchValue.MatchString(*v) || !secretName.MatchString(k) || c.settings[k] {
				continue
			}
			if uses[*v] == nil {
				order = append(order, *v)
			}
			uses[*v] = append(uses[*v], use{name, k})
		}
	}
	for _, v := range order {
		u := uses[v]
		if len(u) == 1 && !placeholder.MatchString(v) {
			r.needsPerson("%s: %s is written into the file; make it a setting (default: generate) unless the app relies on this value", u[0].svc, u[0].key)
			continue
		}
		setting := u[0].key
		for i := 2; c.settings[setting]; i++ {
			setting = fmt.Sprintf("%s_%d", u[0].key, i)
		}
		c.secrets[v] = setting
		c.settings[setting] = true
		var where []string
		for _, x := range u {
			where = append(where, x.svc+"'s "+x.key)
		}
		if len(u) == 1 {
			r.changed("%s's %s is a placeholder (%q); it is the setting %s, made up at install", u[0].svc, u[0].key, v, setting)
		} else {
			r.changed("The password written into %s is the setting %s, made up at install", strings.Join(where, " and "), setting)
		}
	}
}

// imageOf is the image a service runs: compose2quadlet's normalised
// reference, else the file's own.
func (c *conv) imageOf(name string, svc types.ServiceConfig) string {
	if img := c.images[name]; img != "" {
		return img
	}
	return svc.Image
}

// internalNetwork: the file declares networks, and every one is internal.
func (c *conv) internalNetwork() bool {
	declared := topKeys(c.raw, "networks")
	if len(declared) == 0 {
		return false
	}
	for _, n := range declared {
		if net, ok := c.project.Networks[n]; !ok || !net.Internal {
			return false
		}
	}
	return true
}

// reachesOut: a service on a network that is not internal (the implicit
// default network included).
func (c *conv) reachesOut(svc types.ServiceConfig) bool {
	if len(svc.Networks) == 0 {
		return true
	}
	for n := range svc.Networks {
		if net, ok := c.project.Networks[n]; !ok || !net.Internal {
			return true
		}
	}
	return false
}

// commonWeb are container ports apps usually serve their page on.
var commonWeb = []uint32{80, 8080, 3000, 8000, 8096, 8123, 5000, 9000, 443, 8443}

// webPage picks the component and port of the Service's web page: CasaOS's
// main service and port when the file says, else the only published TCP
// port, else a usual web port. A guess is left for a person to confirm.
func (c *conv) webPage() (component string, port uint32, path string) {
	r := &c.res.Report
	path = "/"
	type cand struct {
		svc  string
		port uint32
	}
	var all []cand
	for _, name := range c.project.ServiceNames() {
		for _, p := range c.project.Services[name].Ports {
			if p.Published != "" && (p.Protocol == "" || p.Protocol == "tcp") {
				all = append(all, cand{name, p.Target})
			}
		}
	}
	if main, ok := c.casaos["main"].(string); ok {
		hostPort := fmt.Sprint(c.casaos["port_map"])
		for _, p := range c.project.Services[main].Ports {
			if p.Published == hostPort {
				port = p.Target
			}
		}
		if idx, ok := c.casaos["index"].(string); ok && idx != "" {
			path = idx
		}
		// On the host's network nothing is published: the app listens on
		// the Node's port itself, the one CasaOS opens.
		if n, err := strconv.Atoi(hostPort); port == 0 && err == nil && n > 0 && n < 65536 && c.project.Services[main].NetworkMode == "host" {
			port = uint32(n)
		}
		if port != 0 {
			return main, port, path
		}
	}
	switch {
	case len(all) == 0:
		r.needsPerson("No service publishes a port, so the entry has no web page; add web: if it should have one")
		return "", 0, path
	case len(all) == 1:
		return all[0].svc, all[0].port, path
	}
	for _, w := range commonWeb {
		for _, a := range all {
			if a.port == w {
				r.needsPerson("The web page is taken to be %s's port %d, of %d published ports; confirm web.component and web.port", a.svc, a.port, len(all))
				return a.svc, a.port, path
			}
		}
	}
	r.needsPerson("The web page is taken to be %s's port %d, the first of %d published ports; confirm web.component and web.port", all[0].svc, all[0].port, len(all))
	return all[0].svc, all[0].port, path
}

// dropped are compose2quadlet keys the adapter writes itself, or that
// Maître supplies, so they are left out of the template.
var dropped = map[string]bool{
	"Image": true, "ContainerName": true, "ServiceName": true, "Network": true, "NetworkAlias": true,
	"Environment": true, "EnvironmentFile": true, "Volume": true, "Mount": true, "PublishPort": true,
	"HealthCmd": true, "Pull": true, "AutoUpdate": true, "Secret": true, "Pod": true,
}

// template is one service's [Container] lines, after Image=.
func (c *conv) template(name string, svc types.ServiceConfig, web bool, webPort uint32) []string {
	r := &c.res.Report
	var out []string
	add := func(key, value string) { out = append(out, key+"="+templated(value)) }
	unit := c.units[name]
	for _, s := range unit.Sections {
		switch s.Name {
		case c2q.SectionContainer:
			for _, d := range s.Directives {
				if dropped[d.Key] {
					continue
				}
				if d.Key == "Label" && c.casaos != nil && len(d.Values) > 0 && strings.HasPrefix(d.Values[0], "icon=") {
					continue // CasaOS's own dashboard reads it; the entry has its icon
				}
				for _, v := range d.Values {
					add(d.Key, v)
				}
			}
		case c2q.SectionService, c2q.SectionUnit:
			for _, d := range s.Directives {
				switch d.Key {
				case "Requires", "Wants", "After", "BindsTo", "ExecStartPre":
					// Maître writes ordering and health waits from depends_on.
				case "Restart":
					c.restarted = true
				case "MemoryLow", "MemoryMin", "CPUWeight", "StartupCPUWeight", "IOWeight":
					// deploy.resources.reservations: systemd's soft guarantees.
					r.changed("%s: deploy.resources reservations (%s=) are dropped; Maître sets no reservations", name, d.Key)
				default:
					r.needsPerson("%s: [%s] %s= (from x-systemd) is not carried over; it would run on the Node itself", name, s.Name, d.Key)
				}
			}
		}
	}
	if svc.ContainerName != "" {
		r.changed("%s: container_name %q is dropped; Maître names the container, and other containers reach it as %q", name, svc.ContainerName, name)
	}
	if len(svc.Secrets) > 0 || len(svc.Configs) > 0 {
		r.needsPerson("%s uses secrets: or configs:, which are files on the host; give their contents as settings or a volume", name)
	}
	switch svc.NetworkMode {
	case "host", "none":
		add("Network", svc.NetworkMode)
	}

	// Environment, its placeholders as settings.
	for _, k := range sortedKeys(svc.Environment) {
		v := svc.Environment[k]
		if v == nil {
			r.changed("%s: environment %s takes its value from the host running Compose; dropped", name, k)
			continue
		}
		val := *v
		if k == "TZ" && !tokenRe.MatchString(val) {
			// The time zone of whoever wrote the file; the Node's own instead.
			r.changed("%s: TZ=%s is replaced by the TZ setting, the Node's time zone unless changed", name, val)
			c.tz, val = true, token("TZ")
		}
		if s, ok := c.secrets[val]; ok && secretName.MatchString(k) && (!c.settings[k] || s == k) {
			val = token(s)
		} else if placeholder.MatchString(val) {
			r.needsPerson("%s: %s=%s holds a placeholder to fill in", name, k, val)
		}
		if c.settings[k] && !tokenRe.MatchString(val) {
			// An env_file value of a variable the file also uses as a
			// placeholder is that setting too.
			val = token(k)
		}
		// Quoted first, as Quadlet will read it; escaped for the template after.
		out = append(out, "Environment="+templated(quoteValue(k+"="+val)))
	}
	if _, hasTZ := svc.Environment["TZ"]; !hasTZ && c.mountsLocaltime(svc) {
		out = append(out, `Environment="TZ={{ .Settings.TZ }}"`)
	}

	// Volumes: folders in the Service's Spaces.
	for _, v := range svc.Volumes {
		if line := c.volumeLine(name, v); line != "" {
			out = append(out, line)
		}
	}
	for _, t := range svc.Tmpfs {
		add("Tmpfs", t)
	}

	// Ports: the web port is Maître's, behind Caddy; others are the Node's.
	for _, p := range svc.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		if web && p.Target == webPort && proto == "tcp" {
			continue
		}
		if p.Published == "" {
			r.changed("%s: port %d is not published on the Node (Compose would pick a random one)", name, p.Target)
			continue
		}
		if _, err := strconv.Atoi(p.Published); err != nil {
			r.needsPerson("%s: the port range %s is not supported by host_ports; list its ports one by one", name, p.Published)
			continue
		}
		spec := p.Published + ":" + strconv.Itoa(int(p.Target))
		if p.HostIP != "" {
			spec = p.HostIP + ":" + spec
		}
		if proto != "tcp" {
			spec += "/" + proto
		}
		add("PublishPort", spec)
		c.entry.HostPorts = appendNew(c.entry.HostPorts, p.Published+"/"+proto)
	}

	// The health check, from Compose's own form.
	if hc := svc.HealthCheck; hc != nil {
		switch {
		case hc.Disable || (len(hc.Test) > 0 && hc.Test[0] == "NONE"):
			add("HealthCmd", "none")
		case len(hc.Test) > 1 && hc.Test[0] == "CMD-SHELL":
			add("HealthCmd", strings.Join(hc.Test[1:], " "))
		case len(hc.Test) > 1 && hc.Test[0] == "CMD":
			// Podman reads a JSON array as the exec form.
			args, _ := json.Marshal([]string(hc.Test[1:]))
			add("HealthCmd", string(args))
		}
	}
	return out
}

// mountsLocaltime: the service mounts the host's clock settings.
func (c *conv) mountsLocaltime(svc types.ServiceConfig) bool {
	for _, v := range svc.Volumes {
		if v.Type == types.VolumeTypeBind && (v.Source == "/etc/localtime" || v.Source == "/etc/timezone") {
			return true
		}
	}
	return false
}

// volumeLine maps one Compose volume to a template line.
func (c *conv) volumeLine(svcName string, v types.ServiceVolumeConfig) string {
	r := &c.res.Report
	opts := ""
	if v.ReadOnly {
		opts = "ro,"
	}
	switch v.Type {
	case types.VolumeTypeBind:
		switch {
		case v.Source == "/etc/localtime" || v.Source == "/etc/timezone":
			c.tz = true
			if _, said := c.project.Services[svcName].Environment["TZ"]; !c.settings["TZ"] && !said {
				r.changed("%s: the host's %s is replaced by the TZ setting, the Node's time zone unless changed", svcName, v.Source)
			}
			return ""
		case c.casaos != nil && strings.HasPrefix(v.Source, "/DATA/AppData/"):
			// CasaOS keeps an app's own data in /DATA/AppData/<app>/...
			rel := filepath.Base(v.Source)
			vol := c.volumeFor(v.Source, rel, v.Target, svcName)
			r.changed("%s: CasaOS's %s is the Service volume %q, kept in a Storage Space", svcName, v.Source, vol.name)
			return fmt.Sprintf("Volume={{ .Volumes.%s }}:%s:%s", vol.name, v.Target, opts+"@label@"+vol.name)
		case c.casaos != nil && strings.HasPrefix(v.Source, "/DATA/"):
			// ... and the user's shared folders (Media, Downloads) in /DATA.
			vol := c.volumeFor(v.Source, filepath.Base(v.Source), v.Target, svcName)
			vol.shared = true
			r.changed("%s: CasaOS's shared %s is the shared volume %q; the user picks the folder at install", svcName, v.Source, vol.name)
			return fmt.Sprintf("Volume={{ .Volumes.%s }}:%s:%s", vol.name, v.Target, opts+"z")
		case v.Source == c.dir || strings.HasPrefix(v.Source, c.dir+string(filepath.Separator)):
			rel, _ := filepath.Rel(c.dir, v.Source)
			vol := c.volumeFor(v.Source, rel, v.Target, svcName)
			r.changed("%s: the folder ./%s is the Service volume %q, kept in a Storage Space", svcName, filepath.ToSlash(rel), vol.name)
			return fmt.Sprintf("Volume={{ .Volumes.%s }}:%s:%s", vol.name, v.Target, opts+"@label@"+vol.name)
		default:
			if ok, needs := catalog.Support("Volume=" + filepath.Clean(v.Source)); !ok {
				r.needsPerson("%s mounts the Node's own %s, which Maître does not support yet (it needs %s); drop it if the app works without it, or leave the entry out", svcName, v.Source, needs)
			} else {
				r.needsPerson("%s mounts the Node's own folder %s; point it at a Service volume, or keep it and declare it under grants: with why", svcName, v.Source)
			}
			c.hostMounts[filepath.Clean(v.Source)] = true
			return "Volume=" + templated(v.Source+":"+v.Target+strings.TrimSuffix(":"+opts, ":"))
		}
	case types.VolumeTypeVolume:
		src := v.Source
		if src == "" {
			src = "anonymous:" + v.Target
		}
		if v.Volume != nil && v.Volume.Subpath != "" {
			r.needsPerson("%s mounts only %s of volume %s; volume subpaths are not supported", svcName, v.Volume.Subpath, v.Source)
		}
		label := v.Source
		if label == "" {
			label = filepath.Base(v.Target)
		}
		vol := c.volumeFor(src, label, v.Target, svcName)
		r.changed("%s: the volume %s is the Service volume %q, kept in a Storage Space", svcName, label, vol.name)
		return fmt.Sprintf("Volume={{ .Volumes.%s }}:%s:%s", vol.name, v.Target, opts+"@label@"+vol.name)
	case types.VolumeTypeTmpfs:
		if v.Tmpfs != nil && v.Tmpfs.Size > 0 {
			return fmt.Sprintf("Tmpfs=%s:size=%d", v.Target, int64(v.Tmpfs.Size))
		}
		return "Tmpfs=" + v.Target
	}
	r.needsPerson("%s: a %s volume is not supported", svcName, v.Type)
	return ""
}

// volumeFor is the Service volume for a source, named after label.
func (c *conv) volumeFor(source, label, target, user string) *volume {
	if v := c.volumes[source]; v != nil {
		v.users[user] = true
		return v
	}
	base := fieldName(label)
	n := base
	for i := 2; c.volumeNamed(n); i++ {
		n = fmt.Sprintf("%s_%d", base, i)
	}
	v := &volume{name: n, path: target, users: map[string]bool{user: true}}
	c.volumes[source] = v
	return v
}

func (c *conv) volumeNamed(n string) bool {
	for _, v := range c.volumes {
		if v.name == n {
			return true
		}
	}
	return false
}

var labelRe = regexp.MustCompile(`@label@([a-z0-9_]+)`)

// labels sets each volume line's SELinux label once every container using
// the volume is known: Z when one container has it, z when several share it.
func (c *conv) labels(tmpl string) string {
	return labelRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		name := m[len("@label@"):]
		for _, v := range c.volumes {
			if v.name == name && len(v.users) > 1 {
				return "z"
			}
		}
		return "Z"
	})
}

func (c *conv) isLiteralSecret(setting string) bool {
	for _, s := range c.secrets {
		if s == setting {
			return true
		}
	}
	return false
}

// settingsAndVolumes fills the manifest's settings and volumes, and the
// SELinux label of each volume line: private to one container (Z), or
// shared by several (z).
func (c *conv) settingsAndVolumes() {
	r := &c.res.Report
	if c.tz {
		c.settings["TZ"] = true
	}
	for _, k := range sortedKeys(c.settings) {
		s := catalog.Setting{Key: k, Label: label(k), Type: "text"}
		if l := c.casaEnvLabel(k); l != "" {
			s.Label = l
		}
		v := c.vars[k]
		def, hasDef := c.dotenv[k]
		if !hasDef && v != nil && v.hasDef {
			def, hasDef = v.def, true
		}
		if d, ok := casaProvided[k]; ok && c.casaos != nil && !hasDef {
			def, hasDef = d, true
			r.changed("%s, which CasaOS sets itself, defaults to %q", k, d)
		}
		switch {
		case k == "TZ":
			s.Label, s.Type, s.Default = "Time zone", "timezone", "auto"
		case secretName.MatchString(k):
			s.Type, s.Default = "password", catalog.Generate
			if c.isLiteralSecret(k) {
				// Reported where it was found.
			} else if hasDef && def != "" {
				r.changed("%s is made up at install, instead of %q", k, def)
			} else {
				r.changed("%s is made up at install", k)
			}
		case hasDef:
			s.Default = def
		default:
			r.needsPerson("The setting %s has no default; give one, or it is asked for empty at install", k)
		}
		c.entry.Settings = append(c.entry.Settings, s)
	}
	vols := make([]*volume, 0, len(c.volumes))
	for _, v := range c.volumes {
		vols = append(vols, v)
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].name < vols[j].name })
	for _, v := range vols {
		c.entry.Volumes = append(c.entry.Volumes, catalog.Volume{Name: v.name, Path: v.path, Shared: v.shared})
	}
}

// casaProvided are variables CasaOS fills in for every app, with the
// values it uses for its own first user.
var casaProvided = map[string]string{"PUID": "1000", "PGID": "1000"}

// casaEnvLabel is CasaOS's description of an environment variable, from
// any service's x-casaos envs.
func (c *conv) casaEnvLabel(key string) string {
	for _, svc := range c.project.Services {
		x, _ := svc.Extensions["x-casaos"].(map[string]any)
		envs, _ := x["envs"].([]any)
		for _, e := range envs {
			m, _ := e.(map[string]any)
			if m["container"] == key {
				if l := localized(m["description"]); l != "" {
					return l
				}
			}
		}
	}
	return ""
}

// describe fills what the Console shows: CasaOS's words when the file has
// them, else placeholders for a person.
func (c *conv) describe(web string, port uint32, path string, n int) {
	r := &c.res.Report
	e := &c.entry
	e.Name = localized(c.casaos["title"])
	if e.Name == "" {
		e.Name = title(e.ID)
	}
	e.Summary = localized(c.casaos["tagline"])
	e.Description = localized(c.casaos["description"])
	if e.Summary == "" {
		e.Summary = e.Name + ", converted from a Compose file."
		r.needsPerson("Write the entry's summary and description")
	}
	if icon, ok := c.casaos["icon"].(string); ok {
		e.Icon = icon
	}
	e.Category = casaCategory(fmt.Sprint(c.casaos["category"]))
	if v, ok := c.casaos["version"].(string); ok {
		e.Version = v
	}
	if port != 0 {
		e.Web = &catalog.Web{Port: int(port), Path: path}
		if n > 1 {
			e.Web.Component = web
		}
	}
	from := "compose"
	if c.casaos != nil {
		from = "casaos"
	}
	e.Source = &catalog.Source{From: from, URL: c.opts.URL, Licence: c.opts.Licence, Converted: c.opts.Now.Format("2006-01-02")}
	if c.opts.Licence == "" {
		r.needsPerson("Say under which licence the Compose file was published (source.licence)")
	}
}

// findProject fills the entry's project, for its popularity (docs/
// popularity-and-grants.md, P4), from the org.opencontainers.image.source
// label of the image serving its page, or of its only image.
func (c *conv) findProject(ctx context.Context) {
	r := &c.res.Report
	e := &c.entry
	l, ok := c.opts.Resolver.(Labeller)
	if !ok {
		return
	}
	image := e.Image
	for _, comp := range e.Components {
		if image == "" || e.Web != nil && comp.Name == e.Web.Component {
			image = comp.Image
		}
	}
	labels, err := l.Labels(ctx, image)
	if p := catalog.ProjectFromSource(labels["org.opencontainers.image.source"]); err == nil && p != "" {
		e.Project = p
		r.note("project: %s, from the image's source label; its stars are the entry's popularity", p)
		return
	}
	r.note("No project: for the entry's popularity; add the app's repository on GitHub, Codeberg or GitLab")
}

// check runs the bundle through the Node's own check: the options that
// will ask for grants, and Podman's Quadlet.
func (c *conv) check(ctx context.Context) {
	r := &c.res.Report
	e := c.entry
	if err := e.Validate(); err != nil {
		r.mustFix("The entry is not valid: %v", err)
		return
	}
	res, err := c.opts.Check(ctx, catalog.SpecFor(&e, "", nil, ""))
	switch {
	case errors.Is(err, quadlet.ErrNoGenerator):
		r.note("Podman's Quadlet is not installed here, so it has not read the units; `maitre catalog-check` does, on a Node or in CI")
	case err != nil:
		r.mustFix("The Node's check refused it: %v", err)
		return
	}
	if res != nil {
		for _, p := range res.Protected {
			if path, ok := strings.CutPrefix(p.Option, "Volume="); ok && c.hostMounts[path] {
				continue // reported with what to do about it
			}
			who := p.Component
			if who == "" {
				who = e.ID
			}
			if ok, needs := catalog.Support(p.Option); !ok {
				r.needsPerson("%s asks for %s, which Maître does not support yet (it needs %s); find another way, or leave the entry out", who, p.Option, needs)
				continue
			}
			r.needsPerson("%s asks for %s, which a Trusted Device must allow at install; keep it only if the app needs it, and declare it under grants: with why (optional: if the app works without it)", who, p.Option)
		}
	}
	c.entry.Unfinished = append([]string{}, r.NeedsPerson...)
}

// files are the bundle: manifest, templates, the Compose file as it came,
// and the report.
func (c *conv) files() {
	e := c.entry
	out := map[string][]byte{}
	m, err := catalog.MarshalYAML(&e)
	if err != nil {
		c.res.Report.mustFix("manifest: %v", err)
		return
	}
	out["manifest.yaml"] = m
	if len(e.Components) == 0 {
		out[e.ID+".container"] = []byte(e.Quadlet)
	}
	for _, comp := range e.Components {
		out[comp.Name+".container"] = []byte(comp.Quadlet)
	}
	out["compose.yaml"] = c.raw
	out["CONVERSION.md"] = []byte(c.res.Report.Markdown(e.Name, filepath.Base(c.opts.Compose)))
	c.res.Entry = &e
	c.res.Files = out
}

// Write saves a bundle as <catalog>/entries/<id>. An existing entry is
// replaced only when replace is set.
func Write(catalogDir string, res *Result, replace bool) (string, error) {
	if res.Files == nil {
		return "", errors.New("nothing to write: the conversion has things to fix first")
	}
	dir := filepath.Join(catalogDir, "entries", res.Entry.ID)
	if _, err := os.Stat(dir); err == nil {
		if !replace {
			return "", fmt.Errorf("%s exists; pass --replace to convert over it", dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for name, body := range res.Files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// --- small helpers ---------------------------------------------------------

// imageTag is an image's tag, without a leading v: 17.0.1 of
// codeberg.org/forgejo/forgejo:17.0.1@sha256:...
func imageTag(image string) string {
	ref, _, _ := strings.Cut(image, "@")
	i := strings.LastIndex(ref, ":")
	if i < strings.LastIndex(ref, "/") || i < 0 {
		return ""
	}
	return strings.TrimPrefix(ref[i+1:], "v")
}

var compRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// entryID makes a Catalog id of a project or folder name.
func entryID(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) < 2 {
		s = "app-" + s
	}
	return s
}

// fieldName makes a volume name usable as a template field.
func fieldName(s string) string {
	s = strings.ToLower(strings.Trim(s, "./"))
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "v_" + s
	}
	return strings.TrimSuffix(s, "_")
}

// label makes a setting's label of its key: DB_PASSWORD, "Db password".
func label(k string) string {
	words := strings.Fields(strings.ToLower(strings.ReplaceAll(k, "_", " ")))
	if len(words) == 0 {
		return k
	}
	words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	return strings.Join(words, " ")
}

func title(id string) string {
	words := strings.Fields(strings.ReplaceAll(id, "-", " "))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// quoteValue quotes an Environment= assignment for Quadlet, which splits
// the value at whitespace and reads systemd quoting. One holding a setting
// is always quoted, since what the user types may have spaces.
func quoteValue(v string) string {
	if !strings.ContainsAny(v, " \t\"'\\") && !tokenRe.MatchString(v) {
		return v
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// hasTopKey and topKeys read the file's top level as written.
func hasTopKey(raw []byte, key string) bool {
	var top map[string]yaml.Node
	return yaml.Unmarshal(raw, &top) == nil && top[key].Kind != 0
}

func topKeys(raw []byte, key string) []string {
	var top map[string]map[string]any
	_ = yaml.Unmarshal(raw, &top)
	return sortedKeys(top[key])
}

// localized is CasaOS's text in English: a map by locale, or plain text.
func localized(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case map[string]any:
		for _, k := range []string{"en_us", "en_US", "en_GB", "en"} {
			if s, ok := t[k].(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// casaCategory maps CasaOS's categories to the Catalog's.
func casaCategory(s string) string {
	switch strings.ToLower(s) {
	case "media", "gallery", "music", "video", "photography":
		return "media"
	case "home automation", "smart home":
		return "home"
	case "network", "networking", "network tools", "security":
		return "network"
	}
	return "tools"
}
