// Package dbpgx makes "pgx" a driver a configuration may name.
//
// It is a package rather than a line in `config` because importing a driver
// links it in, and there is no such thing as linking one in for the apps that
// use it only. An app that runs on PostgreSQL says so once, where it says what
// else it is built out of:
//
//	import _ "github.com/lesomnus/payday/config/dbpgx"
//
// and one that does not carries none of it.
package dbpgx

import (
	// PostgreSQL driver. It is a pure Go driver so the binary can still be
	// built with CGO_ENABLED=0.
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/lesomnus/payday/config"
)

func init() {
	config.RegisterDriver("pgx", config.DialectPostgres)
}
