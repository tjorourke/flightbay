package server

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

// Rel is a relationship shown in the object inspector.
type Rel struct {
	Relation  string `json:"relation"`
	Cluster   string `json:"cluster"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func mm(v any) map[string]any { x, _ := v.(map[string]any); return x }
func ll(v any) []any          { x, _ := v.([]any); return x }
func ss(v any) string         { x, _ := v.(string); return x }

func rel(relation string, o *store.Object) Rel {
	return Rel{Relation: relation, Cluster: o.Cluster, Group: o.Group, Resource: o.Resource, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}
}

func findByKind(ws *store.Workspace, cluster, kind, ns, name string) *store.Object {
	for _, r := range ws.Resources(cluster) {
		if r.Kind != kind {
			continue
		}
		if o := ws.Get(cluster, r.Group, r.Resource, ns, name); o != nil {
			return o
		}
		if o := ws.Get(cluster, r.Group, r.Resource, "", name); o != nil {
			return o
		}
	}
	return nil
}

func selectorMatches(sel map[string]any, labels map[string]string) bool {
	if len(sel) == 0 {
		return false
	}
	for k, v := range sel {
		if labels[k] != ss(v) {
			return false
		}
	}
	return true
}

func uid(o *store.Object) string { return ss(mm(o.Obj["metadata"])["uid"]) }

func related(ws *store.Workspace, o *store.Object) []Rel {
	out := []Rel{}
	seen := map[string]bool{}
	add := func(r Rel) {
		k := r.Relation + "/" + r.Group + "/" + r.Resource + "/" + r.Namespace + "/" + r.Name
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	// Owners, walking up the chain (Pod -> ReplicaSet -> Deployment).
	cur := o
	for depth := 0; depth < 5 && cur != nil; depth++ {
		var next *store.Object
		for _, ref := range ll(mm(cur.Obj["metadata"])["ownerReferences"]) {
			r := mm(ref)
			if owner := findByKind(ws, o.Cluster, ss(r["kind"]), cur.Namespace, ss(r["name"])); owner != nil {
				add(rel("owner", owner))
				next = owner
			}
		}
		cur = next
	}
	id := uid(o)
	objs := ws.AllObjects(o.Cluster)
	labels := o.Labels()
	for _, x := range objs {
		if x == o {
			continue
		}
		if o.Namespaced && x.Namespaced && x.Namespace != o.Namespace {
			continue
		}
		for _, ref := range ll(mm(x.Obj["metadata"])["ownerReferences"]) {
			r := mm(ref)
			if (id != "" && ss(r["uid"]) == id) || (ss(r["kind"]) == o.Kind && ss(r["name"]) == o.Name) {
				add(rel("owns", x))
			}
		}
		spec := mm(x.Obj["spec"])
		// Services and controllers that select this pod.
		if o.Kind == "Pod" && (x.Kind == "Service" || x.Kind == "NetworkPolicy" || x.Kind == "PodDisruptionBudget") {
			sel := mm(spec["selector"])
			if x.Kind != "Service" {
				sel = mm(sel["matchLabels"])
				if x.Kind == "NetworkPolicy" {
					sel = mm(mm(spec["podSelector"])["matchLabels"])
				}
			}
			if selectorMatches(sel, labels) {
				add(rel("selected by", x))
			}
		}
		if o.Kind == "Service" && x.Kind == "Pod" && selectorMatches(mm(mm(o.Obj["spec"])["selector"]), x.Labels()) {
			add(rel("selects", x))
		}
		// Routes pointing at this Service or attached to this Gateway.
		if strings.HasSuffix(x.Kind, "Route") {
			for _, rule := range ll(spec["rules"]) {
				for _, b := range ll(mm(rule)["backendRefs"]) {
					bm := mm(b)
					kind := ss(bm["kind"])
					if kind == "" {
						kind = "Service"
					}
					if kind == o.Kind && ss(bm["name"]) == o.Name {
						add(rel("routed from", x))
					}
				}
			}
			for _, p := range ll(spec["parentRefs"]) {
				pm := mm(p)
				kind := ss(pm["kind"])
				if kind == "" {
					kind = "Gateway"
				}
				if kind == o.Kind && ss(pm["name"]) == o.Name {
					add(rel("attached route", x))
				}
			}
		}
		// Policies targeting this object.
		targets := ll(spec["targetRefs"])
		if t := mm(spec["targetRef"]); t != nil {
			targets = append(targets, t)
		}
		for _, t := range targets {
			tm := mm(t)
			if ss(tm["kind"]) == o.Kind && ss(tm["name"]) == o.Name {
				add(rel("targeted by", x))
			}
		}
	}
	// What this route points at.
	if strings.HasSuffix(o.Kind, "Route") {
		spec := mm(o.Obj["spec"])
		for _, rule := range ll(spec["rules"]) {
			for _, b := range ll(mm(rule)["backendRefs"]) {
				bm := mm(b)
				kind := ss(bm["kind"])
				if kind == "" {
					kind = "Service"
				}
				ns := ss(bm["namespace"])
				if ns == "" {
					ns = o.Namespace
				}
				if t := findByKind(ws, o.Cluster, kind, ns, ss(bm["name"])); t != nil {
					add(rel("routes to", t))
				} else {
					add(Rel{Relation: "routes to (not captured)", Cluster: o.Cluster, Resource: strings.ToLower(kind) + "s", Kind: kind, Namespace: ns, Name: ss(bm["name"])})
				}
			}
		}
		for _, p := range ll(spec["parentRefs"]) {
			pm := mm(p)
			kind := ss(pm["kind"])
			if kind == "" {
				kind = "Gateway"
			}
			ns := ss(pm["namespace"])
			if ns == "" {
				ns = o.Namespace
			}
			if t := findByKind(ws, o.Cluster, kind, ns, ss(pm["name"])); t != nil {
				add(rel("parent", t))
			}
		}
	}
	return out
}

// TimelineItem is one entry on the incident timeline.
type TimelineItem struct {
	Time      time.Time `json:"time"`
	Cluster   string    `json:"cluster"`
	Namespace string    `json:"namespace,omitempty"`
	Kind      string    `json:"kind"` // event, termination, rollout, error-spike
	Severity  string    `json:"severity"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail,omitempty"`
	Object    *Rel      `json:"object,omitempty"`
	LogQuery  string    `json:"logQuery,omitempty"`
	From      time.Time `json:"from,omitempty"`
	To        time.Time `json:"to,omitempty"`
}

func parseTime(vals ...any) time.Time {
	for _, v := range vals {
		if t, err := time.Parse(time.RFC3339Nano, ss(v)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func buildTimeline(ws *store.Workspace, cluster string) []TimelineItem {
	out := []TimelineItem{}
	clusters := ws.Clusters()
	if cluster != "" {
		clusters = []string{cluster}
	}
	for _, c := range clusters {
		for _, e := range ws.Events(c) {
			t := parseTime(e.Obj["lastTimestamp"], e.Obj["eventTime"], e.Obj["firstTimestamp"], mm(e.Obj["metadata"])["creationTimestamp"])
			if t.IsZero() {
				continue
			}
			io := mm(e.Obj["involvedObject"])
			sev := "info"
			if ss(e.Obj["type"]) == "Warning" {
				sev = "warning"
			}
			item := TimelineItem{Time: t, Cluster: c, Kind: "event", Severity: sev, Namespace: firstNonEmpty(ss(io["namespace"]), ss(mm(e.Obj["metadata"])["namespace"])), Title: fmt.Sprintf("%s: %s/%s", ss(e.Obj["reason"]), ss(io["kind"]), ss(io["name"])), Detail: ss(e.Obj["message"])}
			if o := findByKind(ws, c, ss(io["kind"]), ss(io["namespace"]), ss(io["name"])); o != nil {
				r := rel("", o)
				item.Object = &r
			}
			out = append(out, item)
		}
		for _, p := range ws.List(c, "", "pods", "", nil) {
			for _, cs := range ll(mm(p.Obj["status"])["containerStatuses"]) {
				cm := mm(cs)
				term := mm(mm(cm["lastState"])["terminated"])
				t := parseTime(term["finishedAt"])
				if t.IsZero() {
					continue
				}
				r := rel("", p)
				out = append(out, TimelineItem{Time: t, Cluster: c, Namespace: p.Namespace, Kind: "termination", Severity: "critical", Object: &r,
					Title:    fmt.Sprintf("%s/%s container %s exited", p.Namespace, p.Name, ss(cm["name"])),
					Detail:   fmt.Sprintf("%s, exit code %v", ss(term["reason"]), term["exitCode"]),
					LogQuery: fmt.Sprintf("pod:%s container:%s stream:previous", p.Name, ss(cm["name"]))})
			}
		}
		for _, rs := range ws.List(c, "apps", "replicasets", "", nil) {
			t := parseTime(mm(rs.Obj["metadata"])["creationTimestamp"])
			if t.IsZero() {
				continue
			}
			owner := ""
			for _, ref := range ll(mm(rs.Obj["metadata"])["ownerReferences"]) {
				owner = ss(mm(ref)["kind"]) + " " + ss(mm(ref)["name"])
			}
			r := rel("", rs)
			out = append(out, TimelineItem{Time: t, Cluster: c, Namespace: rs.Namespace, Kind: "rollout", Severity: "info", Object: &r,
				Title: fmt.Sprintf("ReplicaSet %s/%s created", rs.Namespace, rs.Name), Detail: strings.TrimSpace("Rollout of " + owner)})
		}
	}
	out = append(out, errorRuns(ws.ErrorMinutes(cluster, 3))...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

var _ = bundle.APIResource{}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

// errorRuns folds minutes with error lines into runs per namespace. A steady
// trickle of errors becomes one ranged entry instead of one per minute; a
// minute at least three times the run's median is called out as a spike.
func errorRuns(mins []store.ErrorMinute) []TimelineItem {
	sort.Slice(mins, func(i, j int) bool {
		if mins[i].Cluster+mins[i].Namespace != mins[j].Cluster+mins[j].Namespace {
			return mins[i].Cluster+mins[i].Namespace < mins[j].Cluster+mins[j].Namespace
		}
		return mins[i].Minute.Before(mins[j].Minute)
	})
	var out []TimelineItem
	flush := func(run []store.ErrorMinute) {
		if len(run) == 0 {
			return
		}
		first, last := run[0], run[len(run)-1]
		counts := make([]int, len(run))
		total := 0
		for i, m := range run {
			counts[i], total = m.Count, total+m.Count
		}
		sort.Ints(counts)
		median := counts[len(counts)/2]
		q := fmt.Sprintf("level:error ns:%s", first.Namespace)
		if len(run) == 1 {
			out = append(out, TimelineItem{Time: first.Minute, From: first.Minute, To: first.Minute.Add(time.Minute), Cluster: first.Cluster, Namespace: first.Namespace,
				Kind: "error-spike", Severity: "warning", Title: fmt.Sprintf("%d error log lines in %s", first.Count, first.Namespace), LogQuery: q})
			return
		}
		mins := int(last.Minute.Sub(first.Minute)/time.Minute) + 1
		out = append(out, TimelineItem{Time: first.Minute, From: first.Minute, To: last.Minute.Add(time.Minute), Cluster: first.Cluster, Namespace: first.Namespace,
			Kind: "error-spike", Severity: "warning", LogQuery: q,
			Title:  fmt.Sprintf("Errors in %s for %d minutes (%d lines, about %d a minute)", first.Namespace, mins, total, median),
			Detail: fmt.Sprintf("From %s to %s UTC", first.Minute.Format("15:04"), last.Minute.Add(time.Minute).Format("15:04"))})
		for _, m := range run {
			if len(run) >= 5 && m.Count >= 3*median && m.Count >= 10 {
				out = append(out, TimelineItem{Time: m.Minute, From: m.Minute, To: m.Minute.Add(time.Minute), Cluster: m.Cluster, Namespace: m.Namespace,
					Kind: "error-spike", Severity: "critical", LogQuery: q,
					Title: fmt.Sprintf("Error spike in %s: %d lines in one minute (usual %d)", m.Namespace, m.Count, median)})
			}
		}
	}
	var run []store.ErrorMinute
	for _, m := range mins {
		if len(run) > 0 {
			p := run[len(run)-1]
			// A gap of up to two quiet minutes keeps the run going.
			if p.Cluster != m.Cluster || p.Namespace != m.Namespace || m.Minute.Sub(p.Minute) > 3*time.Minute {
				flush(run)
				run = nil
			}
		}
		run = append(run, m)
	}
	flush(run)
	return out
}
