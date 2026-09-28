package collect

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/flows"
	"github.com/tjorourke/flightbay/internal/redact"
)

// portSession is a forward session that hands out a local port, as the real
// one does for the hubble CLI.
type portSession struct{ *fakeSession }

func (portSession) LocalPort(remote int) int {
	if remote == hubbleRelayPort {
		return 54245
	}
	return 0
}

type portForward struct{ fakeForward }

func (f *portForward) Open(ctx context.Context, ns, pod string, ports []int) (ForwardSession, error) {
	s, _ := f.fakeForward.Open(ctx, ns, pod, ports)
	return portSession{s.(*fakeSession)}, nil
}

func relayPod() *unstructured.Unstructured {
	p := soloPod("kube-system", "hubble-relay-7d9f8c-abcde", "hubble-relay", "quay.io/cilium/hubble-relay:v1.16.0")
	p.SetLabels(map[string]string{"k8s-app": "hubble-relay"})
	return p
}

const hubbleOut = `{"flow":{"verdict":"FORWARDED","IP":{"source":"10.244.1.7","destination":"10.244.2.9"},"l4":{"TCP":{"destination_port":8080}},"source":{"namespace":"payments","pod_name":"checkout-1"},"destination":{"namespace":"payments","pod_name":"ledger-1"},"Type":"L7","l7":{"type":"REQUEST","http":{"method":"GET","url":"http://ledger.payments/balance?access_token=` + canary + `"}},"is_reply":false}}
{"flow":{"verdict":"FORWARDED","IP":{"source":"10.244.1.7","destination":"10.244.3.3"},"l4":{"TCP":{"destination_port":8200}},"source":{"namespace":"payments","pod_name":"checkout-1"},"destination":{"namespace":"secret-team","pod_name":"vault-0"},"Type":"L3_L4","is_reply":false}}
{"flow":{"verdict":"DROPPED","IP":{"source":"10.244.1.7","destination":"140.82.121.4"},"l4":{"TCP":{"destination_port":443}},"source":{"namespace":"payments","pod_name":"checkout-1"},"destination":{"labels":["reserved:world"]},"destination_names":["github.com"],"Type":"L3_L4","is_reply":false}}
`

func TestHubbleFlowsCollectedScopedAndRedacted(t *testing.T) {
	h := newHarness()
	h.dyn = dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, append(objects(), relayPod())...)
	fwd := &portForward{}
	h.fwd = fwd
	p := standard(t)
	p.Flows.Hubble = 500
	p.PortForward = true
	var gotArgs []string
	files, _, _ := run(t, p, h, func(c *Collector) {
		c.Hubble = func(_ context.Context, args []string) ([]byte, error) { gotArgs = args; return []byte(hubbleOut), nil }
	})
	want := "observe --server localhost:54245 --last 500 -o jsonpb --namespace payments"
	if strings.Join(gotArgs, " ") != want {
		t.Fatalf("args %q, want %q", strings.Join(gotArgs, " "), want)
	}
	if len(fwd.opened) != 1 || fwd.opened[0] != "kube-system/hubble-relay-7d9f8c-abcde [4245]" {
		t.Fatalf("opened %v", fwd.opened)
	}
	member := bundle.FlowsPath("mesh1", "hubble", "hubble-relay.jsonl")
	data, ok := files[member]
	if !ok {
		t.Fatalf("no %s in %v", member, keys(files))
	}
	if bytes.Contains(data, []byte(canary)) || bytes.Contains(data, []byte("secret-team")) {
		t.Fatalf("hubble flows not redacted or not scoped:\n%s", data)
	}
	cov := coverage(t, files)["mesh1.flows.hubble"]
	if cov.State != bundle.StateComplete || cov.Items != 2 || cov.Type != bundle.CoverageFlows || !strings.Contains(cov.Reason, "1 flows naming namespaces outside the selection removed") {
		t.Fatalf("%+v", cov)
	}
	fs, _, err := flows.ParseFile(bytes.NewReader(data), "hubble")
	if err != nil || len(fs) != 2 {
		t.Fatalf("%v %+v", err, fs)
	}
}

func TestHubbleFlowsWithoutCLIOrRelay(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	h := newHarness()
	h.fwd = &portForward{}
	p := standard(t)
	p.Flows.Hubble = 10
	p.PortForward = true
	files, _, _ := run(t, p, h, nil)
	cov := coverage(t, files)["mesh1.flows.hubble"]
	if cov.State != bundle.StateFailed || !strings.Contains(cov.Reason, "hubble CLI is not on PATH") {
		t.Fatalf("%+v", cov)
	}
	// With a CLI but no relay pod.
	files, _, _ = run(t, p, h, func(c *Collector) {
		c.Hubble = func(context.Context, []string) ([]byte, error) {
			t.Fatal("hubble run without a relay")
			return nil, nil
		}
	})
	if cov := coverage(t, files)["mesh1.flows.hubble"]; cov.State != bundle.StateFailed || !strings.Contains(cov.Reason, "no running hubble-relay pod") {
		t.Fatalf("%+v", cov)
	}
	// Not asked for: nothing happens.
	p.Flows.Hubble = 0
	files, _, _ = run(t, p, h, nil)
	if _, ok := coverage(t, files)["mesh1.flows.hubble"]; ok {
		t.Fatal("hubble collected without --hubble-flows")
	}
}

func TestHubbleNeedsPortForward(t *testing.T) {
	p := standard(t)
	p.Flows.Hubble = 10
	pl := p.Plan()
	if len(pl.Unavailable) == 0 || !strings.Contains(strings.Join(pl.Unavailable, " "), "hubble") {
		t.Fatalf("%+v", pl)
	}
	p.PortForward = true
	rbac := p.RBAC(nil)
	if !strings.Contains(rbac, "flightbay-hubble-flows") || !strings.Contains(rbac, "namespace: kube-system") || !strings.Contains(rbac, "pods/portforward") {
		t.Fatal(rbac)
	}
	pln, err := (&Collector{Policy: p, Hubble: func(context.Context, []string) ([]byte, error) { return nil, nil }}).DryRun(context.Background(), "mesh1", newHarness().clients())
	if err != nil || len(pln.Flows) != 1 || !strings.Contains(pln.Flows[0], "hubble-relay") {
		t.Fatalf("%v %+v", err, pln)
	}
}

func TestAttachFlowFile(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "firewall export.csv")
	body := "src_ip,src_namespace,src_pod,dst_host,dst_port,action,path\n" +
		"10.244.1.7,payments,checkout-1,api.stripe.com,443,allow,/v1/charges?api_key=" + canary + "\n" +
		"10.244.3.3,secret-team,vault-0,vault.example.com,8200,allow,/\n"
	if err := os.WriteFile(csv, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(bad, []byte("just some notes\n"), 0o600)
	if _, err := DetectFlowFile(bad); err == nil {
		t.Fatal("unknown format accepted")
	}
	// Drive AttachFlowFile through a real writer and read it back.
	var buf bytes.Buffer
	w, _ := bundle.NewWriter(&buf, bundle.WriterOptions{Compression: "fastest"})
	rd := mustRedactor(t)
	p := standard(t)
	p.Flows.Attach = []string{filepath.Base(csv)}
	c := &Collector{Policy: p, Redactor: rd, Writer: w}
	if err := c.Begin([]string{"mesh1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.AttachFlowFile("mesh1", csv); err != nil {
		t.Fatal(err)
	}
	if err := c.AttachFlowFile("mesh1", bad); err != nil {
		t.Fatal(err)
	}
	if err := c.Finish(); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, &buf)
	member := bundle.FlowsPath("mesh1", "generic", "firewall export.csv")
	data := got[member]
	if data == nil || bytes.Contains(data, []byte(canary)) || bytes.Contains(data, []byte("secret-team")) || !bytes.Contains(data, []byte("api.stripe.com")) {
		t.Fatalf("%s:\n%s (members %v)", member, data, keys(got))
	}
	covs := coverage(t, got)
	if cv := covs["mesh1.flows.attached.firewall_export.csv"]; cv.State != bundle.StateComplete || cv.Kind != "generic" || cv.Items != 1 || len(cv.Chunks) != 1 {
		t.Fatalf("%+v", covs)
	}
	if cv := covs["mesh1.flows.attached.notes.txt"]; cv.State != bundle.StateFailed {
		t.Fatalf("%+v", covs)
	}
	if info, ok := bundle.ParsePath(member); !ok || info.Kind != bundle.PathFlows || info.Sub != "generic" || info.Cluster != "mesh1" {
		t.Fatalf("%+v", info)
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustRedactor(t *testing.T) *redact.Redactor {
	t.Helper()
	rd, err := redact.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return rd
}

func readAll(t *testing.T, buf *bytes.Buffer) map[string][]byte {
	t.Helper()
	r, err := bundle.NewReader(buf, bundle.ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for {
		m, err := r.Next()
		if err != nil {
			break
		}
		out[m.Name] = m.Data
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
	return out
}
