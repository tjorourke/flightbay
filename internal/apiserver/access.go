package apiserver

import (
	"encoding/json"
	"io"
	"net/http"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

const authGroup = "authorization.k8s.io"

// decode accepts JSON or Kubernetes protobuf, which recent client-go sends
// for built-in types.
func decode(body []byte, into runtime.Object) error {
	if json.Valid(body) {
		return json.Unmarshal(body, into)
	}
	_, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, into)
	return err
}

// readVerbs are what a snapshot can answer.
var readVerbs = map[string]bool{"get": true, "list": true, "watch": true}

// accessReview answers the access checks clients make before they try
// something. k9s gates every view on SelfSubjectAccessReview, so without this
// it falls back to the contexts screen. Reads are allowed, writes are denied
// with the same read-only explanation the write itself would get. These are
// the only POSTs the snapshot accepts, and they change nothing.
func (s *Server) accessReview(w http.ResponseWriter, r *http.Request, rest []string) {
	if len(rest) == 0 {
		writeJSON(w, 200, s.resourceList("", authGroup, "v1"))
		return
	}
	if len(rest) != 1 || (rest[0] != "selfsubjectaccessreviews" && rest[0] != "selfsubjectrulesreviews") {
		s.readOnly(w)
		return
	}
	if r.Method != http.MethodPost {
		status(w, http.StatusMethodNotAllowed, metav1.StatusReasonMethodNotAllowed, "flightbay: access reviews only support create")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		status(w, 400, metav1.StatusReasonBadRequest, err.Error())
		return
	}
	switch rest[0] {
	case "selfsubjectaccessreviews":
		var rev authv1.SelfSubjectAccessReview
		if err := decode(body, &rev); err != nil {
			status(w, 400, metav1.StatusReasonBadRequest, err.Error())
			return
		}
		verb := ""
		sub := ""
		if ra := rev.Spec.ResourceAttributes; ra != nil {
			verb, sub = ra.Verb, ra.Subresource
		} else if na := rev.Spec.NonResourceAttributes; na != nil {
			verb = na.Verb
		}
		allowed := readVerbs[verb] && (sub == "" || sub == "log" || sub == "status")
		rev.Status = authv1.SubjectAccessReviewStatus{Allowed: allowed}
		if !allowed {
			rev.Status.Reason = "flightbay: read-only snapshot taken " + SnapshotTime(s.WS).Format("2006-01-02T15:04Z")
		}
		rev.APIVersion, rev.Kind = "authorization.k8s.io/v1", "SelfSubjectAccessReview"
		writeJSON(w, http.StatusCreated, rev)
	case "selfsubjectrulesreviews":
		var rev authv1.SelfSubjectRulesReview
		_ = decode(body, &rev)
		rev.APIVersion, rev.Kind = "authorization.k8s.io/v1", "SelfSubjectRulesReview"
		rev.Status = authv1.SubjectRulesReviewStatus{
			ResourceRules:    []authv1.ResourceRule{{Verbs: []string{"get", "list", "watch"}, APIGroups: []string{"*"}, Resources: []string{"*"}}},
			NonResourceRules: []authv1.NonResourceRule{{Verbs: []string{"get"}, NonResourceURLs: []string{"*"}}},
		}
		writeJSON(w, http.StatusCreated, rev)
	}
}
