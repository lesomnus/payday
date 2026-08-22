package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/payday/internal/apptest"
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
}

// TestTheArchiveIsSplitByKindSoThatTheSecondClockCanBe.
//
// The second clock is per kind as well, and [trail.Purge] decides from the
// name of a file rather than by opening it -- so a file holding two kinds with
// two `destroy` windows would be a file that is half destroyable, and there is
// no such thing. The split is what makes the question answerable at all.
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

	dir := t.TempDir()
	s := pd.TrailStore(b.Ent)

	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)

	n, err := trail.Archive(ctx, s, trail.Kinds{}, time.Now().Add(time.Hour), dir)
	x.NoError(err)
	x.Equal(was, n)

	files, err := trail.Files(dir)
	x.NoError(err)
	x.Greater(len(files), 1, "every kind went into one file, so the second clock has nothing to read")

	// Read back as messages, which is the generated half of this: the runtime
	// has no `Audit` type and hands over documents.
	seen := 0
	x.NoError(pd.ReadTrail(files, func(v *app.Audit) error {
		x.NotEmpty(v.GetAction())
		seen++

		return nil
	}))
	x.Equal(n, seen)

	t.Run("and one kind is destroyed while another is not", func(t *testing.T) {
		x := require.New(t)

		// Long past everything, for the robot alone.
		vs, err := trail.Purge(ctx, dir, trail.Only(robot).CutFor(time.Now().AddDate(1, 0, 0)))
		x.NoError(err)
		x.NotEmpty(vs, "the robot's archive was not destroyed")

		left, err := trail.Files(dir)
		x.NoError(err)
		x.NotEmpty(left, "everything was destroyed, not only the kind that was named")

		for _, v := range left {
			x.NotContains(v, ".robot.")
		}
	})
}
