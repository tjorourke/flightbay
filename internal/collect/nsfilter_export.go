package collect

// FilterNamespacesJSON removes everything in a decoded diagnostic document
// that names a namespace for which allowed returns false, the same way
// collection does for namespace-limited captures. nsKeyed is for documents
// keyed by namespace, such as istiod's endpointz. It returns the filtered
// document and how many entries were removed.
func FilterNamespacesJSON(doc any, allowed func(string) bool, nsKeyed bool) (any, int) {
	f := &nsFilter{allowed: allowed}
	if nsKeyed {
		doc = f.nsKeyed(doc)
	}
	return f.Root(doc), f.removed
}

// FilterNamespacesLines is FilterNamespacesJSON for line-oriented output such
// as Prometheus metrics.
func FilterNamespacesLines(text string, allowed func(string) bool) (string, int) {
	f := &nsFilter{allowed: allowed}
	return f.Lines(text), f.removed
}
