//go:build !cgo

// Package duckdb links DuckDB into the workspace store. DuckDB needs cgo;
// in a CGO_ENABLED=0 build this package links nothing and the store runs
// without SQL or log search.
package duckdb
