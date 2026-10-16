// Package meshdiag understands diagnostic output from Envoy,
// ztunnel and agentgateway config dumps, istiod debug endpoints and
// certificates. The desk's proxies view, the fbpc helper and the analyzers
// all read dumps through it.
package meshdiag

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cert is a workload or root certificate seen in a proxy.
type Cert struct {
	Identity  string    `json:"identity"`
	Serial    string    `json:"serial,omitempty"`
	Root      bool      `json:"root,omitempty"`
	State     string    `json:"state,omitempty"`
	NotAfter  time.Time `json:"notAfter"`
	DaysLeft  int       `json:"daysLeft"`
	HoursLeft float64   `json:"hoursLeft"`
}

// Table is a titled grid the UI and fbpc render as-is.
type Table struct {
	Title   string     `json:"title"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// Summary describes one component's captured dumps.
type Summary struct {
	Component string         `json:"component"`
	Counts    map[string]int `json:"counts"`
	Tables    []Table        `json:"tables"`
	Certs     []Cert         `json:"certs"`
	Notes     []string       `json:"notes,omitempty"`
}

func m(v any) map[string]any { x, _ := v.(map[string]any); return x }
func l(v any) []any          { x, _ := v.([]any); return x }
func s(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func decode(b []byte) any {
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

func days(t, ref time.Time) int { return int(t.Sub(ref).Hours() / 24) }
func hours(t, ref time.Time) float64 {
	return float64(int(t.Sub(ref).Hours()*10)) / 10
}

// Summarise builds the summary for a component from its captured files.
func Summarise(component string, files map[string][]byte, ref time.Time) Summary {
	sum := Summary{Component: component, Counts: map[string]int{}}
	switch component {
	case "ztunnel":
		ztunnel(&sum, decode(files["config_dump.json"]), ref)
	case "agentgateway":
		agentgateway(&sum, decode(files["config_dump.json"]))
	case "envoy":
		envoy(&sum, files, ref)
	case "istiod":
		istiod(&sum, files)
	case "controller":
		sum.Notes = append(sum.Notes, "Controllers are captured as metrics only. Their krt snapshot spans every namespace and can hold Secret contents, so the collector never reads it.")
	}
	if len(files) == 0 {
		sum.Notes = append(sum.Notes, "No files captured for this component.")
	}
	sort.Slice(sum.Certs, func(i, j int) bool { return sum.Certs[i].NotAfter.Before(sum.Certs[j].NotAfter) })
	// Always lists, never null: the UI iterates these.
	if sum.Certs == nil {
		sum.Certs = []Cert{}
	}
	if sum.Tables == nil {
		sum.Tables = []Table{}
	}
	if sum.Notes == nil {
		sum.Notes = []string{}
	}
	for i := range sum.Tables {
		if sum.Tables[i].Rows == nil {
			sum.Tables[i].Rows = [][]string{}
		}
	}
	return sum
}

// ZtunnelCerts reads certificates from a ztunnel config_dump.
func ZtunnelCerts(doc any, ref time.Time) []Cert {
	var out []Cert
	for _, c := range l(m(doc)["certificates"]) {
		cm := m(c)
		add := func(e any, root bool) {
			em := m(e)
			t, err := time.Parse(time.RFC3339, s(em["expirationTime"]))
			if err != nil {
				return
			}
			id, state := s(cm["identity"]), s(cm["state"])
			if root {
				// Roots are the trust bundle shared by every identity, not the workload's own.
				id, state = "root CA serial "+s(em["serialNumber"]), ""
			}
			out = append(out, Cert{Identity: id, Serial: s(em["serialNumber"]), Root: root, State: state, NotAfter: t, DaysLeft: days(t, ref), HoursLeft: hours(t, ref)})
		}
		if chain := l(cm["certChain"]); len(chain) > 0 {
			add(chain[0], false) // the leaf; intermediates share the root's lifetime
		} else if st := s(cm["state"]); st != "" {
			// No certificate yet (or signing failed): keep the state, no expiry.
			out = append(out, Cert{Identity: s(cm["identity"]), State: st})
		}
		for _, r := range l(cm["rootCerts"]) {
			add(r, true)
		}
	}
	return dedupe(out)
}

// EnvoyCerts reads Envoy's /certs output.
func EnvoyCerts(b []byte, ref time.Time) []Cert {
	var out []Cert
	for _, c := range l(m(decode(b))["certificates"]) {
		cm := m(c)
		for key, root := range map[string]bool{"cert_chain": false, "ca_cert": true} {
			for _, e := range l(cm[key]) {
				em := m(e)
				t, err := time.Parse(time.RFC3339, s(em["expiration_time"]))
				if err != nil {
					continue
				}
				id := ""
				for _, san := range l(em["subject_alt_names"]) {
					if u := s(m(san)["uri"]); u != "" {
						id = u
					} else if d := s(m(san)["dns"]); d != "" && id == "" {
						id = d
					}
				}
				if id == "" {
					id = s(em["subject"])
				}
				if id == "" && root {
					id = "root CA serial " + s(em["serial_number"])
				}
				out = append(out, Cert{Identity: id, Serial: s(em["serial_number"]), Root: root, NotAfter: t, DaysLeft: days(t, ref), HoursLeft: hours(t, ref)})
			}
		}
	}
	return dedupe(out)
}

func dedupe(in []Cert) []Cert {
	seen := map[string]bool{}
	var out []Cert
	for _, c := range in {
		k := c.Identity + "|" + c.Serial + "|" + strconv.FormatBool(c.Root)
		if !seen[k] {
			seen[k] = true
			out = append(out, c)
		}
	}
	return out
}

func ztunnel(sum *Summary, doc any, ref time.Time) {
	d := m(doc)
	if d == nil {
		sum.Notes = append(sum.Notes, "config_dump.json was not captured.")
		return
	}
	wl, svcs, pols := l(d["workloads"]), l(d["services"]), l(d["policies"])
	sum.Counts["workloads"], sum.Counts["services"], sum.Counts["policies"] = len(wl), len(svcs), len(pols)
	t := Table{Title: "Workloads", Columns: []string{"Namespace", "Name", "IPs", "Service account", "Node", "Protocol", "Waypoint"}}
	for _, w := range wl {
		wm := m(w)
		var ips []string
		for _, ip := range l(wm["workloadIps"]) {
			ips = append(ips, s(ip))
		}
		wp := ""
		if w := m(wm["waypoint"]); w != nil {
			wp = s(m(w["destination"])["hostname"]) + s(m(w["destination"])["address"])
			if wp == "" {
				wp = s(w)
			}
		}
		t.Rows = append(t.Rows, []string{s(wm["namespace"]), s(wm["name"]), strings.Join(ips, ","), s(wm["serviceAccount"]), s(wm["node"]), s(wm["protocol"]), wp})
	}
	sum.Tables = append(sum.Tables, t)
	st := Table{Title: "Services", Columns: []string{"Namespace", "Hostname", "VIPs", "Endpoints", "Waypoint"}}
	for _, v := range svcs {
		vm := m(v)
		var vips []string
		for _, ip := range l(vm["vips"]) {
			vips = append(vips, s(ip))
		}
		n := len(l(vm["endpoints"]))
		if eps := m(vm["endpoints"]); eps != nil {
			n = len(eps)
		}
		wp := ""
		if w := m(vm["waypoint"]); w != nil {
			wp = s(m(w["destination"])["hostname"])
		}
		st.Rows = append(st.Rows, []string{s(vm["namespace"]), s(vm["hostname"]), strings.Join(vips, ","), strconv.Itoa(n), wp})
	}
	sum.Tables = append(sum.Tables, st)
	if len(pols) > 0 {
		pt := Table{Title: "Authorization policies", Columns: []string{"Namespace", "Name", "Action", "Scope"}}
		for _, p := range pols {
			pm := m(p)
			pt.Rows = append(pt.Rows, []string{s(pm["namespace"]), s(pm["name"]), s(pm["action"]), s(pm["scope"])})
		}
		sum.Tables = append(sum.Tables, pt)
	}
	sum.Certs = ZtunnelCerts(doc, ref)
	sum.Counts["certificates"] = len(sum.Certs)
}

func agentgateway(sum *Summary, doc any) {
	d := m(doc)
	if d == nil {
		sum.Notes = append(sum.Notes, "config_dump.json was not captured.")
		return
	}
	binds := l(d["binds"])
	sum.Counts["binds"], sum.Counts["policies"], sum.Counts["backends"] = len(binds), len(l(d["policies"])), len(l(d["backends"]))
	sum.Counts["workloads"], sum.Counts["services"] = len(l(d["workloads"])), len(l(d["services"]))
	rt := Table{Title: "Listeners and routes", Columns: []string{"Bind", "Listener", "Protocol", "Route", "Matches", "Backends"}}
	routes := 0
	for _, b := range binds {
		bm := m(b)
		for _, lk := range sortedKeys(m(bm["listeners"])) {
			lm := m(m(bm["listeners"])[lk])
			rs := m(lm["routes"])
			if len(rs) == 0 {
				rt.Rows = append(rt.Rows, []string{s(bm["address"]), lk, s(lm["protocol"]), "(no routes)", "", ""})
			}
			for _, rk := range sortedKeys(rs) {
				routes++
				r := m(rs[rk])
				var matches, backends []string
				for _, mt := range l(r["matches"]) {
					matches = append(matches, compact(mt))
				}
				for _, be := range l(r["backends"]) {
					backends = append(backends, s(m(be)["backend"]))
				}
				rt.Rows = append(rt.Rows, []string{s(bm["address"]), lk, s(lm["protocol"]), s(r["namespace"]) + "/" + s(r["name"]), strings.Join(matches, " "), strings.Join(backends, ", ")})
			}
		}
	}
	sum.Counts["routes"] = routes
	sum.Tables = append(sum.Tables, rt)
	pt := Table{Title: "Policies", Columns: []string{"Kind", "Policy", "Target", "Type"}}
	for _, p := range l(d["policies"]) {
		pm := m(p)
		name := m(pm["name"])
		var types []string
		for k, v := range m(pm["policy"]) {
			for k2 := range m(v) {
				types = append(types, k+"."+k2)
			}
		}
		sort.Strings(types)
		pt.Rows = append(pt.Rows, []string{s(name["kind"]), s(name["namespace"]) + "/" + s(name["name"]), compact(pm["target"]), strings.Join(types, ", ")})
	}
	sum.Tables = append(sum.Tables, pt)
	bt := Table{Title: "Backends", Columns: []string{"Type", "Name", "Detail"}}
	for _, b := range l(d["backends"]) {
		for typ, v := range m(m(b)["backend"]) {
			vm := m(v)
			bt.Rows = append(bt.Rows, []string{typ, s(vm["namespace"]) + "/" + s(vm["name"]), trim(compact(vm["target"]), 160)})
		}
	}
	sum.Tables = append(sum.Tables, bt)
}

func envoy(sum *Summary, files map[string][]byte, ref time.Time) {
	doc := m(decode(files["config_dump.json"]))
	lt := Table{Title: "Listeners", Columns: []string{"Name", "Address", "Filter chains"}}
	ct := Table{Title: "Clusters", Columns: []string{"Name", "Type", "Endpoints"}}
	rt := Table{Title: "Routes", Columns: []string{"Route config", "Virtual host", "Domains"}}
	eps := map[string]int{}
	for _, c := range l(doc["configs"]) {
		cm := m(c)
		t := s(cm["@type"])
		switch {
		case strings.HasSuffix(t, "ListenersConfigDump"):
			for _, key := range []string{"static_listeners", "dynamic_listeners"} {
				for _, x := range l(cm[key]) {
					xm := m(x)
					lst := m(xm["listener"])
					if lst == nil {
						lst = m(m(xm["active_state"])["listener"])
					}
					addr := m(m(lst["address"])["socket_address"])
					lt.Rows = append(lt.Rows, []string{s(lst["name"]), fmt.Sprintf("%s:%s", s(addr["address"]), s(addr["port_value"])), strconv.Itoa(len(l(lst["filter_chains"])))})
				}
			}
		case strings.HasSuffix(t, "ClustersConfigDump"):
			for _, key := range []string{"static_clusters", "dynamic_active_clusters"} {
				for _, x := range l(cm[key]) {
					cl := m(m(x)["cluster"])
					typ := s(cl["type"])
					if typ == "" {
						typ = s(m(cl["cluster_type"])["name"])
					}
					ct.Rows = append(ct.Rows, []string{s(cl["name"]), typ, ""})
				}
			}
		case strings.HasSuffix(t, "EndpointsConfigDump"):
			for _, x := range l(cm["dynamic_endpoint_configs"]) {
				ec := m(m(x)["endpoint_config"])
				n := 0
				for _, le := range l(ec["endpoints"]) {
					n += len(l(m(le)["lb_endpoints"]))
				}
				eps[s(ec["cluster_name"])] = n
			}
		case strings.HasSuffix(t, "RoutesConfigDump"):
			for _, key := range []string{"static_route_configs", "dynamic_route_configs"} {
				for _, x := range l(cm[key]) {
					rc := m(m(x)["route_config"])
					for _, vh := range l(rc["virtual_hosts"]) {
						vm := m(vh)
						var doms []string
						for _, d := range l(vm["domains"]) {
							doms = append(doms, s(d))
						}
						rt.Rows = append(rt.Rows, []string{s(rc["name"]), s(vm["name"]), trim(strings.Join(doms, ", "), 160)})
					}
				}
			}
		}
	}
	for i, r := range ct.Rows {
		if n, ok := eps[r[0]]; ok {
			ct.Rows[i][2] = strconv.Itoa(n)
		}
	}
	sum.Counts["listeners"], sum.Counts["clusters"], sum.Counts["routes"] = len(lt.Rows), len(ct.Rows), len(rt.Rows)
	sum.Tables = append(sum.Tables, lt, ct, rt)
	if si := m(decode(files["server_info.json"])); si != nil {
		sum.Notes = append(sum.Notes, fmt.Sprintf("Envoy %s, state %s, uptime %s", s(si["version"]), s(si["state"]), s(si["uptime_current_epoch"])))
	}
	sum.Certs = EnvoyCerts(files["certs.json"], ref)
	sum.Counts["certificates"] = len(sum.Certs)
	if doc == nil {
		sum.Notes = append(sum.Notes, "config_dump.json was not captured.")
	}
}

// SyncRow is one proxy in istiod's /debug/syncz.
type SyncRow struct {
	Proxy   string   `json:"proxy"`
	Type    string   `json:"type"`
	Version string   `json:"version"`
	Stale   []string `json:"stale,omitempty"` // xDS types sent but not acknowledged
}

// Syncz parses istiod's /debug/syncz.
func Syncz(b []byte) []SyncRow {
	var out []SyncRow
	for _, p := range l(decode(b)) {
		pm := m(p)
		r := SyncRow{Proxy: s(pm["proxy"]), Type: s(pm["proxy_type"]), Version: s(pm["istio_version"])}
		for typ, v := range m(pm["resources"]) {
			vm := m(v)
			if sent := s(vm["sent"]); sent != "" && sent != s(vm["acked"]) {
				r.Stale = append(r.Stale, typ[strings.LastIndex(typ, "/")+1:])
			}
		}
		// Older istiod shapes report cds/lds/eds/rds as SYNCED or STALE.
		for _, k := range []string{"cluster_sent", "listener_sent", "route_sent", "endpoint_sent"} {
			if sent := s(pm[k]); sent != "" && sent != s(pm[strings.TrimSuffix(k, "_sent")+"_acked"]) {
				r.Stale = append(r.Stale, strings.TrimSuffix(k, "_sent"))
			}
		}
		sort.Strings(r.Stale)
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Proxy < out[j].Proxy })
	return out
}

func istiod(sum *Summary, files map[string][]byte) {
	rows := Syncz(files["syncz.json"])
	st := Table{Title: "Connected proxies (syncz)", Columns: []string{"Proxy", "Type", "Version", "Sync"}}
	stale := 0
	for _, r := range rows {
		state := "synced"
		if len(r.Stale) > 0 {
			state = "STALE: " + strings.Join(r.Stale, ", ")
			stale++
		}
		st.Rows = append(st.Rows, []string{r.Proxy, r.Type, r.Version, state})
	}
	sum.Counts["proxies"], sum.Counts["stale"] = len(rows), stale
	sum.Tables = append(sum.Tables, st)
	ct := Table{Title: "Configuration (configz)", Columns: []string{"Kind", "Namespace", "Name"}}
	for _, c := range l(decode(files["configz.json"])) {
		cm := m(c)
		md := m(cm["metadata"])
		ct.Rows = append(ct.Rows, []string{s(cm["kind"]), s(md["namespace"]), s(md["name"])})
	}
	sort.Slice(ct.Rows, func(i, j int) bool { return strings.Join(ct.Rows[i], "/") < strings.Join(ct.Rows[j], "/") })
	sum.Counts["configs"] = len(ct.Rows)
	sum.Tables = append(sum.Tables, ct)
	if mesh := m(decode(files["mesh.json"])); mesh != nil {
		sum.Notes = append(sum.Notes, fmt.Sprintf("Trust domain %s, root namespace %s", s(mesh["trustDomain"]), s(mesh["rootNamespace"])))
	}
}

func sortedKeys(mm map[string]any) []string {
	out := make([]string, 0, len(mm))
	for k := range mm {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func compact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
