package ambient

import "strings"

// TaskDef is the one description of a configuration task. Summary, All tasks,
// the drawer's Switch task menu and Policies & routes all read it, so a task
// has the same title, short label, group and wording everywhere. A test fails
// when Capabilities() and this table disagree.
type TaskDef struct {
	ID         string `json:"id"`
	Title      string `json:"title"`      // the catalogue title (drawer header, Switch task menu)
	ShortLabel string `json:"shortLabel"` // the label on cards, rows and locked entries
	Group      string `json:"group"`      // access | routing | transport | resilience | multicluster
	Layer      string `json:"layer"`      // L4, L7 or "" (multicluster)
	// AddLabel is the short action for an Add link. "{service}" is replaced by
	// the Service name, see AddLabelFor.
	AddLabel    string `json:"addLabel"`
	Description string `json:"description"` // one line, used in the All tasks cards
}

// GroupDef is one of the five groups the Configuration tab is organised by.
type GroupDef struct {
	ID       string `json:"id"`
	Question string `json:"question"`
}

var TaskGroups = []GroupDef{
	{"access", "Who can call it?"},
	{"routing", "Where does traffic go?"},
	{"transport", "Is traffic encrypted?"},
	{"resilience", "What happens under load or failure?"},
	{"multicluster", "Is it available in other clusters?"},
}

// TaskTable lists every task id Capabilities() can return, in group order and,
// inside a group, in the order Add links are offered (most common first).
var TaskTable = []TaskDef{
	{"access-rule", "Allow or deny requests", "Allow or deny requests", "access", "L7", "Allow or deny requests", "By caller, header, path or method"},
	{"methods", "Control HTTP methods", "Allow or block methods", "access", "L7", "Allow only some methods", "Pick which HTTP methods can reach the Service"},
	{"jwt", "Require a JWT", "Require a JWT", "access", "L7", "Require a JWT", "Require a matching JWT on this ALLOW branch; other ALLOW rules remain additive"},
	{"l4-default-deny", "Deny by default", "Deny by default", "access", "L4", "Deny by default", "Callers need an explicit allow rule"},
	{"require-waypoint", "Block traffic that skips the waypoint", "Block traffic that skips the waypoint", "access", "L4", "Block traffic that skips the waypoint", "Allow the waypoint; remove or narrow conflicting additive ALLOW rules to block bypass"},
	{"l4-allow-namespace", "Allow a namespace", "Allow a namespace", "access", "L4", "Allow a namespace", "Direct traffic without a waypoint"},
	{"l4-allow-service-account", "Allow one service account", "Allow one service account", "access", "L4", "Allow one service account", "Direct L4 traffic without a waypoint; other ALLOW rules remain additive"},

	{"weighted-routing", "Split traffic", "Split traffic", "routing", "L7", "Split traffic", "Send a share of requests to each backend"},
	{"header-routing", "Route matching requests", "Route by header or path", "routing", "L7", "Route by header or path", "Send matching requests to another backend"},
	{"mirroring", "Mirror requests", "Mirror requests", "routing", "L7", "Mirror requests", "Copy requests to another Service"},
	{"load-balancing", "Balance endpoints", "Balance endpoints", "routing", "L7", "Balance endpoints", "Round robin, least request or random"},
	{"locality-failover", "Fail over by region", "Fail over between regions", "routing", "L7", "Fail over between regions", "Prefer the local region"},
	{"service-failover", "Primary and backup Services", "Primary and backup Services", "routing", "L7", "Primary and backup Services", "Use another Service only on failure"},

	{"strict-mtls", "Require mutual TLS", "Require mTLS", "transport", "L4", "Require mTLS for {service}", "Reject callers outside the mesh"},
	{"mesh-strict-mtls", "Mesh-wide STRICT mTLS", "Mesh-wide STRICT mTLS", "transport", "L4", "Mesh-wide STRICT mTLS", "Cluster-wide default in istio-system; more-specific overrides remain effective"},

	{"timeouts", "Set a timeout", "Set a timeout", "resilience", "L7", "Set a timeout", "Stop waiting after a set time"},
	{"endpoint-failover", "Eject failing endpoints", "Eject failing endpoints", "resilience", "L7", "Eject failing endpoints", "Stop sending to Pods that keep failing"},
	{"circuit-breaker", "Set circuit breakers", "Circuit breakers", "resilience", "L7", "Circuit breakers", "Cap connections and queued requests"},
	{"retries", "Retry failed requests", "Retry failed requests", "resilience", "L7", "Retry failed requests", "Try again on errors"},
	{"rate-limit", "Limit request rate", "Limit request rate", "resilience", "L7", "Limit request rate", "Requests per second for each waypoint"},
	{"global-rate-limit", "Share an API rate limit", "Shared rate limit", "resilience", "L7", "Shared rate limit", "One limit across all waypoints"},

	{"global-service", "Publish a global Service", "Publish as a global Service", "multicluster", "", "Publish as a global Service", "Reach the Service in every cluster by one name"},
}

// TaskByID finds a task definition.
func TaskByID(id string) (TaskDef, bool) {
	for _, t := range TaskTable {
		if t.ID == id {
			return t, true
		}
	}
	return TaskDef{}, false
}

// AddLabelFor renders the Add link text for one Service.
func (t TaskDef) AddLabelFor(service string) string {
	return strings.ReplaceAll(t.AddLabel, "{service}", service)
}
