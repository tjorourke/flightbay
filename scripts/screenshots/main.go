// Command screenshots retakes the desk screenshots in docs/images.
//
//	go run ./scripts/screenshots                                  # every shot the bundles allow
//	go run ./scripts/screenshots -mesh ~/Downloads/flightbay-MESH1-*.tar.zst
//	go run ./scripts/screenshots -only overview,review
//
// It opens the sample bundle (and, with -mesh, a capture of a cluster with
// agents for Agent reach) in flightbay-desk, drives its own headless Chrome
// over the DevTools protocol and writes 1440x900 PNGs. It uses its own Chrome
// profile and never the browser you are using. A bundle whose shots are not
// possible (no -mesh) is skipped with a note, and the old image is kept.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/tjorourke/flightbay/internal/review"
	"github.com/tjorourke/flightbay/internal/sign"
)

type shot struct {
	name     string // docs/images/<name>.png
	desk     string // sample, mesh, reviewed
	hash     string // #view
	theme    string // dark or light
	setup    string // JavaScript run after the page has loaded; may return a promise
	wait     time.Duration
	selector string // optional full element capture, including content below the viewport
}

var shots = []shot{
	{name: "ambient-map", desk: "ambient", hash: "ambient", theme: "dark", selector: "#am-graph", setup: `(async()=>{const a=S.ambient.inventory.services.find(s=>s.namespace==='flightbay-ambient-e2e'&&s.name==='edit-front');S.ambient.key=amKey(a);S.ambient.tab='map';await render();})()`},
	{name: "ambient-waypoint", desk: "ambient", hash: "ambient", theme: "dark", setup: `(async()=>{const a=S.ambient.inventory.objects.find(s=>s.kind==='Namespace'&&s.name==='flightbay-ambient-e2e-peer');S.ambient.key=amKey(a);S.ambient.tab='wizard';S.ambient.mode='waypoint';S.ambient.settings={wpMode:'new',wpScope:'shared',wpNamespace:'flightbay-ambient-e2e',wpName:'proposed-shared-waypoint',wpClass:'istio-waypoint',enrolNamespaces:['flightbay-ambient-e2e','flightbay-ambient-e2e-peer']};await render();document.querySelector('#am-setup-parameters').scrollIntoView({block:'start'});})()`},
	{name: "ambient-recovery", desk: "ambient", hash: "ambient", theme: "dark", selector: "#am-repair-parameters", setup: `(async()=>{const a=S.ambient.inventory.services.find(s=>s.namespace==='flightbay-ambient-e2e'&&s.name==='broken-selector');S.ambient.key=amKey(a);S.ambient.tab='wizard';S.ambient.mode='endpoints';await render();const pod=S.ambient.result.recovery.pods.find(p=>p.labels.app==='stable');S.ambient.settings={repairOperation:'service-selector',selectedPod:pod.name,selector:{app:'stable'}};await render();})()`},
	{name: "ambient-edit", desk: "ambient", hash: "ambient", theme: "dark", selector: "#am-parameters", setup: `(async()=>{const a=S.ambient.inventory.services.find(s=>s.namespace==='flightbay-ambient-e2e'&&s.name==='edit-front');S.ambient.key=amKey(a);await render();const e=S.ambient.result.editable.find(e=>e.ref.name==='existing-split');S.ambient.tab='wizard';S.ambient.edit=e.request.edit;S.ambient.tasks=Object.fromEntries(e.request.tasks.map(t=>[t,true]));S.ambient.settings={...e.request};await render();})()`},
	{name: "ambient-investigation", desk: "ambient", hash: "ambient", theme: "dark", setup: `(async () => { const a = S.ambient.inventory.services.find(s => s.namespace === 'flightbay-ambient-e2e' && s.name === 'front'); if (!a) throw new Error('needs the ambient e2e capture'); S.ambient.key = amKey(a); S.ambient.route = 'wrong-parent'; await render(); })()`},
	{name: "ambient-wizard", desk: "ambient", hash: "ambient", theme: "dark", setup: `(async () => { const a = S.ambient.inventory.services.find(s => s.namespace === 'flightbay-ambient-e2e' && s.name === 'front'); if (!a) throw new Error('needs the ambient e2e capture'); S.ambient.key = amKey(a); S.ambient.tab = 'wizard'; S.ambient.tasks = {'weighted-routing':true}; S.ambient.settings = {port:8080,backends:[{name:'stable',port:8080,weight:90},{name:'canary',port:8080,weight:10}]}; await render(); S.ambient.plan = await apiPost('api/ambient/plan',amRead()); await render(); document.querySelector('#am-plan').scrollIntoView({block:'start'}); })()`},
	{name: "start", desk: "start", hash: "", theme: "dark", setup: `document.querySelector('[data-card=file]').click()`},
	{name: "overview", desk: "sample", hash: "overview", theme: "dark"},
	{name: "search", desk: "sample", hash: "search", theme: "dark"},
	{name: "logs", desk: "sample", hash: "logs", theme: "dark"},
	{name: "timeline", desk: "sample", hash: "timeline", theme: "light"},
	{name: "findings", desk: "sample", hash: "findings", theme: "dark"},
	{name: "posture", desk: "sample", hash: "scorecard", theme: "light"},
	{name: "proxies", desk: "sample", hash: "proxies", theme: "dark"},
	{name: "k9s", desk: "sample", hash: "resources", theme: "dark", setup: `document.querySelector('#term-k9s').click()`, wait: 6 * time.Second},
	{name: "review", desk: "sample", hash: "review", theme: "dark",
		setup: `apiPost('api/review/preview', {reviewer: 'Jane Smith, platform', scrubNames: true, remove: {namespaces: [{namespace: 'payments'}]}}).then(() => { S.rv.data = null; return render(); })`},
	{name: "coverage-origin", desk: "reviewed", hash: "coverage", theme: "dark"},
	{name: "scorecard-summary", desk: "mesh", hash: "scorecard", theme: "dark"},
	{name: "navigator", desk: "mesh", hash: "navigator", theme: "dark", setup: `S.nv.resource = 'deployments'; S.nv.group = 'apps'; render()`},
	{name: "search-typeahead", desk: "mesh", hash: "search", theme: "dark",
		setup: `(() => { const i = document.querySelector('#obj-q'); i.focus(); i.value = 'kind:pod label:ap'; i.setSelectionRange(i.value.length, i.value.length); i.dispatchEvent(new Event('input', {bubbles: true})); })()`},
	{name: "reach-list", desk: "mesh", hash: "reach", theme: "dark"},
	{name: "reach-graph", desk: "mesh", hash: "reach?agent=kagent/demo-agent", theme: "dark", setup: `document.querySelector('#rc-detail').scrollIntoView({block: 'start'})`},
	{name: "reach-external", desk: "mesh", hash: "reach?agent=kagent/demo-agent", theme: "dark",
		setup: `document.querySelector('[data-rctab=external]').click(); new Promise(r => setTimeout(r, 800)).then(() => document.querySelector('#rc-detail').scrollIntoView({block: 'start'}))`},
	{name: "agent-mode", desk: "agent", hash: "traffic", theme: "dark"},
	{name: "agent-mode-select", desk: "agent", hash: "traffic", theme: "dark", setup: `S.tf.focus = 'w:kagent/demo-agent'; S.tf.sel = S.tf.focus; render()`},
	{name: "traffic-findings", desk: "mesh", hash: "traffic", theme: "dark"},
	{name: "traffic-stats", desk: "mesh", hash: "traffic", theme: "dark"},
	{name: "traffic-map", desk: "mesh", hash: "traffic", theme: "dark", setup: `document.querySelector('#tf-q').scrollIntoView({block: 'start'})`},
	{name: "traffic-focus", desk: "mesh", hash: "traffic", theme: "dark", setup: `S.tf.focus = 'w:kagent/demo-agent'; S.tf.sel = S.tf.focus; render().then(() => document.querySelector('#tf-q').scrollIntoView({block: 'start'}))`},
	{name: "traffic-tools", desk: "mesh", hash: "traffic", theme: "dark", setup: `S.tf.tab = 'tools'; render().then(() => document.querySelector('#tf-q').scrollIntoView({block: 'start'}))`},
	{name: "traffic-explain", desk: "mesh", hash: "traffic", theme: "dark", setup: `S.tf.tab = 'explain'; S.tf.exFrom = 'kagent/demo-agent'; S.tf.exTo = 'kagent/github-mcp'; render().then(() => new Promise(r => setTimeout(r, 1200))).then(() => document.querySelector('#tf-exform').scrollIntoView({block: 'start'}))`},
	{name: "activity-map", desk: "mesh", hash: "traffic", theme: "dark", setup: `S.tf.layer = 'gateway'; S.tf.found = 'agentgateway'; render().then(() => document.querySelector('#tf-q').scrollIntoView({block: 'start'}))`},
	{name: "activity-caller", desk: "mesh", hash: "traffic", theme: "dark", setup: `S.tf.tab = 'calls'; render().then(() => document.querySelector('#tf-q').scrollIntoView({block: 'start'}))`},
	{name: "tool-map", desk: "mesh", hash: "traffic", theme: "dark", setup: `S.tf.layer = 'gateway'; S.tf.found = 'agentgateway'; S.tf.focus = 'w:kagent/demo-agent'; render().then(() => document.querySelector('#tf-q').scrollIntoView({block: 'start'}))`},
	{name: "tool-access", desk: "mesh", hash: "reach?agent=kagent/demo-agent", theme: "dark", setup: `S.reach.dtab = 'tools'; render().then(() => document.querySelector('#rc-detail').scrollIntoView({block: 'start'}))`},
	{name: "agents-tools", desk: "mesh", hash: "reach?agent=kagent/demo-agent", theme: "dark", setup: `document.querySelector('[data-rctab=tools]').click(); new Promise(r => setTimeout(r, 800)).then(() => document.querySelector('#rc-detail').scrollIntoView({block: 'start'}))`},
	{name: "reach-layers", desk: "mesh", hash: "reach?agent=kagent/demo-agent", theme: "dark", setup: `document.querySelector('#rc-detail').scrollIntoView({block: 'start'})`},
	{name: "reach-used", desk: "mesh", hash: "reach?agent=kagent/demo-agent", theme: "dark", setup: `document.querySelector('[data-rglayer=used]').click(); new Promise(r => setTimeout(r, 800)).then(() => document.querySelector('#rc-detail').scrollIntoView({block: 'start'}))`},
	{name: "reach-timeline", desk: "mesh", hash: "reach?agent=kagent/tom-demo", theme: "dark", setup: `document.querySelector('[data-rctab=usage]').click(); new Promise(r => setTimeout(r, 800)).then(() => document.querySelector('#rc-detail').scrollIntoView({block: 'start'}))`},
	{name: "reach-lockdown", desk: "mesh", hash: "reach?agent=kagent/demo", theme: "light", setup: `document.querySelector('[data-rctab=lockdown]').click(); new Promise(r => setTimeout(r, 800)).then(() => document.querySelector('#rc-detail').scrollIntoView({block: 'start'}))`},
}

func main() {
	root, _ := os.Getwd()
	desk := flag.String("desk", "", "flightbay-desk binary (default: build it into a temporary directory)")
	chrome := flag.String("chrome", defaultChrome(), "Chrome or Chromium binary")
	sample := flag.String("sample", filepath.Join(root, "examples/sample-bundle.tar.zst"), "the sample bundle")
	mesh := flag.String("mesh", "", "a capture with kagent agents, for Agent reach, Navigator and the Scorecard summary (FLIGHTBAY_ENTERPRISE is set for it)")
	ambient := flag.String("ambient", "", "the capture produced by test/e2e/ambient.py, for ambient investigation and wizard images")
	out := flag.String("out", filepath.Join(root, "docs/images"), "where the PNGs go")
	only := flag.String("only", "", "comma-separated shot names")
	flag.Parse()
	if err := run(*desk, *chrome, *sample, *mesh, *ambient, *out, *only); err != nil {
		fmt.Fprintln(os.Stderr, "screenshots:", err)
		os.Exit(1)
	}
}

func defaultChrome() string {
	if c := os.Getenv("CHROME"); c != "" {
		return c
	}
	for _, c := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/usr/bin/google-chrome", "/usr/bin/chromium"} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "google-chrome"
}

func run(deskBin, chromeBin, sample, mesh, ambient, out, only string) error {
	tmp, err := os.MkdirTemp("", "fb-shots-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if deskBin == "" {
		deskBin = filepath.Join(tmp, "flightbay-desk")
		fmt.Println("building flightbay-desk")
		b := exec.Command("go", "build", "-o", deskBin, "./cmd/flightbay-desk")
		b.Stdout, b.Stderr = os.Stdout, os.Stderr
		if err := b.Run(); err != nil {
			return err
		}
	}
	want := map[string]bool{}
	for _, n := range strings.Split(only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	needed := map[string]bool{}
	var todo []shot
	for _, s := range shots {
		if len(want) > 0 && !want[s.name] {
			continue
		}
		if (s.desk == "mesh" || s.desk == "agent") && mesh == "" {
			fmt.Printf("skip %s: needs -mesh\n", s.name)
			continue
		}
		if s.desk == "ambient" && ambient == "" {
			fmt.Printf("skip %s: needs -ambient\n", s.name)
			continue
		}
		needed[s.desk] = true
		todo = append(todo, s)
	}
	if len(todo) == 0 {
		return errors.New("nothing to take")
	}
	// A new documentation image must also have a reproducible capture here.
	// Otherwise a full refresh silently leaves an old, potentially branded image.
	if only == "" {
		known := map[string]bool{}
		for _, s := range shots {
			known[s.name+".png"] = true
		}
		files, err := filepath.Glob(filepath.Join(out, "*.png"))
		if err != nil {
			return err
		}
		for _, file := range files {
			if !known[filepath.Base(file)] {
				return fmt.Errorf("%s has no screenshot definition", filepath.Base(file))
			}
		}
	}

	// A signed bundle with the payments namespace removed, for the coverage view.
	key, _ := sign.Generate("ACME platform team")
	pub := filepath.Join(tmp, "acme.pub")
	if err := os.WriteFile(pub, key.Public().MarshalPublic(), 0o644); err != nil {
		return err
	}
	desks := map[string]*deskProc{}
	defer func() {
		for _, d := range desks {
			d.stop()
		}
	}()
	start := func(name, bundle string, env []string, args ...string) error {
		d, err := startDesk(deskBin, filepath.Join(tmp, name), bundle, env, args...)
		if err != nil {
			return fmt.Errorf("%s desk: %w", name, err)
		}
		desks[name] = d
		return nil
	}
	if needed["sample"] {
		keyFile := filepath.Join(tmp, "acme.key")
		if err := os.WriteFile(keyFile, key.MarshalPrivate(), 0o600); err != nil {
			return err
		}
		demo := filepath.Join(tmp, "flightbay-DEMO-0001-20260928T0036Z.tar.zst")
		if err := copyFile(sample, demo); err != nil {
			return err
		}
		if err := start("sample", demo, nil, "--sign", keyFile); err != nil {
			return err
		}
	}
	if needed["reviewed"] {
		reviewed := filepath.Join(tmp, "flightbay-DEMO-reviewed.tar.zst")
		_, err := review.WriteFile(context.Background(), sample, reviewed, &review.Plan{Reviewer: "Jane Smith", ScrubNames: true,
			Remove: review.Removals{Namespaces: []review.NamespaceRef{{Namespace: "payments"}}, Types: []review.TypeRef{{Resource: "configmaps"}}}},
			review.Options{Signer: key, Tool: "screenshots"})
		if err != nil {
			return err
		}
		if err := start("reviewed", reviewed, nil, "--verify-key", pub); err != nil {
			return err
		}
	}
	if needed["start"] {
		// The start page lists bundles in its directory and ~/Downloads; give
		// it a home of its own so only these two appear.
		home := filepath.Join(tmp, "start-home")
		dl := filepath.Join(home, "Downloads")
		if err := os.MkdirAll(dl, 0o755); err != nil {
			return err
		}
		if err := copyFile(sample, filepath.Join(dl, "flightbay-DEMO-0001-20260928T0036Z.tar.zst")); err != nil {
			return err
		}
		if mesh != "" {
			if err := copyFile(mesh, filepath.Join(dl, filepath.Base(mesh))); err != nil {
				return err
			}
		}
		d, err := startDesk(deskBin, filepath.Join(tmp, "start"), "", []string{"HOME=" + home})
		if err != nil {
			return fmt.Errorf("start page: %w", err)
		}
		desks["start"] = d
	}
	if needed["mesh"] {
		if err := start("mesh", mesh, []string{"FLIGHTBAY_ENTERPRISE=1"}); err != nil {
			return err
		}
	}
	if needed["agent"] {
		if err := start("agent", mesh, []string{"FLIGHTBAY_ENTERPRISE=1", "FLIGHTBAY_AGENT=1"}); err != nil {
			return err
		}
	}
	if needed["ambient"] {
		if err := start("ambient", ambient, nil); err != nil {
			return err
		}
	}

	br, err := startChrome(chromeBin, filepath.Join(tmp, "chrome"))
	if err != nil {
		return err
	}
	defer br.stop()
	for _, s := range todo {
		d := desks[s.desk]
		png, err := br.capture(d, s)
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		if err := os.WriteFile(filepath.Join(out, s.name+".png"), png, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s.png\n", s.name)
	}
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// ---------- desks ----------

type deskProc struct {
	cmd   *exec.Cmd
	base  string
	token string
}

var uiLine = regexp.MustCompile(`UI:\s+(http://[^/\s]+)/\?token=([0-9a-f]+)`)

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func startDesk(bin, home, bundle string, env []string, args ...string) (*deskProc, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	// Run from the bundle's directory so the UI shows file names, not local
	// paths. No bundle means the start page, which is ready once it listens.
	argv := append([]string{"open", filepath.Base(bundle), "--listen", fmt.Sprintf("127.0.0.1:%d", port), "--no-browser"}, args...)
	dir := filepath.Dir(bundle)
	if bundle == "" {
		argv = append([]string{"start", "--listen", fmt.Sprintf("127.0.0.1:%d", port), "--no-browser"}, args...)
		dir = home
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	cmd := exec.Command(bin, argv...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "FLIGHTBAY_HOME="+home), env...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	d := &deskProc{cmd: cmd}
	ready := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		sent := false
		for sc.Scan() {
			line := sc.Text()
			if m := uiLine.FindStringSubmatch(line); m != nil {
				d.base, d.token = m[1], m[2]
				if bundle == "" && !sent {
					ready <- nil
					sent = true
				}
			}
			if !sent && strings.HasPrefix(line, "Import complete") {
				ready <- nil
				sent = true
			}
			if !sent && (strings.HasPrefix(line, "Import stopped") || strings.HasPrefix(line, "Error")) {
				ready <- errors.New(line)
				sent = true
			}
		}
		if !sent {
			ready <- errors.New("desk exited before the import finished")
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			d.stop()
			return nil, err
		}
	case <-time.After(3 * time.Minute):
		d.stop()
		return nil, errors.New("timed out waiting for the import")
	}
	return d, nil
}

func (d *deskProc) stop() {
	if d.cmd.Process != nil {
		_ = d.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = d.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = d.cmd.Process.Kill()
		}
	}
}

// ---------- Chrome over the DevTools protocol ----------

type browser struct {
	cmd     *exec.Cmd
	conn    *websocket.Conn
	next    atomic.Int64
	mu      sync.Mutex
	waiting map[int64]chan json.RawMessage
	errs    map[int64]string
}

func startChrome(bin, profile string) (*browser, error) {
	cmd := exec.Command(bin, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--no-first-run", "--no-default-browser-check",
		"--remote-debugging-port=0", "--user-data-dir="+profile, "about:blank")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var port string
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); err == nil {
			port = strings.SplitN(string(b), "\n", 2)[0]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if port == "" {
		_ = cmd.Process.Kill()
		return nil, errors.New("chrome did not start its DevTools endpoint")
	}
	resp, err := http.Get("http://127.0.0.1:" + port + "/json/version")
	if err != nil {
		return nil, err
	}
	var v struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	err = json.NewDecoder(resp.Body).Decode(&v)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	conn, _, err := websocket.Dial(context.Background(), v.WS, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(64 << 20)
	b := &browser{cmd: cmd, conn: conn, waiting: map[int64]chan json.RawMessage{}, errs: map[int64]string{}}
	go b.read()
	return b, nil
}

func (b *browser) read() {
	for {
		_, data, err := b.conn.Read(context.Background())
		if err != nil {
			return
		}
		var msg struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &msg) != nil || msg.ID == 0 {
			continue // events
		}
		b.mu.Lock()
		ch := b.waiting[msg.ID]
		if msg.Error != nil {
			b.errs[msg.ID] = msg.Error.Message
		}
		b.mu.Unlock()
		if ch != nil {
			ch <- msg.Result
		}
	}
}

func (b *browser) call(session, method string, params any, out any) error {
	id := b.next.Add(1)
	ch := make(chan json.RawMessage, 1)
	b.mu.Lock()
	b.waiting[id] = ch
	b.mu.Unlock()
	msg := map[string]any{"id": id, "method": method, "params": params}
	if session != "" {
		msg["sessionId"] = session
	}
	data, _ := json.Marshal(msg)
	if err := b.conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		return err
	}
	select {
	case res := <-ch:
		b.mu.Lock()
		e := b.errs[id]
		delete(b.waiting, id)
		b.mu.Unlock()
		if e != "" {
			return fmt.Errorf("%s: %s", method, e)
		}
		if out != nil {
			return json.Unmarshal(res, out)
		}
		return nil
	case <-time.After(60 * time.Second):
		return fmt.Errorf("%s: timed out", method)
	}
}

func (b *browser) eval(session, js string) error {
	var r struct {
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := b.call(session, "Runtime.evaluate", map[string]any{"expression": js, "awaitPromise": true}, &r); err != nil {
		return err
	}
	if r.Exception != nil {
		return fmt.Errorf("%s %s", r.Exception.Text, r.Exception.Exception.Description)
	}
	return nil
}

// waitFor polls a JavaScript condition for up to 30 seconds.
func (b *browser) waitFor(session, cond string) error {
	for i := 0; i < 300; i++ {
		var r struct {
			Result struct {
				Value bool `json:"value"`
			} `json:"result"`
		}
		if err := b.call(session, "Runtime.evaluate", map[string]any{"expression": "!!(" + cond + ")", "returnByValue": true}, &r); err == nil && r.Result.Value {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", cond)
}

// capture opens a fresh tab for each shot, so no state leaks between shots.
func (b *browser) capture(d *deskProc, s shot) ([]byte, error) {
	var t struct {
		TargetID string `json:"targetId"`
	}
	if err := b.call("", "Target.createTarget", map[string]any{"url": "about:blank"}, &t); err != nil {
		return nil, err
	}
	defer b.call("", "Target.closeTarget", map[string]any{"targetId": t.TargetID}, nil)
	var a struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.call("", "Target.attachToTarget", map[string]any{"targetId": t.TargetID, "flatten": true}, &a); err != nil {
		return nil, err
	}
	ss := a.SessionID
	if err := b.call(ss, "Emulation.setDeviceMetricsOverride", map[string]any{"width": 1440, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil); err != nil {
		return nil, err
	}
	// Sign in (sets the session cookie), choose the theme, then load the view.
	if err := b.call(ss, "Page.navigate", map[string]any{"url": d.base + "/?token=" + d.token}, nil); err != nil {
		return nil, err
	}
	if err := b.waitFor(ss, fmt.Sprintf(`location.href.startsWith(%q) && document.readyState === 'complete'`, d.base)); err != nil {
		return nil, err
	}
	// The theme is read once per page load, and the next navigation only
	// changes the hash, so apply it to this page as well.
	if err := b.eval(ss, fmt.Sprintf(`localStorage.setItem('flightbay-theme', %[1]q); document.documentElement.dataset.theme = %[1]q; true`, s.theme)); err != nil {
		return nil, err
	}
	if err := b.call(ss, "Page.navigate", map[string]any{"url": d.base + "/#" + s.hash}, nil); err != nil {
		return nil, err
	}
	if err := b.waitFor(ss, `document.readyState === 'complete' && !!document.querySelector('#main .page-head, #main .nv-frame, #main .nv, #cards')`); err != nil {
		return nil, err
	}
	time.Sleep(1500 * time.Millisecond)
	if s.setup != "" {
		if err := b.eval(ss, s.setup); err != nil {
			return nil, fmt.Errorf("setup: %w", err)
		}
	}
	w := s.wait
	if w == 0 {
		w = 2 * time.Second
	}
	time.Sleep(w)
	var shotRes struct {
		Data string `json:"data"`
	}
	params := map[string]any{"format": "png"}
	if s.selector != "" {
		if err := b.eval(ss, `document.querySelector('#terminal')?.style.setProperty('display','none'); true`); err != nil {
			return nil, err
		}
		var rect struct {
			Result struct {
				Value map[string]any `json:"value"`
			} `json:"result"`
		}
		js := fmt.Sprintf(`(()=>{const r=document.querySelector(%q).getBoundingClientRect();return {x:r.left+scrollX,y:r.top+scrollY,width:r.width,height:r.height,scale:1}})()`, s.selector)
		if err := b.call(ss, "Runtime.evaluate", map[string]any{"expression": js, "returnByValue": true}, &rect); err != nil {
			return nil, err
		}
		params["clip"] = rect.Result.Value
		params["captureBeyondViewport"] = true
	}
	if err := b.call(ss, "Page.captureScreenshot", params, &shotRes); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(shotRes.Data)
}

func (b *browser) stop() {
	_ = b.conn.Close(websocket.StatusNormalClosure, "")
	_ = b.cmd.Process.Kill()
	_, _ = b.cmd.Process.Wait()
}
