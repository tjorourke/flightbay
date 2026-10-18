package server

import (
	"net/http"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/reach"
)

func (s *Server) ambientPathOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	writeJSON(w, 200, ambient.RequestPathOptions(s.WS))
}
func (s *Server) ambientRequestPath(w http.ResponseWriter, r *http.Request) {
	var q ambient.PathRequest
	if !ambientBody(w, r, &q) {
		return
	}
	p, err := ambient.TraceRequestPath(s.WS, q)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	policies := []map[string]any{}
	if q.Source.Cluster == q.Destination.Cluster {
		for i, pod := range p.CallerPods {
			if i >= 8 {
				p.Notes = append(p.Notes, "Policy analysis is limited to the first eight caller Pods; other Pods may differ.")
				break
			}
			x, e := reach.Explain(s.WS, pod.Cluster, pod.Namespace+"/"+pod.Name, q.Destination.Namespace+"/"+q.Destination.Name)
			if e != nil {
				policies = append(policies, map[string]any{"pod": pod, "error": e.Error()})
				continue
			}
			policies = append(policies, map[string]any{"pod": pod, "explanation": x})
		}
	} else {
		p.Notes = append(p.Notes, "Cross-cluster policy evaluation is not established by the single-cluster evaluator. Inspect both sides' policies; no end-to-end allow decision is inferred.")
	}
	writeJSON(w, 200, map[string]any{"path": p, "policies": policies, "policyNote": "Policy analysis uses captured configuration per caller Pod. HTTP methods, path conditions, identities, JWT claims and external auth can require additional evidence. No request was sent."})
}
