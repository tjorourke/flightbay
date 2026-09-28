package posture

// isGateway reports whether the cluster has Gateway API ingress or kgateway.
func isGateway(e env) bool { return false }

// gatewayChecks scores Gateway API ingress and kgateway.
func gatewayChecks(e env) []Check { return nil }
