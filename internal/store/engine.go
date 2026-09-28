package store

import (
	"database/sql"
	"database/sql/driver"
	"errors"

	"github.com/tjorourke/flightbay/internal/store/sqldriver"
)

// Without a registered engine (the collector, and flightbay scan) the store
// keeps every object in memory, which is all the Scorecard, reach and diff
// need; SQL and log queries return errNoDB instead of running.
const noDBDriver = "flightbay-nodb"

var errNoDB = errors.New("SQL and log search need DuckDB, which this binary does not link; open the bundle with flightbay-desk")

type noDB struct{}

func (noDB) Open(string) (driver.Conn, error) { return nil, errNoDB }

func init() { sql.Register(noDBDriver, noDB{}) }

func haveDuckDB() bool { return sqldriver.Registered != nil }

func sqlDriverName() string {
	if d := sqldriver.Registered; d != nil {
		return d.Name
	}
	return noDBDriver
}

func duckAppend(dc any, table string, rows [][]driver.Value) error {
	if d := sqldriver.Registered; d != nil {
		return d.Append(dc, table, rows)
	}
	return errNoDB
}

func duckMapOf(m map[string]string) any {
	if d := sqldriver.Registered; d != nil {
		return d.MapOf(m)
	}
	return nil
}

func duckMapValue(v any) (map[string]any, bool) {
	if d := sqldriver.Registered; d != nil {
		return d.MapValue(v)
	}
	return nil, false
}
