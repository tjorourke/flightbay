package server

import (
	"net/http"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubectl/pkg/describe"
)

// describe renders `kubectl describe` for one object, using kubectl's own
// describers against the desk's read-only Kubernetes API (the snapshot or the
// live mirror). The Navigator shows it in place, as k9s does.
//
//	/api/describe?cluster=&group=&resource=&namespace=&name=
func (s *Server) describe(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cluster, group, resource, ns, name := q.Get("cluster"), q.Get("group"), q.Get("resource"), q.Get("namespace"), q.Get("name")
	if cluster == "" || resource == "" || name == "" {
		jerr(w, 400, "cluster, resource and name are required")
		return
	}
	cfg, mapper, err := s.describeClient(cluster)
	if err != nil {
		jerr(w, 500, "describe: "+err.Error())
		return
	}
	gvr, err := mapper.ResourceFor(schema.GroupVersionResource{Group: group, Resource: resource})
	if err != nil {
		jerr(w, 404, "describe: "+err.Error())
		return
	}
	gvk, err := mapper.KindFor(gvr)
	if err != nil {
		jerr(w, 404, "describe: "+err.Error())
		return
	}
	d, ok := describe.DescriberFor(gvk.GroupKind(), cfg)
	if !ok {
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			jerr(w, 404, "describe: "+err.Error())
			return
		}
		if d, ok = describe.GenericDescriberFor(mapping, cfg); !ok {
			jerr(w, 404, "no describer for "+gvk.Kind)
			return
		}
	}
	out, err := d.Describe(ns, name, describe.DescriberSettings{ShowEvents: true, ChunkSize: 500})
	if err != nil {
		jerr(w, 404, "describe: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"text": out, "kind": gvk.Kind})
}

type describeCache struct {
	mu sync.Mutex
	by map[string]*describeEntry
}

type describeEntry struct {
	cfg    *rest.Config
	mapper *restmapper.DeferredDiscoveryRESTMapper
}

// describeClient builds a client for one cluster from the workspace
// kubeconfig (the same one the terminal's kubectl uses).
func (s *Server) describeClient(cluster string) (*rest.Config, *restmapper.DeferredDiscoveryRESTMapper, error) {
	s.descC.mu.Lock()
	defer s.descC.mu.Unlock()
	if s.descC.by == nil {
		s.descC.by = map[string]*describeEntry{}
	}
	if e := s.descC.by[cluster]; e != nil {
		return e.cfg, e.mapper, nil
	}
	suffix := "snapshot"
	if s.WS.IsLive() {
		suffix = "live"
	}
	loading := &clientcmd.ClientConfigLoadingRules{ExplicitPath: s.KubeconfigPath()}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, &clientcmd.ConfigOverrides{CurrentContext: cluster + "@" + suffix}).ClientConfig()
	if err != nil {
		return nil, nil, err
	}
	cfg.QPS, cfg.Burst = 200, 400
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	m := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))
	s.descC.by[cluster] = &describeEntry{cfg: cfg, mapper: m}
	return cfg, m, nil
}
