package apiserver

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/util/jsonpath"

	"github.com/tjorourke/flightbay/internal/bundle"
)

func m(v any) map[string]any { x, _ := v.(map[string]any); return x }
func l(v any) []any          { x, _ := v.([]any); return x }
func s(v any) string         { x, _ := v.(string); return x }

func n(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	}
	return 0
}

func col(name, typ string, priority int32) metav1.TableColumnDefinition {
	return metav1.TableColumnDefinition{Name: name, Type: typ, Format: map[bool]string{true: "name"}[name == "Name"], Priority: priority}
}

// Age formats time since creation, measured at the snapshot time.
func Age(obj map[string]any, ref time.Time) string {
	ts, err := time.Parse(time.RFC3339, s(m(obj["metadata"])["creationTimestamp"]))
	if err != nil {
		return "<unknown>"
	}
	return duration.HumanDuration(ref.Sub(ts))
}

func name(obj map[string]any) string { return s(m(obj["metadata"])["name"]) }

func podStatus(obj map[string]any) (ready string, st string, restarts int64) {
	status := m(obj["status"])
	st = s(status["reason"])
	if st == "" {
		st = s(status["phase"])
	}
	total := len(l(m(obj["spec"])["containers"]))
	readyN := 0
	for _, c := range l(status["initContainerStatuses"]) {
		cm := m(c)
		restarts += n(cm["restartCount"])
		if w := s(m(m(cm["state"])["waiting"])["reason"]); w != "" && w != "PodInitializing" {
			st = "Init:" + w
		}
	}
	for _, c := range l(status["containerStatuses"]) {
		cm := m(c)
		restarts += n(cm["restartCount"])
		if b, _ := cm["ready"].(bool); b {
			readyN++
		}
		state := m(cm["state"])
		if w := s(m(state["waiting"])["reason"]); w != "" {
			st = w
		} else if t := m(state["terminated"]); t != nil {
			if r := s(t["reason"]); r != "" {
				st = r
			} else {
				st = fmt.Sprintf("ExitCode:%d", n(t["exitCode"]))
			}
		}
	}
	if s(m(obj["metadata"])["deletionTimestamp"]) != "" {
		st = "Terminating"
	}
	return fmt.Sprintf("%d/%d", readyN, total), st, restarts
}

func condition(conds []any, typ string) string {
	for _, c := range conds {
		if s(m(c)["type"]) == typ {
			return s(m(c)["status"])
		}
	}
	return "Unknown"
}

func ports(obj map[string]any) string {
	var out []string
	for _, p := range l(m(obj["spec"])["ports"]) {
		pm := m(p)
		v := fmt.Sprintf("%d", n(pm["port"]))
		if np := n(pm["nodePort"]); np > 0 {
			v += fmt.Sprintf(":%d", np)
		}
		proto := s(pm["protocol"])
		if proto == "" {
			proto = "TCP"
		}
		out = append(out, v+"/"+proto)
	}
	if len(out) == 0 {
		return "<none>"
	}
	return strings.Join(out, ",")
}

func orNone(v string) string {
	if v == "" {
		return "<none>"
	}
	return v
}

// crdColumns builds kubectl's columns for a custom resource from its CRD's
// additionalPrinterColumns. ok is false when the CRD defines none.
func crdColumns(r bundle.APIResource, crd map[string]any, ref time.Time) ([]metav1.TableColumnDefinition, func(map[string]any) []any, bool) {
	var apcs []any
	for _, v := range l(m(crd["spec"])["versions"]) {
		if s(m(v)["name"]) == r.Version {
			apcs = l(m(v)["additionalPrinterColumns"])
		}
	}
	if len(apcs) == 0 {
		return nil, nil, false
	}
	defs := []metav1.TableColumnDefinition{col("Name", "string", 0)}
	type getter func(map[string]any) any
	var gets []getter
	for _, a := range apcs {
		am := m(a)
		typ := s(am["type"])
		def := col(s(am["name"]), map[bool]string{true: "string", false: typ}[typ == "date"], int32(n(am["priority"])))
		def.Description = s(am["description"])
		defs = append(defs, def)
		jp := jsonpath.New(def.Name).AllowMissingKeys(true)
		if err := jp.Parse("{" + s(am["jsonPath"]) + "}"); err != nil {
			gets = append(gets, func(map[string]any) any { return "<error>" })
			continue
		}
		gets = append(gets, func(o map[string]any) any {
			res, err := jp.FindResults(o)
			if err != nil || len(res) == 0 || len(res[0]) == 0 {
				return "<none>"
			}
			var parts []string
			for _, v := range res[0] {
				switch x := v.Interface().(type) {
				case []any, map[string]any:
					b, _ := json.Marshal(x) // kubectl prints structured cells as JSON
					parts = append(parts, string(b))
				default:
					parts = append(parts, fmt.Sprint(x))
				}
			}
			out := strings.Join(parts, ",")
			if typ == "date" {
				if t, err := time.Parse(time.RFC3339, out); err == nil {
					return duration.HumanDuration(ref.Sub(t))
				}
			}
			return out
		})
	}
	return defs, func(o map[string]any) []any {
		row := []any{name(o)}
		for _, g := range gets {
			row = append(row, g(o))
		}
		return row
	}, true
}

// columns returns column definitions and a row function for a resource type.
// crd is the collected definition for custom resources, or nil.
func columns(r bundle.APIResource, crd map[string]any, ref time.Time) ([]metav1.TableColumnDefinition, func(map[string]any) []any) {
	age := func(o map[string]any) any { return Age(o, ref) }
	if crd != nil {
		if defs, cells, ok := crdColumns(r, crd, ref); ok {
			return defs, cells
		}
	}
	switch {
	case r.Group == "" && r.Resource == "pods":
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Ready", "string", 0), col("Status", "string", 0), col("Restarts", "integer", 0), col("Age", "string", 0), col("IP", "string", 1), col("Node", "string", 1)},
			func(o map[string]any) []any {
				ready, st, restarts := podStatus(o)
				return []any{name(o), ready, st, restarts, age(o), orNone(s(m(o["status"])["podIP"])), orNone(s(m(o["spec"])["nodeName"]))}
			}
	case r.Group == "apps" && r.Resource == "deployments":
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Ready", "string", 0), col("Up-to-date", "integer", 0), col("Available", "integer", 0), col("Age", "string", 0)},
			func(o map[string]any) []any {
				st := m(o["status"])
				return []any{name(o), fmt.Sprintf("%d/%d", n(st["readyReplicas"]), n(m(o["spec"])["replicas"])), n(st["updatedReplicas"]), n(st["availableReplicas"]), age(o)}
			}
	case r.Group == "apps" && (r.Resource == "replicasets" || r.Resource == "statefulsets"):
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Desired", "integer", 0), col("Current", "integer", 0), col("Ready", "integer", 0), col("Age", "string", 0)},
			func(o map[string]any) []any {
				st := m(o["status"])
				return []any{name(o), n(m(o["spec"])["replicas"]), n(st["replicas"]), n(st["readyReplicas"]), age(o)}
			}
	case r.Group == "apps" && r.Resource == "daemonsets":
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Desired", "integer", 0), col("Current", "integer", 0), col("Ready", "integer", 0), col("Available", "integer", 0), col("Age", "string", 0)},
			func(o map[string]any) []any {
				st := m(o["status"])
				return []any{name(o), n(st["desiredNumberScheduled"]), n(st["currentNumberScheduled"]), n(st["numberReady"]), n(st["numberAvailable"]), age(o)}
			}
	case r.Group == "" && r.Resource == "services":
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Type", "string", 0), col("Cluster-IP", "string", 0), col("External-IP", "string", 0), col("Port(s)", "string", 0), col("Age", "string", 0)},
			func(o map[string]any) []any {
				spec := m(o["spec"])
				var ext []string
				for _, i := range l(m(m(o["status"])["loadBalancer"])["ingress"]) {
					ext = append(ext, s(m(i)["ip"])+s(m(i)["hostname"]))
				}
				t := s(spec["type"])
				if t == "" {
					t = "ClusterIP"
				}
				return []any{name(o), t, orNone(s(spec["clusterIP"])), orNone(strings.Join(ext, ",")), ports(o), age(o)}
			}
	case r.Group == "" && r.Resource == "nodes":
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Status", "string", 0), col("Roles", "string", 0), col("Age", "string", 0), col("Version", "string", 0)},
			func(o map[string]any) []any {
				st := "NotReady"
				if condition(l(m(o["status"])["conditions"]), "Ready") == "True" {
					st = "Ready"
				}
				var roles []string
				for k := range m(m(o["metadata"])["labels"]) {
					if strings.HasPrefix(k, "node-role.kubernetes.io/") {
						roles = append(roles, strings.TrimPrefix(k, "node-role.kubernetes.io/"))
					}
				}
				sort.Strings(roles)
				return []any{name(o), st, orNone(strings.Join(roles, ",")), age(o), s(m(m(o["status"])["nodeInfo"])["kubeletVersion"])}
			}
	case r.Group == "" && r.Resource == "namespaces":
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Status", "string", 0), col("Age", "string", 0)},
			func(o map[string]any) []any { return []any{name(o), s(m(o["status"])["phase"]), age(o)} }
	case r.Group == "" && r.Resource == "configmaps":
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Data", "integer", 0), col("Age", "string", 0)},
			func(o map[string]any) []any {
				return []any{name(o), int64(len(m(o["data"])) + len(m(o["binaryData"]))), age(o)}
			}
	case r.Group == "" && r.Resource == "secrets":
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Type", "string", 0), col("Data", "integer", 0), col("Age", "string", 0)},
			func(o map[string]any) []any {
				return []any{name(o), s(o["type"]), int64(len(l(m(o["x-flightbay"])["keys"]))), age(o)}
			}
	case r.Group == "" && r.Resource == "events":
		return []metav1.TableColumnDefinition{col("Last Seen", "string", 0), col("Type", "string", 0), col("Reason", "string", 0), col("Object", "string", 0), col("Message", "string", 0)},
			func(o map[string]any) []any {
				last := s(o["lastTimestamp"])
				if last == "" {
					last = s(o["eventTime"])
				}
				seen := "<unknown>"
				if t, err := time.Parse(time.RFC3339, last); err == nil {
					seen = duration.HumanDuration(ref.Sub(t))
				}
				io := m(o["involvedObject"])
				return []any{seen, s(o["type"]), s(o["reason"]), strings.ToLower(s(io["kind"])) + "/" + s(io["name"]), s(o["message"])}
			}
	case r.Group == "gateway.networking.k8s.io" && r.Resource == "gateways":
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Class", "string", 0), col("Address", "string", 0), col("Programmed", "string", 0), col("Age", "string", 0)},
			func(o map[string]any) []any {
				addr := ""
				if a := l(m(o["status"])["addresses"]); len(a) > 0 {
					addr = s(m(a[0])["value"])
				}
				return []any{name(o), s(m(o["spec"])["gatewayClassName"]), addr, condition(l(m(o["status"])["conditions"]), "Programmed"), age(o)}
			}
	case r.Group == "gateway.networking.k8s.io" && strings.HasSuffix(r.Resource, "routes"):
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Hostnames", "string", 0), col("Age", "string", 0)},
			func(o map[string]any) []any {
				var hs []string
				for _, h := range l(m(o["spec"])["hostnames"]) {
					hs = append(hs, s(h))
				}
				return []any{name(o), orNone(strings.Join(hs, ",")), age(o)}
			}
	case r.Group == "apiextensions.k8s.io" && r.Resource == "customresourcedefinitions":
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Created At", "string", 0)},
			func(o map[string]any) []any { return []any{name(o), s(m(o["metadata"])["creationTimestamp"])} }
	default:
		return []metav1.TableColumnDefinition{col("Name", "string", 0), col("Age", "string", 0)},
			func(o map[string]any) []any { return []any{name(o), age(o)} }
	}
}

// Columns exposes the table printers to the desk UI so it renders the same
// columns kubectl shows.
func Columns(r bundle.APIResource, crd map[string]any, ref time.Time) ([]string, func(map[string]any) []any) {
	defs, cells := columns(r, crd, ref)
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names, cells
}
