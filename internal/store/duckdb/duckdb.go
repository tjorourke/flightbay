//go:build cgo

// Package duckdb links DuckDB into the workspace store. Import it for its
// side effect from binaries and tests that need SQL and log search:
//
//	import _ "github.com/tjorourke/flightbay/internal/store/duckdb"
package duckdb

import (
	"database/sql/driver"
	"fmt"

	"github.com/duckdb/duckdb-go/v2"

	"github.com/tjorourke/flightbay/internal/store/sqldriver"
)

func init() {
	sqldriver.Register(sqldriver.Driver{Name: "duckdb", Append: appendRows, MapOf: mapOf, MapValue: mapValue})
}

func appendRows(dc any, table string, rows [][]driver.Value) error {
	a, err := duckdb.NewAppenderFromConn(dc.(driver.Conn), "", table)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := a.AppendRow(r...); err != nil {
			_ = a.Close()
			return err
		}
	}
	return a.Close() // commits the chunk
}

func mapOf(m map[string]string) any {
	out := duckdb.Map{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// mapValue converts the shapes the driver uses for MAP columns.
func mapValue(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case duckdb.Map:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[fmt.Sprint(k)] = x
		}
		return m, true
	case duckdb.OrderedMap:
		return mapValue(&t)
	case *duckdb.OrderedMap:
		m := make(map[string]any, t.Len())
		for _, k := range t.Keys() {
			x, _ := t.Get(k)
			m[fmt.Sprint(k)] = x
		}
		return m, true
	}
	return nil, false
}
