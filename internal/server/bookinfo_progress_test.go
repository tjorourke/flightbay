package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBookinfoDeploymentReadinessRequiresCurrentFullRollout(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		observed, updated, available int64
		want                         string
	}{
		{"old generation", 1, 2, 2, "pending"}, {"partial replicas", 2, 1, 1, "pending"}, {"old replicas available", 2, 1, 2, "pending"}, {"full rollout", 2, 2, 2, "ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"generation": int64(2)}, "spec": map[string]any{"replicas": int64(2)}, "status": map[string]any{"replicas": int64(2), "observedGeneration": tc.observed, "updatedReplicas": tc.updated, "availableReplicas": tc.available}}}
			if got, _ := demoDeploymentReady(u); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestBookinfoReadinessRejectsStaleControllerConditions(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"kind": "Gateway", "metadata": map[string]any{"generation": int64(2)}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Programmed", "status": "True", "observedGeneration": int64(1)}}}}}
	if ready, _ := currentDemoCondition(u, "Programmed"); ready {
		t.Fatal("stale condition accepted")
	}
}

func TestBookinfoProgressReadsSnapshotsWithoutApplyLock(t *testing.T) {
	c, _, rec := bookinfoRemovalFixture(t)
	rec.Context = "mesh1"
	rec.Server = c.host
	rec.Token = "progress-test"
	rec.ApplyState = "applied"
	rec.Validated = time.Now()
	// Only a Service is needed to prove stage/identity handling; no controller wait.
	rec.Created = rec.Created[:1]
	rec.Plan.Objects = []map[string]any{{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"namespace": "bookinfo", "name": "productpage"}}}
	s := &Server{AmbientStateDir: t.TempDir()}
	s.ambientS.connect = func(string) (ambientConnection, error) { return c, nil }
	if err := s.saveAmbientRecord(rec); err != nil {
		t.Fatal(err)
	}
	s.ambientS.mu.Lock()
	defer s.ambientS.mu.Unlock()
	check := func() demoProgress {
		t.Helper()
		w := httptest.NewRecorder()
		s.bookinfoProgress(w, httptest.NewRequest("GET", "/api/ambient/bookinfo/progress?cluster=mesh1&namespace=bookinfo&context=mesh1&planId=plan", nil))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var out demoProgress
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := check(); out.State != "ready" || out.Ready != 1 || !strings.Contains(out.Message, "connectivity has not been tested") {
		t.Fatalf("unexpected readiness: %+v", out)
	}
	rec.Created[0].UID = "replacement"
	_ = s.saveAmbientRecord(rec)
	if out := check(); out.State != "failed" || out.Ready != 0 {
		t.Fatalf("changed UID counted ready: %+v", out)
	}
	rec.ApplyState = "applying"
	rec.Created = nil
	_ = s.saveAmbientRecord(rec)
	if out := check(); out.State != "applying" || out.Created != 0 {
		t.Fatalf("missing in-flight state: %+v", out)
	}
	w := httptest.NewRecorder()
	s.bookinfoInstallations(w, httptest.NewRequest("GET", "/api/ambient/bookinfo/installations?namespace=bookinfo", nil))
	var rows []ambientRecord
	_ = json.Unmarshal(w.Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0].ApplyState != "applying" {
		t.Fatalf("reload loses in-progress record: %s", w.Body)
	}
	rec.ApplyState = "failed"
	rec.ApplyError = "waypoint timed out"
	_ = s.saveAmbientRecord(rec)
	if out := check(); out.State != "failed" || out.Message != rec.ApplyError {
		t.Fatalf("missing apply failure: %+v", out)
	}
	rec.ApplyState = "applied"
	rec.Created = []ambientCreated{{APIVersion: "v1", Kind: "Service", Namespace: "bookinfo", Name: "productpage", UID: "svc"}}
	rec.Plan.Request.Demo = &ambient.BookinfoRequest{Stage: "namespace"}
	_ = s.saveAmbientRecord(rec)
	if out := check(); out.State != "namespace-ready" || !strings.Contains(out.Message, "still need") {
		t.Fatalf("namespace claimed complete deployment: %+v", out)
	}
	rec.Server = "https://different"
	_ = s.saveAmbientRecord(rec)
	w = httptest.NewRecorder()
	s.bookinfoProgress(w, httptest.NewRequest("GET", "/api/ambient/bookinfo/progress?cluster=mesh1&namespace=bookinfo&context=mesh1&planId=plan", nil))
	if w.Code != 409 {
		t.Fatalf("context mismatch accepted: %d", w.Code)
	}
}
