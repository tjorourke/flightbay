package store

import (
	"regexp"
	"strings"
)

func firstController(obj map[string]any) (kind, name string) {
	md, _ := obj["metadata"].(map[string]any)
	refs, _ := md["ownerReferences"].([]any)
	var fk, fn string
	for _, r := range refs {
		m, _ := r.(map[string]any)
		k, _ := m["kind"].(string)
		n, _ := m["name"].(string)
		if c, _ := m["controller"].(bool); c {
			return k, n
		}
		if fk == "" {
			fk, fn = k, n
		}
	}
	return fk, fn
}

var (
	rsHashRe   = regexp.MustCompile(`^(.+)-[a-z0-9]{6,10}$`)             // ReplicaSet name: <deployment>-<hash>
	deployPod  = regexp.MustCompile(`^(.+)-[a-z0-9]{6,10}-[a-z0-9]{5}$`) // <deployment>-<rs hash>-<pod hash>
	statefulRe = regexp.MustCompile(`^(.+)-[0-9]+$`)
	dsPodRe    = regexp.MustCompile(`^(.+)-[a-z0-9]{5}$`)
)

// workloadLocked resolves the workload a pod belongs to by following owner
// references (Pod -> ReplicaSet -> Deployment, or DaemonSet, StatefulSet,
// Job -> CronJob). Without the pod object it falls back to the name patterns
// the controllers use, which is right for almost every cluster.
func (w *Workspace) workloadLocked(cluster, ns, pod string) (kind, name string) {
	if o := w.objects[objKey(cluster, "", "pods", ns, pod)]; o != nil {
		k, n := firstController(o.Obj)
		switch k {
		case "":
			return "Pod", pod
		case "Node": // static (mirror) pods such as kube-apiserver
			return "StaticPod", strings.TrimSuffix(pod, "-"+strings.TrimPrefix(nodeOf(o.Obj), ""))
		case "ReplicaSet":
			if rs := w.objects[objKey(cluster, "apps", "replicasets", ns, n)]; rs != nil {
				if dk, dn := firstController(rs.Obj); dk != "" {
					return dk, dn
				}
				return "ReplicaSet", n
			}
			if m := rsHashRe.FindStringSubmatch(n); m != nil {
				return "Deployment", m[1]
			}
			return "ReplicaSet", n
		case "Job":
			if j := w.objects[objKey(cluster, "batch", "jobs", ns, n)]; j != nil {
				if ck, cn := firstController(j.Obj); ck == "CronJob" {
					return ck, cn
				}
			}
			return "Job", n
		default:
			return k, n
		}
	}
	switch {
	case deployPod.MatchString(pod):
		return "Deployment", deployPod.FindStringSubmatch(pod)[1]
	case statefulRe.MatchString(pod):
		return "StatefulSet", statefulRe.FindStringSubmatch(pod)[1]
	case dsPodRe.MatchString(pod):
		return "DaemonSet", dsPodRe.FindStringSubmatch(pod)[1]
	}
	return "Pod", pod
}

func nodeOf(obj map[string]any) string {
	spec, _ := obj["spec"].(map[string]any)
	n, _ := spec["nodeName"].(string)
	return n
}
