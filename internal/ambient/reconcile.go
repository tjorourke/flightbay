package ambient

import (
	"fmt"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func flightbayOwned(o *store.Object) bool {
	return o != nil && o.Labels()["app.kubernetes.io/managed-by"] == "flightbay" && o.Labels()["flightbay.dev/test-id"] != ""
}

// ReconcilePlan gives every generated Configure resource the same lifecycle:
// create if absent, patch reviewed fields if owned, and omit matching fields.
// Explicit edits retain their dedicated field-level editors. No local installer
// history is needed, and existing ownership labels are never reassigned.
func ReconcilePlan(ws *store.Workspace, p *Plan) (*Plan, error) {
	if p.Request.Demo != nil {
		return p, nil // the demo installer deliberately requires a fresh namespace
	}
	creates := []map[string]any{}
	reconciled := false
	for _, desired := range p.Objects {
		gv, err := schema.ParseGroupVersion(S(desired["apiVersion"]))
		if err != nil {
			return nil, err
		}
		kind, ns, name := S(desired["kind"]), S(Path(desired, "metadata", "namespace")), S(Path(desired, "metadata", "name"))
		current := Target(ws, p.Request.Cluster, kind, ns, name)
		if current == nil || current.Group != gv.Group {
			creates = append(creates, desired)
			continue
		}
		if !flightbayOwned(current) {
			return nil, fmt.Errorf("A %s named %s/%s already exists and is not managed by Flightbay. Choose another name.", kind, ns, name)
		}
		if kind == "AuthorizationPolicy" && contains(p.Request.Tasks, "access-rule") && !Equal(Path(current.Obj, "spec", "rules"), Path(desired, "spec", "rules")) {
			return nil, fmt.Errorf("select the existing access policy explicitly to append rules or review a full-policy replacement")
		}
		values := map[string]any{}
		configurationFields(kind, "/spec", current.Obj["spec"], desired["spec"], values)
		for _, field := range []string{"labels", "annotations"} {
			for key, value := range M(Path(desired, "metadata", field)) {
				if field == "labels" && (key == "flightbay.dev/test-id" || key == "app.kubernetes.io/managed-by") {
					continue
				}
				values["/metadata/"+field+"/"+pointerKey(key)] = value
			}
		}
		change(p, current, "Update the existing Flightbay configuration; preserve unedited fields and ownership", values)
		reconciled = true
	}
	if !reconciled {
		return p, nil
	}
	p.Objects = creates
	return finishPlan(p)
}

func appendUniqueRules(before, fresh []any) []any {
	for _, rule := range fresh {
		found := false
		for _, old := range before {
			if Equal(old, rule) {
				found = true
				break
			}
		}
		if !found {
			before = append(before, Copy(rule))
		}
	}
	return before
}

func configurationFields(kind, path string, before, desired any, values map[string]any) {
	if want, ok := desired.(map[string]any); ok {
		if have, ok := before.(map[string]any); ok {
			for key, value := range want {
				configurationFields(kind, path+"/"+pointerKey(key), have[key], value, values)
			}
			return
		}
	}
	if kind == "HTTPRoute" && path == "/spec/rules" && before != nil {
		old, want := L(before), L(desired)
		if len(old) >= len(want) {
			merged := L(Copy(old))
			for i, rule := range want {
				fields := map[string]any{}
				configurationFields(kind, "", old[i], rule, fields)
				for field, value := range fields {
					setSnippet(M(merged[i]), strings.Split(strings.TrimPrefix(field, "/"), "/"), value)
				}
			}
			values[path] = merged
			return
		}
	}
	if strings.HasSuffix(path, "/backendRefs") || path == "/spec/parentRefs" || path == "/spec/targetRefs" || (kind == "Service" && path == "/spec/ports") {
		desired = configuredReferences(path, L(before), L(desired))
	}
	values[path] = desired
}

// API admission fills reference defaults. Match references by identity rather
// than array position, retaining per-backend filters and other unedited fields.
func configuredReferences(path string, before, desired []any) []any {
	out := []any{}
	for _, item := range desired {
		want := M(Copy(item))
		for _, old := range before {
			have := M(old)
			if S(have["name"]) != S(want["name"]) || N(have["port"]) != N(want["port"]) {
				continue
			}
			matches := true
			for _, key := range []string{"group", "kind", "namespace"} {
				if value, explicit := want[key]; explicit && !Equal(value, have[key]) {
					matches = false
				}
			}
			if !matches {
				continue
			}
			merged := M(Copy(have))
			for key, value := range want {
				merged[key] = value
			}
			if strings.HasSuffix(path, "/backendRefs") {
				if _, explicit := want["weight"]; !explicit {
					if _, admitted := have["weight"]; admitted {
						merged["weight"] = 1
					}
				}
			}
			want = merged
			break
		}
		out = append(out, want)
	}
	return out
}
