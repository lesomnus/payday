package config

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"

	"github.com/lesomnus/z"
)

// The SQL dialects a driver may speak.
//
// They are the names ent and Atlas know them by, written out here rather than
// taken from `entgo.io/ent/dialect` so that nothing in payday depends on ent --
// see [DbConfig.Open] for why that matters. They are strings on a wire between
// three libraries that already agree on them, so writing them down costs a
// constant and buys a package boundary.
const (
	DialectMySQL    = "mysql"
	DialectPostgres = "postgres"
	DialectSQLite   = "sqlite3"
)

type DbConfig struct {
	// Driver is the name of the database/sql driver to open the connection
	// with, e.g. "sqlite3" or "pgx". A driver is registered by a package that
	// imports it -- `config/dbpgx` and `config/dbsqlite3` are the two that come
	// with payday -- and an unknown name is reported along with the ones the
	// app actually linked in.
	Driver string `yaml:"driver"`

	// Dialect is the SQL dialect spoken to the database, one of [DialectMySQL],
	// [DialectPostgres] or [DialectSQLite]. It is derived from Driver if it is
	// empty, which works for every driver registered with [RegisterDriver].
	Dialect string `yaml:"dialect"`

	// Dsn is the data source name given to the driver, e.g.
	// "postgres://user:password@host:5432/database?sslmode=disable" or
	// "file:data.db?_pragma=foreign_keys(1)".
	Dsn string `yaml:"dsn"`

	// DevDsn names the dev database that `migrate plan` works out the
	// migrations with. It is emptied every time it is used, so it must not be
	// a database that holds anything.
	DevDsn string `yaml:"dev_dsn"`

	// MaxOpenConns limits the number of open connections to the database.
	// Zero means unlimited.
	MaxOpenConns int `yaml:"max_open_conns"`
	// MaxIdleConns limits the number of idle connections in the pool.
	MaxIdleConns int `yaml:"max_idle_conns"`

	// Migrate has the app bring the schema up to date as it starts, rather
	// than a migration run against the database before it. It is convenient
	// while developing and it is not what production wants: the process that
	// serves requests is not the one that should be deciding to alter a table.
	//
	// It is a switch between two behaviours and not between one and nothing.
	// Off -- the default -- the app looks at the database before it serves and
	// refuses one that is not the shape its schema describes; see
	// [payday/migrate.Check]. So the answer to leaving this alone is a server
	// that does not start, rather than one that starts and fails per request in
	// whichever handler first touches the column that moved.
	//
	// Which means turning it on is a decision about who may alter tables, and
	// the check does not run behind it: an operator who said the serving
	// process may do that is not then argued with.
	Migrate bool `yaml:"migrate"`
}

// dialects holds the SQL dialect each known database/sql driver speaks.
var dialects = map[string]string{}

// RegisterDriver records that the driver named `driver` speaks `dialect`, so
// that a configuration only has to name the driver.
//
// A driver is a package that has to be linked in whether anything opens a
// connection with it or not, which is why payday does not import any: an app
// that runs on PostgreSQL would otherwise carry a SQLite engine compiled to
// Wasm in its binary, and one that runs on SQLite would carry a PostgreSQL
// client. So each registration is a package of its own -- see `config/dbpgx`
// and `config/dbsqlite3` -- and an app blank imports the ones it uses:
//
//	import _ "github.com/lesomnus/payday/config/dbpgx"
//
// Another database is another such package, anywhere at all, doing the same two
// lines from its init.
func RegisterDriver(driver string, dialect string) {
	dialects[driver] = dialect
}

// Drivers lists the registered drivers in lexical order.
func Drivers() []string {
	return slices.Sorted(maps.Keys(dialects))
}

// DriverFor names a registered driver that speaks the given dialect, which is
// how a command that works on one kind of database only, such as `migrate`,
// finds what to open it with. The first one in lexical order is taken if more
// than one speaks it.
func DriverFor(dialect string) (string, bool) {
	for _, v := range Drivers() {
		if dialects[v] == dialect {
			return v, true
		}
	}

	return "", false
}

func (c DbConfig) dialect() (string, error) {
	if c.Dialect != "" {
		return c.Dialect, nil
	}

	v, ok := dialects[c.Driver]
	if !ok {
		return "", fmt.Errorf("unknown driver %q: it must be one of %v, or db.dialect must be given", c.Driver, Drivers())
	}

	return v, nil
}

// Open connects to the database and answers with the connection and the
// dialect it speaks.
//
// The template this came from answered with an `*ent.Client`, and this cannot:
// the client is generated into the app, from the app's own schema, and payday
// has no name for that type. Nor should it -- an app is free to be built on
// something other than ent, and a framework that returned one would have
// decided that for it.
//
// So the two things a client is made of are what comes back, and the app puts
// them together where the rest of its wiring is:
//
//	db, dialect, err := c.Db.Open(ctx)
//	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect, db)))
//
// The dialect is answered as well as the connection because it is not on the
// connection: `database/sql` knows the driver's name and nothing about what it
// speaks, and the migrations, which are SQL and not ent, need to be told.
//
// The caller owns the connection and must close it.
func (c DbConfig) Open(ctx context.Context) (*sql.DB, string, error) {
	dialect, err := c.dialect()
	if err != nil {
		return nil, "", err
	}

	db, err := sql.Open(c.Driver, c.Dsn)
	if err != nil {
		return nil, "", z.Err(err, "open")
	}
	if c.MaxOpenConns > 0 {
		db.SetMaxOpenConns(c.MaxOpenConns)
	}
	if c.MaxIdleConns > 0 {
		db.SetMaxIdleConns(c.MaxIdleConns)
	}

	// Fail fast if the database is not reachable.
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, "", z.Err(err, "ping")
	}

	return db, dialect, nil
}
