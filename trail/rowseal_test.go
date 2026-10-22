package trail

import (
	"testing"
	"uuid"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
)

// TestForgettingBlanksTheRowsSeals: a seal is a MAC of what the person was,
// and the forgetting that blanks the trail about them blanks it too -- leaving
// one that says they were forgotten, so that the row is told from one somebody
// took the seal off -- whatever a hold keeps of the trail, since a hold keeps
// the record of what happened and a seal is no part of it (payday#115).
func TestForgettingBlanksTheRowsSeals(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d, err := audit.NewDigest([]byte("the first key, thirty-two bytes!"))
	x.NoError(err)

	held := newTenant()
	who, other := uuid.NewV7(), uuid.NewV7()
	s := &fakeStore{
		seals:    map[uuid.UUID][]byte{who: {1, 2, 3}, other: {4, 5, 6}},
		sealedIn: map[uuid.UUID]uuid.UUID{who: held.Uuid(), other: held.Uuid()},
	}
	s.add(fakeRow{tenant: held.Uuid(), object: who, domain: person, created: ago(day), value: "a name"})

	p := Policy{
		Signer: signer, Archive: flob.NewMemStores(), Digest: d,
		Tenants: answers(map[pdid.Id]Tenant{held: {Hold: onHold()}}),
	}
	got, err := p.Forget(ctx, s, []pdid.Id{pdid.Id(who)})
	x.NoError(err)
	x.Equal(1, got.Seals)
	x.Equal(1, got.Held.Rows, "the hold kept the trail")
	x.Equal(d.Forgotten(pdid.Id(who)), s.seals[who])
	x.Equal([]byte{4, 5, 6}, s.seals[other], "a seal of somebody else was blanked")

	t.Run("and removes it under a policy with no digest key", func(t *testing.T) {
		x := require.New(t)

		got, err := Policy{Signer: signer, Archive: flob.NewMemStores()}.Forget(ctx, s, []pdid.Id{pdid.Id(other)})
		x.NoError(err)
		x.Equal(1, got.Seals)
		x.NotContains(s.seals, other)
	})
}

// TestATenantLeavingTakesItsRowsSeals: a seal is of a live row, and a tenant
// that leaves takes its rows -- so it takes their seals, and nobody else's.
func TestATenantLeavingTakesItsRowsSeals(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	leaving, staying := newTenant(), newTenant()
	mine, theirs := uuid.NewV7(), uuid.NewV7()
	s := &fakeStore{
		seals:    map[uuid.UUID][]byte{mine: {1}, theirs: {2}},
		sealedIn: map[uuid.UUID]uuid.UUID{mine: leaving.Uuid(), theirs: staying.Uuid()},
	}

	p := Policy{Signer: signer, Archive: flob.NewMemStores()}
	plan, err := p.PlanTenantPurge(ctx, s, leaving)
	x.NoError(err)
	x.Zero(plan.Seals)
	x.Contains(s.seals, mine, "a plan took a seal")

	got, err := p.PurgeTenant(ctx, s, leaving)
	x.NoError(err)
	x.Equal(1, got.Seals)
	x.NotContains(s.seals, mine)
	x.Contains(s.seals, theirs)
}
