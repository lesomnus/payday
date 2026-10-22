package trail

import (
	"testing"
	"time"

	"filippo.io/age"
	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// retiredIn is the tenants the archive keeps the record of a retirement for,
// as an act reads them: by the manifest.
func retiredIn(t *testing.T, p Policy, s Store) map[pdid.Id]time.Time {
	t.Helper()

	x, err := p.start(s)
	require.NoError(t, err)
	require.NoError(t, x.trust(t.Context()))

	vs, err := x.retired(t.Context())
	require.NoError(t, err)

	return vs
}

// retiring is the receipts of a tenant's retirement, by where.
func retiring(t *testing.T, a flob.Stores, tenant pdid.Id) map[string]Receipt {
	t.Helper()

	out := map[string]Receipt{}
	for _, v := range receiptsIn(t, a) {
		if v.Act == "retire-tenant" && v.Tenant == tenant.String() {
			out[v.Where] = v
		}
	}

	return out
}

// TestARetiredTenantKeepsWhatAFloorIsOnUntilItPasses.
//
// A tenant that retires takes its trail out but for what a floor of the
// deployment's is still on: those rows stay as they are, in the database and in
// the archive, and the rest goes now, as a purge's does. From then on a pass
// takes it to keep nothing of its own, which the floors raise -- a row goes
// once its floor has passed, whatever the deployment's window for its kind --
// and once the last floor has, a pass purges what is left.
func TestARetiredTenantKeepsWhatAFloorIsOnUntilItPasses(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	leaving, other := newTenant(), newTenant()

	s := &fakeStore{}
	// Moved into the archive by the first pass: a person's, a robot's a floor
	// is still on, and a robot's whose floor has passed.
	personOld := s.add(fakeRow{tenant: leaving.Uuid(), domain: person, created: ago(40 * day), value: "a person, archived"})
	robotOld := s.add(fakeRow{tenant: leaving.Uuid(), domain: robot, created: ago(40 * day), value: "a robot, archived"})
	robotAncient := s.add(fakeRow{tenant: leaving.Uuid(), domain: robot, created: ago(400 * day), value: "a robot, past its floor"})
	// In the database still.
	personNew := s.add(fakeRow{tenant: leaving.Uuid(), domain: person, created: ago(day), value: "a person"})
	robotNew := s.add(fakeRow{tenant: leaving.Uuid(), domain: robot, created: ago(day), value: "a robot"})
	sharedPerson := s.add(fakeRow{tenant: leaving.Uuid(), actor: other.Uuid(), domain: person, created: ago(day), value: "a person, shared"})
	sharedRobot := s.add(fakeRow{tenant: leaving.Uuid(), actor: other.Uuid(), domain: robot, created: ago(day), value: "a robot, shared"})

	a := flob.NewMemStores()
	p := Policy{
		Signer: signer, Archive: a,
		Keep: Keep{Retain: 30 * day},

		// The deployment keeps what its robots did for ever, and owes a
		// year of it to whoever it answers to.
		By:    map[pdid.Domain]Keep{robot: {Retain: 30 * day}},
		MinBy: map[pdid.Domain]Floor{robot: {Destroy: 365 * day}},

		Tenants: answers(map[pdid.Id]Tenant{}),
	}
	p.Pass(ctx, s)

	plan, err := p.PlanTenantRetire(ctx, s, leaving)
	x.NoError(err)

	got, err := p.RetireTenant(ctx, s, leaving)
	x.NoError(err)

	x.WithinDuration(time.Now().Add(365*day), got.Until, time.Minute, "it does not say when the last floor passes")
	x.WithinDuration(got.Until, plan.Until, time.Minute)
	plan.Until, got.Until = time.Time{}, time.Time{}
	x.Equal(plan, got, "the plan is not what the retirement did")
	x.Equal(TenantPurge{
		Removed: 1, Blanked: 1,
		Chunks: 2, Rows: 2,
		Kept: map[string]int{"robot": 3},
	}, got)

	x.False(s.has(personNew.id), "a row under no floor outlived the retirement")
	x.True(s.has(robotNew.id), "a row under a floor went with the retirement")
	r, _ := s.get(sharedPerson.id)
	x.Empty(r.value, "what it shared under no floor kept what it said")
	r, _ = s.get(sharedRobot.id)
	x.NotEmpty(r.value, "what it shared under a floor lost what it said")

	in := docsIn(t, a)
	x.NotContains(in, personOld.id, "a chunk under no floor outlived the retirement")
	x.NotContains(in, robotAncient.id, "a chunk whose floor has passed outlived the retirement")
	x.Contains(in, robotOld.id, "a chunk under a floor went with the retirement")

	rs := retiring(t, a, leaving)
	x.Equal(1, rs["database"].Rows)
	x.Equal(1, rs["database"].Blanked)
	x.Equal(2, rs["database"].Kept)
	x.Equal(2, rs["archive"].Rows)
	x.Equal(1, rs["archive"].Kept)
	x.Equal([]string{"robot"}, rs["database"].Kinds)
	x.False(rs["archive"].Until.IsZero(), "a receipt does not say until when")
	x.Contains(retiredIn(t, p, s), leaving, "nothing records that it retired")

	// Its floor still on it, a pass takes nothing of what was kept.
	p.Pass(ctx, s)
	in = docsIn(t, a)
	x.Contains(in, robotOld.id, "a pass destroyed what a floor is still on")
	x.True(s.has(robotNew.id) || in[robotNew.id] != nil, "a pass destroyed what a floor is still on")

	// A floor that passes sooner: what it was on goes when it passes, and
	// not when the deployment's window -- for ever -- would have it.
	p.MinBy = map[pdid.Domain]Floor{robot: {Destroy: 20 * day}}
	p.Pass(ctx, s)
	in = docsIn(t, a)
	x.NotContains(in, robotOld.id, "a retired tenant's row outlived its floor")
	x.Contains(in, robotNew.id, "a pass destroyed what a floor is still on")
	x.Contains(retiredIn(t, p, s), leaving)

	// And with every floor passed, what is left goes, and the record with it.
	p.MinBy = nil
	p.Pass(ctx, s)
	in = docsIn(t, a)
	x.NotContains(in, robotNew.id, "a retired tenant's row outlived the last of its floors")
	r, _ = s.get(sharedRobot.id)
	x.Empty(r.value, "what it shared kept what it said after its floors had passed")
	x.NotContains(retiredIn(t, p, s), leaving, "the record that it retired outlived what it kept")

	purged := false
	for _, v := range receiptsIn(t, a) {
		if v.Act == "purge-tenant" && v.Tenant == leaving.String() {
			purged = true
		}
	}
	x.True(purged, "the end of a retirement is not receipted")
}

// TestARetiredTenantUnderAHoldKeepsWhatItKept.
//
// A hold blocks a retirement as it does a purge. On a tenant that has retired,
// it keeps what the retirement kept past its floors, and the purge at their
// end waits for it to lift.
func TestARetiredTenantUnderAHoldKeepsWhatItKept(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	leaving := newTenant()
	s := &fakeStore{}
	r := s.add(fakeRow{tenant: leaving.Uuid(), domain: robot, created: ago(40 * day), value: "a robot"})

	a := flob.NewMemStores()
	vs := map[pdid.Id]Tenant{leaving: {Hold: onHold()}}
	p := Policy{
		Signer: signer, Archive: a,
		Keep:    Keep{Retain: 30 * day},
		MinBy:   map[pdid.Domain]Floor{robot: {Destroy: 365 * day}},
		Tenants: answers(vs),
	}
	p.Pass(ctx, s)

	_, err := p.PlanTenantRetire(ctx, s, leaving)
	x.ErrorIs(err, ErrHeld)
	_, err = p.RetireTenant(ctx, s, leaving)
	x.ErrorIs(err, ErrHeld)
	x.NotContains(retiredIn(t, p, s), leaving)

	vs[leaving] = Tenant{}
	got, err := p.RetireTenant(ctx, s, leaving)
	x.NoError(err)
	x.Equal(map[string]int{"robot": 1}, got.Kept)

	// Held again, and its floors gone.
	vs[leaving] = Tenant{Hold: onHold()}
	p.MinBy = nil
	p.Pass(ctx, s)
	x.Contains(docsIn(t, a), r.id, "a pass destroyed what a hold is on")
	x.Contains(retiredIn(t, p, s), leaving, "a retired tenant under a hold was purged")

	vs[leaving] = Tenant{}
	p.Pass(ctx, s)
	x.NotContains(docsIn(t, a), r.id)
	x.NotContains(retiredIn(t, p, s), leaving)
}

// TestARetiredTenantKeepsItsKeyUntilTheLastOfWhatItKept.
//
// What a retirement keeps is still readable by whoever holds the master, so
// its key stays; it goes with the last of what was kept. A retirement that
// keeps nothing is a purge, key and all.
func TestARetiredTenantKeepsItsKeyUntilTheLastOfWhatItKept(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	leaving, nothing := newTenant(), newTenant()
	k, master := sealing(t)

	s := &fakeStore{}
	kept := s.add(fakeRow{tenant: leaving.Uuid(), domain: robot, created: ago(40 * day), value: "a robot"})
	gone := s.add(fakeRow{tenant: nothing.Uuid(), domain: person, created: ago(40 * day), value: "a person"})

	p := Policy{
		Signer: signer, Archive: flob.NewMemStores(), Keys: k,
		Keep:  Keep{Retain: 30 * day},
		MinBy: map[pdid.Domain]Floor{robot: {Destroy: 365 * day}},
	}
	p.Pass(ctx, s)
	x.Len(keysOf(t, k, leaving.String()), 1)
	x.Len(keysOf(t, k, nothing.String()), 1)

	got, err := p.RetireTenant(ctx, s, leaving)
	x.NoError(err)
	x.Equal(map[string]int{"robot": 1}, got.Kept)
	x.Zero(got.Keys, "a retirement destroyed the key of what it kept")
	x.Len(keysOf(t, k, leaving.String()), 1)

	opens := p
	opens.Keys = &Keys{Store: k.Store, Master: k.Master, Identities: []age.Identity{master}}
	v, err := said(t, func(fn func([]byte) error) error { return opens.ReadTenant(ctx, s, leaving, fn) })
	x.NoError(err)
	x.Equal(kept.value, v[kept.id], "what a retirement kept cannot be read")

	// A tenant with nothing under a floor keeps nothing, key and all.
	got, err = p.RetireTenant(ctx, s, nothing)
	x.NoError(err)
	x.Empty(got.Kept)
	x.Equal(1, got.Keys)
	x.Empty(keysOf(t, k, nothing.String()))
	x.NotContains(raw(t, p.Archive), gone.id)
	x.NotContains(retiredIn(t, p, s), nothing, "a retirement that kept nothing is recorded as one")

	// The floor gone, the pass that finds so purges what is left, the key
	// with it.
	p.MinBy = nil
	p.Pass(ctx, s)
	x.Empty(keysOf(t, k, leaving.String()), "the key outlived the last of what was kept")
	left, err := k.left(ctx, leaving.String())
	x.NoError(err)
	x.False(left.IsZero(), "a tenant whose retirement ended left no mark")
	x.NotContains(raw(t, p.Archive), kept.id)
}

// TestARetirementKeepsFloorsOnlyInAnArchive.
//
// What a retirement keeps, and the record that it left, are the archive's to
// keep. With floors and no archive it is refused rather than made a purge
// nobody asked for; with no floors it keeps nothing, and is a purge.
func TestARetirementKeepsFloorsOnlyInAnArchive(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	leaving := newTenant()
	s := &fakeStore{}
	r := s.add(fakeRow{tenant: leaving.Uuid(), domain: robot, created: ago(day), value: "a robot"})

	p := Policy{Keep: Keep{Retain: 400 * day, Discard: true}, MinBy: map[pdid.Domain]Floor{robot: {Destroy: 365 * day}}}
	_, err := p.RetireTenant(ctx, s, leaving)
	x.Error(err)
	x.True(s.has(r.id), "a retirement refused took rows all the same")

	p.MinBy = nil
	got, err := p.RetireTenant(ctx, s, leaving)
	x.NoError(err)
	x.Equal(1, got.Removed)
	x.Empty(got.Kept)
	x.False(s.has(r.id))
}
