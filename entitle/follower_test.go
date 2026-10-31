package entitle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/entitle"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// TestAGrantIsAppliedByItsVersion: a grant the copy does not have is written,
// a newer version of one it has is written over it, and one at its version or
// below changes nothing -- so the order grants are heard in does not matter.
func TestAGrantIsAppliedByItsVersion(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	acme := pdid.New(1)
	src, c := newSource(), newCopy(acme)
	f := entitle.Follower{Source: served(t, src), Copy: c}

	src.hold(grant("g", acme, "acme.robots", 2, entitle.Limited(5)))
	got, err := f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(1, got.Applied)
	x.Equal(entitle.Limited(5), c.of(acme)["g"].Value)
	x.Equal(uint64(2), c.of(acme)["g"].Version)

	// An older version, heard late: nothing.
	src.hold(grant("g", acme, "acme.robots", 1, entitle.Unlimited))
	got, err = f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(0, got.Applied)
	x.Equal(1, got.Heard)
	x.Equal(entitle.Limited(5), c.of(acme)["g"].Value, "an older version was written over a newer")

	// The same version again: nothing either.
	src.hold(grant("g", acme, "acme.robots", 2, entitle.Unlimited))
	got, err = f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(0, got.Applied)
	x.Equal(entitle.Limited(5), c.of(acme)["g"].Value)

	// A newer one is written over it, and the row is the same row.
	id := c.of(acme)["g"].Id
	src.hold(grant("g", acme, "acme.robots", 3, entitle.Unlimited))
	got, err = f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(1, got.Applied)
	x.Equal(entitle.Unlimited, c.of(acme)["g"].Value)
	x.Equal(id, c.of(acme)["g"].Id)

	// Each write is on the trail, under the grant's tenant.
	acts := c.acts()
	x.Len(acts, 2)
	for _, v := range acts {
		x.Equal(entitle.Applied, v.action)
		x.Equal(acme, v.tenant)
	}
}

// TestACopyTakesAwayOnlyWhatHasEnded: a grant the source lists no more is
// taken out once it has ended, and kept until then -- a source that means to
// take one away ends it, and a list that left one out by mistake takes nothing.
func TestACopyTakesAwayOnlyWhatHasEnded(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	acme := pdid.New(1)
	src, c := newSource(), newCopy(acme)
	f := entitle.Follower{Source: served(t, src), Copy: c}

	now := time.Now()
	src.hold(
		grant("ended", acme, "acme.export", 1, entitle.On, now.Add(-48*time.Hour), now.Add(-time.Hour)),
		grant("in", acme, "acme.robots", 1, entitle.Limited(3)),
		grant("stays", acme, "acme.seats", 1, entitle.Unlimited),
	)
	_, err := f.Reconcile(ctx)
	x.NoError(err)
	x.Len(c.of(acme), 3)

	// The source stops listing the two of them.
	src.mu.Lock()
	delete(src.grants, "ended")
	delete(src.grants, "in")
	src.mu.Unlock()

	got, err := f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(1, got.Forgotten)
	x.Equal(1, got.Kept)
	x.Contains(c.of(acme), "in", "a grant still in force was taken away on a guess")
	x.Contains(c.of(acme), "stays")
	x.NotContains(c.of(acme), "ended")
	x.Equal(entitle.Forgot, c.acts()[len(c.acts())-1].action)

	// Ended as a source ends one -- sent again, ending now -- it goes once the
	// source stops listing it.
	src.hold(grant("in", acme, "acme.robots", 2, entitle.Limited(3), now.Add(-24*time.Hour), now.Add(-time.Second)))
	_, err = f.Reconcile(ctx)
	x.NoError(err)
	x.True(c.of(acme)["in"].Ended(time.Now()))
	src.mu.Lock()
	delete(src.grants, "in")
	src.mu.Unlock()
	_, err = f.Reconcile(ctx)
	x.NoError(err)
	x.NotContains(c.of(acme), "in")
}

// TestAListTheSourceDidNotFinishTakesNothing: the whole of the list is read
// before the copy is touched, so a source that falls over halfway is not a
// source that took away the half it did not send.
func TestAListTheSourceDidNotFinishTakesNothing(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	acme := pdid.New(1)
	src, c := newSource(), newCopy(acme)
	f := entitle.Follower{Source: served(t, src), Copy: c}

	now := time.Now()
	ended := func(id string) *pdpb.EntitlementGrant {
		return grant(id, acme, "acme.export", 1, entitle.On, now.Add(-48*time.Hour), now.Add(-time.Hour))
	}
	src.hold(ended("a"), ended("b"), ended("c"), ended("d"))
	_, err := f.Reconcile(ctx)
	x.NoError(err)
	x.Len(c.of(acme), 4)

	// Two of them are listed no more, and the second page fails.
	src.mu.Lock()
	delete(src.grants, "a")
	delete(src.grants, "b")
	src.failList = "1"
	src.page = 1
	src.mu.Unlock()

	_, err = f.Reconcile(ctx)
	x.Error(err)
	x.Len(c.of(acme), 4, "a list that did not end took something out")

	// And a source whose list never ends is one whose list is not read.
	stuck := &stuckSource{}
	_, err = entitle.Follower{Source: served(t, stuck), Copy: c}.Reconcile(ctx)
	x.ErrorContains(err, "does not end")
	x.Len(c.of(acme), 4)
}

// stuckSource answers every page with the cursor it was asked for.
type stuckSource struct {
	pdpb.UnimplementedGrantServiceServer
}

func (stuckSource) List(context.Context, *pdpb.GrantListRequest) (*pdpb.GrantListResponse, error) {
	return pdpb.GrantListResponse_builder{Next: "again"}.Build(), nil
}

// TestWhatIsNoGrantIsLeftOut: a grant with no end, no value, no version, a key
// that is not one or a tenant that is not one is said in the log and not
// applied, and stops nothing -- the rest are applied.
func TestWhatIsNoGrantIsLeftOut(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	acme := pdid.New(1)
	src, c := newSource(), newCopy(acme)
	f := entitle.Follower{Source: served(t, src), Copy: c}

	noEnd := grant("no-end", acme, "acme.robots", 1, entitle.Limited(1))
	noEnd.ClearValidUntil()
	noValue := grant("no-value", acme, "acme.robots", 1, entitle.Limited(1))
	noValue.ClearValue()
	negative := grant("negative", acme, "acme.robots", 1, entitle.Limited(1))
	negative.SetLimit(-1)
	noTenant := grant("no-tenant", acme, "acme.robots", 1, entitle.Limited(1))
	noTenant.SetTenantId([]byte("acme"))
	src.hold(
		noEnd, noValue, negative, noTenant,
		grant("no-version", acme, "acme.robots", 0, entitle.Limited(1)),
		grant("bad-key", acme, "Acme.Robots", 1, entitle.Limited(1)),
		grant(string(make([]byte, 65)), acme, "acme.robots", 1, entitle.Limited(1)),
		grant("fine", acme, "acme.robots", 1, entitle.Limited(1)),
	)

	got, err := f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(7, got.Refused)
	x.Equal(1, got.Applied)
	x.Equal([]string{"fine"}, keys(c.of(acme)))
}

// TestAGrantOfATenantTheAppDoesNotHaveIsAppliedToNothing: a tenant the source
// knows of and the app does not -- not made yet, or destroyed -- has no copy,
// so a tenant destroyed is not given one back by the next grant said of it.
func TestAGrantOfATenantTheAppDoesNotHaveIsAppliedToNothing(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	acme, gone := pdid.New(1), pdid.New(1)
	src, c := newSource(), newCopy(acme)
	f := entitle.Follower{Source: served(t, src), Copy: c}

	src.hold(grant("theirs", gone, "acme.robots", 1, entitle.Limited(1)), grant("mine", acme, "acme.robots", 1, entitle.Limited(1)))
	got, err := f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(1, got.Unknown)
	x.Equal(1, got.Applied)
	x.Empty(c.of(gone))
}

// TestAGrantMovedToAnotherTenantIsRefused: a grant's tenant and key are its
// own, and a source that says otherwise is not believed -- nor is the row it
// had taken away, which lapses when it ends.
func TestAGrantMovedToAnotherTenantIsRefused(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	acme, other := pdid.New(1), pdid.New(1)
	src, c := newSource(), newCopy(acme, other)
	f := entitle.Follower{Source: served(t, src), Copy: c}

	src.hold(grant("g", acme, "acme.robots", 1, entitle.Limited(1)))
	_, err := f.Reconcile(ctx)
	x.NoError(err)

	src.hold(grant("g", other, "acme.robots", 2, entitle.Limited(9)))
	got, err := f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(1, got.Refused)
	x.Equal(entitle.Limited(1), c.of(acme)["g"].Value)
	x.Empty(c.of(other))

	// And to another key.
	src.hold(grant("g", acme, "acme.seats", 3, entitle.Limited(9)))
	got, err = f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(1, got.Refused)
	x.Equal("acme.robots", c.of(acme)["g"].Key)
}

// TestSyncComparesOneTenant: what an app calls once it has made a tenant,
// rather than waiting for the next comparison; it touches no other tenant's.
func TestSyncComparesOneTenant(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	acme, other := pdid.New(1), pdid.New(1)
	src, c := newSource(), newCopy(acme, other)
	f := entitle.Follower{Source: served(t, src), Copy: c}

	now := time.Now()
	over := []time.Time{now.Add(-48 * time.Hour), now.Add(-time.Hour)}
	src.hold(
		grant("a", acme, "acme.export", 1, entitle.On, over...),
		grant("o", other, "acme.export", 1, entitle.On, over...),
	)
	_, err := f.Reconcile(ctx)
	x.NoError(err)
	src.mu.Lock()
	src.grants = map[string]*pdpb.EntitlementGrant{}
	src.mu.Unlock()
	src.hold(grant("new", acme, "acme.robots", 1, entitle.Limited(2)))

	got, err := f.Sync(ctx, acme)
	x.NoError(err)
	x.Equal(1, got.Applied)
	x.Equal(1, got.Forgotten)
	x.Equal([]string{"new"}, keys(c.of(acme)))
	x.Contains(c.of(other), "o", "a sync of one tenant forgot another's")

	_, err = f.Sync(ctx, pdid.Nil)
	x.Error(err)
}

// TestATransactionThatFailsIsTriedOnceMore: two writers of a grant new to the
// copy can each find no row, and the second is refused by the index the
// first filled; tried again, it finds the row.
func TestATransactionThatFailsIsTriedOnceMore(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	acme := pdid.New(1)
	src, c := newSource(), newCopy(acme)
	f := entitle.Follower{Source: served(t, src), Copy: c}

	src.hold(grant("g", acme, "acme.robots", 1, entitle.Limited(1)))
	c.fail = errors.New("unique: grant")
	got, err := f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(1, got.Applied)
}

// TestTheFollowerHearsWhatChangesAndComparesOnceReady: a grant the source held
// before the follower began is applied by the comparison that follows the
// stream's ready, a grant changed while it is open by the stream, and a
// stream that ends is opened again, beginning with another comparison.
func TestTheFollowerHearsWhatChangesAndComparesOnceReady(t *testing.T) {
	x := require.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	acme := pdid.New(1)
	src, c := newSource(), newCopy(acme)
	src.hold(grant("before", acme, "acme.export", 1, entitle.On))

	done := make(chan error, 1)
	go func() {
		done <- entitle.Follower{Source: served(t, src), Copy: c, Every: time.Hour}.Spin(ctx)
	}()

	<-src.watched
	x.Eventually(func() bool { _, ok := c.of(acme)["before"]; return ok }, 5*time.Second, 10*time.Millisecond)

	src.say(grant("heard", acme, "acme.robots", 1, entitle.Limited(4)))
	x.Eventually(func() bool { return c.of(acme)["heard"].Value == entitle.Limited(4) }, 5*time.Second, 10*time.Millisecond)

	// A grant changed while no stream is open is missed by the stream, and
	// the comparison the next one begins with puts it right.
	src.drop()
	src.hold(grant("heard", acme, "acme.robots", 2, entitle.Limited(9)))
	<-src.watched
	x.Eventually(func() bool { return c.of(acme)["heard"].Value == entitle.Limited(9) }, 5*time.Second, 10*time.Millisecond)

	cancel()
	x.NoError(<-done)
}

// TestAStreamThatDoesNotBeginWithReadyIsNotFollowed: without ready, what the
// stream carries cannot be told from what it missed.
func TestAStreamThatDoesNotBeginWithReadyIsNotFollowed(t *testing.T) {
	x := require.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	acme := pdid.New(1)
	src, c := newSource(), newCopy(acme)
	src.noReady = true
	src.hold(grant("held", acme, "acme.export", 1, entitle.On))

	done := make(chan error, 1)
	go func() { done <- entitle.Follower{Source: served(t, src), Copy: c}.Spin(ctx) }()

	<-src.watched
	src.say(grant("said", acme, "acme.robots", 1, entitle.Limited(1)))
	time.Sleep(100 * time.Millisecond)
	x.Empty(c.of(acme))

	cancel()
	x.NoError(<-done)
}

// TestAFollowerWithoutItsPartsGivesUp: a copy nothing keeps answers what it
// last heard for as long as its grants last, and nothing else would say so.
func TestAFollowerWithoutItsPartsGivesUp(t *testing.T) {
	x := require.New(t)

	src := served(t, newSource())
	for _, f := range []entitle.Follower{
		{Copy: newCopy()},
		{Source: src},
		{Source: src, Copy: newCopy(), Every: -time.Second},
	} {
		x.Error(f.Spin(t.Context()))
		_, err := f.Reconcile(t.Context())
		x.Error(err)
	}
	x.Equal("payday.entitle.follow", entitle.Follower{}.SpinName())
}

func keys(m map[string]entitle.Row) []string {
	vs := []string{}
	for k := range m {
		vs = append(vs, k)
	}

	return vs
}
