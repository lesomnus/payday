package server_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/internal/ent"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
)

// App is the app under test, twice: the servers as they are served, and the
// ones the deployment does its own work through.
//
// The second is not a privilege anybody has. It is a server instance the wall
// was never installed on, which is how a test arranges the tenants it is about
// -- there is nobody to be inside a tenant before there are tenants.
type App struct {
	Db *ent.Client

	// Walled is what a caller reaches. Every read it makes carries the
	// predicate the schema declared.
	Walled app.Server

	// Ungated is the same database with no wall, for arranging state.
	Ungated app.Server
}

func New(t *testing.T) *App {
	t.Helper()
	x := require.New(t)

	// The database this suite runs on, which is SQLite unless somebody named
	// another; see [pdtest.DB]. Everything payday generates is SQL, so a suite
	// that only ever ran on SQLite has never seen the statements it will issue.
	drv, dsn := pdtest.DB(t)
	db, err := sql.Open(drv, dsn)
	x.NoError(err)

	// One connection, because an in-memory SQLite database belongs to the
	// connection that opened it. PostgreSQL has no such rule and pooling is the
	// point of it there.
	dia := dialect.Postgres
	if drv == "sqlite3" {
		db.SetMaxOpenConns(1)
		dia = dialect.SQLite
	}

	c := ent.NewClient(ent.Driver(entsql.OpenDB(dia, db)))
	t.Cleanup(func() { c.Close() })
	x.NoError(c.Schema.Create(t.Context()))

	// The two hooks the schema declared, and nothing written by hand: the
	// minter comes out of `(payday.entity).domain` and the wall out of the
	// tenancy beside it.
	walled, err := bare.NewServer(c, bare.WithMinter(pd.Minter()), bare.WithScope(pd.Wall()))
	x.NoError(err)

	ungated, err := bare.NewServer(c, bare.WithMinter(pd.Minter()))
	x.NoError(err)

	return &App{Db: c, Walled: walled, Ungated: ungated}
}

// As answers with a context that sees exactly the given tenants.
func As(ctx context.Context, ids ...pdid.Id) context.Context {
	f := frame.New(pdid.New(pd.TenantDomain), pdid.Nil, frame.Whole())
	return frame.Into(ctx, f.WithScope(frame.Only(ids...)))
}

// tenantOf puts a tenant in and answers with its identifier.
func (a *App) tenantOf(ctx context.Context, x *require.Assertions, alias string) pdid.Id {
	v, err := a.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias}.Build())
	x.NoError(err)

	k, err := pdid.From(v.GetId())
	x.NoError(err)

	return k
}

// robotOf puts a robot in the given tenant and answers with its identifier.
func (a *App) robotOf(ctx context.Context, x *require.Assertions, t pdid.Id, alias string) pdid.Id {
	v, err := a.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: t.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	x.NoError(err)

	k, err := pdid.From(v.GetId())
	x.NoError(err)

	return k
}
