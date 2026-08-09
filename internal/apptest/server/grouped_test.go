package server_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// grouped is the app with both axes on: the wall the schema declared, and the
// set beside it.
//
// `bare.Scopes` is what says both are meant. `bare.WithScope` refuses being
// given twice, because losing one is silent and so is overlapping them, and the
// call site does not say which was intended.
func (a *App) grouped(t *testing.T, of pd.Sets) app.Server {
	t.Helper()

	s, err := bare.NewServer(a.Db,
		bare.WithMinter(pd.Minter()),
		bare.WithScope(bare.Scopes{pd.Wall(), pd.Grouped(of)}),
	)
	require.NoError(t, err)

	return s
}

// cellOf puts a cell in the given tenant and answers with its identifier.
func (a *App) cellOf(ctx context.Context, x *require.Assertions, t pdid.Id, alias string) pdid.Id {
	v, err := a.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: t.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	x.NoError(err)

	k, err := pdid.From(v.GetId())
	x.NoError(err)

	return k
}

// robotIn puts a robot in the given cell of the given tenant.
func (a *App) robotIn(ctx context.Context, x *require.Assertions, t, c pdid.Id, alias string) pdid.Id {
	v, err := a.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: t.Bytes()}.Build(),
		Cell:   app.CellRef_builder{Id: c.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	x.NoError(err)

	k, err := pdid.From(v.GetId())
	x.NoError(err)

	return k
}

// within is a caller of `tenant` whose credential was made for `sets`, and for
// every set there is when none are named.
func within(ctx context.Context, tenant pdid.Id, sets ...pdid.Id) context.Context {
	g := frame.Whole()
	if len(sets) > 0 {
		g = g.Within(sets...)
	}

	f := frame.New(pdid.New(pd.HolderDomain), tenant, g)

	return frame.Into(ctx, f.WithScope(frame.Only(tenant)))
}

// sees is which of the given robots a server answers about.
func sees(ctx context.Context, x *require.Assertions, s app.Server, ids ...pdid.Id) []bool {
	vs := make([]bool, len(ids))
	for i, id := range ids {
		_, err := s.Robot().Get(ctx, app.RobotGetRequest_builder{
			Ref: app.RobotRef_builder{Id: id.Bytes()}.Build(),
		}.Build())
		if err == nil {
			vs[i] = true
			continue
		}

		x.ErrorContains(err, "not found", "narrowing is not refusing: a row out of scope is a row the query did not match")
	}

	return vs
}

// TestACredentialNarrowsToASet.
//
// The second axis is what a schema declares at field 3, and until now only a
// policy could move it: `Grouped` was handed a closure and asked it, so what a
// caller may see was the whole of the answer. A credential had nowhere to say
// "this key is for one site" -- which is the ordinary shape for a deployment
// that has sites, since such a key is issued where the work is, outlives
// whoever issued it, and must not be usable anywhere else.
//
// The tenant axis cannot say it. Every site of a tenant is inside that tenant,
// so a key narrowed to the tenant reaches all of them.
func TestACredentialNarrowsToASet(t *testing.T) {
	arrange := func(t *testing.T) (*App, context.Context, pdid.Id, [2]pdid.Id, [2]pdid.Id) {
		t.Helper()
		x := require.New(t)

		a := New(t)
		ctx := t.Context()

		acme := a.tenantOf(ctx, x, "acme")
		north := a.cellOf(ctx, x, acme, "north")
		south := a.cellOf(ctx, x, acme, "south")

		return a, ctx, acme,
			[2]pdid.Id{north, south},
			[2]pdid.Id{
				a.robotIn(ctx, x, acme, north, "arm-01"),
				a.robotIn(ctx, x, acme, south, "arm-02"),
			}
	}

	// The hole this closes. An app that declared a set but wired no rule for
	// who may see which -- because it leaves that to the credential -- had
	// `Grouped(nil)`, which narrowed nothing at all. So the token said one site
	// and reached both, and nothing anywhere said so.
	t.Run("with no policy at all, which is where it was lost", func(t *testing.T) {
		x := require.New(t)

		a, ctx, acme, cells, robots := arrange(t)
		s := a.grouped(t, nil)

		x.Equal([]bool{true, true}, sees(within(ctx, acme), x, s, robots[:]...),
			"a credential that narrows nothing sees the tenant's own")
		x.Equal([]bool{true, false}, sees(within(ctx, acme, cells[0]), x, s, robots[:]...))
		x.Equal([]bool{false, true}, sees(within(ctx, acme, cells[1]), x, s, robots[:]...))
	})

	// And beside a policy it is the meet of the two, the way [frame.Grant] is
	// everywhere else: a credential only ever narrows what its holder may see,
	// and never widens it.
	t.Run("beside a policy it is the meet", func(t *testing.T) {
		x := require.New(t)

		a, ctx, acme, cells, robots := arrange(t)

		only := func(vs ...pdid.Id) pd.Sets {
			return func(context.Context) ([]uuid.UUID, bool, error) {
				us := make([]uuid.UUID, len(vs))
				for i, v := range vs {
					us[i] = v.Uuid()
				}

				return us, false, nil
			}
		}

		// The policy says north; the credential says nothing.
		x.Equal([]bool{true, false},
			sees(within(ctx, acme), x, a.grouped(t, only(cells[0])), robots[:]...))

		// The policy says both; the credential says south.
		x.Equal([]bool{false, true},
			sees(within(ctx, acme, cells[1]), x, a.grouped(t, only(cells[0], cells[1])), robots[:]...))

		// They disagree, so the meet is empty and the answer is no rows --
		// **not** the credential's answer. A token cannot reach a set its
		// holder may not see.
		x.Equal([]bool{false, false},
			sees(within(ctx, acme, cells[1]), x, a.grouped(t, only(cells[0])), robots[:]...))
	})

	// A row in no set, which a nullable field 3 makes possible and an app that
	// added the set to a schema that already had rows makes certain.
	//
	// It is invisible to a read narrowed to a set, and visible to one that is
	// not narrowed at all. That is fail-closed and it is the right way round --
	// a row nobody put in a site is not in every site -- but it is surprising
	// enough to be written down: an app that backfills field 3 will find rows
	// that nobody with a site-scoped credential can see until it does.
	t.Run("a row in no set is out of every set", func(t *testing.T) {
		x := require.New(t)

		a, ctx, acme, cells, _ := arrange(t)
		s := a.grouped(t, nil)

		loose := a.robotOf(ctx, x, acme, "arm-03")

		x.Equal([]bool{true}, sees(within(ctx, acme), x, s, loose),
			"a credential that narrows no set reads it")
		x.Equal([]bool{false}, sees(within(ctx, acme, cells[0]), x, s, loose))
		x.Equal([]bool{false}, sees(within(ctx, acme, cells[1]), x, s, loose))
	})

	// An entity with no field 3 is in no set, so the second axis says nothing
	// about it. That is not a hole: a row that is in no set cannot be narrowed
	// to one, and the wall is still in front of it.
	t.Run("an entity that declared no set is not narrowed by one", func(t *testing.T) {
		x := require.New(t)

		a, ctx, acme, cells, robots := arrange(t)
		s := a.grouped(t, nil)

		// Joint reaches its tenant through Robot and declares no set of its
		// own, so a credential for the other cell still reads it.
		v, err := a.Ungated.Joint().Add(ctx, app.JointAddRequest_builder{
			Robot: app.RobotRef_builder{Id: robots[0].Bytes()}.Build(),
			Alias: "elbow",
		}.Build())
		x.NoError(err)

		_, err = s.Joint().Get(within(ctx, acme, cells[1]), app.JointGetRequest_builder{
			Ref: app.JointRef_builder{Id: v.GetId()}.Build(),
		}.Build())
		x.NoError(err)
	})
}
