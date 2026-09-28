package scan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	yaml3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

// doc is one Kubernetes object read from a manifest, with the line it starts on.
type doc struct {
	obj  map[string]any
	line int
}

// errNotK8s marks a file that parsed but holds no Kubernetes objects.
var errNotK8s = errors.New("not a Kubernetes manifest (no apiVersion and kind)")

// parseManifests reads every document of a YAML or JSON stream. Documents
// without apiVersion and kind are dropped; a List is expanded into its items.
// Each object keeps the line its document (or list item) starts on.
func parseManifests(data []byte) ([]doc, int, error) {
	dec := yaml3.NewDecoder(bytes.NewReader(data))
	var out []doc
	skipped := 0
	for {
		var n yaml3.Node
		err := dec.Decode(&n)
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, skipped, fmt.Errorf("parse: %w", err)
		}
		if n.Kind != yaml3.DocumentNode || len(n.Content) == 0 {
			continue
		}
		root := n.Content[0]
		if root.Kind == yaml3.ScalarNode && (root.Tag == "!!null" || root.Value == "") {
			continue // an empty document or one holding only comments
		}
		if root.Kind != yaml3.MappingNode {
			skipped++
			continue
		}
		obj, err := nodeObject(root)
		if err != nil {
			return out, skipped, err
		}
		if obj == nil {
			continue
		}
		if asStr(obj["apiVersion"]) == "" || asStr(obj["kind"]) == "" {
			skipped++
			continue
		}
		if strings.HasSuffix(asStr(obj["kind"]), "List") && obj["items"] != nil {
			lines := itemLines(root)
			for i, it := range asList(obj["items"]) {
				im := asMap(it)
				if asStr(im["apiVersion"]) == "" || asStr(im["kind"]) == "" {
					skipped++
					continue
				}
				ln := root.Line
				if i < len(lines) {
					ln = lines[i]
				}
				out = append(out, doc{obj: im, line: ln})
			}
			continue
		}
		out = append(out, doc{obj: obj, line: docLine(root)})
	}
	if len(out) == 0 && skipped > 0 {
		return nil, skipped, errNotK8s
	}
	return out, skipped, nil
}

// docLine is the line of the apiVersion key, which is where an editor jumps to.
func docLine(n *yaml3.Node) int {
	if len(n.Content) > 0 {
		return n.Content[0].Line
	}
	return n.Line
}

func itemLines(root *yaml3.Node) []int {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "items" && root.Content[i+1].Kind == yaml3.SequenceNode {
			var out []int
			for _, c := range root.Content[i+1].Content {
				out = append(out, docLine(c))
			}
			return out
		}
	}
	return nil
}

// nodeObject converts a YAML node to the JSON shapes a capture holds
// (map[string]any, []any, float64, string, bool).
func nodeObject(n *yaml3.Node) (map[string]any, error) {
	b, err := yaml3.Marshal(n)
	if err != nil {
		return nil, err
	}
	j, err := yaml.YAMLToJSON(b)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(j, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}
