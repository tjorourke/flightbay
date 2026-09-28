// Package sqldriver is the hook between the workspace store and DuckDB.
// The store never imports DuckDB itself: flightbay-desk links it by
// importing internal/store/duckdb, which registers here. The collector
// (flightbay, including flightbay scan) does not, so it stays free of cgo and
// of DuckDB's size, and its workspaces keep objects in memory only.
package sqldriver

import "database/sql/driver"

// Driver is what the store needs from a SQL engine beyond database/sql.
type Driver struct {
	Name     string                                                    // database/sql driver name
	Append   func(conn any, table string, rows [][]driver.Value) error // bulk append on a raw connection
	MapOf    func(map[string]string) any                               // a MAP column value
	MapValue func(any) (map[string]any, bool)                          // read a MAP column value
}

// Registered is the engine in use, nil when none is linked.
var Registered *Driver

// Register installs the engine. It is called from an init function.
func Register(d Driver) { Registered = &d }
