package server

import (
	"net/http"

	"github.com/tjorourke/flightbay/internal/rbac"
)

func (s *Server) rbacServiceAccount(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cluster := q.Get("cluster")
	namespace := q.Get("namespace")
	name := q.Get("name")
	if cluster == "" || namespace == "" || name == "" {
		jerr(w, 400, "cluster, namespace and name are required")
		return
	}
	writeJSON(w, 200, rbac.ExplainSA(s.WS, cluster, namespace, name))
}

func (s *Server) rbacWho(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	qq := rbac.WhoQuery{
		Cluster:     q.Get("cluster"),
		Namespace:   q.Get("namespace"),
		Verb:        q.Get("verb"),
		Group:       q.Get("group"),
		Resource:    q.Get("resource"),
		Subresource: q.Get("subresource"),
		Name:        q.Get("name"),
	}
	if qq.Cluster == "" || qq.Namespace == "" || qq.Verb == "" || qq.Resource == "" {
		jerr(w, 400, "cluster, namespace, verb and resource are required")
		return
	}
	if qq.Group == "" {
		qq.Group = ""
	}
	writeJSON(w, 200, rbac.WhoCan(s.WS, qq))
}
