package server

import (
	"net/http"
	"strings"
	"sync"

	"github.com/tjorourke/flightbay/internal/store"
)

// suggestCache holds recent typeahead answers for the current revision of
// the workspace, so retyping or backspacing does not re-run a query.
type suggestCache struct {
	mu  sync.Mutex
	rev int64
	m   map[string][]store.Suggestion
}

func (c *suggestCache) get(rev int64, key string) ([]store.Suggestion, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rev != rev {
		return nil, false
	}
	v, ok := c.m[key]
	return v, ok
}

func (c *suggestCache) put(rev int64, key string, v []store.Suggestion) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rev != rev || c.m == nil || len(c.m) > 2000 {
		c.rev, c.m = rev, map[string][]store.Suggestion{}
	}
	c.m[key] = v
}

// suggest serves typeahead values for the query inputs:
//
//	GET /api/suggest?scope=objects|logs&field=label&prefix=app&q=kind:pod&cluster=
//
// An empty field lists the keys valid for the scope. The timeline and
// navigator build their suggestions in the browser from data they already
// hold, so those scopes return no values.
func (s *Server) suggest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope, field, prefix, rest, cluster := q.Get("scope"), strings.ToLower(q.Get("field")), q.Get("prefix"), q.Get("q"), q.Get("cluster")
	if scope == "" {
		scope = "objects"
	}
	out := map[string]any{"scope": scope, "field": field}
	var fn func(field, prefix, query, cluster string, limit int) ([]store.Suggestion, error)
	switch scope {
	case "objects":
		fn = s.WS.SuggestObjects
	case "logs":
		fn = s.WS.SuggestLogs
	case "timeline", "navigator":
		out["values"], out["clientSide"] = []store.Suggestion{}, true
		writeJSON(w, 200, out)
		return
	default:
		jerr(w, 400, "scope must be objects, logs, timeline or navigator")
		return
	}
	rev := s.WS.Revision()
	key := strings.Join([]string{scope, field, prefix, rest, cluster}, "\x00")
	vals, ok := s.sugg.get(rev, key)
	if !ok {
		var err error
		if vals, err = fn(field, prefix, rest, cluster, store.SuggestLimit); err != nil {
			jerr(w, 400, err.Error())
			return
		}
		s.sugg.put(rev, key, vals)
	}
	out["values"] = vals
	writeJSON(w, 200, out)
}
