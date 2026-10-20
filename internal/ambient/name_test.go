package ambient

import (
	"strings"
	"testing"
)

func TestDefaultNameIsServiceAndTask(t *testing.T) {
	p, err := Generate(fixture(t), effectReq("methods", Request{Methods: []string{"GET"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Objects) != 1 || S(Path(p.Objects[0], "metadata", "name")) != "front-methods" {
		t.Fatalf("%v", p.Objects)
	}
	labels := M(Path(p.Objects[0], "metadata", "labels"))
	if labels["app.kubernetes.io/managed-by"] != "flightbay" || labels["flightbay.dev/test-id"] != p.ID {
		t.Fatalf("ownership labels lost: %v", labels)
	}
}

func TestRequestedNameNamesThePrimaryResource(t *testing.T) {
	req := effectReq("methods", Request{Name: "read-only", Methods: []string{"GET"}})
	p, err := Generate(fixture(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := S(Path(p.Objects[0], "metadata", "name")); got != "read-only" {
		t.Fatalf("name %q", got)
	}
	if !strings.Contains(p.YAML, "name: read-only") || !strings.Contains(p.YAML, "flightbay.dev/test-id: "+p.ID) {
		t.Fatal(p.YAML)
	}
}

func TestInvalidNameIsRefused(t *testing.T) {
	for _, n := range []string{"Read_Only", "-lead", "trail-", "has space", strings.Repeat("a", 64)} {
		if _, err := Generate(fixture(t), effectReq("methods", Request{Name: n, Methods: []string{"GET"}})); err == nil || !strings.Contains(err.Error(), "not a valid name") {
			t.Errorf("%q: %v", n, err)
		}
	}
}

func TestMultipleTasksKeepHashNames(t *testing.T) {
	req := effectReq("strict-mtls", Request{MTLSMode: "STRICT", Methods: []string{"GET"}})
	req.Tasks = []string{"strict-mtls", "methods"}
	p, err := Generate(fixture(t), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range p.Objects {
		if n := S(Path(o, "metadata", "name")); !strings.HasPrefix(n, "flightbay-test-"+p.ID) {
			t.Errorf("name %q", n)
		}
	}
}

func TestRequestedNameWithSeveralResourcesSuffixesTheRest(t *testing.T) {
	req := effectReq("jwt", Request{Name: "front-auth", Issuer: "https://issuer.example", JWKSURI: "https://issuer.example/keys", RequestPrincipals: []string{"https://issuer.example/*"}})
	ws := fixture(t)
	put(ws, "security.istio.io", "requestauthentications", "RequestAuthentication")
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Objects) < 2 || S(Path(p.Objects[0], "metadata", "name")) != "front-auth" || S(Path(p.Objects[1], "metadata", "name")) == "front-auth" {
		t.Fatalf("%v", p.Objects)
	}
}

func TestNameClashWithUnownedResourceIsRefused(t *testing.T) {
	ws := fixture(t)
	put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", object("AuthorizationPolicy", "front-methods", map[string]any{}))
	_, err := Generate(ws, effectReq("methods", Request{Methods: []string{"GET"}}))
	want := "A AuthorizationPolicy named app/front-methods already exists and is not managed by Flightbay. Choose another name."
	if err == nil || err.Error() != want {
		t.Fatalf("%v", err)
	}
	// Choosing another name resolves it.
	if _, err := Generate(ws, effectReq("methods", Request{Name: "front-reads", Methods: []string{"GET"}})); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedNameIsUpdatedNotRecreated(t *testing.T) {
	ws := fixture(t)
	first, err := Generate(ws, effectReq("methods", Request{Methods: []string{"GET"}}))
	if err != nil {
		t.Fatal(err)
	}
	capturePlanObjects(t, ws, first)
	again, err := Generate(ws, effectReq("methods", Request{Methods: []string{"GET", "HEAD"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Objects) != 0 || len(again.Changes) != 1 || again.Changes[0].Ref.Name != "front-methods" {
		t.Fatalf("owned resource should be updated in place: %+v", again)
	}
}
