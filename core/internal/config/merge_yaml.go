package config

import (
	"bytes"
	"errors"
	"strings"

	"gopkg.in/yaml.v3"
)

// MergeYAML overlays patch onto base. Mapping keys recurse; every other node
// replaces the existing value. Unmentioned keys stay, so two section edits
// applied in sequence do not drop each other.
func MergeYAML(base, patch string) (string, error) {
	if strings.TrimSpace(patch) == "" {
		return "", errors.New("config patch is empty")
	}
	baseDoc, err := parseYAMLDoc(base)
	if err != nil {
		return "", err
	}
	patchDoc, err := parseYAMLDoc(patch)
	if err != nil {
		return "", err
	}
	dst, err := mappingNode(baseDoc)
	if err != nil {
		return "", err
	}
	src, err := mappingNode(patchDoc)
	if err != nil {
		return "", err
	}
	if err := overlayMap(dst, src); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(baseDoc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func parseYAMLDoc(raw string) (*yaml.Node, error) {
	var doc yaml.Node
	if strings.TrimSpace(raw) == "" {
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}, nil
	}
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	return &doc, nil
}

func mappingNode(doc *yaml.Node) (*yaml.Node, error) {
	n := doc
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) != 1 {
			return nil, errors.New("config patch must be a mapping")
		}
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil, errors.New("config patch must be a mapping")
	}
	return n, nil
}

func overlayMap(dst, src *yaml.Node) error {
	if len(src.Content)%2 != 0 {
		return errors.New("config patch mapping is malformed")
	}
	for i := 0; i < len(src.Content); i += 2 {
		key := src.Content[i]
		val := src.Content[i+1]
		if existing := get(dst, key.Value); existing != nil && existing.Kind == yaml.MappingNode && val.Kind == yaml.MappingNode {
			if err := overlayMap(existing, val); err != nil {
				return err
			}
			continue
		}
		replaceKey(dst, key.Value, val)
	}
	return nil
}
