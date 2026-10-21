package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/collect"
	"github.com/tjorourke/flightbay/internal/reach"
	"github.com/tjorourke/flightbay/internal/store"
)

type ambientConnection struct {
	client dynamic.Interface
	mapper meta.RESTMapper
	host   string
}
type ambientCreated struct {
	APIVersion string    `json:"apiVersion"`
	Kind       string    `json:"kind"`
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	UID        types.UID `json:"uid"`
}
type ambientRecord struct {
	ApplyState    string           `json:"applyState,omitempty"`
	ApplyError    string           `json:"applyError,omitempty"`
	ApplyStarted  time.Time        `json:"applyStarted,omitempty"`
	ApplyFinished time.Time        `json:"applyFinished,omitempty"`
	Modified      []ambient.Change `json:"modified,omitempty"`
	Owner         string           `json:"owner"`
	Token         string           `json:"token"`
	Context       string           `json:"context"`
	Server        string           `json:"server"`
	Plan          *ambient.Plan    `json:"plan"`
	Validated     time.Time        `json:"validated"`
	Created       []ambientCreated `json:"created"`
	// Verified is set after the saved tests ran against this change: true when
	// they all pass, false when one fails. nil means they have not run.
	Verified   *bool     `json:"verified,omitempty"`
	VerifiedAt time.Time `json:"verifiedAt,omitempty"`
}
type ambientState struct {
	mu      sync.Mutex
	records map[string]*ambientRecord
	connect func(string) (ambientConnection, error)
}
type ambientTestRequest struct {
	PlanID     string          `json:"planId"`
	Operation  string          `json:"operation"`
	Context    string          `json:"context"`
	Validation string          `json:"validation"`
	Request    ambient.Request `json:"request"`
	Verified   bool            `json:"verified"`
}

func (s *Server) ambientInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	writeJSON(w, 200, ambient.List(s.WS))
}
func (s *Server) ambientInvestigation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	q := r.URL.Query()
	kind := q.Get("kind")
	name := q.Get("name")
	if name == "" {
		name = q.Get("service")
		kind = "Service"
	}
	out, err := ambient.NewView(s.WS, q.Get("cluster"), kind, q.Get("namespace"), name, q.Get("route"))
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) ambientCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	q := r.URL.Query()
	writeJSON(w, 200, ambient.Capabilities(s.WS, q.Get("cluster"), q.Get("namespace"), q.Get("service")))
}
func ambientBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		jerr(w, 405, "POST only")
		return false
	}
	if !sameOrigin(r) {
		jerr(w, 403, "cross-origin request refused")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		jerr(w, 400, err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		jerr(w, 400, "expected one JSON request")
		return false
	}
	return true
}
func (s *Server) ambientPlan(w http.ResponseWriter, r *http.Request) {
	var req ambient.Request
	if !ambientBody(w, r, &req) {
		return
	}
	plan, err := s.generateReviewedPlan(req)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	ambient.AnnotatePlan(s.WS, plan)
	writeJSON(w, 200, plan)
}
func (s *Server) ambientContexts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	cs, _, _, err := KubeContexts(s.AmbientKubeconfig)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	out := []map[string]string{}
	for _, c := range cs {
		row := map[string]string{"name": c.Name, "server": c.Server}
		if cluster := s.ambientLiveCluster(c.Name, c.Server); cluster != "" {
			row["liveCluster"] = cluster
		}
		out = append(out, row)
	}
	writeJSON(w, 200, out)
}

// A matching display name alone is not authority to query another cluster.
func (s *Server) ambientLiveCluster(name, server string) string {
	if s.WS == nil || !s.WS.IsLive() || server == "" {
		return ""
	}
	live := s.WS.State().Live
	if live == nil {
		return ""
	}
	if len(live.Clusters) == 0 && live.Context == name && live.Server == server {
		return bundle.SafeID(name)
	}
	for _, cluster := range live.Clusters {
		if cluster.Context == name && cluster.Server == server {
			return bundle.SafeID(name)
		}
	}
	return ""
}

func (s *Server) ambientConnect(name string) (ambientConnection, error) {
	if name == "" {
		return ambientConnection{}, errors.New("select a real kubeconfig context explicitly")
	}
	if s.ambientS.connect != nil {
		return s.ambientS.connect(name)
	}
	rules, err := collect.KubeconfigRules(s.AmbientKubeconfig)
	if err != nil {
		return ambientConnection{}, err
	}
	raw, err := rules.Load()
	if err != nil {
		return ambientConnection{}, err
	}
	if raw.Contexts[name] == nil {
		return ambientConnection{}, fmt.Errorf("context %q is not in the local kubeconfig", name)
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: name}).ClientConfig()
	if err != nil {
		return ambientConnection{}, err
	}
	cfg.Timeout = 20 * time.Second
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return ambientConnection{}, err
	}
	dis, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return ambientConnection{}, err
	}
	groups, err := restmapper.GetAPIGroupResources(dis)
	if err != nil {
		return ambientConnection{}, err
	}
	return ambientConnection{dc, restmapper.NewDiscoveryRESTMapper(groups), cfg.Host}, nil
}
func ambientResource(c ambientConnection, api, kind, ns string) (dynamic.ResourceInterface, error) {
	gv, err := schema.ParseGroupVersion(api)
	if err != nil {
		return nil, err
	}
	mapping, err := c.mapper.RESTMapping(schema.GroupKind{Group: gv.Group, Kind: kind}, gv.Version)
	if err != nil {
		return nil, err
	}
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		if kind == "Namespace" && gv.Group == "" && mapping.Resource.Resource == "namespaces" && ns == "" {
			return c.client.Resource(mapping.Resource), nil
		}
		return nil, errors.New("test plans may only create namespaced configuration")
	}
	return c.client.Resource(mapping.Resource).Namespace(ns), nil
}

// Only service identity/selection/ports/waypoint labels are compared. Workload
// status may change normally; a recreated Service or a changed selector may not.
func ambientServiceFingerprint(o map[string]any) string {
	lb := ambient.M(ambient.Path(o, "metadata", "labels"))
	b, _ := json.Marshal(map[string]any{"uid": ambient.Path(o, "metadata", "uid"), "ports": ambient.Path(o, "spec", "ports"), "selector": ambient.Path(o, "spec", "selector"), "waypoint": lb["istio.io/use-waypoint"], "waypointNamespace": lb["istio.io/use-waypoint-namespace"]})
	return string(b)
}
func (s *Server) ambientPreflight(ctx context.Context, c ambientConnection, p *ambient.Plan) error {
	q := p.Request
	if q.Demo != nil {
		return s.bookinfoPreflight(ctx, c, p)
	}
	for _, e := range p.Evidence {
		if e.Field == "l4-dependencies" {
			list, err := c.client.Resource(schema.GroupVersionResource{Group: e.Ref.Group, Version: "v1", Resource: e.Ref.Resource}).List(ctx, metav1.ListOptions{})
			if err != nil {
				return fmt.Errorf("recheck L4 evidence: %w", err)
			}
			values := map[string]any{}
			for _, o := range list.Items {
				values[o.GetNamespace()+"/"+o.GetName()] = ambient.L4DependencyObject(o.Object)
			}
			b, _ := json.Marshal(values)
			if string(b) != e.Value {
				return errors.New("L4 policy or waypoint identity dependencies changed; refresh the preview")
			}
			continue
		}
		if e.Field != "spec.serviceAccountName" && e.Field != "data.mesh" {
			continue
		}
		captured := s.WS.Get(e.Ref.Cluster, e.Ref.Group, e.Ref.Resource, e.Ref.Namespace, e.Ref.Name)
		if captured == nil {
			return errors.New("waypoint identity evidence is no longer captured")
		}
		live, err := c.client.Resource(schema.GroupVersionResource{Group: captured.Group, Version: captured.Version, Resource: captured.Resource}).Namespace(captured.Namespace).Get(ctx, captured.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		keys := strings.Split(e.Field, ".")
		if ambient.S(ambient.Path(captured.Obj, "metadata", "uid")) != string(live.GetUID()) || !ambient.Equal(ambient.Path(captured.Obj, keys...), ambient.Path(live.Object, keys...)) {
			return errors.New("waypoint identity or trust domain changed; refresh before testing")
		}
	}
	if q.Explain != nil {
		x, err := reach.Explain(s.WS, q.Cluster, q.Explain.From, q.Explain.To)
		if err != nil {
			return err
		}
		if x.From.Ref == nil {
			return errors.New("capture the source workload before testing")
		}
		ref := x.From.Ref
		captured := s.WS.Get(q.Cluster, ref.Group, ref.Resource, ref.Namespace, ref.Name)
		if captured == nil || ambient.S(ambient.Path(captured.Obj, "metadata", "uid")) == "" {
			return errors.New("testing an explainer repair requires a runtime source UID")
		}
		live, err := c.client.Resource(schema.GroupVersionResource{Group: captured.Group, Version: captured.Version, Resource: captured.Resource}).Namespace(captured.Namespace).Get(ctx, captured.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if ambient.S(ambient.Path(captured.Obj, "metadata", "uid")) != string(live.GetUID()) || !ambient.Equal(captured.Obj["spec"], live.Object["spec"]) || !ambient.Equal(ambient.Path(captured.Obj, "metadata", "labels"), ambient.Path(live.Object, "metadata", "labels")) {
			return errors.New("source identity or workload configuration changed; refresh before testing the repair")
		}
	}
	if q.Scorecard != nil {
		ref := q.Scorecard.Target
		captured := ambient.Target(s.WS, ref.Cluster, ref.Kind, ref.Namespace, ref.Name)
		if captured == nil || ambient.S(ambient.Path(captured.Obj, "metadata", "uid")) == "" {
			return errors.New("refresh the runtime capture before testing this Scorecard fix")
		}
		gvr := schema.GroupVersionResource{Group: captured.Group, Version: captured.Version, Resource: captured.Resource}
		res := c.client.Resource(gvr)
		var live *unstructured.Unstructured
		var err error
		if captured.Namespace == "" {
			live, err = res.Get(ctx, captured.Name, metav1.GetOptions{})
		} else {
			live, err = res.Namespace(captured.Namespace).Get(ctx, captured.Name, metav1.GetOptions{})
		}
		if err != nil {
			return err
		}
		if string(live.GetUID()) != ambient.S(ambient.Path(captured.Obj, "metadata", "uid")) {
			return errors.New("target belongs to another cluster or was recreated; refresh before applying")
		}
		if !ambient.Equal(captured.Obj["spec"], live.Object["spec"]) || !ambient.Equal(ambient.Path(captured.Obj, "metadata", "labels"), ambient.Path(live.Object, "metadata", "labels")) {
			return errors.New("target configuration changed since capture; refresh before applying the Scorecard fix")
		}
	}
	names := map[string]bool{}
	if q.Service != "" {
		names[q.Service] = true
	}
	for _, b := range q.Backends {
		names[b.Name] = true
	}
	if q.Mirror != nil {
		names[q.Mirror.Name] = true
	}
	for name := range names {
		captured := s.WS.Get(q.Cluster, "", "services", q.Namespace, name)
		if captured == nil || ambient.S(ambient.Path(captured.Obj, "metadata", "uid")) == "" {
			return errors.New("testing requires a runtime capture with Service UIDs; open a live cluster or make a fresh capture")
		}
		live, err := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace(q.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if ambientServiceFingerprint(captured.Obj) != ambientServiceFingerprint(live.Object) {
			return fmt.Errorf("Service %s/%s differs from the capture (UID, selector, port or waypoint); choose the matching context and refresh the workspace", q.Namespace, name)
		}
	}
	// Namespace inheritance and waypoint settings must not drift between preview and apply.
	if n := s.WS.Get(q.Cluster, "", "namespaces", "", q.Namespace); n != nil {
		live, err := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, q.Namespace, metav1.GetOptions{})
		if err != nil {
			return err
		}
		for _, k := range []string{"istio.io/dataplane-mode", "istio.io/use-waypoint", "istio.io/use-waypoint-namespace"} {
			if n.Labels()[k] != live.GetLabels()[k] {
				return fmt.Errorf("namespace %s label %s changed; refresh the workspace", q.Namespace, k)
			}
		}
	}
	svc := s.WS.Get(q.Cluster, "", "services", q.Namespace, q.Service)
	var wn, wp string
	var gw *store.Object
	if svc != nil {
		wn, wp, gw = ambient.Waypoint(s.WS, svc)
	}
	if gw != nil {
		live, err := c.client.Resource(schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gateways"}).Namespace(wn).Get(ctx, wp, metav1.GetOptions{})
		if err != nil {
			return err
		}
		a, _ := json.Marshal(gw.Obj["spec"])
		b, _ := json.Marshal(live.Object["spec"])
		if string(a) != string(b) || ambient.S(ambient.Path(gw.Obj, "metadata", "uid")) != string(live.GetUID()) {
			return errors.New("waypoint configuration changed or belongs to a different cluster; refresh the workspace")
		}
	}
	if q.Waypoint != nil && q.Waypoint.Mode == "new" {
		cls, e := c.client.Resource(schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gatewayclasses"}).Get(ctx, q.Waypoint.Class, metav1.GetOptions{})
		if e != nil {
			return fmt.Errorf("verify waypoint GatewayClass: %w", e)
		}
		controller := ambient.S(ambient.Path(cls.Object, "spec", "controllerName"))
		if !strings.Contains(controller, "mesh-controller") && !strings.Contains(controller, "agentgateway") {
			return fmt.Errorf("selected class is not served by a supported waypoint controller")
		}
	}
	return ambientChangesPreflight(ctx, c, p)
}

func (s *Server) ambientRecords() error {
	if s.ambientS.records == nil {
		s.ambientS.records = map[string]*ambientRecord{}
	}
	dir, err := s.ambientDir()
	if err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var rec ambientRecord
		if err = json.Unmarshal(b, &rec); err != nil {
			return err
		}
		decoded, e := hex.DecodeString(rec.Token)
		if e != nil || len(decoded) != 24 || rec.Plan == nil {
			return fmt.Errorf("invalid ambient test record %s", file)
		}
		s.ambientS.records[rec.Token] = &rec
	}
	return nil
}
func (s *Server) ambientDir() (string, error) {
	dir := s.AmbientStateDir
	if dir == "" {
		home := os.Getenv("FLIGHTBAY_HOME")
		if home == "" {
			h, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			home = filepath.Join(h, ".flightbay")
		}
		dir = filepath.Join(home, "ambient-tests")
	}
	return dir, os.MkdirAll(dir, 0o700)
}
func (s *Server) ambientOwner() string {
	sum := sha256.Sum256([]byte(s.Token))
	return hex.EncodeToString(sum[:])
}
func (s *Server) saveAmbientRecord(rec *ambientRecord) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	dir, err := s.ambientDir()
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".ambient-tests-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, rec.Token+".json"))
}
func (s *Server) ambientTests(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.ambientForget(w, r)
		return
	}
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET or POST only")
		return
	}
	s.ambientS.mu.Lock()
	defer s.ambientS.mu.Unlock()
	if err := s.ambientRecords(); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	out := []*ambientRecord{}
	for _, rec := range s.ambientS.records {
		// A change whose namespace no longer exists went with it: mark it so
		// the Changes list does not keep offering Undo for nothing.
		if rec.ApplyState == "applied" && rec.Plan != nil && rec.Plan.Request.Namespace != "" && s.WS != nil && time.Since(rec.ApplyFinished) > 2*time.Minute {
			q := rec.Plan.Request
			if s.WS.Get(q.Cluster, "", "namespaces", "", q.Namespace) == nil && s.WS.CollectionState(q.Cluster, "", "namespaces", "Namespace", false).State == store.StateComplete {
				rec.ApplyState = "removed"
				rec.ApplyError = "namespace " + q.Namespace + " was deleted, and this change with it"
				_ = s.saveAmbientRecord(rec)
			}
		}
		if len(rec.Created) > 0 || len(rec.Modified) > 0 {
			out = append(out, rec)
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) ambientTest(w http.ResponseWriter, r *http.Request) {
	var req ambientTestRequest
	if !ambientBody(w, r, &req) {
		return
	}
	s.ambientS.mu.Lock()
	defer s.ambientS.mu.Unlock()
	if err := s.ambientRecords(); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if req.Operation == "validate" {
		if req.Request.Demo != nil && req.Request.Demo.Preview {
			jerr(w, 400, "a preview plan cannot be validated or applied; prepare the real plan first")
			return
		}
		if req.Context == "" {
			jerr(w, 400, "select a real kubeconfig context explicitly")
			return
		}
		p, err := s.generateReviewedPlan(req.Request)
		if err != nil {
			jerr(w, 400, err.Error())
			return
		}
		if req.PlanID == "" || req.PlanID != p.ID {
			jerr(w, 409, "the configuration or choices changed since preview; regenerate the YAML before validating")
			return
		}
		if len(p.Objects) == 0 && len(p.Changes) == 0 {
			jerr(w, 400, "this preview contains no changes to apply")
			return
		}
		c, err := s.ambientConnect(req.Context)
		if err != nil {
			jerr(w, 400, err.Error())
			return
		}
		if err = s.ambientPreflight(ctx, c, p); err != nil {
			jerr(w, 409, err.Error())
			return
		}
		for _, o := range p.Objects {
			u := &unstructured.Unstructured{Object: o}
			res, err := ambientResource(c, u.GetAPIVersion(), u.GetKind(), u.GetNamespace())
			if err != nil {
				jerr(w, 400, err.Error())
				return
			}
			if _, err = res.Create(ctx, u, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "flightbay-tests"}); err != nil {
				jerr(w, 400, "server dry-run: "+err.Error())
				return
			}
		}
		for _, ch := range p.Changes {
			if _, err := ambientPatch(ctx, c, ch, false, true); err != nil {
				jerr(w, 400, "server dry-run of existing-resource change: "+err.Error())
				return
			}
		}
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		token := hex.EncodeToString(b)
		rec := &ambientRecord{Owner: s.ambientOwner(), Token: token, Context: req.Context, Server: c.host, Plan: p, Validated: time.Now().UTC(), Created: []ambientCreated{}}
		s.ambientS.records[token] = rec
		if err = s.saveAmbientRecord(rec); err != nil {
			delete(s.ambientS.records, token)
			jerr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, rec)
		return
	}
	rec := s.ambientS.records[req.Validation]
	if rec == nil || req.Context != rec.Context {
		jerr(w, 400, "select the same context and validate this plan first")
		return
	}
	c, err := s.ambientConnect(req.Context)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	if c.host != rec.Server {
		jerr(w, 409, "the kubeconfig context now names a different server; validate again")
		return
	}
	switch req.Operation {
	case "apply":
		if rec.Owner != s.ambientOwner() {
			jerr(w, 409, "validate again in this desk before applying; earlier test resources can still be cleaned up")
			return
		}
		if time.Since(rec.Validated) > 10*time.Minute || len(rec.Created) > 0 || len(rec.Modified) > 0 {
			jerr(w, 409, "validation expired or test already applied; cleanup and validate again")
			return
		}
		rec.ApplyState = "applying"
		rec.ApplyError = ""
		rec.ApplyStarted = time.Now().UTC()
		rec.ApplyFinished = time.Time{}
		if err := s.saveAmbientRecord(rec); err != nil {
			jerr(w, 500, "cannot record installation start: "+err.Error())
			return
		}
		if err := s.ambientPreflight(ctx, c, rec.Plan); err != nil {
			rec.ApplyState = "failed"
			rec.ApplyError = err.Error()
			rec.ApplyFinished = time.Now().UTC()
			_ = s.saveAmbientRecord(rec)
			jerr(w, 409, err.Error())
			return
		}
		for _, o := range rec.Plan.Objects {
			u := &unstructured.Unstructured{Object: o}
			res, err := ambientResource(c, u.GetAPIVersion(), u.GetKind(), u.GetNamespace())
			if err == nil {
				var created *unstructured.Unstructured
				created, err = res.Create(ctx, u, metav1.CreateOptions{FieldManager: "flightbay-tests"})
				if err == nil {
					rec.Created = append(rec.Created, ambientCreated{u.GetAPIVersion(), u.GetKind(), u.GetNamespace(), u.GetName(), created.GetUID()})
					err = s.saveAmbientRecord(rec)
				}
			}
			if err != nil {
				cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
				rollback := cleanupAmbient(cleanupCtx, c, rec)
				stop()
				rec.ApplyState = "failed"
				rec.ApplyError = fmt.Sprintf("apply failed: %v; rollback: %v", err, rollback)
				rec.ApplyFinished = time.Now().UTC()
				_ = s.saveAmbientRecord(rec)
				writeJSON(w, 400, map[string]any{"error": fmt.Sprintf("apply failed: %v; rollback: %v", err, rollback), "test": rec})
				return
			}
		}
		applyErr := waitAmbientWaypoint(ctx, c, rec.Plan)
		if applyErr == nil {
			for _, ch := range rec.Plan.Changes {
				applied, e := ambientPatch(ctx, c, ch, false, false)
				if e != nil {
					applyErr = e
					break
				}
				// Admission can materialise defaults, for example omitted route
				// weights. Undo must compare with the actual admitted field values.
				ch.Fields = append([]ambient.FieldChange{}, ch.Fields...)
				for i := range ch.Fields {
					v, present := ambient.Pointer(applied.Object, ch.Fields[i].Path)
					ch.Fields[i].After = ambient.Copy(v)
					ch.Fields[i].AfterPresent = present
				}
				rec.Modified = append(rec.Modified, ch)
				if e := s.saveAmbientRecord(rec); e != nil {
					applyErr = e
					break
				}
				if ch.Delete {
					if e := waitAmbientDeletion(ctx, c, ch); e != nil {
						applyErr = e
						break
					}
				}
			}
		}
		if applyErr != nil {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			rollback := cleanupAmbient(cleanupCtx, c, rec)
			stop()
			rec.ApplyState = "failed"
			rec.ApplyError = fmt.Sprintf("test change failed: %v; undo: %v", applyErr, rollback)
			rec.ApplyFinished = time.Now().UTC()
			_ = s.saveAmbientRecord(rec)
			writeJSON(w, 400, map[string]any{"error": fmt.Sprintf("test change failed: %v; undo: %v", applyErr, rollback), "test": rec})
			return
		}
		rec.ApplyState = "applied"
		rec.ApplyFinished = time.Now().UTC()
		if err := s.saveAmbientRecord(rec); err != nil {
			writeJSON(w, 500, map[string]any{"test": rec, "error": "Configuration was applied, but its final status could not be saved: " + err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"test": rec, "message": "Configuration applied. Workload readiness and request connectivity have not yet been verified."})
	case "verified":
		ok := req.Verified
		rec.Verified = &ok
		rec.VerifiedAt = time.Now().UTC()
		if err := s.saveAmbientRecord(rec); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"test": rec})
	case "cleanup":
		if err := ambientCleanupDependency(rec, s.ambientS.records); err != nil {
			jerr(w, 409, err.Error())
			return
		}
		if err := cleanupAmbient(ctx, c, rec); err != nil {
			_ = s.saveAmbientRecord(rec)
			writeJSON(w, 409, map[string]any{"error": err.Error(), "test": rec})
			return
		}
		rec.ApplyState = "removed"
		if err := s.saveAmbientRecord(rec); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"test": rec, "message": "Test changes were undone and created resources are absent. Unrelated fields were preserved."})
	case "status":
		out := []map[string]any{}
		for _, ch := range rec.Modified {
			res, e := ambientChangeResource(c, ch)
			if e != nil {
				jerr(w, 400, e.Error())
				return
			}
			o, e := res.Get(ctx, ch.Ref.Name, metav1.GetOptions{})
			item := map[string]any{"changed": ch.Ref}
			if ch.Delete && apierrors.IsNotFound(e) {
				item["deleted"] = true
			} else if e != nil {
				item["error"] = e.Error()
			} else {
				item["status"] = o.Object["status"]
				fields := map[string]any{}
				for _, f := range ch.Fields {
					v, _ := ambient.Pointer(o.Object, f.Path)
					fields[f.Path] = v
				}
				item["currentFields"] = fields
			}
			out = append(out, item)
		}
		for _, o := range rec.Created {
			res, err := ambientResource(c, o.APIVersion, o.Kind, o.Namespace)
			if err != nil {
				jerr(w, 400, err.Error())
				return
			}
			u, err := res.Get(ctx, o.Name, metav1.GetOptions{})
			if err != nil {
				out = append(out, map[string]any{"object": o, "error": err.Error()})
				continue
			}
			if u.GetUID() != o.UID {
				out = append(out, map[string]any{"object": o, "error": "object UID changed; this is not the created test resource"})
				continue
			}
			out = append(out, map[string]any{"object": o, "status": u.Object["status"], "generation": u.GetGeneration()})
		}
		writeJSON(w, 200, out)
	default:
		jerr(w, 400, "operation must be validate, apply, status or cleanup")
	}
}
func cleanupAmbient(ctx context.Context, c ambientConnection, rec *ambientRecord) error {
	demo := rec.Plan.Request.Demo != nil && rec.Plan.Request.Demo.Stage == "workloads"
	if demo {
		if err := bookinfoDeletePreflight(ctx, c, rec); err != nil {
			return err
		}
	}
	if err := undoAmbientChanges(ctx, c, rec); err != nil {
		return err
	}
	if demo {
		if err := bookinfoClearLegacyBinding(ctx, c, rec); err != nil {
			return err
		}
	}
	left := []ambientCreated{}
	var errs error
	for i := len(rec.Created) - 1; i >= 0; i-- {
		o := rec.Created[i]
		res, err := ambientResource(c, o.APIVersion, o.Kind, o.Namespace)
		if err == nil {
			var u *unstructured.Unstructured
			u, err = res.Get(ctx, o.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err == nil {
				if o.Kind == "Namespace" {
					err = fmt.Errorf("namespace %s is retained; namespace deletion is not performed automatically", o.Name)
				} else if o.UID == "" || u.GetUID() != o.UID || u.GetLabels()["app.kubernetes.io/managed-by"] != "flightbay" || u.GetLabels()["flightbay.dev/test-id"] != rec.Plan.ID {
					err = fmt.Errorf("refusing to delete %s: UID or ownership differs", o.Name)
				} else {
					err = res.Delete(ctx, o.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &o.UID}})
					if apierrors.IsNotFound(err) {
						err = nil
					}
					if err == nil {
						for {
							_, e := res.Get(ctx, o.Name, metav1.GetOptions{})
							if apierrors.IsNotFound(e) {
								break
							}
							if e != nil {
								err = e
								break
							}
							select {
							case <-ctx.Done():
								err = ctx.Err()
							case <-time.After(200 * time.Millisecond):
							}
							if err != nil {
								break
							}
						}
					}
				}
			}
		}
		if err != nil {
			left = append(left, o)
			errs = errors.Join(errs, err)
		}
	}
	rec.Created = left
	return errs
}

// ambientForget stops tracking change records: the local JSON is deleted and
// nothing in any cluster is touched. Records still applied can only be
// forgotten one at a time with force, so live changes are not orphaned by a
// bulk clear.
func (s *Server) ambientForget(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Tokens []string `json:"tokens"`
		Force  bool     `json:"force"`
	}
	if !ambientBody(w, r, &q) {
		return
	}
	s.ambientS.mu.Lock()
	defer s.ambientS.mu.Unlock()
	if err := s.ambientRecords(); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	dir, err := s.ambientDir()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	forgotten := 0
	for _, t := range q.Tokens {
		rec := s.ambientS.records[t]
		if rec == nil {
			continue
		}
		if (rec.ApplyState == "applied" || rec.ApplyState == "applying") && !q.Force {
			jerr(w, 409, "a change that is still applied must be undone first, or forgotten with force (the cluster is left as it is)")
			return
		}
		if err := os.Remove(filepath.Join(dir, rec.Token+".json")); err != nil && !os.IsNotExist(err) {
			jerr(w, 500, err.Error())
			return
		}
		delete(s.ambientS.records, t)
		forgotten++
	}
	writeJSON(w, 200, map[string]any{"forgotten": forgotten})
}
