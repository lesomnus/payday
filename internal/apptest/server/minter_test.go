package server_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"uuid"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/lesomnus/payday/pdid"
)

// TestIdentifiersSayWhatTheyName is the first half of what the schema
// declaration buys: nothing here was written by hand, and every row comes out
// of the database naming what kind of thing it is.
func TestIdentifiersSayWhatTheyName(t *testing.T) {
	t.Run("a new row carries its entity's domain", func(t *testing.T) {
		x := require.New(t)
		a := New(t)
		ctx := t.Context()

		tenant := a.tenantOf(ctx, x, "acme")
		x.Equal(pd.TenantDomain, tenant.Domain())

		robot := a.robotOf(ctx, x, tenant, "arm-01")
		x.Equal(pd.RobotDomain, robot.Domain())

		// And it reads without counting bytes, which is why byte 9.
		x.Equal("robot", robot.Domain().String())
	})

	t.Run("a request may name its own, of the right kind", func(t *testing.T) {
		x := require.New(t)
		a := New(t)
		ctx := t.Context()

		tenant := a.tenantOf(ctx, x, "acme")
		want := pdid.New(pd.RobotDomain)

		v, err := a.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Id:     want.Bytes(),
			Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
			Alias:  "arm-01",
		}.Build())
		x.NoError(err)
		x.Equal(want.Bytes(), v.GetId())
	})
}

// TestWrongKindIsRefusedBeforeTheDatabase is the reason the minter checks
// rather than only stamps.
//
// A request may say what identifier it wants. Without the check a Robot could
// be stored under an identifier whose domain says Tenant, and then everything
// that reads that domain back -- an audit trail, a reference, a log line -- is
// reading the caller's word rather than the schema's.
func TestWrongKindIsRefusedBeforeTheDatabase(t *testing.T) {
	x := require.New(t)
	a := New(t)
	ctx := t.Context()

	tenant := a.tenantOf(ctx, x, "acme")

	// An identifier of the wrong kind. It is a perfectly good identifier and
	// it names the wrong sort of thing.
	wrong := pdid.New(pd.TenantDomain)

	_, err := a.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Id:     wrong.Bytes(),
		Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.Error(err)
	x.Equal(codes.InvalidArgument, status.Code(err))

	// The message says what it actually was, which a query that matched
	// nothing could not have said.
	x.Contains(err.Error(), "tenant")
	x.Contains(err.Error(), "robot")

	// And nothing was written.
	n, err := a.Db.Robot.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)
}

// uuidBytes is a v4, which is sixteen perfectly good bytes whose ninth says
// nothing about what they name.
func uuidBytes() []byte {
	v := uuid.New()
	return v[:]
}

func TestIdentifiersThisAppDidNotMake(t *testing.T) {
	x := require.New(t)
	a := New(t)
	ctx := t.Context()

	tenant := a.tenantOf(ctx, x, "acme")

	// A v4 is sixteen perfectly good bytes whose ninth says nothing. Taking it
	// would mean the domain of that row is whatever the caller's randomness
	// happened to be.
	_, err := a.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Id:     uuidBytes(),
		Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
	}.Build())
	x.Error(err)
}
