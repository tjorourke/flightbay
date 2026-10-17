package ambient

import "github.com/tjorourke/flightbay/internal/store"

// Agentgateway waypoint controllers also support a declared HTTP mesh listener
// on 15088 while rendering a separate HBONE transport listener on 15008.
func waypointMeshListener(gw *store.Object, listener map[string]any) bool {
	if S(listener["protocol"]) == "HBONE" {
		return true
	}
	cls := S(Path(gw.Obj, "spec", "gatewayClassName"))
	return (cls == "agentgateway-waypoint" || cls == "enterprise-agentgateway-waypoint") && S(listener["protocol"]) == "HTTP" && N(listener["port"]) == 15088
}
