package trail

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"testing"
	"time"

	"uuid"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// answers is a callback over a table the test edits as it goes, which is what
// an app's contract entity is.
func answers(vs map[pdid.Id]Tenant) func(context.Context, pdid.Id) (Tenant, error) {
	return func(ctx context.Context, id pdid.Id) (Tenant, error) { return vs[id], nil }
}

func onHold(kinds ...pdid.Domain) *Hold { return &Hold{Kinds: kinds, Why: "case 1"} }

// archived is every row the archive holds, by identifier, with its document.
func docsIn(t *testing.T, a flob.Stores) map[uuid.UUID]map[string]any {
	t.Helper()

	out := map[uuid.UUID]map[string]any{}
	require.NoError(t, Read(t.Context(), a, func(doc []byte) error {
		h, err := headOf(doc)
		require.NoError(t, err)

		b, err := base64.StdEncoding.DecodeString(h.Id)
		require.NoError(t, err)

		out[uuid.UUID(b)] = map[string]any{"value": h.Value, "tenant": h.Tenant}
		return nil
	}))

	return out
}

func receiptsIn(t *testing.T, a flob.Stores) []Receipt {
	t.Helper()

	vs := []Receipt{}
	require.NoError(t, Receipts(t.Context(), a, func(v Receipt) error {
		vs = append(vs, v)
		return nil
	}))

	return vs
}

// TestAHeldTenantIsMovedAndNeverDestroyed.
//
// A hold stops what makes a row stop existing and nothing else: the window
// still moves a held tenant's rows out of the database -- into the archive even
// when it says discard -- and lifting the hold brings the windows back.
func TestAHeldTenantIsMovedAndNeverDestroyed(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	free, held := newTenant(), newTenant()

	s := &fakeStore{}
	gone := s.add(fakeRow{tenant: free.Uuid(), domain: person, created: ago(60 * day)})
	moved := s.add(fakeRow{tenant: held.Uuid(), domain: person, created: ago(60 * day)})

	discard := Keep{Retain: 30 * day, Discard: true}
	vs := map[pdid.Id]Tenant{
		free: {Keep: &discard},
		held: {Keep: &discard, Hold: onHold()},
	}
	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 90 * day}, Tenants: answers(vs)}

	p.Pass(ctx, s)
	x.False(s.has(gone.id))
	x.False(s.has(moved.id), "a hold kept a row in the database past its window")

	in := docsIn(t, p.Archive)
	x.NotContains(in, gone.id, "a discarded row was kept")
	x.Contains(in, moved.id, "a held row was discarded")

	t.Run("and its archive outlives its window until the hold lifts", func(t *testing.T) {
		x := require.New(t)

		old := s.add(fakeRow{tenant: held.Uuid(), domain: person, created: ago(200 * day)})
		short := Keep{Retain: 30 * day, Destroy: 100 * day}
		vs[held] = Tenant{Keep: &short, Hold: onHold()}

		p.Pass(ctx, s)
		x.Contains(docsIn(t, p.Archive), old.id, "a held chunk was destroyed")

		vs[held] = Tenant{Keep: &short}
		p.Pass(ctx, s)
		x.NotContains(docsIn(t, p.Archive), old.id, "lifting the hold did not bring the window back")

		destroyed := 0
		for _, v := range receiptsIn(t, p.Archive) {
			if v.Act == "destroy" && v.Tenant == held.String() {
				destroyed += v.Rows
			}
		}
		x.NotZero(destroyed)
	})

	t.Run("and with no archive it stays where it is", func(t *testing.T) {
		x := require.New(t)

		r := s.add(fakeRow{tenant: held.Uuid(), domain: person, created: ago(60 * day)})
		vs[held] = Tenant{Keep: &discard, Hold: onHold()}

		p := Policy{Keep: Keep{Retain: 90 * day, Discard: true}, Tenants: answers(vs)}
		p.Pass(ctx, s)
		x.True(s.has(r.id), "a held row was discarded for want of an archive")
	})
}

// TestAHoldOnSomeKindsHoldsOnlyThose.
func TestAHoldOnSomeKindsHoldsOnlyThose(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	held := newTenant()

	s := &fakeStore{}
	people := s.add(fakeRow{tenant: held.Uuid(), domain: person, created: ago(60 * day)})
	machines := s.add(fakeRow{tenant: held.Uuid(), domain: robot, created: ago(60 * day)})

	discard := Keep{Retain: 30 * day, Discard: true}
	p := Policy{
		Archive: flob.NewMemStores(),
		Keep:    discard,
		Tenants: answers(map[pdid.Id]Tenant{held: {Keep: &discard, Hold: onHold(person)}}),
	}

	p.Pass(ctx, s)

	in := docsIn(t, p.Archive)
	x.Contains(in, people.id, "the kind the hold names was discarded")
	x.NotContains(in, machines.id, "a kind the hold does not name was kept")
}

// TestARowSharedWithAHeldTenantIsHeld.
//
// A hold on a tenant holds everything that tenant may read, and a row filed
// under somebody else that names it is one of those.
func TestARowSharedWithAHeldTenantIsHeld(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	free, held := newTenant(), newTenant()

	s := &fakeStore{}
	shared := s.add(fakeRow{tenant: free.Uuid(), actor: held.Uuid(), domain: person, created: ago(60 * day)})

	discard := Keep{Retain: 30 * day, Discard: true}
	p := Policy{
		Archive: flob.NewMemStores(),
		Keep:    discard,
		Tenants: answers(map[pdid.Id]Tenant{held: {Hold: onHold()}}),
	}

	p.Pass(ctx, s)
	x.False(s.has(shared.id))
	x.Contains(docsIn(t, p.Archive), shared.id, "a row the held tenant may read was discarded")
}

// TestAHoldKeepsItsOwnRowsOfAFileOfEverybodys.
//
// A file from before held every tenant's rows together. A hold on one of them
// is no reason to keep the others' past their window, so the file is written
// again with what is held and nothing else.
func TestAHoldKeepsItsOwnRowsOfAFileOfEverybodys(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	free, held := newTenant(), newTenant()
	a := flob.NewMemStores()

	month := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	mine := fakeRow{id: uuid.NewV7(), tenant: free.Uuid(), actor: free.Uuid(), domain: person, created: month.Add(day)}
	theirs := fakeRow{id: uuid.NewV7(), tenant: held.Uuid(), actor: held.Uuid(), domain: person, created: month.Add(2 * day)}

	_, err := put(ctx, a, Chunk{Namespace: LegacyNamespace, Kind: "person", Month: month, Name: "audit-2020-01.person.0" + Ext},
		[][]byte{mine.doc(), theirs.doc()})
	x.NoError(err)

	p := Policy{
		Archive: a,
		Keep:    Keep{Retain: 30 * day, Destroy: year},
		Tenants: answers(map[pdid.Id]Tenant{held: {Hold: onHold()}}),
	}
	p.Pass(ctx, &fakeStore{})

	in := docsIn(t, a)
	x.NotContains(in, mine.id, "a row nobody holds outlived its window for sharing a file with one somebody does")
	x.Contains(in, theirs.id, "the held row went with the file")

	cs, err := chunksIn(ctx, a, LegacyNamespace)
	x.NoError(err)
	x.Len(cs, 1)
	x.Equal(1, cs[0].Rows)

	rs := receiptsIn(t, a)
	x.Len(rs, 1)
	x.Equal(1, rs[0].Rows)
	x.Equal(1, rs[0].Held)
}

// TestPurgeByHandLeavesWhatIsHeldAndSaysSo.
//
// Refusing would keep every other tenant's archive past what they are owed, so
// a purge goes ahead without what is held and answers with it.
func TestPurgeByHandLeavesWhatIsHeldAndSaysSo(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	free, held := newTenant(), newTenant()

	s := &fakeStore{}
	a := s.add(fakeRow{tenant: free.Uuid(), domain: person, created: ago(60 * day)})
	b := s.add(fakeRow{tenant: held.Uuid(), domain: person, created: ago(60 * day)})

	vs := map[pdid.Id]Tenant{}
	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Tenants: answers(vs)}
	p.Pass(ctx, s)

	vs[held] = Tenant{Hold: onHold()}

	doomed, h, err := p.Doomed(ctx, Before(time.Now()))
	x.NoError(err)
	x.Len(doomed, 1)
	x.Equal(1, h.Chunks)

	gone, h, err := p.Purge(ctx, Before(time.Now()))
	x.NoError(err)
	x.Equal(doomed, gone, "the dry run and the act disagree")
	x.Equal([]pdid.Id{held}, h.By)
	x.Equal(1, h.Chunks)

	in := docsIn(t, p.Archive)
	x.NotContains(in, a.id)
	x.Contains(in, b.id, "a purge by hand destroyed what a hold is on")
}

// TestCollectingByHandLeavesWhatIsHeld.
func TestCollectingByHandLeavesWhatIsHeld(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	free, held := newTenant(), newTenant()

	s := &fakeStore{}
	a := s.add(fakeRow{tenant: free.Uuid(), domain: person, created: ago(60 * day)})
	b := s.add(fakeRow{tenant: held.Uuid(), domain: person, created: ago(60 * day)})

	p := Policy{Archive: flob.NewMemStores(), Tenants: answers(map[pdid.Id]Tenant{held: {Hold: onHold()}})}

	n, h, err := p.Collect(ctx, s, Kinds{}, time.Now())
	x.NoError(err)
	x.Equal(1, n)
	x.Equal(1, h.Rows)
	x.Equal([]pdid.Id{held}, h.By)
	x.False(s.has(a.id))
	x.True(s.has(b.id), "a row a hold is on was removed by hand")

	rs := receiptsIn(t, p.Archive)
	x.Len(rs, 1)
	x.Equal("discard", rs[0].Act)
	x.Equal(1, rs[0].Held)
}

// TestErasingASubjectLeavesWhatAHoldIsOnAndSaysWhose.
//
// A legal claim overrides an erasure request. What the caller hears back is
// which rows and whose hold, so that it can erase again when the hold lifts.
func TestErasingASubjectLeavesWhatAHoldIsOnAndSaysWhose(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	free, held := newTenant(), newTenant()
	who := uuid.NewV7()

	s := &fakeStore{}
	// One of each in the archive, and one of each still in the database.
	oldFree := s.add(fakeRow{tenant: free.Uuid(), object: who, domain: person, created: ago(60 * day), value: "a name"})
	oldHeld := s.add(fakeRow{tenant: held.Uuid(), object: who, domain: person, created: ago(60 * day), value: "a name"})

	vs := map[pdid.Id]Tenant{}
	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Tenants: answers(vs)}
	p.Pass(ctx, s)

	newFree := s.add(fakeRow{tenant: free.Uuid(), object: who, domain: person, created: ago(day), value: "a name"})
	newHeld := s.add(fakeRow{tenant: held.Uuid(), object: who, domain: person, created: ago(day), value: "a name"})

	vs[held] = Tenant{Hold: onHold()}

	got, err := p.Forget(ctx, s, []pdid.Id{pdid.Id(who)})
	x.NoError(err)
	x.Equal(1, got.Rows)
	x.Equal(1, got.Archived)
	x.Equal(2, got.Held.Rows)
	x.Equal([]pdid.Id{held}, got.Held.By)

	r, _ := s.get(newFree.id)
	x.Empty(r.value)
	r, _ = s.get(newHeld.id)
	x.NotEmpty(r.value, "a held row in the database was blanked")

	in := docsIn(t, p.Archive)
	x.Empty(in[oldFree.id]["value"])
	x.NotEmpty(in[oldHeld.id]["value"], "a held row in the archive was blanked")

	t.Run("and erases it when the hold lifts", func(t *testing.T) {
		x := require.New(t)

		vs[held] = Tenant{}

		got, err := p.Forget(ctx, s, []pdid.Id{pdid.Id(who)})
		x.NoError(err)
		x.False(got.Held.Any())

		r, _ := s.get(newHeld.id)
		x.Empty(r.value)
		x.Empty(docsIn(t, p.Archive)[oldHeld.id]["value"])
	})
}

// TestATenantLeaves.
//
// What was its alone goes. What it shares stays as an event with its contents
// gone, because it is the other tenant's evidence as much as its own -- except
// what it shares with a tenant under a hold, which stays exactly as it is. What
// is filed under somebody else is theirs and is not touched.
func TestATenantLeaves(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	leaving, other, held := newTenant(), newTenant(), newTenant()

	s := &fakeStore{}
	alone := s.add(fakeRow{tenant: leaving.Uuid(), domain: person, created: ago(day), value: "v"})
	sharing := s.add(fakeRow{tenant: leaving.Uuid(), actor: other.Uuid(), domain: person, created: ago(day), value: "v"})
	sharingHeld := s.add(fakeRow{tenant: leaving.Uuid(), actor: held.Uuid(), domain: person, created: ago(day), value: "v"})
	theirs := s.add(fakeRow{tenant: other.Uuid(), counterpart: z(leaving.Uuid()), domain: person, created: ago(day), value: "v"})

	a := flob.NewMemStores()
	month := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	row := func(tenant, actor pdid.Id) fakeRow {
		return fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: actor.Uuid(), domain: person, created: month.Add(day), value: "v"}
	}

	own := row(leaving, leaving)
	_, err := put(ctx, a, Chunk{Namespace: leaving.String(), Kind: "person", Month: month, Rows: 1, First: own.created, Last: own.created},
		[][]byte{own.doc()})
	x.NoError(err)

	set := []pdid.Id{leaving, other}
	slices.SortFunc(set, byId)
	sharedMine, sharedTheirs := row(leaving, other), row(other, leaving)
	_, err = put(ctx, a, Chunk{Namespace: SharedNamespace, Kind: "person", Month: month, Rows: 2, Tenants: set,
		First: sharedMine.created, Last: sharedTheirs.created}, [][]byte{sharedMine.doc(), sharedTheirs.doc()})
	x.NoError(err)

	legacyMine, legacyShared, legacyTheirs := row(leaving, leaving), row(leaving, other), row(other, other)
	_, err = put(ctx, a, Chunk{Namespace: LegacyNamespace, Kind: "person", Month: month},
		[][]byte{legacyMine.doc(), legacyShared.doc(), legacyTheirs.doc()})
	x.NoError(err)

	p := Policy{Archive: a, Tenants: answers(map[pdid.Id]Tenant{held: {Hold: onHold()}})}

	plan, err := p.PlanTenantPurge(ctx, s, leaving)
	x.NoError(err)

	got, err := p.PurgeTenant(ctx, s, leaving)
	x.NoError(err)

	// Plan and act agree, except on whose holds: those are counted as found.
	plan.Held.By, got.Held.By = nil, nil
	x.Equal(plan, got, "the plan is not what the purge did")

	x.Equal(TenantPurge{
		Removed: 1, Blanked: 1,
		Chunks: 1, Rows: 2,
		Rewritten: 2, Edited: 2,
		Held: Held{Rows: 1},
	}, got)

	x.False(s.has(alone.id), "a row that was its alone outlived it")

	r, ok := s.get(sharing.id)
	x.True(ok, "a row it shares went")
	x.Empty(r.value, "a row it shares kept what it said")

	r, _ = s.get(sharingHeld.id)
	x.NotEmpty(r.value, "a row it shares with a held tenant was blanked")

	r, _ = s.get(theirs.id)
	x.NotEmpty(r.value, "a row filed under somebody else was touched")

	in := docsIn(t, a)
	x.NotContains(in, own.id, "its namespace outlived it")
	x.Contains(in, sharedMine.id)
	x.Empty(in[sharedMine.id]["value"])
	x.NotEmpty(in[sharedTheirs.id]["value"], "the other tenant's side of a shared chunk was blanked")
	x.NotContains(in, legacyMine.id, "its row in a file of everybody's outlived it")
	x.Empty(in[legacyShared.id]["value"])
	x.NotEmpty(in[legacyTheirs.id]["value"], "somebody else's row in a file of everybody's was blanked")

	acts := map[string]int{}
	for _, v := range receiptsIn(t, a) {
		if v.Act == "purge-tenant" && v.Tenant == leaving.String() {
			acts[v.Where] += v.Rows
		}
	}
	x.Equal(map[string]int{"database": 1, "archive": 2}, acts)
}

// TestATenantUnderAHoldIsNotPurged.
func TestATenantUnderAHoldIsNotPurged(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	held := newTenant()

	s := &fakeStore{}
	r := s.add(fakeRow{tenant: held.Uuid(), domain: person, created: ago(day)})

	p := Policy{Archive: flob.NewMemStores(), Tenants: answers(map[pdid.Id]Tenant{held: {Hold: onHold()}})}

	_, err := p.PurgeTenant(ctx, s, held)
	x.ErrorIs(err, ErrHeld)
	x.True(s.has(r.id))

	t.Run("and neither is one with no answer", func(t *testing.T) {
		x := require.New(t)

		p := Policy{Tenants: func(context.Context, pdid.Id) (Tenant, error) { return Tenant{}, errors.New("blinked") }}

		_, err := p.PurgeTenant(ctx, s, held)
		x.ErrorContains(err, "whether a hold is on it is not known")
		x.True(s.has(r.id))
	})
}

// TestAPreviewKnowsAboutTheHold.
func TestAPreviewKnowsAboutTheHold(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()

	s := &fakeStore{}
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(60 * day)})

	discard := Keep{Retain: 30 * day, Discard: true}
	p := Policy{Archive: flob.NewMemStores(), Keep: discard}

	got, err := p.Preview(ctx, s, tenant, Tenant{Keep: &discard})
	x.NoError(err)
	x.Equal(map[string]Removal{"person": {Discarded: 1}}, got)

	got, err = p.Preview(ctx, s, tenant, Tenant{Keep: &discard, Hold: onHold()})
	x.NoError(err)
	x.Equal(map[string]Removal{"person": {Archived: 1}}, got, "a held row would be discarded")
}

// reclaiming is an archive that says when it is asked to give bytes back.
type reclaiming struct {
	*flob.MemStores
	asked []time.Duration
}

func (r *reclaiming) Reclaim(ctx context.Context, grace time.Duration) (int, error) {
	r.asked = append(r.asked, grace)
	return 0, nil
}

// TestAPassGivesBackWhatTheArchiveNoLongerHolds.
//
// Destroying a chunk erases its reference, and on S3 nothing else would ever
// take the bytes: a pass that destroyed a tenant's history and stopped there
// would leave it in the bucket for as long as the bucket lasts.
func TestAPassGivesBackWhatTheArchiveNoLongerHolds(t *testing.T) {
	x := require.New(t)

	a := &reclaiming{MemStores: flob.NewMemStores()}
	p := Policy{Archive: a, Keep: Keep{Retain: 30 * day, Destroy: year}}
	p.Pass(t.Context(), &fakeStore{})

	x.Equal([]time.Duration{Reclaimed}, a.asked)
}
