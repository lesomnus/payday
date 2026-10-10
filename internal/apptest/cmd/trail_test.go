package cmd_test

import (
	"context"
	"encoding/base64"
	"slices"
	"testing"
	"time"

	"uuid"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/internal/ent"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// TestTheTrailIsKeptPerKindOfThing.
//
// The first shape of this was one clock over the whole table, and it is wrong
// in a way that only shows up in an app with more than one kind of entity in
// it. A deployment's obligations are not uniform: what was done to a **person**
// is under a privacy regime and eventually has to stop existing, and what a
// **machine** did is an operating record whose requirement is the opposite one.
// One clock forces the shorter of the two onto everything, and there is no
// global answer that is honest for both.
//
// So the policy names kinds. The kind is `Audit.domain`, which is a column for
// exactly this reason: the domain byte inside `object_id` carries the same fact
// and no database can index into it, so *what kind was this row about* was
// answerable and *which rows were about robots* was not.
func TestTheTrailIsKeptPerKindOfThing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Two kinds of write, which is what makes this a test about kinds.
	_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "a-machine",
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "a-person",
	}.Build())
	x.NoError(err)

	robot, ok := pdid.DomainOf("robot")
	x.True(ok, "the schema registered no `robot` domain, so this proves nothing")

	kinds := func(t *testing.T) map[pdid.Domain]int {
		t.Helper()

		vs, err := b.Ent.Audit.Query().All(ctx)
		require.NoError(t, err)

		out := map[pdid.Domain]int{}
		for _, v := range vs {
			out[pdid.Domain(v.Domain)]++
		}

		return out
	}

	was := kinds(t)
	x.NotZero(was[robot], "no write about a robot was recorded")
	x.Greater(len(was), 1, "every row is the same kind, so this proves nothing")

	// The rule, resolved the way a deployment writes it: everything goes, and
	// the machine's record stays.
	p, err := config.AuditConfig{
		Discard: true,
		Retain:  time.Nanosecond,
		By: map[string]config.AuditKeepConfig{
			"robot": {Profile: "forever"},
		},
	}.Policy()
	x.NoError(err)

	s := pd.TrailStore(b.Ent)

	// One pass of each half, which is what `trail.Sweep` does on its clock.
	n, err := trail.Collect(ctx, s, trail.Except(robot), time.Now().Add(time.Hour))
	x.NoError(err)
	x.NotZero(n)

	x.Equal(trail.Keep{Note: p.For(robot).Note}, p.For(robot),
		"the robot's policy has a clock on it")

	left := kinds(t)
	x.Equal(was[robot], left[robot], "the record of what a machine did was swept")
	x.Len(left, 1, "something other than the robot's record survived the window")
}

// TestAKindTheAppDoesNotHaveIsRefused.
//
// A deployment that meant `robot` and wrote `robots` has configured a retention
// policy for nothing at all, and the rows it thought it was protecting fall to
// the default -- which is the failure this whole package exists to make loud.
// Refused where the process comes up, like every other refusal in `config`.
func TestAKindTheAppDoesNotHaveIsRefused(t *testing.T) {
	x := require.New(t)

	_, err := config.AuditConfig{
		Archive: "/tmp/nowhere",
		Retain:  time.Hour,
		By:      map[string]config.AuditKeepConfig{"robots": {Profile: "forever"}},
	}.Policy()
	x.ErrorContains(err, "not a kind this app has")

	t.Run("and so is a profile it does not have", func(t *testing.T) {
		x := require.New(t)

		_, err := config.AuditConfig{Profile: "pci-dss", Discard: true, Retain: time.Hour}.Policy()
		x.ErrorContains(err, "is not one this knows")
	})

	t.Run("and a window with nowhere to put what leaves it", func(t *testing.T) {
		x := require.New(t)

		_, err := config.AuditConfig{
			Retain: time.Hour,
			By:     map[string]config.AuditKeepConfig{"robot": {Retain: time.Hour}},
		}.Policy()
		x.ErrorContains(err, "nowhere to put")
	})

	t.Run("and a window below the deployment's own floor", func(t *testing.T) {
		x := require.New(t)

		_, err := config.AuditConfig{
			Archive: "/tmp/nowhere",
			Retain:  time.Hour,
			Destroy: 24 * time.Hour,
			Min:     config.AuditMinConfig{Profile: "pipa"},
		}.Policy()
		x.ErrorContains(err, "the floor is")

		_, err = config.AuditConfig{
			Archive: "/tmp/nowhere",
			Retain:  time.Hour,
			Min:     config.AuditMinConfig{Profile: "forever"},
		}.Policy()
		x.ErrorContains(err, "no floor")
	})

	t.Run("and a floor of one kind alone is a floor and no window", func(t *testing.T) {
		x := require.New(t)

		p, err := config.AuditConfig{
			Archive: "/tmp/nowhere",
			Retain:  time.Hour,
			By:      map[string]config.AuditKeepConfig{"robot": {Min: config.AuditMinConfig{Retain: time.Minute}}},
		}.Policy()
		x.NoError(err)

		robot, _ := pdid.DomainOf("robot")
		x.Equal(p.Keep, p.For(robot), "a floor became the kind's window")
		x.Equal(time.Minute, p.MinBy[robot].Retain)
	})
}

// TestTheArchiveIsSplitByKindSoThatTheSecondClockCanBe.
//
// The second clock is per kind as well, and [trail.Purge] decides from the
// labels of a chunk rather than by opening it -- so a chunk holding two kinds
// with two `destroy` windows would be a chunk that is half destroyable, and
// there is no such thing. The split is what makes the question answerable at
// all.
func TestTheArchiveIsSplitByKindSoThatTheSecondClockCanBe(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "a-machine",
	}.Build())
	x.NoError(err)

	robot, ok := pdid.DomainOf("robot")
	x.True(ok)

	a := flob.NewOsStores(t.TempDir())
	s := pd.TrailStore(b.Ent)

	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)

	n, err := trail.Archive(ctx, s, trail.Kinds{}, time.Now().Add(time.Hour), a)
	x.NoError(err)
	x.Equal(was, n)

	cs, err := trail.Chunks(ctx, a)
	x.NoError(err)
	x.Greater(len(cs), 1, "every kind went into one chunk, so the second clock has nothing to read")

	// Read back as messages, which is the generated half of this: the runtime
	// has no `Audit` type and hands over documents.
	seen := 0
	x.NoError(trail.Read(ctx, a, pd.TrailOf(func(v *app.Audit) error {
		x.NotEmpty(v.GetAction())
		seen++

		return nil
	})))
	x.Equal(n, seen)

	t.Run("and one kind is destroyed while another is not", func(t *testing.T) {
		x := require.New(t)

		// Long past everything, for the robot alone.
		vs, err := trail.Purge(ctx, a, trail.Only(robot).CutFor(time.Now().AddDate(1, 0, 0)))
		x.NoError(err)
		x.NotEmpty(vs, "the robot's archive was not destroyed")

		left, err := trail.Chunks(ctx, a)
		x.NoError(err)
		x.NotEmpty(left, "everything was destroyed, not only the kind that was named")

		for _, v := range left {
			x.NotEqual("robot", v.Kind)
		}

		rs := 0
		x.NoError(trail.Receipts(ctx, a, func(v trail.Receipt) error {
			x.Equal("purge", v.Act)
			x.Equal("robot", v.Kind)
			rs++

			return nil
		}))
		x.NotZero(rs, "the purge left no receipt")
	})
}

// TestOnePersonLeavesTheTrailWithoutTakingTheEventWithThem.
//
// The retention policy is about **age** and reaches everybody at once. A person
// asking to be forgotten is about a **subject**, and the two need different
// machinery: this is payday's half of the second one, which is two columns of
// its own table blanked for a set the app chose.
//
// What has to survive is the event. Who acted, what they did, which object and
// when are the record a trail exists to be, and are what a legal-obligation
// exemption is an exemption *for*.
func TestOnePersonLeavesTheTrailWithoutTakingTheEventWithThem(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "one",
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Robot().Patch(ctx, app.RobotPatchRequest_builder{
		Ref:              app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias:            z.Ptr("a-name-worth-forgetting"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	who := must(pdid.From(v.GetId()))

	rows := func() []*ent.Audit {
		vs, err := b.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(who.Uuid())).All(ctx)
		require.NoError(t, err)

		return vs
	}

	was := rows()
	x.Len(was, 2, "an Add and a Patch should both be on record")

	had := false
	for _, u := range was {
		if len(u.Value) > 0 || len(u.Patch) > 0 {
			had = true
		}
	}
	x.True(had, "the trail carried no contents, so this proves nothing")

	// Somebody else's row, which must not move.
	other, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "two",
	}.Build())
	x.NoError(err)

	n, err := pd.ForgetInTrail(ctx, b.Ent, []pdid.Id{who})
	x.NoError(err)
	x.Equal(len(was), n)

	for _, u := range rows() {
		x.Empty(u.Value, "the contents of a write about them survived")
		x.Empty(u.Patch, "the document of a write about them survived")

		// And the event did not.
		x.NotEmpty(u.Action, "the record of what happened went with the contents")
		x.Equal(who.Uuid(), u.ObjectId)
		x.False(u.DateCreated.IsZero())
	}

	vs, err := b.Ent.Audit.Query().
		Where(entaudit.ObjectIdEQ(must(pdid.From(other.GetId())).Uuid())).
		All(ctx)
	x.NoError(err)
	x.NotEmpty(vs)
	for _, u := range vs {
		x.NotEmpty(u.Value, "somebody else's record was blanked")
	}
}

// TestAnArchivedRowIsForgottenToo.
//
// A mechanism that stopped at the database would destroy the copy an operator
// can see and leave the copy in the archive beside it. That is not a gap in a
// retention policy; it is an answer that is wrong in the direction that matters.
//
// It is also the one act in this package that **edits** an archive -- `Purge`
// destroys whole chunks and a pass only adds them, precisely so that nothing
// does. Written again beside itself, and the old one erased only once the new
// one is held.
func TestAnArchivedRowIsForgottenToo(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "one",
	}.Build())
	x.NoError(err)

	who := must(pdid.From(v.GetId()))

	a := flob.NewMemStores()
	_, err = trail.Archive(ctx, pd.TrailStore(b.Ent), trail.Kinds{}, time.Now().Add(time.Hour), a)
	x.NoError(err)

	held := 0
	x.NoError(trail.Read(ctx, a, pd.TrailOf(func(u *app.Audit) error {
		if string(u.GetObjectId()) == string(who.Bytes()) && len(u.GetValue()) > 0 {
			held++
		}

		return nil
	})))
	x.NotZero(held, "the archive holds nothing about them, so this proves nothing")

	// The object as protojson writes it, which is what the runtime matches on:
	// it has no `Audit` type and reads the document as JSON.
	n, err := trail.Forget(ctx, a, []string{base64.StdEncoding.EncodeToString(who.Bytes())})
	x.NoError(err)
	x.NotZero(n)

	left := 0
	events := 0
	x.NoError(trail.Read(ctx, a, pd.TrailOf(func(u *app.Audit) error {
		if string(u.GetObjectId()) != string(who.Bytes()) {
			return nil
		}

		events++
		if len(u.GetValue()) > 0 || len(u.GetPatch()) > 0 {
			left++
		}
		x.NotEmpty(u.GetAction(), "the archived event went with the contents")

		return nil
	})))
	x.Zero(left, "an archived row still holds what it said about them")
	x.NotZero(events, "the archived events went as well as their contents")
}

// trailRow writes one row of the trail through ent, the way an app records
// what the servers cannot see -- which is the one way to have a row that names
// two tenants without a transfer to make it.
func trailRow(t *testing.T, ctx context.Context, db *ent.Client, tenant, actor pdid.Id, counterpart *pdid.Id, at time.Time) *ent.Audit {
	t.Helper()

	robot, _ := pdid.DomainOf("robot")

	q := db.Audit.Create().
		SetId(uuid.NewV7()).
		SetTenantId(tenant.Uuid()).
		SetActorTenantId(actor.Uuid()).
		SetActorId(uuid.Nil()).
		SetTraceId([]byte{}).
		SetAction("/test/Write").
		SetObjectId(pdid.New(robot).Uuid()).
		SetDomain(uint32(robot)).
		SetPatch([]byte{}).
		SetValue([]byte("contents")).
		SetDateCreated(at)
	if counterpart != nil {
		q = q.SetCounterpartTenantId(counterpart.Uuid())
	}

	return must(q.Save(ctx))
}

// TestTheStoreSaysWhoseARowIs.
//
// Through the generated store and a real database, because the three scopes
// are predicates over the three columns the wall reads, and a predicate is
// only as right as the SQL it becomes.
func TestTheStoreSaysWhoseARowIs(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other := must(pdid.From(must(b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())).GetId()))
	nobody := pdid.Nil
	at := time.Now().Add(-time.Minute)

	mine := trailRow(t, ctx, b.Ent, b.Tenant, b.Tenant, nil, at)
	byNobody := trailRow(t, ctx, b.Ent, b.Tenant, nobody, nil, at)
	byOther := trailRow(t, ctx, b.Ent, b.Tenant, other, nil, at)
	toOther := trailRow(t, ctx, b.Ent, other, other, &b.Tenant, at)
	theirs := trailRow(t, ctx, b.Ent, other, other, nil, at)

	s := pd.TrailStore(b.Ent)
	now := time.Now()

	keys := func(of trail.Scope) []uuid.UUID {
		t.Helper()

		vs, err := s.Older(ctx, of, now, trail.Cursor{}, trail.Batch)
		require.NoError(t, err)

		out := []uuid.UUID{}
		for _, v := range vs {
			k := v.Key.(uuid.UUID)
			for _, id := range []uuid.UUID{mine.Id, byNobody.Id, byOther.Id, toOther.Id, theirs.Id} {
				if k == id {
					out = append(out, k)
				}
			}
		}
		slices.SortFunc(out, uuid.UUID.Compare)

		return out
	}
	sorted := func(vs ...uuid.UUID) []uuid.UUID {
		slices.SortFunc(vs, uuid.UUID.Compare)
		return vs
	}

	x.Equal(sorted(mine.Id, byNobody.Id, byOther.Id), keys(trail.Scope{Whose: trail.FiledUnder, Tenant: b.Tenant}))
	x.Equal(sorted(mine.Id, byNobody.Id), keys(trail.Scope{Whose: trail.Alone, Tenant: b.Tenant}),
		"a row the deployment wrote into a tenant is not that tenant's alone")
	x.Equal(sorted(byOther.Id, toOther.Id), keys(trail.Scope{Whose: trail.Together, Tenant: b.Tenant}))
	x.Equal(sorted(byOther.Id, toOther.Id), keys(trail.Scope{Whose: trail.Together, Tenant: other}))

	n, err := s.Count(ctx, trail.Scope{Whose: trail.Alone, Tenant: other}, now)
	x.NoError(err)
	x.GreaterOrEqual(n, 1)

	t.Run("and every row names its tenants", func(t *testing.T) {
		x := require.New(t)

		// The one it is filed under first.
		want := map[uuid.UUID][]pdid.Id{
			byOther.Id: {b.Tenant, other},
			toOther.Id: {other, b.Tenant},
		}

		vs, err := s.Older(ctx, trail.Scope{Whose: trail.Together, Tenant: b.Tenant}, now, trail.Cursor{}, trail.Batch)
		x.NoError(err)
		x.Len(vs, 2)
		for _, v := range vs {
			x.Equal(want[v.Key.(uuid.UUID)], v.Tenants)
		}

		vs, err = s.Older(ctx, trail.Scope{Whose: trail.Alone, Tenant: b.Tenant}, now, trail.Cursor{}, trail.Batch)
		x.NoError(err)
		for _, v := range vs {
			x.Equal([]pdid.Id{b.Tenant}, v.Tenants, "nobody was counted as a tenant")
		}
	})

	t.Run("and the tenants rows are filed under, one seek each", func(t *testing.T) {
		x := require.New(t)

		vs, err := s.Tenants(ctx, pdid.Nil, trail.Batch)
		x.NoError(err)
		x.Contains(vs, b.Tenant)
		x.Contains(vs, other)
		x.True(slices.IsSortedFunc(vs, func(a, b pdid.Id) int { return uuid.UUID(a).Compare(uuid.UUID(b)) }))

		first, err := s.Tenants(ctx, pdid.Nil, 1)
		x.NoError(err)
		x.Len(first, 1)

		rest, err := s.Tenants(ctx, first[0], trail.Batch)
		x.NoError(err)
		x.Equal(vs[1:], rest)
	})
}

// TestRowsWrittenAtOneInstantAreReadOnceEach.
//
// A pass leaves some rows where they are and carries on past them, by the
// instant and then the identifier. Two rows written in the same instant are the
// case that tells whether the second half of that works on the database at
// hand -- an equality on a timestamp is exactly where a driver's idea of one
// shows.
func TestRowsWrittenAtOneInstantAreReadOnceEach(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	at := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	want := []uuid.UUID{}
	for range 5 {
		want = append(want, trailRow(t, ctx, b.Ent, b.Tenant, b.Tenant, nil, at).Id)
	}
	slices.SortFunc(want, uuid.UUID.Compare)

	s := pd.TrailStore(b.Ent)
	of := trail.Scope{Kinds: trail.Only(must(trail.DomainOf("robot"))), Whose: trail.FiledUnder, Tenant: b.Tenant}

	got := []uuid.UUID{}
	c := trail.Cursor{}
	for range 20 {
		vs, err := s.Older(ctx, of, time.Now(), c, 1)
		x.NoError(err)
		if len(vs) == 0 {
			break
		}

		c = trail.Cursor{Created: vs[0].Created, Key: vs[0].Key}
		if k := vs[0].Key.(uuid.UUID); slices.Contains(want, k) {
			got = append(got, k)
		}
	}

	x.Equal(want, got, "a row was read twice, or not at all")
}

// TestEachTenantKeepsItsTrailForItsOwnWindow.
//
// The deployment says ninety days for everybody and one tenant's contract says
// otherwise. Through the generated store, the app's own callback, and an
// archive on the disk: the tenant on the short window loses its rows from the
// database and then from the archive, and the other tenant keeps both.
func TestEachTenantKeepsItsTrailForItsOwnWindow(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other := must(pdid.From(must(b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())).GetId()))
	for _, id := range []pdid.Id{b.Tenant, other} {
		_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: id.Bytes()}.Build(),
			Alias:  "a-machine",
		}.Build())
		x.NoError(err)
	}

	p, err := config.AuditConfig{
		Archive: t.TempDir(),
		Retain:  90 * 24 * time.Hour,
	}.Policy()
	x.NoError(err)

	// Gone from the database at once and from the archive straight after,
	// which is a contract nobody signs and the shortest way to see both clocks
	// run in one pass.
	short := trail.Keep{Retain: time.Nanosecond, Destroy: 2 * time.Nanosecond}
	asked := []pdid.Id{}
	p.Tenants = func(ctx context.Context, id pdid.Id) (trail.Tenant, error) {
		asked = append(asked, id)
		if id == other {
			return trail.Tenant{Keep: &short}, nil
		}

		return trail.Tenant{}, nil
	}
	x.NoError(p.Valid())

	count := func(id pdid.Id) int {
		n, err := b.Ent.Audit.Query().Where(entaudit.TenantIdEQ(id.Uuid())).Count(ctx)
		require.NoError(t, err)

		return n
	}

	mine, theirs := count(b.Tenant), count(other)
	x.NotZero(mine)
	x.NotZero(theirs)

	p.Pass(ctx, pd.TrailStore(b.Ent))

	x.Equal(mine, count(b.Tenant), "the tenant on the deployment's window lost rows")
	x.Zero(count(other), "the tenant on its own window kept its rows")
	x.Contains(asked, other)
	x.Contains(asked, b.Tenant)

	held := map[string]int{}
	cs, err := trail.Chunks(ctx, p.Archive)
	x.NoError(err)
	for _, c := range cs {
		held[c.Namespace] += c.Rows
	}
	x.Zero(held[other.String()], "the short window's archive outlived it")

	destroyed := 0
	x.NoError(trail.Receipts(ctx, p.Archive, func(v trail.Receipt) error {
		if v.Act == "destroy" && v.Tenant == other.String() {
			destroyed += v.Rows
		}

		return nil
	}))
	x.Equal(theirs, destroyed, "what was destroyed is not what the receipts say")

	t.Run("and a preview of the same answer for the other says what it would take", func(t *testing.T) {
		x := require.New(t)

		got, err := p.Preview(ctx, pd.TrailStore(b.Ent), b.Tenant, trail.Tenant{Keep: &short})
		x.NoError(err)

		n := 0
		for _, v := range got {
			x.Zero(v.Archived, "a row on a window of nanoseconds would be kept")
			n += v.Discarded
		}
		x.Equal(count(b.Tenant), n)
	})
}

// TestARowTwoTenantsMayReadLastsAsLongAsTheLongerKeepsIt.
func TestARowTwoTenantsMayReadLastsAsLongAsTheLongerKeepsIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other := must(pdid.From(must(b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())).GetId()))

	// Filed under this tenant, written by an operator of the other.
	both := trailRow(t, ctx, b.Ent, b.Tenant, other, nil, time.Now().Add(-time.Minute))

	gone := trail.Keep{Retain: time.Nanosecond, Discard: true}
	kept := trail.Keep{Retain: time.Hour}
	answers := map[pdid.Id]trail.Keep{b.Tenant: gone, other: kept}

	p := trail.Policy{
		Archive: flob.NewMemStores(),
		Tenants: func(ctx context.Context, id pdid.Id) (trail.Tenant, error) {
			k := answers[id]
			return trail.Tenant{Keep: &k}, nil
		},
	}
	x.NoError(p.Valid())

	s := pd.TrailStore(b.Ent)
	exists := func() bool {
		n, err := b.Ent.Audit.Query().Where(entaudit.IdEQ(both.Id)).Count(ctx)
		require.NoError(t, err)

		return n > 0
	}

	p.Pass(ctx, s)
	x.True(exists(), "a row the other tenant keeps for an hour was discarded on this one's window")

	answers[other] = trail.Keep{Retain: time.Nanosecond}
	p.Pass(ctx, s)
	x.False(exists())

	cs, err := trail.Chunks(ctx, p.Archive)
	x.NoError(err)

	shared := []trail.Chunk{}
	for _, c := range cs {
		if c.Namespace == trail.SharedNamespace {
			shared = append(shared, c)
		}
	}
	x.Len(shared, 1, "a row two tenants may read was kept as one of theirs")
	x.ElementsMatch([]pdid.Id{b.Tenant, other}, shared[0].Tenants)

	t.Run("and both of them read it back", func(t *testing.T) {
		x := require.New(t)

		for _, id := range []pdid.Id{b.Tenant, other} {
			found := false
			x.NoError(trail.ReadTenant(ctx, p.Archive, id, pd.TrailOf(func(v *app.Audit) error {
				if string(v.GetId()) == string(both.Id[:]) {
					found = true
				}

				return nil
			})))
			x.True(found, "a tenant the row names cannot read it back")
		}
	})
}
