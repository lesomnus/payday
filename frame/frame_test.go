package frame_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

const (
	Tenant pdid.Domain = 1
	Holder pdid.Domain = 2
	Cell   pdid.Domain = 10
)

func TestMain(m *testing.M) {
	pdid.Register("test.Tenant", Tenant, "tenant")
	pdid.Register("test.Holder", Holder, "holder")
	pdid.Register("test.Cell", Cell, "cell")

	m.Run()
}

func TestTenants(t *testing.T) {
	t.Run("the zero value sees nothing, and nothing is not everything", func(t *testing.T) {
		x := require.New(t)

		var z frame.Tenants
		x.False(z.All())
		x.Empty(z.Uuids())

		// This is the whole safety of it. `IdIn()` with an empty list renders
		// as WHERE FALSE, so a caller who may see nothing sees no rows. A
		// scope that reported "nothing to narrow by" here would open up as it
		// ran out.
		x.Equal(frame.Nothing, z)
	})

	t.Run("Everything narrows nothing", func(t *testing.T) {
		x := require.New(t)

		x.True(frame.Everything.All())
	})

	t.Run("Only holds what it was given", func(t *testing.T) {
		x := require.New(t)

		a, b := pdid.New(Tenant), pdid.New(Tenant)
		s := frame.Only(a, b)

		x.False(s.All())
		x.ElementsMatch([]pdid.Id{a, b}, s.Ids())
		x.Len(s.Uuids(), 2)
		x.Equal(a.Uuid(), s.Uuids()[0])
	})
}

func TestMeet(t *testing.T) {
	a, b, c := pdid.New(Tenant), pdid.New(Tenant), pdid.New(Tenant)

	t.Run("a credential that narrows nothing changes nothing", func(t *testing.T) {
		x := require.New(t)

		s := frame.Only(a, b).Meet(frame.Whole())
		x.ElementsMatch([]pdid.Id{a, b}, s.Ids())

		x.True(frame.Everything.Meet(frame.Whole()).All())
	})

	t.Run("it only ever narrows", func(t *testing.T) {
		x := require.New(t)

		// A token naming a tenant its holder cannot see does not reach it.
		s := frame.Only(a).Meet(frame.Grant{}.In(a, b, c))
		x.ElementsMatch([]pdid.Id{a}, s.Ids())
		x.False(s.All())

		// And one held by somebody who may see everything is narrowed to what
		// it names, rather than the other way round.
		s = frame.Everything.Meet(frame.Grant{}.In(b))
		x.False(s.All())
		x.ElementsMatch([]pdid.Id{b}, s.Ids())
	})

	t.Run("disjoint is nothing, and nothing reads as no rows", func(t *testing.T) {
		x := require.New(t)

		s := frame.Only(a).Meet(frame.Grant{}.In(b))
		x.False(s.All())
		x.Empty(s.Uuids())
	})
}

func TestGrant(t *testing.T) {
	t.Run("the zero value allows nothing", func(t *testing.T) {
		x := require.New(t)

		var z frame.Grant
		x.False(z.IsWhole())
		x.False(z.AnyTenant())
		x.False(z.AnySet())
		x.False(z.AnyAction())
		x.False(z.Allows("/app.RobotService/Get"))
		x.Empty(z.TenantIds())
		x.Empty(z.SetIds())
		x.Empty(z.Actions())
	})

	t.Run("Whole allows whatever the actor does", func(t *testing.T) {
		x := require.New(t)

		g := frame.Whole()
		x.True(g.IsWhole())
		x.True(g.AnyTenant())
		x.True(g.AnySet())
		x.True(g.AnyAction())
		x.True(g.Allows("/anything/at/all"))
	})

	// Every axis readable, which is what it takes to write one down.
	//
	// The two answers a list cannot tell apart are "every method" and "no
	// method at all" -- both hold an empty slice -- so anything encoding a
	// Grant has to write the flag beside the list. This is the test that says
	// the flag is reachable at all; `TestGrantRoundTrip` in `auth` is the one
	// that says an encoder used it.
	t.Run("every axis says whether it narrows", func(t *testing.T) {
		x := require.New(t)

		every := frame.Whole()
		none := frame.Whole().In().Within().To()

		// Indistinguishable by their lists.
		x.Empty(every.TenantIds())
		x.Empty(every.SetIds())
		x.Empty(every.Actions())
		x.Empty(none.TenantIds())
		x.Empty(none.SetIds())
		x.Empty(none.Actions())

		// Told apart only by the flags.
		x.True(every.AnyTenant())
		x.True(every.AnySet())
		x.True(every.AnyAction())
		x.False(none.AnyTenant())
		x.False(none.AnySet())
		x.False(none.AnyAction())
	})

	t.Run("Actions is what To was given", func(t *testing.T) {
		x := require.New(t)

		ms := []string{"/app.RobotService/Get", "/app.RobotService/List"}
		g := frame.Whole().To(ms...)

		x.False(g.AnyAction())
		x.Equal(ms, g.Actions())

		// And what it was given is what it allows, which is the pair anything
		// re-encoding a Grant depends on.
		for _, m := range g.Actions() {
			x.True(g.Allows(m))
		}
		x.False(g.Allows("/app.RobotService/Erase"))
	})

	// What a pattern reaches is [frame.Covers]'s question, answered over
	// there; this one is whether Allows asks it at all. As a membership test
	// Allows passes every case above unchanged, and a stored
	// "/app.RobotService/*" quietly allows nothing.
	t.Run("an action may be a pattern", func(t *testing.T) {
		x := require.New(t)

		g := frame.Whole().To("/app.RobotService/*")
		x.True(g.Allows("/app.RobotService/Get"))
		x.False(g.Allows("/hq.RobotService/Get"))
	})

	t.Run("naming none allows none", func(t *testing.T) {
		x := require.New(t)

		x.False(frame.Whole().To().Allows("/app.RobotService/Get"))
		x.Empty(frame.Whole().In().TenantIds())
		x.False(frame.Whole().In().AnyTenant())
		x.Empty(frame.Whole().Within().SetIds())
		x.False(frame.Whole().Within().AnySet())
	})

	t.Run("narrowing one axis leaves the others", func(t *testing.T) {
		x := require.New(t)

		g := frame.Whole().To("/app.RobotService/Get")
		x.True(g.AnyTenant())
		x.True(g.AnySet())
		x.True(g.Allows("/app.RobotService/Get"))
		x.False(g.Allows("/app.RobotService/Erase"))

		// The set is its own axis and not a finer tenant: narrowing to a site
		// says nothing about which tenants, and a credential for one site of
		// one tenant has to say both.
		h := frame.Whole().Within(pdid.New(Cell))
		x.True(h.AnyTenant())
		x.True(h.Allows("/anything/at/all"))
		x.False(h.AnySet())
		x.False(h.IsWhole())
	})
}

// TestNarrowSet is the second axis's [frame.Narrow], and what it adds is the
// meet: `of` answers about the actor, and the credential narrows that.
//
// It is here rather than left to whatever calls it because the failure is
// silent. An app that answers "which sets may this caller see" correctly and
// never thinks about the credential hands out site-scoped keys that reach every
// site, and nothing says so.
func TestNarrowSet(t *testing.T) {
	north, south := pdid.New(Cell), pdid.New(Cell)

	only := func(vs ...pdid.Id) frame.Sets {
		return func(context.Context) ([]uuid.UUID, bool, error) {
			us := make([]uuid.UUID, len(vs))
			for i, v := range vs {
				us[i] = v.Uuid()
			}

			return us, false, nil
		}
	}
	every := frame.Sets(func(context.Context) ([]uuid.UUID, bool, error) {
		return nil, true, nil
	})

	as := func(g frame.Grant) context.Context {
		return frame.Into(t.Context(), frame.New(pdid.New(Holder), pdid.New(Tenant), g))
	}

	for _, tc := range []struct {
		name  string
		of    frame.Sets
		grant frame.Grant
		all   bool
		vs    []uuid.UUID
	}{
		{"nothing narrows", nil, frame.Whole(), true, nil},
		{"only the credential does", nil, frame.Whole().Within(north), false, []uuid.UUID{north.Uuid()}},
		{"only the policy does", only(north), frame.Whole(), false, []uuid.UUID{north.Uuid()}},
		{"the policy says every one", every, frame.Whole().Within(south), false, []uuid.UUID{south.Uuid()}},
		{"both, and they agree", only(north, south), frame.Whole().Within(south), false, []uuid.UUID{south.Uuid()}},
		{"both, and they do not", only(north), frame.Whole().Within(south), false, []uuid.UUID{}},

		// An empty list is not "no narrowing". `IdIn()` renders as
		// `WHERE FALSE`, so a credential made for no set reads no rows -- read
		// the other way round it would open up as it ran out.
		{"a credential for no set at all", only(north), frame.Whole().Within(), false, []uuid.UUID{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			vs, all, err := frame.NarrowSet(as(tc.grant), tc.of)
			x.NoError(err)
			x.Equal(tc.all, all)
			x.Equal(tc.vs, vs)
		})
	}

	t.Run("a request with no frame is refused rather than served as anybody", func(t *testing.T) {
		x := require.New(t)

		_, _, err := frame.NarrowSet(t.Context(), every)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})
}

func TestScope(t *testing.T) {
	t.Run("a request with no frame is refused, not served as anybody", func(t *testing.T) {
		x := require.New(t)

		_, err := frame.Scope(context.Background())
		x.Equal(codes.Unauthenticated, status.Code(err))

		// And Narrow says the same thing rather than answering "nothing to
		// narrow by", which is what would open every row.
		_, all, err := frame.Narrow(context.Background())
		x.Error(err)
		x.True(all, "all is meaningless beside an error; the error is the answer")
	})

	t.Run("is what the frame was given", func(t *testing.T) {
		x := require.New(t)

		a := pdid.New(Tenant)
		f := frame.New(pdid.New(Holder), a, frame.Whole()).WithScope(frame.Only(a))
		ctx := frame.Into(context.Background(), f)

		s, err := frame.Scope(ctx)
		x.NoError(err)
		x.ElementsMatch([]pdid.Id{a}, s.Ids())

		vs, all, err := frame.Narrow(ctx)
		x.NoError(err)
		x.False(all)
		x.Equal([]uuid.UUID{a.Uuid()}, vs)
	})

	t.Run("a frame that says nothing about scope reads no rows", func(t *testing.T) {
		x := require.New(t)

		f := frame.New(pdid.New(Holder), pdid.New(Tenant), frame.Whole())
		ctx := frame.Into(context.Background(), f)

		vs, all, err := frame.Narrow(ctx)
		x.NoError(err)
		x.False(all)
		x.Empty(vs)
	})
}

func TestWith(t *testing.T) {
	x := require.New(t)

	a := pdid.New(Tenant)
	f := frame.New(pdid.New(Holder), a, frame.Whole())

	g := f.WithScope(frame.Everything)
	x.True(g.Scope.All())
	x.False(f.Scope.All(), "the original was changed")
}
