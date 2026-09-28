package posture

// isKubernetes reports whether pods were captured with full detail.
func isKubernetes(e env) bool { return false }

// kubernetesChecks scores workload and platform hygiene.
func kubernetesChecks(e env) []Check { return nil }
