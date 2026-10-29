package catalog

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SetIcon records in an entry's manifest.yaml that its icon is the file
// iconFile beside it, copied from credit's pack. The manifest is edited in
// place, so its comments and order stay as their author wrote them.
func SetIcon(dir, iconFile string, credit IconCredit) error {
	path := filepath.Join(dir, "manifest.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: not a mapping", path)
	}
	top := doc.Content[0]
	setScalar(top, "icon", iconFile)
	source := child(top, "source")
	if source == nil {
		source = &yaml.Node{Kind: yaml.MappingNode}
		setScalar(source, "from", "hand")
		top.Content = append(top.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "source"}, source)
	}
	var icon yaml.Node
	if err := icon.Encode(credit); err != nil {
		return err
	}
	if n := child(source, "icon"); n != nil {
		*n = icon
	} else {
		source.Content = append(source.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "icon"}, &icon)
	}
	out, err := MarshalYAML(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// MarshalYAML writes YAML as the Catalog's manifests are written: two
// spaces to a level.
func MarshalYAML(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	err := enc.Close()
	return b.Bytes(), err
}

func child(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setScalar(m *yaml.Node, key, value string) {
	if n := child(m, key); n != nil {
		n.Kind, n.Tag, n.Value, n.Style = yaml.ScalarNode, "", value, 0
		return
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Value: value})
}
