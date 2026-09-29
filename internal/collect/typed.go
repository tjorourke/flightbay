package collect

import (
	"context"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// TypedLister lists built-in types (pods, events, ConfigMaps and the rest of
// what client-go knows) as protobuf and hands them back as unstructured maps.
// The API server encodes protobuf more cheaply than JSON, and a page is
// about half the bytes. Custom resources have no protobuf encoding and go
// through the dynamic client as JSON.
type TypedLister interface {
	// Supports reports whether a type can be listed this way.
	Supports(gvr schema.GroupVersionResource, kind string) bool
	// List returns one page and the continue token.
	List(ctx context.Context, gvr schema.GroupVersionResource, kind, ns string, opts metav1.ListOptions) ([]map[string]any, string, error)
}

type protoLister struct {
	cfg     *rest.Config
	mu      sync.Mutex
	clients map[schema.GroupVersion]rest.Interface
}

// NewProtoLister builds a protobuf lister on cfg. cfg should carry the
// shared rate limiter, so its requests count against the same budget.
func NewProtoLister(cfg *rest.Config) TypedLister {
	return &protoLister{cfg: cfg, clients: map[schema.GroupVersion]rest.Interface{}}
}

func (p *protoLister) Supports(gvr schema.GroupVersionResource, kind string) bool {
	gv := gvr.GroupVersion()
	return kind != "" && scheme.Scheme.Recognizes(gv.WithKind(kind)) && scheme.Scheme.Recognizes(gv.WithKind(kind+"List"))
}

func (p *protoLister) client(gv schema.GroupVersion) (rest.Interface, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[gv]; ok {
		return c, nil
	}
	c := rest.CopyConfig(p.cfg)
	c.GroupVersion = &gv
	c.APIPath = "/apis"
	if gv.Group == "" {
		c.APIPath = "/api"
	}
	c.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	c.ContentType = runtime.ContentTypeProtobuf
	c.AcceptContentTypes = runtime.ContentTypeProtobuf + "," + runtime.ContentTypeJSON
	rc, err := rest.RESTClientFor(c)
	if err != nil {
		return nil, err
	}
	p.clients[gv] = rc
	return rc, nil
}

func (p *protoLister) List(ctx context.Context, gvr schema.GroupVersionResource, kind, ns string, opts metav1.ListOptions) ([]map[string]any, string, error) {
	gv := gvr.GroupVersion()
	rc, err := p.client(gv)
	if err != nil {
		return nil, "", err
	}
	obj, err := scheme.Scheme.New(gv.WithKind(kind + "List"))
	if err != nil {
		return nil, "", err
	}
	if err := rc.Get().NamespaceIfScoped(ns, ns != "").Resource(gvr.Resource).
		VersionedParams(&opts, scheme.ParameterCodec).Do(ctx).Into(obj); err != nil {
		return nil, "", err
	}
	items, err := meta.ExtractList(obj)
	if err != nil {
		return nil, "", err
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(it)
		if err != nil {
			return nil, "", err
		}
		// Items in a typed list carry no type meta; the JSON path has it.
		m["apiVersion"], m["kind"] = gv.String(), kind

		out = append(out, m)
	}
	la, err := meta.ListAccessor(obj)
	if err != nil {
		return nil, "", err
	}
	return out, la.GetContinue(), nil
}

// clientGoMinor is the Kubernetes minor version the linked client-go knows
// (client-go v0.37.x is Kubernetes 1.37), 0 when it cannot be told.
func clientGoMinor() int {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return 0
	}
	for _, d := range bi.Deps {
		if d.Path == "k8s.io/client-go" {
			parts := strings.Split(strings.TrimPrefix(d.Version, "v"), ".")
			if len(parts) >= 2 && parts[0] == "0" {
				n, _ := strconv.Atoi(parts[1])
				return n
			}
		}
	}
	return 0
}

// protobufSafe reports whether typed decoding keeps every field: a server
// newer than client-go can send fields the typed structs do not have, and
// protobuf decoding would drop them silently. JSON keeps everything.
func protobufSafe(serverMinor string) bool {
	known := clientGoMinor()
	n, err := strconv.Atoi(strings.TrimRight(serverMinor, "+"))
	return known > 0 && err == nil && n <= known
}
