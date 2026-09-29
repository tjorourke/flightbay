package server

import (
	"net/http"
	"sort"

	"github.com/tjorourke/flightbay/internal/store"
)

// overviewCharts feeds the three charts on the Overview page: findings grouped by
// rule, container restarts summed per namespace from pod status, and log volume
// over the capture window with the error lines as a second series.
func (s *Server) overviewCharts(w http.ResponseWriter, r *http.Request) {
	c := r.URL.Query().Get("cluster")

	type ruleCount struct {
		Rule     string `json:"rule"`
		Severity string `json:"severity"`
		Count    int    `json:"count"`
	}
	sevRank := map[string]int{"critical": 3, "warning": 2, "info": 1}
	rules := map[string]*ruleCount{}
	for _, f := range s.findingsList() {
		if c != "" && f.Cluster != c {
			continue
		}
		rc := rules[f.Rule]
		if rc == nil {
			rc = &ruleCount{Rule: f.Rule, Severity: f.Severity}
			rules[f.Rule] = rc
		}
		rc.Count++
		if sevRank[f.Severity] > sevRank[rc.Severity] {
			rc.Severity = f.Severity
		}
	}
	byRule := make([]ruleCount, 0, len(rules))
	for _, rc := range rules {
		byRule = append(byRule, *rc)
	}
	sort.Slice(byRule, func(i, j int) bool {
		if byRule[i].Count != byRule[j].Count {
			return byRule[i].Count > byRule[j].Count
		}
		return byRule[i].Rule < byRule[j].Rule
	})

	type nsCount struct {
		Namespace string `json:"namespace"`
		Restarts  int    `json:"restarts"`
		Pods      int    `json:"pods"`
	}
	restarts := map[string]*nsCount{}
	for _, cl := range s.WS.Clusters() {
		if c != "" && cl != c {
			continue
		}
		for _, o := range s.WS.List(cl, "", "pods", "", nil) {
			n := podRestarts(o)
			if n == 0 {
				continue
			}
			nc := restarts[o.Namespace]
			if nc == nil {
				nc = &nsCount{Namespace: o.Namespace}
				restarts[o.Namespace] = nc
			}
			nc.Restarts += n
			nc.Pods++
		}
	}
	byNS := make([]nsCount, 0, len(restarts))
	for _, nc := range restarts {
		byNS = append(byNS, *nc)
	}
	sort.Slice(byNS, func(i, j int) bool {
		if byNS[i].Restarts != byNS[j].Restarts {
			return byNS[i].Restarts > byNS[j].Restarts
		}
		return byNS[i].Namespace < byNS[j].Namespace
	})

	out := map[string]any{"rules": byRule, "restarts": byNS}
	if all, err := s.WS.SearchLogs(store.LogFilter{Cluster: c, Limit: 1}); err == nil {
		logs := map[string]any{"total": all.Total, "histogram": all.Histogram, "loading": all.Loading}
		if errs, err := s.WS.SearchLogs(store.LogFilter{Cluster: c, Level: "error", Limit: 1}); err == nil {
			logs["errors"] = errs.Histogram
			logs["errorTotal"] = errs.Total
		}
		out["logs"] = logs
	}
	writeJSON(w, 200, out)
}

// podRestarts sums restartCount over a pod's init and app containers.
func podRestarts(o *store.Object) int {
	st, _ := o.Obj["status"].(map[string]any)
	n := 0
	for _, k := range []string{"initContainerStatuses", "containerStatuses"} {
		list, _ := st[k].([]any)
		for _, x := range list {
			cs, _ := x.(map[string]any)
			if v, ok := cs["restartCount"].(float64); ok {
				n += int(v)
			}
		}
	}
	return n
}
