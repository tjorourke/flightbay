package ambient

import (
	"embed"
	"fmt"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

//go:embed demo/*.yaml
var bookinfoFiles embed.FS

type BookinfoRequest struct {
	Stage         string `json:"stage"` // namespace, workloads
	WaypointClass string `json:"waypointClass"`
	WaypointScope string `json:"waypointScope,omitempty"` // namespace or service
	WaypointName  string `json:"waypointName,omitempty"`  // shared name or dedicated prefix
	Global        bool   `json:"global"`
	Traffic       bool   `json:"traffic"`
	// Preview plans the workloads before the namespace exists, so one
	// review can show everything. A preview cannot be validated or applied.
	Preview bool `json:"preview,omitempty"`
}

var bookinfoServices = []string{"productpage", "details", "ratings", "reviews"}

func BookinfoSettings(d BookinfoRequest) (BookinfoRequest, error) {
	if d.WaypointScope == "" {
		d.WaypointScope = "namespace"
	}
	if d.WaypointName == "" {
		d.WaypointName = "bookinfo-waypoint"
	}
	if d.WaypointScope != "namespace" && d.WaypointScope != "service" {
		return d, fmt.Errorf("choose namespace or service waypoint scope")
	}
	if len(validation.IsDNS1123Label(d.WaypointName)) > 0 {
		return d, fmt.Errorf("use a valid Kubernetes name for the waypoint")
	}
	for _, svc := range bookinfoServices {
		if len(validation.IsDNS1123Label(BookinfoWaypointName(d, svc))) > 0 {
			return d, fmt.Errorf("waypoint prefix is too long for the dedicated Service names")
		}
	}
	return d, nil
}
func BookinfoWaypointName(d BookinfoRequest, svc string) string {
	if d.WaypointScope == "service" {
		return d.WaypointName + "-" + svc
	}
	return d.WaypointName
}
func BookinfoNamespaceBinding(d BookinfoRequest) string {
	if d.WaypointScope == "service" {
		return ""
	}
	return d.WaypointName
}

type DemoFile struct {
	Name string `json:"name"`
	YAML string `json:"yaml"`
}
type DemoCluster struct {
	OwnedResources  int           `json:"ownedResources"`
	Cluster         string        `json:"cluster"`
	NamespaceState  string        `json:"namespaceState"`
	Installed       bool          `json:"installed"`
	Classes         []ClassChoice `json:"classes"`
	GlobalSupported bool          `json:"globalSupported"`
	GlobalReason    string        `json:"globalReason"`
	Anchor          bool          `json:"anchor"`
}
type BookinfoCatalog struct {
	Clusters []DemoCluster `json:"clusters"`
	Files    []DemoFile    `json:"files"`
	Images   []string      `json:"images"`
	Notes    []string      `json:"notes"`
}

func demoGlobalSupported(ws *store.Workspace, c string, e Environment) (bool, string) {
	enterprise := false
	for _, comp := range e.Components {
		enterprise = enterprise || comp.Product == "Istio" && comp.Edition == "enterprise"
	}
	if !enterprise || !hasResource(ws, c, "networking.istio.io", "serviceentries") {
		return false, "Global publication requires captured Solo Enterprise Istio and its ServiceEntry API."
	}
	peer := false
	for _, g := range ws.List(c, GatewayGroup, "gateways", "", nil) {
		peer = peer || S(Path(g.Obj, "spec", "gatewayClassName")) == "istio-remote"
	}
	if !peer {
		return false, "No remote-peer Gateway is captured. Establish mesh peering before global publication."
	}
	return true, "Publishes productpage, details, ratings and reviews. Existing peering and an appropriate Istio licence are prerequisites; generated global DNS is verified after install."
}
func BookinfoOptions(ws *store.Workspace, ns string) BookinfoCatalog {
	out := BookinfoCatalog{Clusters: []DemoCluster{}, Files: []DemoFile{}, Images: []string{}, Notes: []string{"The application manifests are bundled with Flightbay; no remote YAML is fetched at deployment time.", "Images are pinned Docker Hub Istio Bookinfo 1.20.2 and curlimages/curl 8.12.1. Waypoint images are supplied by your installed Gateway controller, not replaced by Flightbay.", "Install has two explicit stages: create and confirm the namespace, then validate and confirm the namespaced workloads. Kubernetes cannot server-dry-run resources in a namespace that does not yet exist.", "No public ingress or cloud LoadBalancer is created. Use Test service or a Productpage port-forward after installation."}}
	for _, c := range ws.Clusters() {
		e := DetectEnvironment(ws, c, nil)
		row := DemoCluster{Cluster: c, NamespaceState: "absent", Classes: []ClassChoice{}}
		for _, cl := range e.Classes {
			if cl.Supported && strings.Contains(cl.Name, "waypoint") {
				row.Classes = append(row.Classes, cl)
			}
		}
		if n := ws.Get(c, "", "namespaces", "", ns); n != nil {
			row.NamespaceState = "occupied"
			if n.Labels()["flightbay.dev/demo"] == "bookinfo" {
				row.NamespaceState = "prepared"
			}
		}
		row.Installed = ws.Get(c, "apps", "deployments", ns, "productpage-v1") != nil
		for _, r := range []struct{ g, res string }{{"", "services"}, {"", "serviceaccounts"}, {"apps", "deployments"}, {GatewayGroup, "gateways"}, {GatewayGroup, "httproutes"}} {
			for _, o := range ws.List(c, r.g, r.res, ns, nil) {
				if o.Labels()["flightbay.dev/demo"] == "bookinfo" && o.Labels()["app.kubernetes.io/managed-by"] == "flightbay" {
					row.OwnedResources++
				}
			}
		}
		row.GlobalSupported, row.GlobalReason = demoGlobalSupported(ws, c, e)
		if a := ws.Get(c, "", "namespaces", "", "kube-system"); a != nil {
			row.Anchor = S(Path(a.Obj, "metadata", "uid")) != ""
		}
		out.Clusters = append(out.Clusters, row)
	}
	for _, name := range []string{"bookinfo.yaml", "waypoint-istio.yaml", "waypoint-agentgateway.yaml", "client.yaml"} {
		b, _ := bookinfoFiles.ReadFile("demo/" + name)
		out.Files = append(out.Files, DemoFile{name, string(b)})
		for _, doc := range strings.Split(string(b), "\n---\n") {
			var o map[string]any
			if yaml.Unmarshal([]byte(doc), &o) == nil {
				for _, ct := range L(Path(o, "spec", "template", "spec", "containers")) {
					image := S(M(ct)["image"])
					if image != "" && !contains(out.Images, image) {
						out.Images = append(out.Images, image)
					}
				}
			}
		}
	}
	return out
}
func GenerateBookinfo(ws *store.Workspace, req Request) (*Plan, error) {
	if req.Demo == nil || len(req.Tasks) != 1 || req.Tasks[0] != "bookinfo-demo" || req.Service != "" || req.Edit != nil {
		return nil, fmt.Errorf("review a Bookinfo demo separately from other tasks")
	}
	if len(validation.IsDNS1123Label(req.Namespace)) > 0 || contains([]string{"default", "istio-system", "istio-eastwest"}, req.Namespace) || strings.HasPrefix(req.Namespace, "kube-") {
		return nil, fmt.Errorf("choose a valid new application namespace, not a system namespace")
	}
	settings, err := BookinfoSettings(*req.Demo)
	if err != nil {
		return nil, err
	}
	req.Demo = &settings
	anchor := ws.Get(req.Cluster, "", "namespaces", "", "kube-system")
	if anchor == nil || S(Path(anchor.Obj, "metadata", "uid")) == "" {
		return nil, fmt.Errorf("capture cluster-scoped Namespace identities before deploying the demo")
	}
	e := DetectEnvironment(ws, req.Cluster, nil)
	var class *ClassChoice
	for _, cl := range e.Classes {
		if cl.Name == req.Demo.WaypointClass && cl.Supported && strings.Contains(cl.Name, "waypoint") {
			v := cl
			class = &v
		}
	}
	if class == nil {
		return nil, fmt.Errorf("choose a supported waypoint GatewayClass captured in this cluster")
	}
	if req.Demo.Global {
		if ok, why := demoGlobalSupported(ws, req.Cluster, e); !ok {
			return nil, fmt.Errorf("%s", why)
		}
	}
	p := basePlan(req, anchor)
	p.ServiceUID = S(Path(anchor.Obj, "metadata", "uid"))
	p.Notes = append(p.Notes, "Bookinfo is a demo application. Images are fetched by Kubernetes from Docker Hub; packaged YAML is shown exactly before applying.")
	ns := ws.Get(req.Cluster, "", "namespaces", "", req.Namespace)
	if req.Demo.Stage == "namespace" {
		if ns != nil {
			return nil, fmt.Errorf("namespace %s already exists; this installer does not adopt or overwrite an existing namespace", req.Namespace)
		}
		p.Objects = append(p.Objects, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": req.Namespace, "labels": bookinfoNamespaceLabels(ws, req.Cluster, settings)}})
		return finishPlan(p)
	}
	if req.Demo.Stage != "workloads" {
		return nil, fmt.Errorf("choose namespace or workloads stage")
	}
	if ns == nil && req.Demo.Preview {
		ns = &store.Object{Cluster: req.Cluster, Version: "v1", Resource: "namespaces", Kind: "Namespace", Name: req.Namespace, Obj: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": req.Namespace, "labels": bookinfoNamespaceLabels(ws, req.Cluster, settings)}}}
	}
	if ns == nil || ns.Labels()["flightbay.dev/demo"] != "bookinfo" {
		return nil, fmt.Errorf("prepare this installer-owned namespace first, then refresh the live capture")
	}
	binding := ns.Labels()["istio.io/use-waypoint"]
	if ns.Labels()["istio.io/dataplane-mode"] != "ambient" || (binding != "" && binding != BookinfoNamespaceBinding(settings)) || (ns.Labels()["flightbay.dev/waypoint-scope"] != "" && ns.Labels()["flightbay.dev/waypoint-scope"] != settings.WaypointScope) || (ns.Labels()["flightbay.dev/waypoint-name"] != "" && ns.Labels()["flightbay.dev/waypoint-name"] != settings.WaypointName) {
		return nil, fmt.Errorf("namespace was prepared with different waypoint settings; use matching settings or create a new namespace")
	}
	p.ServiceUID += "/" + S(Path(ns.Obj, "metadata", "uid"))
	if settings.WaypointScope == "namespace" && binding == "" {
		change(p, ns, "Set the demo namespace default after its new waypoint is programmed", map[string]any{"/metadata/labels/istio.io~1use-waypoint": settings.WaypointName})
	}
	// No routing.yaml: Bookinfo deploys without an HTTPRoute so traffic
	// splitting can be set up and tested from Configure.
	files := []string{"waypoint-istio.yaml", "bookinfo.yaml"}
	if class.Product == "agentgateway" {
		files[0] = "waypoint-agentgateway.yaml"
	}
	if req.Demo.Traffic {
		files = append(files, "client.yaml")
	}
	for _, file := range files {
		b, err := bookinfoFiles.ReadFile("demo/" + file)
		if err != nil {
			return nil, err
		}
		for _, doc := range strings.Split(string(b), "\n---\n") {
			var o map[string]any
			if err = yaml.Unmarshal([]byte(doc), &o); err != nil {
				return nil, err
			}
			if o == nil {
				continue
			}
			md := M(o["metadata"])
			md["namespace"] = req.Namespace
			lb := M(md["labels"])
			if lb == nil {
				lb = map[string]any{}
				md["labels"] = lb
			}
			lb["flightbay.dev/demo"] = "bookinfo"
			kind, name := S(o["kind"]), S(md["name"])
			if kind == "Gateway" {
				M(o["spec"])["gatewayClassName"] = class.Name
				if settings.WaypointScope == "service" {
					for _, svc := range bookinfoServices {
						gateway := M(Copy(o))
						name := BookinfoWaypointName(settings, svc)
						M(gateway["metadata"])["name"] = name
						if ws.Get(req.Cluster, GatewayGroup, "gateways", req.Namespace, name) != nil {
							return nil, fmt.Errorf("Gateway %s/%s already exists", req.Namespace, name)
						}
						p.Objects = append(p.Objects, gateway)
					}
					continue
				}
				name = settings.WaypointName
				md["name"] = name
			}
			if kind == "Service" && settings.WaypointScope == "service" {
				if contains(bookinfoServices, name) {
					lb["istio.io/use-waypoint"] = BookinfoWaypointName(settings, name)
				} else {
					lb["istio.io/use-waypoint"] = "none"
				}
			}
			if kind == "Service" && req.Demo.Global && contains([]string{"productpage", "details", "ratings", "reviews"}, name) {
				lb["solo.io/service-scope"] = "global"
			}
			if kind == "Deployment" {
				M(Path(o, "spec", "template", "metadata", "labels"))["flightbay.dev/demo"] = "bookinfo"
			}
			group, res := "", ""
			switch kind {
			case "Service":
				res = "services"
			case "ServiceAccount":
				res = "serviceaccounts"
			case "Deployment":
				group, res = "apps", "deployments"
			case "Gateway":
				group, res = GatewayGroup, "gateways"
			case "HTTPRoute":
				group, res = GatewayGroup, "httproutes"
			default:
				return nil, fmt.Errorf("unsupported packaged demo kind %s", kind)
			}
			if ws.Get(req.Cluster, group, res, req.Namespace, name) != nil {
				return nil, fmt.Errorf("%s %s/%s already exists; installer will not overwrite it", kind, req.Namespace, name)
			}
			p.Objects = append(p.Objects, o)
		}
	}
	p.Notes = append(p.Notes, "No HTTPRoute is created: reviews-v1 and reviews-v2 are ready for a traffic split from Configure.")
	if settings.WaypointScope == "service" {
		p.Notes = append(p.Notes, "Creates four new dedicated waypoints and binds the four main Services explicitly. The namespace has no waypoint default. reviews-v1/v2 are route backends, not independently enrolled frontends; they bypass further waypoint selection.")
	} else {
		p.Notes = append(p.Notes, "Creates a new shared namespace waypoint named "+settings.WaypointName+". Services inherit the namespace default; this is not selection of an existing waypoint.")
	}
	if req.Demo.Global {
		p.Notes = append(p.Notes, "The four primary Services are published globally in this cluster. Publish on both selected clusters to contribute both sets of endpoints. This does not create peering or alter local-name takeover.")
	}
	return finishPlan(p)
}

func bookinfoNamespaceLabels(ws *store.Workspace, cluster string, settings BookinfoRequest) map[string]any {
	labels := map[string]any{"flightbay.dev/demo": "bookinfo", "istio.io/dataplane-mode": "ambient", "flightbay.dev/waypoint-scope": settings.WaypointScope, "flightbay.dev/waypoint-name": settings.WaypointName}
	if n := ws.Get(cluster, "", "namespaces", "", "istio-system"); n != nil && n.Labels()["topology.istio.io/network"] != "" {
		labels["topology.istio.io/network"] = n.Labels()["topology.istio.io/network"]
	}
	return labels
}
