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
)

func TestMain(m *testing.M) {
	pdid.Register("test.Tenant", Tenant, "tenant")
	pdid.Register("test.Holder", Holder, "holder")

	m.Run()
}

func TestTenants(t *testing.T) {
	t.Run("the zero value sees nothing, and nothing is not everything", func(t *testing.T) {
		x := require.New(t)

		var z frame.Tenants
		x.False(z.All())
		x.Empty(z.Uuids())

		// This is the whole safety of it. `IDIn()` with an empty list renders
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
		x.False(z.Allows("/app.RobotService/Get"))
		x.Empty(z.TenantIds())
	})

	t.Run("Whole allows whatever the actor does", func(t *testing.T) {
		x := require.New(t)

		g := frame.Whole()
		x.True(g.IsWhole())
		x.True(g.AnyTenant())
		x.True(g.Allows("/anything/at/all"))
	})

	t.Run("naming none allows none", func(t *testing.T) {
		x := require.New(t)

		x.False(frame.Whole().To().Allows("/app.RobotService/Get"))
		x.Empty(frame.Whole().In().TenantIds())
		x.False(frame.Whole().In().AnyTenant())
	})

	t.Run("narrowing one axis leaves the other", func(t *testing.T) {
		x := require.New(t)

		g := frame.Whole().To("/app.RobotService/Get")
		x.True(g.AnyTenant())
		x.True(g.Allows("/app.RobotService/Get"))
		x.False(g.Allows("/app.RobotService/Erase"))
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
