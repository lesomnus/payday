package frame_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// A call an account makes as itself (payday#184): the account where a holder
// would be, no tenant, and nothing to read that it was not given.
func TestOfAccount(t *testing.T) {
	const own, other = "/app.InvitationService/Accept", "/app.RobotService/Get"
	calls := func(m string) bool { return m == own }

	t.Run("holds the account where a holder would be, and no tenant", func(t *testing.T) {
		x := require.New(t)

		a := pdid.New(Holder)
		f := frame.OfAccount(a, frame.Whole(), calls)
		x.True(f.AsAccount())
		x.Equal(a, f.Actor, "the trail, the past and the access record name it as who acted")
		x.Equal(a, f.Account)
		x.True(f.Tenant.IsZero())

		got, ok := frame.AccountOf(frame.Into(t.Context(), f))
		x.True(ok)
		x.Equal(a, got)
	})

	t.Run("a frame written out field by field is never one", func(t *testing.T) {
		x := require.New(t)

		a := pdid.New(Holder)
		f := &frame.Frame{Actor: a, Account: a}
		x.False(f.AsAccount())
		x.False(frame.New(a, pdid.Nil, frame.Whole()).AsAccount())

		_, ok := frame.AccountOf(frame.Into(t.Context(), f))
		x.False(ok)

		var none *frame.Frame
		x.False(none.AsAccount())
	})

	t.Run("may be of the methods it was made for and no other", func(t *testing.T) {
		x := require.New(t)

		f := frame.OfAccount(pdid.New(Holder), frame.Whole(), calls)
		x.True(f.Calls(own))
		x.False(f.Calls(other))

		// Made for nothing, it may do nothing: the safe way round for a frame
		// somebody built without saying.
		x.False(frame.OfAccount(pdid.New(Holder), frame.Whole(), nil).Calls(own))

		// And a call served as a membership is the policy's to say about.
		x.True(frame.New(pdid.New(Holder), pdid.New(Tenant), frame.Whole()).Calls(other))
	})

	t.Run("sees no tenant, whatever it is given", func(t *testing.T) {
		x := require.New(t)

		f := frame.OfAccount(pdid.New(Holder), frame.Whole(), calls)
		for _, s := range []frame.Tenants{frame.Everything, frame.Only(pdid.New(Tenant))} {
			g := f.WithScope(s)
			x.True(g.AsAccount(), "a copy is one still")
			x.False(g.Scope.All())
			x.Empty(g.Scope.Ids())

			vs, all, err := frame.Narrow(frame.Into(t.Context(), g))
			x.NoError(err)
			x.False(all, "everything, for somebody in no tenant, would be every tenant")
			x.Empty(vs)
		}
	})

	t.Run("is in no set, and the rule for them is not asked", func(t *testing.T) {
		x := require.New(t)

		asked := false
		of := frame.Sets(func(context.Context) ([]uuid.UUID, bool, error) {
			asked = true
			return nil, true, nil
		})

		ctx := frame.Into(t.Context(), frame.OfAccount(pdid.New(Holder), frame.Whole(), calls))
		vs, all, err := frame.NarrowSet(ctx, of)
		x.NoError(err)
		x.False(all)
		x.Empty(vs)
		x.False(asked, "a rule written about a member's sets, asked about somebody who is no member")
	})

	t.Run("a membership says which account it is of, and an account is not moved", func(t *testing.T) {
		x := require.New(t)

		a := pdid.New(Holder)
		m := frame.New(pdid.New(Holder), pdid.New(Tenant), frame.Whole())
		g := m.WithAccount(a)
		x.Equal(a, g.Account)
		x.False(g.AsAccount(), "a membership of an account is not the account")
		x.True(m.Account.IsZero(), "the original was changed")

		f := frame.OfAccount(a, frame.Whole(), calls)
		x.Equal(a, f.WithAccount(pdid.New(Holder)).Account, "an account's frame naming another would name two callers")
	})
}
