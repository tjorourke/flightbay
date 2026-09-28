package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"
)

// The activity, traffic and explain helpers read the desk API into local
// types: the collector binary links this package, and must not link the
// evaluator (and DuckDB with it).

type cliFinding struct {
	Severity, Title, Detail, Fix, Source, Docs string
	Items                                      []struct{ Target, Class, Access, Note string }
	Advice                                     []string
}

func printFindings(w io.Writer, fs []cliFinding, showFix bool) {
	if len(fs) == 0 {
		return
	}
	fmt.Fprintln(w, "\nFindings:")
	for _, f := range fs {
		fmt.Fprintf(w, "  [%s] %s\n      %s\n", strings.ToUpper(f.Severity), f.Title, f.Detail)
		for i, it := range f.Items {
			if i == 8 {
				fmt.Fprintf(w, "      ... and %d more\n", len(f.Items)-8)
				break
			}
			fmt.Fprintf(w, "      - %s", it.Target)
			for _, x := range []string{it.Class, it.Access, it.Note} {
				if x != "" {
					fmt.Fprintf(w, "  (%s)", x)
				}
			}
			fmt.Fprintln(w)
		}
		for _, a := range f.Advice {
			fmt.Fprintf(w, "      > %s\n", a)
		}
		if f.Source != "" {
			fmt.Fprintf(w, "      Guide: %s\n", f.Source)
		}
		if showFix && f.Fix != "" {
			fmt.Fprintf(w, "\n%s\n", indent(f.Fix, "      "))
		}
	}
}

func indent(s, p string) string {
	return p + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+p)
}

// Activity prints who called which tool, model or agent (the fbactivity
// helper). view is "", "tools", "models", "denials" or "changes".
func Activity(w io.Writer, base, token, view string) error {
	var r struct {
		Callers []struct {
			ID, Kind, Label, AgentType string
			Requests, Denied, Errors   int
			Tokens                     int64
			Anonymous                  bool
		}
		Targets []struct {
			ID, Kind, Label, Server, Provider string
			Requests, Denied, Errors          int
			InputTokens, OutputTokens         int64
			Cost                              float64
			Declared, Undeclared              bool
		}
		Edges []struct {
			From, To                  string
			Requests, Denied, Errors  int
			InputTokens, OutputTokens int64
			OutsideAllowlist          bool
		}
		Denials []struct {
			Caller, Target, Gateway, Route, Reason, Meaning, Look, Error string
			Count                                                        int
			Policies                                                     []struct {
				Ref      struct{ Kind, Namespace, Name string }
				Target   string
				Sections []string
				Matches  bool
			}
		}
		Changes *struct {
			Baseline                string
			Added                   []struct{ Server, Name, Description string }
			Removed                 []struct{ Server, Name, Description string }
			Described               []struct{ Server, Name, Before, After string }
			NewlyCalled, NewCallers []string
			Compared                bool
			BaselineNote            string
		}
		Totals struct {
			Requests, ToolCalls, LLMCalls, Denied, Errors, Anonymous int
			InputTokens, OutputTokens                                int64
			Cost                                                     float64
		}
		Coverage []struct {
			Gateway  string
			Requests int
			Waypoint bool
		}
		Findings []cliFinding
		Notes    []string
	}
	if err := deskGet(base, token, "/api/activity", url.Values{}, &r); err != nil {
		return err
	}
	callers, targets := map[string]string{}, map[string]string{}
	for _, c := range r.Callers {
		callers[c.ID] = c.Label
	}
	for _, t := range r.Targets {
		switch t.Kind {
		case "tool":
			targets[t.ID] = "tool " + t.Server + "/" + t.Label
		case "mcp":
			targets[t.ID] = "MCP " + t.Label + " (session, list)"
		case "model":
			targets[t.ID] = "model " + strings.TrimPrefix(t.Provider+"/", "/") + t.Label
		case "agent":
			targets[t.ID] = "agent " + t.Label
		default:
			targets[t.ID] = t.Label
		}
	}
	t := r.Totals
	fmt.Fprintf(w, "%d requests through %d gateways: %d tool calls, %d LLM calls, %d refused, %d failed, %d with no identity. Tokens: %d in, %d out",
		t.Requests, len(r.Coverage), t.ToolCalls, t.LLMCalls, t.Denied, t.Errors, t.Anonymous, t.InputTokens, t.OutputTokens)
	if t.Cost > 0 {
		fmt.Fprintf(w, ", cost %.4f", t.Cost)
	}
	fmt.Fprintln(w, ".")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	switch view {
	case "", "calls":
		fmt.Fprintln(tw, "\nCALLER\tTARGET\tCALLS\tREFUSED\tFAILED\tTOKENS\tNOTE")
		for _, e := range r.Edges {
			if strings.HasPrefix(e.To, "http:") {
				continue
			}
			note := ""
			if e.OutsideAllowlist {
				note = "not in the agent's tool list"
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%s\n", callers[e.From], targets[e.To], e.Requests, e.Denied, e.Errors, e.InputTokens+e.OutputTokens, note)
		}
	case "tools":
		fmt.Fprintln(tw, "\nTOOL\tSERVER\tCALLS\tREFUSED\tFAILED\tLISTED")
		for _, x := range r.Targets {
			if x.Kind != "tool" {
				continue
			}
			listed := "unknown"
			if x.Declared {
				listed = "yes"
			} else if x.Undeclared {
				listed = "NO"
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n", x.Label, x.Server, x.Requests, x.Denied, x.Errors, listed)
		}
	case "models":
		fmt.Fprintln(tw, "\nMODEL\tPROVIDER\tCALLS\tIN\tOUT\tCOST")
		for _, x := range r.Targets {
			if x.Kind == "model" {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%.4f\n", x.Label, x.Provider, x.Requests, x.InputTokens, x.OutputTokens, x.Cost)
			}
		}
		fmt.Fprintln(tw, "\nCALLER\tTOKENS\tCALLS")
		cs := r.Callers
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].Tokens > cs[j].Tokens })
		for _, c := range cs {
			if c.Tokens > 0 {
				fmt.Fprintf(tw, "%s\t%d\t%d\n", c.Label, c.Tokens, c.Requests)
			}
		}
	case "denials":
		tw.Flush()
		if len(r.Denials) == 0 {
			fmt.Fprintln(w, "\nNo refused calls in the captured gateway logs.")
		}
		for _, d := range r.Denials {
			fmt.Fprintf(w, "\n%d x %s -> %s  refused: %s  (gateway %s, route %s)\n  %s\n", d.Count, d.Caller, d.Target, d.Reason, d.Gateway, d.Route, d.Meaning)
			if d.Error != "" {
				fmt.Fprintf(w, "  Logged error: %s\n", d.Error)
			}
			for _, p := range d.Policies {
				mark := " "
				if p.Matches {
					mark = "*"
				}
				fmt.Fprintf(w, "  %s %s %s/%s on %s: %s\n", mark, p.Ref.Kind, p.Ref.Namespace, p.Ref.Name, p.Target, strings.Join(p.Sections, ", "))
			}
			if d.Look != "" {
				fmt.Fprintf(w, "  Next: %s\n", d.Look)
			}
		}
	case "changes":
		tw.Flush()
		c := r.Changes
		if c == nil {
			fmt.Fprintln(w, "\nNo baseline loaded. Open one with flightbay-desk open --baseline, or from the Compare page.")
			return nil
		}
		fmt.Fprintf(w, "\nSince %s:\n", c.Baseline)
		if c.BaselineNote != "" {
			fmt.Fprintf(w, "  %s\n", c.BaselineNote)
		}
		for _, x := range c.Described {
			fmt.Fprintf(w, "  CHANGED  %s/%s\n    was: %s\n    now: %s\n", x.Server, x.Name, x.Before, x.After)
		}
		for _, x := range c.Added {
			fmt.Fprintf(w, "  ADDED    %s/%s  %s\n", x.Server, x.Name, x.Description)
		}
		for _, x := range c.Removed {
			fmt.Fprintf(w, "  REMOVED  %s/%s\n", x.Server, x.Name)
		}
		for _, x := range c.NewlyCalled {
			fmt.Fprintf(w, "  NOT CALLED IN BASELINE  %s\n", x)
		}
		for _, x := range c.NewCallers {
			fmt.Fprintf(w, "  NEW PAIR                %s\n", x)
		}
	default:
		return fmt.Errorf("unknown view %q: use calls, tools, models, denials or changes", view)
	}
	tw.Flush()
	if view == "" || view == "calls" {
		printFindings(w, r.Findings, false)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "\nNote: %s\n", n)
	}
	return nil
}

// Traffic prints the ambient traffic map (the fbtraffic helper). flag keeps
// only edges with that flag (plaintext, unpoliced, denied, mismatch).
func Traffic(w io.Writer, base, token, flag, ns string, showFix bool) error {
	var r struct {
		Nodes []struct{ ID, Label string }
		Edges []struct {
			From, To, Verdict, TLS, Risk, Why, Via string
			Connections, Denied                    int
			Flags                                  []string
		}
		Totals struct {
			Edges, Connections, Plaintext, Unpoliced, Denied, Mismatch int
		}
		Findings []cliFinding
		Notes    []string
	}
	if err := deskGet(base, token, "/api/traffic", url.Values{}, &r); err != nil {
		return err
	}
	lab := map[string]string{}
	for _, n := range r.Nodes {
		lab[n.ID] = n.Label
	}
	t := r.Totals
	fmt.Fprintf(w, "%d edges, %d connections: %d plaintext inside the mesh, %d with no policy, %d with refusals, %d where logs and policy disagree.\n\n",
		t.Edges, t.Connections, t.Plaintext, t.Unpoliced, t.Denied, t.Mismatch)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RISK\tFROM\tTO\tCONNS\tREFUSED\tTLS\tPOLICY\tVIA\tWHY")
	for _, e := range r.Edges {
		if flag != "" && !contains(e.Flags, flag) {
			continue
		}
		if ns != "" && !strings.HasPrefix(lab[e.From], ns+"/") && !strings.HasPrefix(lab[e.To], ns+"/") {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n", e.Risk, lab[e.From], lab[e.To], e.Connections, e.Denied, e.TLS, e.Verdict, e.Via, e.Why)
	}
	tw.Flush()
	printFindings(w, r.Findings, showFix)
	for _, n := range r.Notes {
		fmt.Fprintf(w, "\nNote: %s\n", n)
	}
	fmt.Fprintln(w, "\nfbexplain <from-workload> <to-service> for why one edge is allowed or denied.")
	return nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Explain prints why from can or cannot reach to (the fbexplain helper).
func Explain(w io.Writer, base, token, cluster, from, to string) error {
	q := url.Values{"from": {from}, "to": {to}}
	if cluster != "" {
		q.Set("cluster", cluster)
	}
	req, err := http.NewRequest("GET", strings.TrimRight(base, "/")+"/api/explain?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach flightbay-desk at %s: %w", base, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 404 {
		var e struct {
			Error       string
			Suggestions []string
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			msg := e.Error
			if len(e.Suggestions) > 0 {
				msg += "\n  Did you mean: " + strings.Join(e.Suggestions, ", ")
			}
			return fmt.Errorf("%s", msg)
		}
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("flightbay-desk: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var x struct {
		From, To struct {
			Label, Principal, Waypoint string
			InMesh                     bool
		}
		Verdict, Layer, Summary, Fix, FixNote string
		Methods, Conditions, Advice           []string
		Steps                                 []struct {
			Point, Verdict, Detail, Backend string
			Policies                        []struct{ Kind, Namespace, Name string }
		}
		Observed *struct {
			Connections, Denied, HBONE, Plaintext int
			Agrees                                bool
			Note                                  string
		}
	}
	if err := json.Unmarshal(body, &x); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s\n\n", x.Summary)
	fmt.Fprintf(w, "From %s (%s, %s)\nTo   %s", x.From.Label, x.From.Principal, map[bool]string{true: "in the mesh", false: "not in the mesh"}[x.From.InMesh], x.To.Label)
	if x.To.Waypoint != "" {
		fmt.Fprintf(w, " (waypoint %s)", x.To.Waypoint)
	}
	fmt.Fprintf(w, "\n\nVerdict: %s", strings.ToUpper(x.Verdict))
	if len(x.Methods) > 0 {
		fmt.Fprintf(w, " (%s only)", strings.Join(x.Methods, ", "))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "\nChecks, in the order traffic meets them:")
	last := ""
	for i, s := range x.Steps {
		if s.Backend != "" && s.Backend != last {
			fmt.Fprintf(w, "  to pod of %s\n", s.Backend)
			last = s.Backend
		}
		fmt.Fprintf(w, "  %d. %-44s %s\n", i+1, s.Point, strings.ToUpper(s.Verdict))
		if s.Detail != "" {
			fmt.Fprintf(w, "     %s\n", s.Detail)
		}
		for _, p := range s.Policies {
			fmt.Fprintf(w, "     - %s %s/%s\n", p.Kind, p.Namespace, p.Name)
		}
	}
	for _, c := range x.Conditions {
		fmt.Fprintf(w, "\nNot evaluated: %s\n", c)
	}
	if o := x.Observed; o != nil {
		fmt.Fprintf(w, "\nIn the access logs: %d connections, %d refused, %d over HBONE, %d plaintext.", o.Connections, o.Denied, o.HBONE, o.Plaintext)
		if o.Note != "" {
			fmt.Fprintf(w, " %s.", o.Note)
		}
		fmt.Fprintln(w)
	}
	for _, a := range x.Advice {
		fmt.Fprintf(w, "\n%s\n", a)
	}
	if x.Fix != "" {
		fmt.Fprintf(w, "\nTo allow it (%s):\n\n%s\n", x.FixNote, indent(x.Fix, "  "))
	}
	return nil
}
