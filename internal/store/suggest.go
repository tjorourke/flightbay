package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/logs"
)

// Suggestion is one typeahead entry: a field key (when no field is given) or
// a value for a field, with how many captured objects or lines carry it.
type Suggestion struct {
	Value   string `json:"value"`
	Count   int    `json:"count"`
	Hint    string `json:"hint,omitempty"`
	Example string `json:"example,omitempty"`
	// More means the value is a stem, e.g. a label key, and the UI should
	// keep suggesting after inserting it rather than finish the term.
	More bool `json:"more,omitempty"`
}

// SuggestLimit caps how many suggestions a call returns.
const SuggestLimit = 50

// ObjectFieldKeys are the keys the object search understands.
func ObjectFieldKeys() []Suggestion {
	return []Suggestion{
		{Value: "kind", Hint: "resource type (also po, deploy, svc)", Example: "kind:pod"},
		{Value: "ns", Hint: "namespace", Example: "ns:istio-system"},
		{Value: "label", Hint: "label key or key=value", Example: "label:app=checkout"},
		{Value: "name", Hint: "object name, * for a prefix", Example: "name:checkout*"},
		{Value: "cluster", Hint: "cluster in the bundle", Example: "cluster:mesh1"},
	}
}

// LogFieldKeys are the fixed keys the log query understands. Structured
// fields parsed from JSON or key=value lines come on top, see SuggestLogs.
func LogFieldKeys() []Suggestion {
	return []Suggestion{
		{Value: "level", Hint: "error, warn, info, debug", Example: "level:error"},
		{Value: "ns", Hint: "namespace", Example: "ns:payments"},
		{Value: "pod", Hint: "pod name, * for a prefix", Example: "pod:checkout*"},
		{Value: "container", Hint: "container name", Example: "container:istio-proxy"},
		{Value: "workload", Hint: "Deployment, DaemonSet or StatefulSet name", Example: "workload:istiod"},
		{Value: "stream", Hint: "current, previous, init, ephemeral", Example: "stream:previous"},
		{Value: "since", Hint: "relative to the end of the capture", Example: "since:15m"},
		{Value: "node", Hint: "node the pod ran on", Example: "node:worker-1"},
		{Value: "scope", Hint: "logger scope (istio, envoy)", Example: "scope:xds"},
		{Value: "cluster", Hint: "cluster in the bundle", Example: "cluster:mesh1"},
	}
}

var objectFieldAlias = map[string]string{"type": "kind", "k": "kind", "namespace": "ns", "l": "label"}

func likePrefix(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(s)) + "%"
}

func querySuggestions(ctx context.Context, db *sql.DB, q string, args ...any) ([]Suggestion, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Suggestion{}
	for rows.Next() {
		var v, hint sql.NullString
		var n int
		if err := rows.Scan(&v, &n, &hint); err != nil {
			return nil, err
		}
		if v.String == "" {
			continue
		}
		s := v.String
		if len(s) > 160 {
			s = s[:160]
		}
		out = append(out, Suggestion{Value: s, Count: n, Hint: hint.String})
	}
	return out, rows.Err()
}

// SuggestObjects lists values for one object search field that start with
// prefix, narrowed by the structured terms (kind, ns, label...) of query.
// An empty field returns the keys. Values come from the captured objects.
func (w *Workspace) SuggestObjects(field, prefix, query, cluster string, limit int) ([]Suggestion, error) {
	field = strings.ToLower(field)
	if a, ok := objectFieldAlias[field]; ok {
		field = a
	}
	if field == "" {
		return ObjectFieldKeys(), nil
	}
	if limit <= 0 || limit > 200 {
		limit = SuggestLimit
	}
	// Free text scans every object's JSON: too slow to run per keystroke,
	// so only the structured terms narrow suggestions.
	where, args, _, err := w.objectConds(query, func(k string) bool {
		switch k {
		case "kind", "type", "k", "ns", "namespace", "cluster", "name", "label", "l":
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if cluster != "" {
		where = append(where, "cluster = ?")
		args = append(args, cluster)
	}
	where = append(where, "TRUE")
	cond := strings.Join(where, " AND ")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db := w.ldb.db
	p := likePrefix(prefix)
	switch field {
	case "kind":
		return querySuggestions(ctx, db, `SELECT lower(kind), count(*), string_agg(DISTINCT CASE WHEN api_group = '' THEN resource ELSE resource || '.' || api_group END, ', ')
		  FROM `+objectsLatest+` WHERE `+cond+` AND (lower(kind) LIKE ? ESCAPE '\' OR resource LIKE ? ESCAPE '\')
		  GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT ?`, append(args, p, p, limit)...)
	case "ns":
		return querySuggestions(ctx, db, `SELECT namespace, count(*), '' FROM `+objectsLatest+` WHERE `+cond+` AND namespace <> '' AND lower(namespace) LIKE ? ESCAPE '\'
		  GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT ?`, append(args, p, limit)...)
	case "cluster":
		return querySuggestions(ctx, db, `SELECT cluster, count(*), '' FROM `+objectsLatest+` WHERE `+cond+` AND lower(cluster) LIKE ? ESCAPE '\'
		  GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT ?`, append(args, p, limit)...)
	case "name":
		// Names that start with the prefix first, then names that contain it.
		c := "%" + strings.TrimSuffix(likePrefix(prefix), "%") + "%"
		return querySuggestions(ctx, db, `SELECT name, count(*), string_agg(DISTINCT kind, ', ') FROM `+objectsLatest+` WHERE `+cond+` AND lower(name) LIKE ? ESCAPE '\'
		  GROUP BY 1 ORDER BY bool_or(lower(name) LIKE ? ESCAPE '\') DESC, 2 DESC, 1 LIMIT ?`, append(args, c, p, limit)...)
	case "label":
		if k, vp, ok := strings.Cut(prefix, "="); ok {
			out, err := querySuggestions(ctx, db, `SELECT labels[?], count(*), '' FROM `+objectsLatest+` WHERE `+cond+` AND labels[?] IS NOT NULL AND lower(labels[?]) LIKE ? ESCAPE '\'
			  GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT ?`, append(append([]any{k}, args...), k, k, likePrefix(vp), limit)...)
			for i := range out {
				out[i].Value = k + "=" + out[i].Value
			}
			return out, err
		}
		out, err := querySuggestions(ctx, db, `SELECT k, count(*), CASE count(DISTINCT v) WHEN 1 THEN '1 value' ELSE count(DISTINCT v) || ' values' END FROM (
		    SELECT unnest(map_keys(labels)) AS k, unnest(map_values(labels)) AS v FROM `+objectsLatest+` WHERE `+cond+` AND labels IS NOT NULL)
		  WHERE lower(k) LIKE ? ESCAPE '\' GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT ?`, append(args, p, limit)...)
		for i := range out {
			out[i].More = true
		}
		return out, err
	}
	return []Suggestion{}, nil
}

var logColumns = map[string]string{"ns": "namespace", "namespace": "namespace", "pod": "pod", "container": "container", "cluster": "cluster",
	"stream": "stream", "scope": "scope", "node": "node", "level": "level", "workload": "workload", "deploy": "workload", "deployment": "workload"}

var streamHints = map[string]string{"current": "running container", "previous": "container before its last restart", "init": "init containers", "ephemeral": "debug containers"}

// structuredQuery keeps the field terms of a log query and drops free text,
// so suggestions narrow by what is cheap to evaluate.
func structuredQuery(q string) string {
	toks, err := logs.Tokenize(q)
	if err != nil {
		return ""
	}
	var keep []string
	for _, t := range toks {
		b := strings.TrimPrefix(t, "-")
		if k, _, ok := strings.Cut(b, ":"); ok && k != "" && !strings.HasPrefix(b, `"`) && !strings.ContainsAny(k, ` "`) {
			keep = append(keep, t)
		}
	}
	return strings.Join(keep, " ")
}

// SuggestLogs lists values for one log query field that start with prefix,
// with line counts, narrowed by the field terms of query. An empty field
// returns the fixed keys and the most common structured fields.
func (w *Workspace) SuggestLogs(field, prefix, query, cluster string, limit int) ([]Suggestion, error) {
	field = strings.ToLower(field)
	if limit <= 0 || limit > 200 {
		limit = SuggestLimit
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db := w.ldb.db
	if field == "" {
		out := LogFieldKeys()
		have := map[string]bool{"msg": true, "message": true, "time": true, "ts": true, "timestamp": true}
		for _, k := range out {
			have[k.Value] = true
		}
		// A sample keeps this fast on large captures; counts are indicative.
		extra, err := querySuggestions(ctx, db, `SELECT k, count(*), '' FROM (SELECT unnest(map_keys(fields)) AS k FROM (SELECT fields FROM log_lines WHERE fields IS NOT NULL LIMIT 200000))
		  GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 60`)
		if err != nil {
			return out, nil
		}
		for _, e := range extra {
			if have[strings.ToLower(e.Value)] || !validLogKey(e.Value) {
				continue
			}
			out = append(out, Suggestion{Value: e.Value, Count: e.Count, Hint: "structured field", Example: e.Value + ":…"})
		}
		return out, nil
	}
	if field == "since" {
		out := []Suggestion{}
		for _, d := range [][2]string{{"5m", "last 5 minutes"}, {"15m", "last 15 minutes"}, {"30m", "last 30 minutes"}, {"1h", "last hour"}, {"6h", "last 6 hours"}, {"24h", "last 24 hours"}} {
			if strings.HasPrefix(d[0], prefix) {
				out = append(out, Suggestion{Value: d[0], Hint: d[1] + " of the capture"})
			}
		}
		return out, nil
	}
	q, err := logs.ParseQuery(structuredQuery(query))
	if err != nil {
		return nil, err
	}
	w.mu.RLock()
	ref := w.logNowLocked()
	w.mu.RUnlock()
	where, args := q.SQL(ref)
	if cluster != "" {
		where += " AND cluster = ?"
		args = append(args, cluster)
	}
	p := likePrefix(prefix)
	if col, ok := logColumns[field]; ok {
		hint := "''"
		if col == "workload" {
			hint = "any_value(workload_kind)"
		}
		out, err := querySuggestions(ctx, db, `SELECT `+col+`, count(*), `+hint+` FROM log_lines WHERE `+where+` AND `+col+` <> '' AND lower(`+col+`) LIKE ? ESCAPE '\'
		  GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT ?`, append(args, p, limit)...)
		if col == "stream" {
			for i := range out {
				out[i].Hint = streamHints[out[i].Value]
			}
		}
		return out, err
	}
	if !validLogKey(field) {
		return []Suggestion{}, nil
	}
	// A structured field: rarer, and a map lookup on every line, so it has
	// the timeout above as a backstop.
	return querySuggestions(ctx, db, `SELECT fields[?] AS v, count(*), '' FROM log_lines WHERE fields IS NOT NULL AND `+where+` AND fields[?] IS NOT NULL AND lower(fields[?]) LIKE ? ESCAPE '\'
	  GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT ?`, append(append([]any{field}, args...), field, field, p, limit)...)
}

func validLogKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && (r == '.' || r == '-' || (r >= '0' && r <= '9')))
		if !ok {
			return false
		}
	}
	return true
}
