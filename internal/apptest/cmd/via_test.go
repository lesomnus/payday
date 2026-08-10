package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/lesomnus/payday/pdid"
)

// TestARowThatNamesNoNextHopCanStillBeWritten is the trail walking a `via` path
// that is not there.
//
// An entity behind the wall by another row reaches its tenant through an edge,
// and that edge may be **nullable** -- payday asks apps for exactly that on
// field 3, since a schema gains one after it already has rows and a required
// edge could never be added to one. So a row naming no next hop is a real row.
//
// The recorder walks the same path to file the trail under a tenant. Finding no
// edge, it parsed no bytes as an identifier and failed -- and because it runs
// **inside the transaction that makes the write**, the write failed too, with
// an Internal, from a layer the caller never asked for. The row could not be
// created at all.
//
// It answers `uuid.Nil` now, which the recorder already knew what to do with:
// file it under the actor's own tenant, since a record nobody can read is not a
// record.
//
// It is in this package rather than beside the other wall tests because only
// this harness installs a recorder -- which is also why the defect survived.
//
// Found by roster, whose Team is on field 3 and reaches its tenant through it.
func TestARowThatNamesNoNextHopCanStillBeWritten(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, cmd.Config{
		Db:    dbOf(t),
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))

	before, err := s.Ent.Audit.Query().Count(ctx)
	x.NoError(err)

	v, err := s.Ungated.Joint().Add(ctx, app.JointAddRequest_builder{Alias: "orphan"}.Build())
	x.NoError(err)
	x.False(v.HasRobot())

	after, err := s.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Equal(before+1, after, "the write happened and the trail did not record it")
}

// TestTheWallStillHoldsForARowWithNoNextHop is the other half, and the one that
// would be a hole.
//
// A row that reaches no tenant is behind every wall or none, and it has to be
// none: a predicate that matched it would show one tenant a row belonging to
// nobody, and there is no tenant it could correctly belong to.
func TestTheWallStillHoldsForARowWithNoNextHop(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, cmd.Config{
		Db:    dbOf(t),
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))

	tenant, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)

	v, err := s.Ungated.Joint().Add(ctx, app.JointAddRequest_builder{Alias: "orphan"}.Build())
	x.NoError(err)

	k, err := pdid.From(tenant.GetId())
	x.NoError(err)

	f := frame.New(pdid.New(pd.HolderDomain), k, frame.Whole()).WithScope(frame.Only(k))

	_, err = s.Walled.Joint().Get(frame.Into(ctx, f), app.JointGetRequest_builder{Ref: v.Ref()}.Build())
	x.Equal(codes.NotFound, status.Code(err), "a row that belongs to no tenant was visible to one")
}
