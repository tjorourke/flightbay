package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/flows"
	"github.com/tjorourke/flightbay/internal/policy"
)

// LocalPorter is implemented by forward sessions that can hand the local
// port to a non-HTTP client (the hubble CLI speaks gRPC to hubble-relay).
type LocalPorter interface {
	LocalPort(remote int) int
}

// HubbleRunner runs the hubble CLI with args and returns its stdout.
type HubbleRunner func(ctx context.Context, args []string) ([]byte, error)

// hubbleRelayPort is the relay's gRPC container port (the Service maps 80 to it).
const hubbleRelayPort = 4245

// ErrNoHubbleCLI means the hubble CLI is not on PATH.
var ErrNoHubbleCLI = errors.New("the hubble CLI is not on PATH: install it (https://github.com/cilium/hubble/releases) or omit --hubble-flows")

func defaultHubble(ctx context.Context, args []string) ([]byte, error) {
	bin, err := exec.LookPath("hubble")
	if err != nil {
		return nil, ErrNoHubbleCLI
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("hubble %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(firstLine(stderr.Bytes())))
	}
	return out, nil
}

// HubbleCLIAvailable reports whether the hubble CLI is on PATH.
func HubbleCLIAvailable() bool { _, err := exec.LookPath("hubble"); return err == nil }

// DetectFlowFile reads a flow log file's head and names its format, so a
// collection can refuse an unreadable file before anything is collected.
func DetectFlowFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 64<<10)
	n, _ := f.Read(head)
	format := flows.Detect(head[:n])
	if format == "" {
		return "", fmt.Errorf("%s: unrecognised flow log format (supported: %s)", path, strings.Join(flows.Formats, ", "))
	}
	return format, nil
}

// flowCoverage records one flow file in the bundle.
func (c *Collector) flowCoverage(cluster, id, file, format, state, reason, member string, items int) error {
	cov := bundle.Coverage{ID: cluster + ".flows." + id, Cluster: cluster, Type: bundle.CoverageFlows, Resource: file, Kind: format,
		Items: items, State: state, Reason: reason}
	if member != "" {
		cov.Chunks = []string{member}
	}
	if state == bundle.StateFailed {
		c.record(LogEntry{Cluster: cluster, Operation: "flows", Target: file, Result: "failed", Error: reason})
	}
	return c.Writer.Coverage(cov)
}

// writeFlows filters a flow file to the selected namespaces, redacts it and
// writes it as a bundle member, returning the member name and record count.
func (c *Collector) writeFlows(cluster, format, file string, data []byte) (string, int, int, error) {
	loc := bundle.FlowsPath(cluster, format, file)
	lr := c.Redactor.Lines(loc)
	out, dropped, err := flows.Filter(data, format, c.Policy.NamespaceAllowed, lr.Line)
	if err != nil {
		return "", 0, 0, err
	}
	fs, _, err := flows.ParseFile(bytes.NewReader(out), format)
	if err != nil && len(fs) == 0 && len(bytes.TrimSpace(out)) > 0 {
		return "", 0, dropped, err
	}
	n := 0
	for _, f := range fs {
		n += f.Count
	}
	if err := c.Writer.Add(loc, out); err != nil {
		return "", 0, dropped, err
	}
	return loc, n, dropped, nil
}

// AttachFlowFile embeds a customer-supplied flow log in the bundle under
// clusters/<cluster>/flows/<format>/<file>, so the manifest's SHA-256 covers
// it like every other member. Records naming namespaces outside the
// selection are dropped and every line is redacted first.
func (c *Collector) AttachFlowFile(cluster, path string) error {
	c.defaults()
	cluster = bundle.SafeID(cluster)
	name := bundle.SafeID(filepath.Base(path))
	format, err := DetectFlowFile(path)
	if err != nil {
		return c.flowCoverage(cluster, "attached."+name, name, "", bundle.StateFailed, err.Error(), "", 0)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c.flowCoverage(cluster, "attached."+name, name, format, bundle.StateFailed, err.Error(), "", 0)
	}
	if int64(len(data)) > bundle.DefaultMaxMember {
		return c.flowCoverage(cluster, "attached."+name, name, format, bundle.StateFailed,
			fmt.Sprintf("%d bytes is over the %d byte member limit; split the file", len(data), bundle.DefaultMaxMember), "", 0)
	}
	member, n, dropped, err := c.writeFlows(cluster, format, name, data)
	if err != nil {
		return c.flowCoverage(cluster, "attached."+name, name, format, bundle.StateFailed, err.Error(), "", 0)
	}
	reason := format + " flow log attached by the customer with --attach-flows (" + filepath.Base(path) + ")"
	if dropped > 0 {
		reason += fmt.Sprintf("; %d records naming namespaces outside the selection removed", dropped)
	}
	c.progress("%s: attached %s flows from %s (%d records)", cluster, format, filepath.Base(path), n)
	c.record(LogEntry{Cluster: cluster, Operation: "attach-flows", Target: filepath.Base(path), Result: "ok"})
	return c.flowCoverage(cluster, "attached."+name, name, format, bundle.StateComplete, reason, member, n)
}

// hubbleArgs builds the hubble observe command line.
func (c *Collector) hubbleArgs(port int) []string {
	args := []string{"observe", "--server", "localhost:" + strconv.Itoa(port), "--last", strconv.Itoa(c.Policy.Flows.Hubble), "-o", "jsonpb"}
	// An explicit namespace list narrows the query at the relay; flows are
	// still filtered afterwards, because a flow matches --namespace when
	// either end is in it and the other end may be excluded.
	for _, ns := range c.Policy.ExplicitNamespaces() {
		if c.Policy.NamespaceAllowed(ns) {
			args = append(args, "--namespace", ns)
		}
	}
	return args
}

// collectHubble reads recent flows from hubble-relay through port-forward
// and the local hubble CLI.
func (c *Collector) collectHubble(ctx context.Context, cluster string, cl Clients) error {
	const id, file = "hubble", "hubble-relay.jsonl"
	fail := func(reason string) error {
		return c.flowCoverage(cluster, id, file, flows.FmtHubble, bundle.StateFailed, reason, "", 0)
	}
	if cl.Forward == nil {
		return fail("port-forward is not available")
	}
	if c.overBudget() {
		return c.flowCoverage(cluster, id, file, flows.FmtHubble, bundle.StateTruncated, "bundle size budget reached before this collection", "", 0)
	}
	run := c.Hubble
	if run == nil {
		run = defaultHubble
		if !HubbleCLIAvailable() {
			c.progress("%s: hubble flows skipped: %v", cluster, ErrNoHubbleCLI)
			return fail(ErrNoHubbleCLI.Error())
		}
	}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	l, err := cl.Dynamic.Resource(gvr).Namespace(policy.HubbleNamespace).List(ctx, metav1.ListOptions{LabelSelector: "k8s-app=" + policy.HubbleRelay})
	if err != nil {
		return fail("listing hubble-relay pods: " + reason(err))
	}
	pod := ""
	for _, it := range l.Items {
		if p := parsePod(it.Object); p.running {
			pod = p.name
			break
		}
	}
	if pod == "" {
		return fail("no running hubble-relay pod in " + policy.HubbleNamespace + " (is Cilium Hubble enabled with the relay?)")
	}
	c.progress("%s: hubble flows from %s/%s (last %d)", cluster, policy.HubbleNamespace, pod, c.Policy.Flows.Hubble)
	sess, err := cl.Forward.Open(ctx, policy.HubbleNamespace, pod, []int{hubbleRelayPort})
	if err != nil {
		c.record(LogEntry{Cluster: cluster, Operation: "port-forward", Target: pod, Namespace: policy.HubbleNamespace, Result: "failed", Error: err.Error()})
		return fail("port-forward failed: " + err.Error())
	}
	defer sess.Close()
	lp, ok := sess.(LocalPorter)
	if !ok || lp.LocalPort(hubbleRelayPort) == 0 {
		return fail("port-forward session does not expose a local port")
	}
	out, err := run(ctx, c.hubbleArgs(lp.LocalPort(hubbleRelayPort)))
	if err != nil {
		return fail(err.Error())
	}
	member, n, dropped, err := c.writeFlows(cluster, flows.FmtHubble, file, out)
	if err != nil {
		return fail(err.Error())
	}
	reason := fmt.Sprintf("last %d flows from %s/%s through the hubble CLI; HTTP paths redacted", c.Policy.Flows.Hubble, policy.HubbleNamespace, pod)
	if dropped > 0 {
		reason += fmt.Sprintf("; %d flows naming namespaces outside the selection removed", dropped)
	}
	return c.flowCoverage(cluster, id, file, flows.FmtHubble, bundle.StateComplete, reason, member, n)
}

// flowPlan describes, for --dry-run, the flow sources a collection adds.
func (c *Collector) flowPlan() []string {
	var out []string
	if c.Policy.HubbleSelected() {
		cli := "found on PATH"
		if c.Hubble == nil && !HubbleCLIAvailable() {
			cli = "NOT found on PATH: install it or the flows are skipped"
		}
		out = append(out, fmt.Sprintf("hubble: last %d flows from svc/%s in %s through port-forward to port %d and the local hubble CLI (%s). "+
			"Flows naming namespaces outside the selection are dropped and HTTP paths redacted before writing.",
			c.Policy.Flows.Hubble, policy.HubbleRelay, policy.HubbleNamespace, hubbleRelayPort, cli))
	}
	for _, f := range c.Policy.Flows.Attach {
		out = append(out, "attached flow log "+f+": embedded under flows/ and covered by the manifest; records naming namespaces outside the selection removed, lines redacted.")
	}
	return out
}
