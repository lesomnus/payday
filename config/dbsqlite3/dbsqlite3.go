// Package dbsqlite3 makes "sqlite3" a driver a configuration may name.
//
// It is a package rather than a line in `config` because importing a driver
// links it in, and this one brings a SQLite engine compiled to Wasm with it. An
// app that wants it says so once:
//
//	import _ "github.com/lesomnus/payday/config/dbsqlite3"
//
// and one that does not carries none of it.
package dbsqlite3

import (
	// SQLite driver. It runs SQLite compiled to Wasm, so it needs neither cgo
	// nor a system library.
	//
	// Note that foreign keys are off by default in SQLite; the DSN should ask
	// for them, e.g. "file:data.db?_pragma=foreign_keys(1)".
	_ "github.com/ncruces/go-sqlite3/driver"

	"github.com/lesomnus/payday/config"
)

func init() {
	config.RegisterDriver("sqlite3", config.DialectSQLite)

	// Times as nanoseconds since the epoch, because this driver's default is
	// `time.RFC3339Nano` text with the fraction's trailing zeros trimmed:
	// `05:38:42Z` and `05:38:42.093Z`, which SQLite compares as text, where `Z`
	// sorts after `.` and so the earlier one is the greater. Every `<`, `>=`
	// and `ORDER BY` on a time column is that comparison.
	//
	// Integers compare as numbers and keep every digit Go has. What it gives up
	// is a database readable by eye -- `datetime(x/1e9, 'unixepoch')` reads one
	// -- and the years outside 1678-2262. This driver's other formats do not
	// do better: `sqlite` is whole seconds, `rfc3339` is the same trimmed text,
	// and a fixed-width layout keeps each value's own zone, so it sorts only if
	// every caller stores UTC.
	config.RegisterDsnDefault("sqlite3", "_timefmt", "unixepoch_nano")
}
