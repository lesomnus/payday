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
}
