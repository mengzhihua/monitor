package preprocess

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

const (
	maxXMLBytes = 256 * 1024
	maxXMLNodes = 2000
	maxXMLDepth = 16
	maxXMLAttrs = 32
	maxXMLName  = 128
	maxXMLText  = 4096
)

type xmlBuild struct {
	name  string
	attrs []xml.Attr
	kids  []*xmlBuild
	text  strings.Builder
}

func applyXMLToJSON(text string) (string, error) {
	if len(text) > maxXMLBytes {
		return "", fmt.Errorf("xml_to_json: input too large")
	}
	if strings.Contains(strings.ToLower(text), "<!doctype") || strings.Contains(strings.ToLower(text), "<!entity") {
		return "", fmt.Errorf("xml_to_json: directives are not allowed")
	}
	root, err := parseXMLBuild(text)
	if err != nil {
		return "", err
	}
	value, err := root.jsonValue()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{root.name: value})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func parseXMLBuild(text string) (*xmlBuild, error) {
	dec := xml.NewDecoder(strings.NewReader(text))
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if charset == "" || strings.EqualFold(charset, "utf-8") || strings.EqualFold(charset, "utf8") {
			return input, nil
		}
		return nil, fmt.Errorf("xml_to_json: unsupported charset %s", charset)
	}
	var stack []*xmlBuild
	var root *xmlBuild
	nodes := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xml_to_json: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			nodes++
			if nodes > maxXMLNodes {
				return nil, fmt.Errorf("xml_to_json: too many elements")
			}
			if len(stack)+1 > maxXMLDepth {
				return nil, fmt.Errorf("xml_to_json: document is too deep")
			}
			if len(t.Attr) > maxXMLAttrs {
				return nil, fmt.Errorf("xml_to_json: too many attributes")
			}
			name := t.Name.Local
			if name == "" || len(name) > maxXMLName {
				return nil, fmt.Errorf("xml_to_json: bad element name")
			}
			node := &xmlBuild{name: name, attrs: append([]xml.Attr(nil), t.Attr...)}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("xml_to_json: multiple roots")
				}
				root = node
			} else {
				stack[len(stack)-1].kids = append(stack[len(stack)-1].kids, node)
			}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("xml_to_json: unexpected end")
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				continue
			}
			node := stack[len(stack)-1]
			if node.text.Len()+len(t) > maxXMLText {
				return nil, fmt.Errorf("xml_to_json: text too long")
			}
			node.text.Write(t)
		case xml.Comment, xml.ProcInst:
		case xml.Directive:
			return nil, fmt.Errorf("xml_to_json: directives are not allowed")
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, fmt.Errorf("xml_to_json: empty document")
	}
	return root, nil
}

func (n *xmlBuild) jsonValue() (any, error) {
	text := strings.TrimSpace(n.text.String())
	if len(n.kids) == 0 && len(n.attrs) == 0 {
		return text, nil
	}
	obj := map[string]any{}
	for _, attr := range n.attrs {
		key := "@" + attr.Name.Local
		if attr.Name.Local == "" || len(key) > maxXMLName+1 {
			return nil, fmt.Errorf("xml_to_json: bad attribute name")
		}
		if _, exists := obj[key]; exists {
			return nil, fmt.Errorf("xml_to_json: duplicate attribute %s", key)
		}
		obj[key] = attr.Value
	}
	grouped := map[string][]any{}
	var order []string
	for _, child := range n.kids {
		value, err := child.jsonValue()
		if err != nil {
			return nil, err
		}
		if _, ok := grouped[child.name]; !ok {
			if _, exists := obj[child.name]; exists {
				return nil, fmt.Errorf("xml_to_json: element %s collides with an attribute", child.name)
			}
			order = append(order, child.name)
		}
		grouped[child.name] = append(grouped[child.name], value)
	}
	for _, name := range order {
		values := grouped[name]
		if len(values) == 1 {
			obj[name] = values[0]
		} else {
			obj[name] = values
		}
	}
	if text != "" {
		if _, exists := obj["#text"]; exists {
			return nil, fmt.Errorf("xml_to_json: duplicate text")
		}
		obj["#text"] = text
	}
	return obj, nil
}
