package collect

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

type mixedVersionDiscovery struct{ *fakeDiscovery }

func (d mixedVersionDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return []*metav1.APIGroup{{Name: "networking.istio.io", PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "networking.istio.io/v1", Version: "v1"}}}, []*metav1.APIResourceList{
		{GroupVersion: "networking.istio.io/v1alpha3", APIResources: []metav1.APIResource{res("envoyfilters", "EnvoyFilter", true), res("destinationrules", "DestinationRule", true)}},
		{GroupVersion: "networking.istio.io/v1", APIResources: []metav1.APIResource{res("destinationrules", "DestinationRule", true)}},
	}, nil
}
func TestDiscoveryKeepsVersionSpecificResourcesWithoutDuplicatingPreferredOnes(t *testing.T) {
	c := &Collector{}
	resources, err := c.discover("mesh1", Clients{Discovery: mixedVersionDiscovery{&fakeDiscovery{}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 {
		t.Fatalf("lost or duplicated resources: %+v", resources)
	}
	for _, r := range resources {
		if r.Resource == "envoyfilters" && r.Version != "v1alpha3" {
			t.Fatal("EnvoyFilter version changed")
		}
		if r.Resource == "destinationrules" && r.Version != "v1" {
			t.Fatal("preferred DestinationRule version lost")
		}
	}
}
