package store

import (
	"fmt"
	"strings"

	"github.com/tjorourke/flightbay/internal/logs"
)

// objectsLatest is the objects table with one row per object. A reload of
// deferred objects can append a second copy: keep the newest.
const objectsLatest = `(SELECT * FROM objects QUALIFY row_number() OVER (PARTITION BY cluster, api_group, resource, namespace, name ORDER BY rowid DESC) = 1)`

// ObjectHit is one object found by SearchObjects.
type ObjectHit struct {
	Cluster   string            `json:"cluster"`
	Group     string            `json:"group"`
	Version   string            `json:"version"`
	Resource  string            `json:"resource"`
	Kind      string            `json:"kind"`
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels,omitempty"`
	Match     string            `json:"match"`             // name, label or content
	Snippet   string            `json:"snippet,omitempty"` // where the free text matched
}

// ObjectResult is a page of object search results.
type ObjectResult struct {
	Total   int            `json:"total"`
	ByKind  map[string]int `json:"byKind"`
	Results []ObjectHit    `json:"results"`
	Loading bool           `json:"loading"`
	Message string         `json:"message,omitempty"`
}

// SearchObjects finds captured objects of any type. Terms:
//
//	kind:pod (also po, pods, deployment.apps, httproute)   ns:payments   cluster:mesh1
//	name:checkout*   label:app=checkout   label:app   "free text" (name, labels, spec, status...)
//	-term negates. Space is AND.
func (w *Workspace) SearchObjects(query, cluster string, limit int) (*ObjectResult, error) {
	if limit <= 0 {
		limit = 200
	}
	where, args, texts, err := w.objectConds(query, nil)
	if err != nil {
		return nil, err
	}
	if cluster != "" {
		where = append(where, "cluster = ?")
		args = append(args, cluster)
	}
	if len(where) == 0 {
		where = []string{"TRUE"}
	}
	base := objectsLatest
	cond := strings.Join(where, " AND ")
	w.mu.RLock()
	loading := w.state.Phase == PhaseLoading || w.state.Phase == PhaseWaiting
	w.mu.RUnlock()
	res := &ObjectResult{ByKind: map[string]int{}, Results: []ObjectHit{}, Loading: loading}
	rows, err := w.ldb.db.Query("SELECT kind, count(*) FROM "+base+" WHERE "+cond+" GROUP BY kind", args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, err
		}
		res.ByKind[k] = n
		res.Total += n
	}
	rows.Close()

	// Rank: exact name, name contains the text, label match, then content.
	rank := "0"
	snippet := "''"
	var rargs []any
	if len(texts) > 0 {
		t := texts[0]
		rank = "CASE WHEN lower(name) = ? THEN 0 WHEN contains(lower(name), ?) THEN 1 WHEN contains(lower(CAST(labels AS VARCHAR)), ?) THEN 2 ELSE 3 END"
		rargs = []any{t, t, t}
		snippet = "substr(object, greatest(1, strpos(lower(object), ?) - 70), 170)"
		rargs = append(rargs, t)
	}
	q := fmt.Sprintf(`SELECT cluster, api_group, version, resource, kind, namespace, name, labels, %s AS rnk, %s AS snip
	  FROM %s WHERE %s ORDER BY rnk, kind, namespace, name LIMIT ?`, rank, snippet, base, cond)
	rows, err = w.ldb.db.Query(q, append(append(rargs, args...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h ObjectHit
		var labels any
		var rnk int
		var snip string
		if err := rows.Scan(&h.Cluster, &h.Group, &h.Version, &h.Resource, &h.Kind, &h.Namespace, &h.Name, &labels, &rnk, &snip); err != nil {
			return nil, err
		}
		if m := mapValue(labels); len(m) > 0 {
			h.Labels = map[string]string{}
			for k, v := range m {
				h.Labels[k] = fmt.Sprint(v)
			}
		}
		h.Match = []string{"name", "name", "label", "content"}[min(rnk, 3)]
		if len(texts) == 0 {
			h.Match = "filter"
		}
		if h.Match == "content" {
			h.Snippet = strings.TrimSpace(snip)
		}
		res.Results = append(res.Results, h)
	}
	if res.Total == 0 {
		if loading {
			res.Message = "No matching objects in the data loaded so far."
		} else {
			res.Message = "No captured objects match. Types the customer excluded or that failed to collect are not searchable; see Coverage."
		}
	}
	return res, rows.Err()
}

// objectConds turns an object query into SQL conditions over the objects
// table. keep, when set, drops terms whose key it rejects ("" is free text).
func (w *Workspace) objectConds(query string, keep func(key string) bool) (where []string, args []any, texts []string, err error) {
	toks, err := logs.Tokenize(query)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, t := range toks {
		neg := strings.HasPrefix(t, "-") && len(t) > 1
		if neg {
			t = t[1:]
		}
		key, val, isField := strings.Cut(t, ":")
		if !isField || strings.HasPrefix(t, `"`) || strings.Contains(key, " ") {
			key, val = "", t
		}
		if keep != nil && !keep(strings.ToLower(key)) {
			continue
		}
		val = strings.Trim(val, `"`)
		lv := strings.ToLower(val)
		var clause string
		var cargs []any
		switch strings.ToLower(key) {
		case "kind", "type", "k":
			var alts []string
			for _, c := range w.Clusters() {
				if r, ok := w.FindResource(c, lv); ok {
					alts = append(alts, "(resource = ? AND api_group = ?)")
					cargs = append(cargs, r.Resource, r.Group)
				}
			}
			alts = append(alts, "lower(kind) = ?", "resource = ?")
			cargs = append(cargs, lv, lv)
			clause = "(" + strings.Join(alts, " OR ") + ")"
		case "ns", "namespace":
			clause, cargs = likeOrEq("namespace", lv)
		case "cluster":
			clause, cargs = likeOrEq("cluster", lv)
		case "name":
			clause, cargs = likeOrEq("name", lv)
		case "label", "l":
			k, v, hasV := strings.Cut(val, "=")
			if hasV {
				clause, cargs = "coalesce(labels[?] = ?, false)", []any{k, v}
			} else {
				clause, cargs = "labels[?] IS NOT NULL", []any{k}
			}
		default:
			if key != "" {
				lv = strings.ToLower(t) // e.g. "image:nginx" searched as text
			}
			clause, cargs = "contains(lower(object), ?)", []any{lv}
			if !neg {
				texts = append(texts, lv)
			}
		}
		if neg {
			clause = "NOT coalesce(" + clause + ", false)"
		}
		where = append(where, clause)
		args = append(args, cargs...)
	}
	return where, args, texts, nil
}

func likeOrEq(col, v string) (string, []any) {
	if strings.HasSuffix(v, "*") {
		return "lower(" + col + ") LIKE ? ESCAPE '\\'", []any{strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSuffix(v, "*")) + "%"}
	}
	return "lower(" + col + ") = ?", []any{v}
}
