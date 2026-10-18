package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type demoResourceProgress struct {
	Ref    ambient.Ref `json:"ref"`
	State  string      `json:"state"`
	Detail string      `json:"detail"`
}
type demoProgress struct {
	Cluster   string                 `json:"cluster"`
	Namespace string                 `json:"namespace"`
	Context   string                 `json:"context"`
	PlanID    string                 `json:"planId"`
	Stage     string                 `json:"stage"`
	State     string                 `json:"state"`
	Message   string                 `json:"message"`
	Expected  int                    `json:"expected"`
	Created   int                    `json:"created"`
	Ready     int                    `json:"ready"`
	Resources []demoResourceProgress `json:"resources"`
	CheckedAt time.Time              `json:"checkedAt"`
}

func currentDemoCondition(u *unstructured.Unstructured, typ string) (bool, string) {
	conditions := ambient.L(ambient.Path(u.Object, "status", "conditions"))
	if u.GetKind() == "HTTPRoute" {
		conditions = nil
		for _, p := range ambient.L(ambient.Path(u.Object, "status", "parents")) {
			conditions = append(conditions, ambient.L(ambient.M(p)["conditions"])...)
		}
	}
	for _, v := range conditions {
		c := ambient.M(v)
		if ambient.S(c["type"]) == typ && int64(ambient.N(c["observedGeneration"])) == u.GetGeneration() {
			return ambient.S(c["status"]) == "True", strings.TrimSpace(ambient.S(c["reason"]) + " " + ambient.S(c["message"]))
		}
	}
	return false, "No current " + typ + " condition yet"
}
func demoDeploymentReady(u *unstructured.Unstructured) (string, string) {
	want := ambient.N(ambient.Path(u.Object, "spec", "replicas"))
	if ambient.Path(u.Object, "spec", "replicas") == nil {
		want = 1
	}
	if want == 0 {
		return "failed", "Deployment is scaled to zero; no Bookinfo Pods are running"
	}
	observed := int64(ambient.N(ambient.Path(u.Object, "status", "observedGeneration")))
	if observed >= u.GetGeneration() && ambient.N(ambient.Path(u.Object, "status", "availableReplicas")) >= want && ambient.N(ambient.Path(u.Object, "status", "updatedReplicas")) == want && ambient.N(ambient.Path(u.Object, "status", "replicas")) == want {
		return "ready", fmt.Sprintf("%d/%d current replicas available", want, want)
	}
	for _, v := range ambient.L(ambient.Path(u.Object, "status", "conditions")) {
		c := ambient.M(v)
		if ambient.S(c["type"]) == "ReplicaFailure" && ambient.S(c["status"]) == "True" || ambient.S(c["reason"]) == "ProgressDeadlineExceeded" {
			return "failed", strings.TrimSpace(ambient.S(c["reason"]) + " " + ambient.S(c["message"]))
		}
	}
	return "pending", fmt.Sprintf("Starting Pods: %d/%d replicas available", ambient.N(ambient.Path(u.Object, "status", "availableReplicas")), want)
}

func (s *Server) bookinfoInstallations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	dir, err := s.ambientDir()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	out := []*ambientRecord{}
	for _, name := range files {
		b, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		var rec ambientRecord
		if json.Unmarshal(b, &rec) != nil || rec.Plan == nil || rec.Plan.Request.Demo == nil || rec.Plan.Request.Namespace != r.URL.Query().Get("namespace") || rec.ApplyState == "removed" {
			continue
		}
		if s.WS != nil {
			known := false
			for _, cluster := range s.WS.Clusters() {
				known = known || cluster == rec.Plan.Request.Cluster
			}
			if !known {
				continue
			}
		}
		if rec.ApplyState == "" && len(rec.Created) == 0 && len(rec.Modified) == 0 {
			continue
		}
		out = append(out, &rec)
	}
	writeJSON(w, 200, out)
}

// Reads atomic on-disk snapshots rather than the apply mutex. This lets the UI
// display progress while the apply request is creating/waiting on resources.
func (s *Server) bookinfoProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	q := r.URL.Query()
	dir, err := s.ambientDir()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	var rec *ambientRecord
	for _, name := range files {
		b, e := os.ReadFile(name)
		if e != nil {
			continue
		}
		var candidate ambientRecord
		if json.Unmarshal(b, &candidate) != nil || candidate.Plan == nil || candidate.Plan.Request.Demo == nil {
			continue
		}
		p := candidate.Plan
		if p.ID != q.Get("planId") || p.Request.Cluster != q.Get("cluster") || p.Request.Namespace != q.Get("namespace") || candidate.Context != q.Get("context") {
			continue
		}
		if candidate.ApplyState == "" && len(candidate.Created) == 0 && len(candidate.Modified) == 0 {
			continue
		}
		if candidate.ApplyState == "removed" {
			continue
		}
		if rec == nil || candidate.Validated.After(rec.Validated) {
			copy := candidate
			rec = &copy
		}
	}
	out := demoProgress{Cluster: q.Get("cluster"), Namespace: q.Get("namespace"), Context: q.Get("context"), PlanID: q.Get("planId"), State: "pending", Message: "Waiting for the installation request to start", Resources: []demoResourceProgress{}, CheckedAt: time.Now().UTC()}
	if rec == nil {
		writeJSON(w, 200, out)
		return
	}
	p := rec.Plan
	out.Stage = p.Request.Demo.Stage
	out.Expected = len(p.Objects) + len(p.Changes)
	out.Created = len(rec.Created) + len(rec.Modified)
	if rec.ApplyState == "failed" {
		out.State = "failed"
		out.Message = rec.ApplyError
	}
	var c ambientConnection
	if len(rec.Created) > 0 || len(rec.Modified) > 0 {
		c, err = s.ambientConnect(rec.Context)
		if err != nil {
			jerr(w, 400, err.Error())
			return
		}
		if c.host != rec.Server {
			jerr(w, 409, "Recorded context now names another API server; readiness not verified")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	created := map[string]ambientCreated{}
	var podBlocker string
	for _, o := range rec.Created {
		created[o.APIVersion+"/"+o.Kind+"/"+o.Namespace+"/"+o.Name] = o
	}
	for _, obj := range p.Objects {
		u := &unstructured.Unstructured{Object: obj}
		ref := ambient.Ref{Cluster: p.Request.Cluster, Group: u.GroupVersionKind().Group, Kind: u.GetKind(), Namespace: u.GetNamespace(), Name: u.GetName()}
		row := demoResourceProgress{Ref: ref, State: "pending", Detail: "Not created yet"}
		key := u.GetAPIVersion() + "/" + u.GetKind() + "/" + u.GetNamespace() + "/" + u.GetName()
		o, exists := created[key]
		if exists {
			res, e := ambientResource(c, o.APIVersion, o.Kind, o.Namespace)
			if e != nil {
				row.State, row.Detail = "unknown", e.Error()
			} else {
				live, e := res.Get(ctx, o.Name, metav1.GetOptions{})
				if apierrors.IsNotFound(e) {
					row.Detail = "Created resource is not present; waiting for reconciliation or inspect the installation"
				} else if e != nil {
					row.State, row.Detail = "unknown", e.Error()
				} else if live.GetUID() != o.UID {
					row.State, row.Detail = "failed", "Resource UID changed; this is not the recorded installation"
				} else if live.GetDeletionTimestamp() != nil {
					row.State, row.Detail = "pending", "Resource is being deleted"
				} else {
					row.State, row.Detail = "ready", "Created and ownership UID verified"
					switch o.Kind {
					case "Namespace":
						if ambient.S(ambient.Path(live.Object, "status", "phase")) != "Active" {
							row.State, row.Detail = "pending", "Waiting for namespace to become Active"
						}
					case "Deployment":
						row.State, row.Detail = demoDeploymentReady(live)
						if row.State == "pending" {
							match, _, _ := unstructured.NestedStringMap(live.Object, "spec", "selector", "matchLabels")
							if len(match) > 0 {
								pods, e := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(o.Namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(match).String()})
								if e == nil {
									for _, pod := range pods.Items {
										for _, v := range ambient.L(ambient.Path(pod.Object, "status", "conditions")) {
											cond := ambient.M(v)
											if ambient.S(cond["type"]) == "PodScheduled" && ambient.S(cond["status"]) == "False" {
												row.Detail = "Pod " + pod.GetName() + ": " + ambient.S(cond["reason"]) + " " + ambient.S(cond["message"])
												podBlocker = row.Detail
											}
										}
									}
								}
							}
						}
					case "Gateway", "HTTPRoute":
						types := []string{"Accepted", "ResolvedRefs"}
						if o.Kind == "Gateway" {
							types = []string{"Accepted", "Programmed"}
						}
						for _, typ := range types {
							ok, detail := currentDemoCondition(live, typ)
							if !ok {
								row.State, row.Detail = "pending", typ+": "+detail
								break
							}
						}
						if row.State == "ready" {
							row.Detail = "Current " + strings.Join(types, " and ") + " conditions are True"
						}
					}
				}
			}
		}
		if row.State == "ready" {
			out.Ready++
		}
		out.Resources = append(out.Resources, row)
	}
	for _, change := range p.Changes {
		row := demoResourceProgress{Ref: change.Ref, State: "pending", Detail: "Namespace binding has not been applied yet"}
		var done *ambient.Change
		for i := range rec.Modified {
			ch := &rec.Modified[i]
			if ch.Ref == change.Ref {
				done = ch
				break
			}
		}
		if done != nil {
			res, e := ambientChangeResource(c, *done)
			if e != nil {
				row.State, row.Detail = "unknown", e.Error()
			} else {
				live, e := res.Get(ctx, done.Ref.Name, metav1.GetOptions{})
				if e != nil {
					row.State, row.Detail = "unknown", e.Error()
				} else if string(live.GetUID()) != done.UID {
					row.State, row.Detail = "failed", "Namespace identity changed"
				} else {
					row.State, row.Detail = "ready", "Reviewed namespace binding applied"
					for _, f := range done.Fields {
						v, present := ambient.Pointer(live.Object, f.Path)
						if present != f.AfterPresent || present && !ambient.Equal(v, f.After) {
							row.State, row.Detail = "failed", "Reviewed namespace binding changed after installation"
						}
					}
				}
			}
		}
		if row.State == "ready" {
			out.Ready++
		}
		out.Resources = append(out.Resources, row)
	}
	if rec.ApplyState != "failed" {
		out.State = "starting"
		out.Message = "Configuration applied; waiting for workloads, waypoints and routes to become ready"
		if rec.ApplyState == "applying" {
			out.State = "applying"
			out.Message = "Creating reviewed resources and waiting for waypoint programming"
		} else if out.Expected > 0 && out.Ready == out.Expected {
			if out.Stage == "namespace" {
				out.State = "namespace-ready"
				out.Message = "Namespace is ready. Bookinfo workloads still need their own review and deployment."
			} else {
				out.State = "ready"
				out.Message = "Bookinfo workloads are ready; current waypoint and route conditions passed. Request connectivity has not been tested."
			}
		} else {
			for _, row := range out.Resources {
				if row.State == "failed" {
					out.State = "failed"
					out.Message = "Installation needs attention: " + row.Ref.Kind + " " + row.Ref.Name + ": " + row.Detail
					break
				}
				if row.State == "unknown" {
					out.State = "unknown"
					out.Message = "Readiness could not be fully verified; inspect resource feedback"
				}
			}
		}
	}
	if podBlocker != "" && out.State != "failed" {
		out.Message += ". Scheduling is blocked: " + podBlocker
	}
	writeJSON(w, 200, out)
}
