package redact

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// FieldRule removes one path from every object, for example
//
//	metadata.annotations["corp.example/*"]
//	spec.template.spec.containers[*].env
//	data.license
//
// Segments are separated by dots. A segment in ["..."] may contain dots and
// slashes (annotation and label keys). Both forms accept * and ? globs. [*]
// steps into every element of a list. The last segment names the key to
// remove.
type FieldRule struct {
	Source string
	segs   []seg
}

type seg struct {
	key  string // glob; empty when list is set
	list bool   // [*]: every element
}

// ParseFieldRule compiles one path.
func ParseFieldRule(src string) (FieldRule, error) {
	s := strings.TrimSpace(src)
	fr := FieldRule{Source: s}
	bad := func(why string) (FieldRule, error) {
		return FieldRule{}, fmt.Errorf("field path %q: %s", src, why)
	}
	for s != "" {
		switch {
		case strings.HasPrefix(s, "[*]") || strings.HasPrefix(s, "[]"):
			fr.segs = append(fr.segs, seg{list: true})
			s = s[strings.Index(s, "]")+1:]
		case strings.HasPrefix(s, `["`):
			end := strings.Index(s, `"]`)
			if end < 0 {
				return bad(`missing "]`)
			}
			fr.segs = append(fr.segs, seg{key: s[2:end]})
			s = s[end+2:]
		case strings.HasPrefix(s, "."):
			s = s[1:]
			if s == "" || s[0] == '.' || s[0] == '[' {
				return bad("empty segment")
			}
			continue
		default:
			end := strings.IndexAny(s, ".[")
			if end < 0 {
				end = len(s)
			}
			fr.segs = append(fr.segs, seg{key: s[:end]})
			s = s[end:]
		}
		if s != "" && s[0] != '.' && s[0] != '[' {
			return bad("expected . or [ after a segment")
		}
	}
	if len(fr.segs) == 0 {
		return bad("empty")
	}
	if fr.segs[len(fr.segs)-1].list {
		return bad("the last segment must name a key, not [*]")
	}
	for _, sg := range fr.segs {
		if _, err := path.Match(sg.key, ""); err != nil {
			return bad(err.Error())
		}
	}
	if fr.segs[0].key == "kind" || fr.segs[0].key == "apiVersion" || (len(fr.segs) == 2 && fr.segs[0].key == "metadata" && (fr.segs[1].key == "name" || fr.segs[1].key == "namespace" || fr.segs[1].key == "*")) {
		return bad("removing an object's kind, apiVersion, name or namespace would make it unreadable; remove the object instead")
	}
	return fr, nil
}

// FieldRules is a compiled list.
type FieldRules []FieldRule

// ParseFieldRules compiles a list of paths.
func ParseFieldRules(paths []string) (FieldRules, error) {
	var out FieldRules
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		fr, err := ParseFieldRule(p)
		if err != nil {
			return nil, err
		}
		out = append(out, fr)
	}
	return out, nil
}

func match(glob, key string) bool {
	if glob == key {
		return true
	}
	ok, _ := path.Match(glob, key)
	return ok
}

func (fr FieldRule) apply(v any, i int) int {
	sg := fr.segs[i]
	last := i == len(fr.segs)-1
	n := 0
	switch t := v.(type) {
	case map[string]any:
		if sg.list {
			return 0
		}
		for k, val := range t {
			if !match(sg.key, k) {
				continue
			}
			if last {
				delete(t, k)
				n++
			} else {
				n += fr.apply(val, i+1)
			}
		}
	case []any:
		if sg.list {
			for _, el := range t {
				n += fr.apply(el, i+1)
			}
		}
	}
	return n
}

// Apply removes every matching field from obj in place and returns how many
// were removed per rule. A last-applied-configuration annotation is a full
// copy of the object, so the rules apply inside it too.
func (rs FieldRules) Apply(obj map[string]any) map[string]int {
	if len(rs) == 0 || obj == nil {
		return nil
	}
	counts := map[string]int{}
	for _, fr := range rs {
		if n := fr.apply(obj, 0); n > 0 {
			counts[fr.Source] += n
		}
	}
	if md, ok := obj["metadata"].(map[string]any); ok {
		if ann, ok := md["annotations"].(map[string]any); ok {
			if la, ok := ann[LastApplied].(string); ok {
				var inner map[string]any
				if json.Unmarshal([]byte(la), &inner) == nil {
					removed := 0
					for _, fr := range rs {
						if n := fr.apply(inner, 0); n > 0 {
							counts[fr.Source] += n
							removed += n
						}
					}
					if removed > 0 {
						b, _ := json.Marshal(inner)
						ann[LastApplied] = string(b)
					}
				}
			}
		}
	}
	return counts
}

// DropFields makes the Redactor remove these paths from every object it
// scrubs. Removals are counted in the redaction report as drop-field-N.
func (r *Redactor) DropFields(paths []string) error {
	rs, err := ParseFieldRules(paths)
	if err != nil {
		return err
	}
	r.fields = rs
	return nil
}

func (r *Redactor) dropFields(loc string, obj map[string]any) {
	if len(r.fields) == 0 {
		return
	}
	counts := r.fields.Apply(obj)
	for i, fr := range r.fields {
		for n := counts[fr.Source]; n > 0; n-- {
			r.count(loc, fmt.Sprintf("drop-field-%d", i+1))
		}
	}
}
