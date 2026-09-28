package posture

import (
	"math"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// Evaluate runs every check on every cluster and scores both areas.
func Evaluate(ws *store.Workspace) Report {
	var checks []Check
	agenticPresent, ambientPresent := false, false
	for _, cl := range ws.Clusters() {
		e := env{ws: ws, cluster: cl}
		if isAgentic(e) {
			agenticPresent = true
			checks = merge(checks, agenticChecks(e))
		}
		if isAmbient(e) {
			ambientPresent = true
			checks = merge(checks, ambientChecks(e))
		}
	}
	for i := range checks {
		finish(&checks[i])
	}
	sort.SliceStable(checks, func(i, j int) bool {
		a, b := checks[i], checks[j]
		if a.Area != b.Area {
			return a.Area < b.Area
		}
		if statusRank[a.Status] != statusRank[b.Status] {
			return statusRank[a.Status] < statusRank[b.Status]
		}
		return weight[a.Severity] > weight[b.Severity]
	})
	r := Report{Checks: checks}
	r.Areas = append(r.Areas, area(checks, "agentic", "Agentic (agentgateway, kagent, MCP)", agenticPresent, "no agentgateway, kagent or agentregistry objects in this capture"))
	r.Areas = append(r.Areas, area(checks, "ambient", "Istio ambient", ambientPresent, "no Istio ambient in this capture (no ztunnel or Istio resources)"))
	if r.Checks == nil {
		r.Checks = []Check{}
	}
	return r
}

var statusRank = map[Status]int{Fail: 0, Warn: 1, Pass: 2, NA: 3}

func isAgentic(e env) bool {
	for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaypolicies"}, {"agentgateway.dev", "agentgatewaybackends"},
		{"enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"}, {"kagent.dev", "agents"}} {
		if len(e.list(g.group, g.res)) > 0 {
			return true
		}
	}
	for _, gw := range e.list("gateway.networking.k8s.io", "gateways") {
		if strings.Contains(s(path(gw.Obj, "spec", "gatewayClassName")), "agentgateway") {
			return true
		}
	}
	return false
}

func isAmbient(e env) bool {
	for _, p := range e.ws.List(e.cluster, "", "pods", "", nil) {
		if strings.HasPrefix(p.Name, "ztunnel-") || p.Labels()["app"] == "ztunnel" {
			return true
		}
	}
	if ns, ok := e.nsLabels(); ok {
		for _, lb := range ns {
			if lb["istio.io/dataplane-mode"] == "ambient" {
				return true
			}
		}
	}
	return false
}

// merge combines per-cluster results of the same check.
func merge(all, add []Check) []Check {
	for _, c := range add {
		found := false
		for i := range all {
			if all[i].ID == c.ID {
				found = true
				all[i].Items = append(all[i].Items, c.Items...)
				if all[i].Status == NA && c.Status != NA {
					all[i].Status, all[i].Reason = c.Status, ""
				}
			}
		}
		if !found {
			all = append(all, c)
		}
	}
	return all
}

func finish(c *Check) {
	if c.Items == nil {
		c.Items = []Item{}
	}
	var got, total float64
	worst := Pass
	for _, it := range c.Items {
		if it.Status == NA {
			continue
		}
		total++
		switch it.Status {
		case Pass:
			got++
			c.Passed++
		case Warn:
			got += 0.5
		}
		if statusRank[it.Status] < statusRank[worst] {
			worst = it.Status
		}
	}
	c.Total = int(total)
	if total == 0 {
		c.Status = NA
		if c.Reason == "" {
			c.Reason = "nothing to evaluate"
		}
		return
	}
	c.Status = worst
	c.Score = got / total
	sort.SliceStable(c.Items, func(i, j int) bool { return statusRank[c.Items[i].Status] < statusRank[c.Items[j].Status] })
}

func grade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 75:
		return "B"
	case score >= 60:
		return "C"
	case score >= 40:
		return "D"
	}
	return "F"
}

func area(checks []Check, id, title string, present bool, why string) AreaScore {
	a := AreaScore{Area: id, Title: title, Present: present, Categories: map[string]int{}, Counts: map[Status]int{}}
	if !present {
		a.Reason = why
		return a
	}
	var got, total float64
	cat := map[string][2]float64{}
	for _, c := range checks {
		if c.Area != id {
			continue
		}
		a.Counts[c.Status]++
		if c.Status == NA {
			continue
		}
		w := weight[c.Severity]
		got += w * c.Score
		total += w
		v := cat[c.Category]
		cat[c.Category] = [2]float64{v[0] + w*c.Score, v[1] + w}
	}
	if total == 0 {
		a.Reason = "no checks could be evaluated from this capture"
		return a
	}
	a.Score = int(math.Round(100 * got / total))
	a.Grade = grade(a.Score)
	for k, v := range cat {
		a.Categories[k] = int(math.Round(100 * v[0] / v[1]))
	}
	return a
}
