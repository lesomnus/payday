// Package dbsqlite3wasm makes "sqlite3-wasm" a driver a configuration may name.
//
// It is SQLite in the browser: the engine runs in a Web Worker of its own,
// beside the one the Go program is in, and speaks a binary protocol to it. What
// it is for is a whole app compiled to wasm and served from the page it serves
// -- a reload is a server restart, and there is no backend to start.
//
// The other SQLite driver cannot do that job. It runs the engine on wazero,
// which is a wasm runtime written in Go, so under GOOS=js it would be wasm
// inside wasm.
//
//	import _ "github.com/lesomnus/payday/config/dbsqlite3wasm"
//
// The package builds everywhere. Off js/wasm the driver it registers is a stub
// whose Open says why, which is what keeps `go build ./...` and `go vet` honest
// on a developer's machine.
package dbsqlite3wasm

import (
	_ "github.com/lesomnus/sqlite3-wasm"

	"github.com/lesomnus/payday/config"
)

func init() {
	config.RegisterDriver("sqlite3-wasm", config.DialectSQLite)

	// The same times-as-integers the other SQLite driver asks for, under the
	// name this one gives it. Both are set so that a database one of them
	// writes is one the other reads: the page loads a script a process dumped.
	//
	// It matters here even though this driver's text is fixed-width and does
	// sort: written with no format at all it would read those integers back
	// with mattn's heuristic, where anything above 1e12 is milliseconds -- and
	// nanoseconds always are, so every timestamp would come back in the year
	// 56000-odd.
	config.RegisterDsnDefault("sqlite3-wasm", "_time_integer_format", "unix_nano")
}
