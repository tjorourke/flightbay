package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/sign"
)

const cl = "mesh1"

func list(items ...map[string]any) map[string]any {
	its := make([]any, len(items))
	for i, it := range items {
		its[i] = it
	}
	return map[string]any{"apiVersion": "v1", "kind": "List", "items": its}
}

func obj(kind, ns, name string, extra map[string]any) map[string]any {
	md := map[string]any{"name": name}
	if ns != "" {
		md["namespace"] = ns
	}
	o := map[string]any{"kind": kind, "metadata": md}
	for k, v := range extra {
		o[k] = v
	}
	return o
}

// capture builds a small bundle with two namespaces: shop (kept) and
// secret-project (to be removed without naming it).
func capture(t *testing.T, signer *sign.PrivateKey) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := bundle.NewWriter(&buf, bundle.WriterOptions{Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	pol, _ := json.Marshal(map[string]any{"namespaces": []string{"shop", "secret-project"}})
	if err := w.WriteHeader(bundle.Header{BundleID: "orig-1", Case: "42", CaptureStart: time.Unix(0, 0).UTC(), Contexts: []string{cl}, Policy: pol}); err != nil {
		t.Fatal(err)
	}
	add := func(name string, v any) {
		t.Helper()
		var b []byte
		switch x := v.(type) {
		case string:
			b = []byte(x)
		default:
			b, _ = json.Marshal(x)
		}
		if err := w.Add(name, b); err != nil {
			t.Fatal(err)
		}
	}
	cov := func(c bundle.Coverage) {
		t.Helper()
		c.Cluster = cl
		if err := w.Coverage(c); err != nil {
			t.Fatal(err)
		}
	}
	nsP := bundle.ResourcePath(cl, "", "v1", "namespaces", 1)
	add(nsP, list(obj("Namespace", "", "shop", nil), obj("Namespace", "", "secret-project", nil)))
	cov(bundle.Coverage{ID: "res.namespaces", Type: bundle.CoverageResource, Version: "v1", Resource: "namespaces", Chunks: []string{nsP}, Items: 2, State: bundle.StateComplete})

	podP := bundle.ResourcePath(cl, "", "v1", "pods", 1)
	add(podP, list(
		obj("Pod", "shop", "web", map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "env": []any{map[string]any{"name": "DB", "value": "db.secret-project.svc"}}}}},
			"metadata": map[string]any{"name": "web", "namespace": "shop", "annotations": map[string]any{"corp.example/owner": "jane", "keep": "yes"}}}),
		obj("Pod", "shop", "debug", nil),
		obj("Pod", "secret-project", "model", nil),
	))
	cov(bundle.Coverage{ID: "res.pods", Type: bundle.CoverageResource, Version: "v1", Resource: "pods", Chunks: []string{podP}, Items: 3, State: bundle.StateComplete})

	cmP := bundle.ResourcePath(cl, "", "v1", "configmaps", 1)
	add(cmP, list(obj("ConfigMap", "shop", "settings", map[string]any{"data": map[string]any{"k": "v"}})))
	cov(bundle.Coverage{ID: "res.configmaps", Type: bundle.CoverageResource, Version: "v1", Resource: "configmaps", Chunks: []string{cmP}, Items: 1, State: bundle.StateComplete})
	cov(bundle.Coverage{ID: "res.networkpolicies", Type: bundle.CoverageResource, Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies", Items: 0, State: bundle.StateComplete})

	evP := bundle.EventsPath(cl, 1)
	add(evP, list(
		obj("Event", "shop", "e1", map[string]any{"involvedObject": map[string]any{"kind": "Pod", "namespace": "shop", "name": "web", "apiVersion": "v1"}, "message": "pulled"}),
		obj("Event", "shop", "e2", map[string]any{"involvedObject": map[string]any{"kind": "Pod", "namespace": "shop", "name": "debug", "apiVersion": "v1"}, "message": "started"}),
		obj("Event", "secret-project", "e3", map[string]any{"involvedObject": map[string]any{"kind": "Pod", "namespace": "secret-project", "name": "model"}}),
	))
	cov(bundle.Coverage{ID: "events", Type: bundle.CoverageEvents, Chunks: []string{evP}, Items: 3, State: bundle.StateComplete})

	webLog := bundle.LogPath(cl, "shop", "web", "app", "current", 1)
	add(webLog, "2026-09-28T10:00:00.000000001Z starting\n2026-09-28T10:00:01.000000001Z calling model.secret-project.svc for ACME-123456\n\n2026-09-28T10:00:02.000000001Z health check ok\n2026-09-28T10:00:03.000000001Z customer_id=99 bought\n")
	cov(bundle.Coverage{ID: "logs.shop.web", Type: bundle.CoverageLogs, Namespace: "shop", Pod: "web", Container: "app", Stream: "current", Chunks: []string{webLog}, Items: 4, State: bundle.StateComplete})
	dbgLog := bundle.LogPath(cl, "shop", "debug", "app", "current", 1)
	add(dbgLog, "2026-09-28T10:00:00Z noisy\n")
	cov(bundle.Coverage{ID: "logs.shop.debug", Type: bundle.CoverageLogs, Namespace: "shop", Pod: "debug", Container: "app", Stream: "current", Chunks: []string{dbgLog}, Items: 1, State: bundle.StateComplete})
	secLog := bundle.LogPath(cl, "secret-project", "model", "srv", "current", 1)
	add(secLog, "2026-09-28T10:00:00Z weights loaded\n")
	cov(bundle.Coverage{ID: "logs.secret.model", Type: bundle.CoverageLogs, Namespace: "secret-project", Pod: "model", Container: "srv", Stream: "current", Chunks: []string{secLog}, Items: 1, State: bundle.StateComplete})

	// A ztunnel dump in shop that knows about workloads in both namespaces.
	dump := bundle.SoloPath(cl, "proxies", "shop", "ztunnel-1", "config_dump.json")
	add(dump, map[string]any{"workloads": []any{
		map[string]any{"uid": "mesh1//Pod/shop/web", "namespace": "shop", "name": "web"},
		map[string]any{"uid": "mesh1//Pod/secret-project/model", "namespace": "secret-project", "name": "model"},
	}, "services": map[string]any{"web.shop.svc.cluster.local": map[string]any{}, "model.secret-project.svc.cluster.local": map[string]any{}}})
	cov(bundle.Coverage{ID: "diag.ztunnel", Type: bundle.CoverageDiagnostic, Namespace: "shop", Pod: "ztunnel-1", Resource: "config_dump.json", Chunks: []string{dump}, Items: 1, State: bundle.StateComplete})
	secDump := bundle.SoloPath(cl, "proxies", "secret-project", "waypoint-1", "config_dump.json")
	add(secDump, `{"x":1}`)
	cov(bundle.Coverage{ID: "diag.waypoint", Type: bundle.CoverageDiagnostic, Namespace: "secret-project", Pod: "waypoint-1", Resource: "config_dump.json", Chunks: []string{secDump}, Items: 1, State: bundle.StateComplete})

	flow := bundle.FlowsPath(cl, "hubble", "hubble.jsonl")
	add(flow, `{"flow":{"source":{"namespace":"shop"}}}`+"\n")
	cov(bundle.Coverage{ID: "flows.hubble", Type: bundle.CoverageFlows, Chunks: []string{flow}, Items: 1, State: bundle.StateComplete})

	add(bundle.CollectionLog, `{"cluster":"mesh1","operation":"list","target":"pods","result":"ok"}`+"\n"+`{"cluster":"mesh1","operation":"logs","target":"secret-project/model/srv","result":"ok"}`+"\n")
	add(bundle.RedactionName, map[string]any{"total": 3, "locations": map[string]any{
		"clusters/mesh1/logs/shop/web/app":             map[string]int{"jwt": 1},
		"clusters/mesh1/logs/secret-project/model/srv": map[string]int{"api-key": 2},
	}, "note": "Counts only."})
	if err := w.Close(bundle.Manifest{Complete: true}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func read(t *testing.T, data []byte, trusted []sign.PublicKey) (map[string][]byte, *bundle.Reader) {
	t.Helper()
	r, err := bundle.NewReader(bytes.NewReader(data), bundle.ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for {
		m, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[m.Name] = m.Data
	}
	if err := r.Verify(); err != nil {
		t.Fatalf("reviewed bundle verifies: %v", err)
	}
	return out, r
}

func TestApply(t *testing.T) {
	customer, _ := sign.Generate("ACME platform team")
	orig := capture(t, customer)
	webLog := bundle.LogPath(cl, "shop", "web", "app", "current", 1)
	plan := &Plan{
		Reviewer:   "Jane",
		ScrubNames: true,
		Remove: Removals{
			Namespaces: []NamespaceRef{{Namespace: "secret-project"}},
			Types:      []TypeRef{{Resource: "configmaps"}, {Group: "networking.k8s.io", Resource: "networkpolicies"}},
			Objects:    []ObjectRef{{Cluster: cl, Resource: "pods", Namespace: "shop", Name: "debug"}},
			LogStreams: []LogRef{{Namespace: "shop", Pod: "debug"}},
			// index 0 is "starting"; the empty line does not count
			LogLines: []LineRef{{Chunk: webLog, Index: 0, SHA256: LineSHA("2026-09-28T10:00:00.000000001Z starting")}},
		},
		DropLogLinesMatching: []string{`health check`},
		Redact:               []string{`ACME-[0-9]{6}`, `customer_id=\d+`},
		DropFields:           []string{`metadata.annotations["corp.example/*"]`},
	}
	var out bytes.Buffer
	led, err := Apply(context.Background(), bytes.NewReader(orig), &out, plan, Options{Signer: customer, Trusted: []sign.PublicKey{customer.Public()}, Tool: "test"})
	if err != nil {
		t.Fatal(err)
	}
	files, r := read(t, out.Bytes(), nil)

	// Signed again, with a new identity that points back at the original.
	if s := r.Signature([]sign.PublicKey{customer.Public()}); !s.Valid || !s.Trusted {
		t.Fatalf("reviewed bundle signature: %+v", s)
	}
	h := r.Header()
	if h.BundleID == "orig-1" || h.Review == nil || h.Review.OriginalBundleID != "orig-1" || h.Review.Reviewer != "Jane" {
		t.Fatalf("header: %+v %+v", h.BundleID, h.Review)
	}
	if led.Original.Signature == nil || !led.Original.Signature.Trusted || led.Original.ManifestSHA256 == "" {
		t.Fatalf("ledger records the original and its signature: %+v", led.Original)
	}

	// The hidden namespace is not named anywhere in what is sent.
	for name, data := range files {
		if strings.Contains(name, "secret-project") || strings.Contains(string(data), "secret-project") {
			t.Errorf("%s names the removed namespace: %.200s", name, data)
		}
	}
	if strings.Contains(string(files[bundle.LedgerName]), "debug") {
		t.Error("names are not disclosed by default")
	}

	// Counts.
	rm := led.Removed
	if rm.Namespaces != 1 || rm.Types != 2 || rm.ObjectsByType != 1 || rm.ObjectsByNamespace != 2 || rm.Objects != 4 {
		t.Errorf("objects removed: %+v", rm) // configmap by type; model pod and the Namespace by namespace; debug by name
	}
	if rm.Events != 2 { // e3 by namespace, e2 because its object was removed
		t.Errorf("events removed: %d", rm.Events)
	}
	if rm.LogLines != 4 || rm.LogStreams != 1 { // starting, health check, debug stream, secret stream
		t.Errorf("log lines %d, streams %d", rm.LogLines, rm.LogStreams)
	}
	if rm.DiagnosticFiles != 1 || rm.DiagnosticEntries == 0 {
		t.Errorf("diagnostics: %+v", rm)
	}
	if led.Redacted.Replacements != 2 || led.Redacted.FieldsRemoved != 1 || led.Redacted.NamesScrubbed == 0 {
		t.Errorf("redacted: %+v", led.Redacted)
	}
	if led.Kept.Objects != 2 || led.Kept.LogLines != 2 || led.Kept.Events != 1 {
		t.Errorf("kept: %+v", led.Kept)
	}
	if strings.Join(led.Types, ",") != "configmaps,networkpolicies.networking.k8s.io" {
		t.Errorf("types are always listed: %v", led.Types)
	}

	// Content.
	logText := string(files[webLog])
	if strings.Contains(logText, "starting") || strings.Contains(logText, "health check") || strings.Contains(logText, "ACME-123456") || strings.Contains(logText, "customer_id=99") {
		t.Errorf("log: %q", logText)
	}
	if !strings.Contains(logText, "[REDACTED] bought") || !strings.Contains(logText, "[REMOVED NAMESPACE]") {
		t.Errorf("log keeps the rest, redacted: %q", logText)
	}
	var pods map[string]any
	_ = json.Unmarshal(files[bundle.ResourcePath(cl, "", "v1", "pods", 1)], &pods)
	if items := pods["items"].([]any); len(items) != 1 {
		t.Fatalf("one pod kept, got %d", len(items))
	}
	ann := pods["items"].([]any)[0].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)
	if _, ok := ann["corp.example/owner"]; ok || ann["keep"] != "yes" {
		t.Errorf("field rule: %v", ann)
	}
	if _, ok := files[bundle.ResourcePath(cl, "", "v1", "configmaps", 1)]; ok {
		t.Error("removed type's chunk is dropped")
	}
	var rep map[string]any
	_ = json.Unmarshal(files[bundle.RedactionName], &rep)
	if rep["total"].(float64) != 1 {
		t.Errorf("redaction report drops locations in removed namespaces: %v", rep)
	}

	// Coverage.
	covs := map[string]bundle.Coverage{}
	for name, data := range files {
		if info, ok := bundle.ParsePath(name); ok && info.Kind == bundle.PathCoverage {
			var c bundle.Coverage
			_ = json.Unmarshal(data, &c)
			covs[c.ID] = c
		}
	}
	if c := covs["res.configmaps"]; c.State != bundle.StateRemoved || len(c.Chunks) != 0 {
		t.Errorf("removed type: %+v", c)
	}
	if c := covs["res.networkpolicies"]; c.State != bundle.StateRemoved {
		t.Errorf("an empty removed type is still marked removed: %+v", c)
	}
	if c := covs["res.pods"]; c.State != bundle.StateComplete || c.Trimmed == nil || c.Trimmed.Objects != 1 || c.Trimmed.ByNamespace != 1 || c.Items != 1 {
		t.Errorf("trimmed pods: %+v %+v", c, c.Trimmed)
	}
	if _, ok := covs["logs.secret.model"]; ok {
		t.Error("coverage inside a hidden namespace is dropped")
	}
	if _, ok := covs["logs.shop.debug"]; ok {
		t.Error("a removed stream's record names its pod, so it is dropped when names are hidden")
	}
	if _, ok := covs["diag.waypoint"]; ok {
		t.Error("diagnostic record inside a hidden namespace is dropped")
	}
	if c := covs["logs.shop.web"]; c.Trimmed == nil || c.Trimmed.Lines != 2 || c.Items != 2 {
		t.Errorf("trimmed log stream: %+v", c)
	}

	// Review the reviewed bundle again: the earlier ledger is kept.
	var twice bytes.Buffer
	led2, err := Apply(context.Background(), bytes.NewReader(out.Bytes()), &twice, &Plan{Remove: Removals{Events: true}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if led2.Previous == nil || led2.Previous.Reviewer != "Jane" || led2.Removed.Events != 1 {
		t.Errorf("second review: %+v", led2)
	}
}

func TestDiscloseNames(t *testing.T) {
	orig := capture(t, nil)
	var out bytes.Buffer
	led, err := Apply(context.Background(), bytes.NewReader(orig), &out, &Plan{DiscloseNames: true, Remove: Removals{
		Namespaces: []NamespaceRef{{Namespace: "secret-project"}},
		Objects:    []ObjectRef{{Resource: "pods", Namespace: "shop", Name: "debug"}},
		LogStreams: []LogRef{{Pod: "debug"}},
	}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(led.Namespaces, ",") != "mesh1/secret-project" || strings.Join(led.Objects, ",") != "mesh1/pods/shop/debug" || len(led.LogStreams) != 1 {
		t.Errorf("names listed: %+v", led)
	}
	files, _ := read(t, out.Bytes(), nil)
	var c bundle.Coverage
	_ = json.Unmarshal(files[bundle.CoveragePath(cl, "logs.shop.debug")], &c)
	if c.State != bundle.StateRemoved {
		t.Errorf("with names disclosed, the stream's record says it was removed: %+v", c)
	}
}

func TestRefusesDamagedOriginal(t *testing.T) {
	orig := capture(t, nil)
	cut := orig[:len(orig)-40] // truncated upload
	_, err := Apply(context.Background(), bytes.NewReader(cut), io.Discard, &Plan{}, Options{})
	if !errors.Is(err, ErrUnverified) {
		t.Fatalf("a damaged original is never re-signed: %v", err)
	}
}

func TestPlanValidation(t *testing.T) {
	for _, p := range []Plan{
		{Redact: []string{"("}},
		{Redact: []string{".*"}},
		{DropLogLinesMatching: []string{"x?"}},
		{DropFields: []string{"metadata.name"}},
		{Remove: Removals{Namespaces: []NamespaceRef{{}}}},
		{Remove: Removals{Objects: []ObjectRef{{Resource: "pods"}}}},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%+v should be refused", p)
		}
	}
	if !(&Plan{}).Empty() || (&Plan{Remove: Removals{Events: true}}).Empty() {
		t.Error("Empty")
	}
	if got := OutputName("dir/flightbay-1-2.tar.zst.age", false); got != "dir/flightbay-1-2-reviewed.tar.zst" {
		t.Error(got)
	}
	if got := OutputName("flightbay-1-2-reviewed.tar.zst", true); got != "flightbay-1-2-reviewed.tar.zst.age" {
		t.Error(got)
	}
}
