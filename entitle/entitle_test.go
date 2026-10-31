package entitle_test

import (
	"context"
	"testing"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/entitle"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

func TestAValueSaysWhatItEntitlesTo(t *testing.T) {
	x := require.New(t)

	for _, v := range []struct {
		v       entitle.Value
		has     bool
		allows  []int64
		refuses []int64
		says    string
	}{
		{entitle.None, false, []int64{0, -1}, []int64{1}, "none"},
		{entitle.Off, false, []int64{0}, []int64{1}, "off"},
		// A switch is not an amount: it is read with Has.
		{entitle.On, true, []int64{0}, []int64{1}, "on"},
		{entitle.Limited(0), false, []int64{0}, []int64{1}, "0"},
		{entitle.Limited(3), true, []int64{0, 3}, []int64{4}, "3"},
		{entitle.Limited(-2), false, []int64{0}, []int64{1}, "0"},
		{entitle.Unlimited, true, []int64{0, 1 << 62}, nil, "unlimited"},
	} {
		x.Equal(v.has, v.v.Has(), v.says)
		x.Equal(v.says, v.v.String())
		for _, n := range v.allows {
			x.True(v.v.Allows(n), "%s allows %d", v.says, n)
		}
		for _, n := range v.refuses {
			x.False(v.v.Allows(n), "%s refuses %d", v.says, n)
		}
	}

	// Unlimited is a value, and none is the absence of one.
	x.True(entitle.None.IsNone())
	x.False(entitle.Unlimited.IsNone())
	x.NotEqual(entitle.None, entitle.Unlimited)

	// What a row holds reads back as what was written.
	for _, v := range []entitle.Value{entitle.Off, entitle.On, entitle.Limited(7), entitle.Unlimited, entitle.None} {
		n, _ := v.Number()
		x.Equal(v, entitle.ValueOf(v.Kind(), n))
	}
	x.Equal(entitle.None, entitle.ValueOf(entitle.Kind(99), 3))
}

func TestTheGrantThatBeganLastDecides(t *testing.T) {
	x := require.New(t)

	now := time.Now()
	day := 24 * time.Hour
	row := func(grant string, v entitle.Value, from, until time.Time) entitle.Row {
		return entitle.Row{Key: "acme.robots", Value: v, From: from, Until: until, Grant: []byte(grant), Version: 1}
	}

	// This period's, in its grace, and the next one's, begun: the next decides
	// -- a downgrade too, which the more generous one would put off.
	got := entitle.Decide("acme.robots", []entitle.Row{
		row("this", entitle.Limited(500), now.Add(-30*day), now.Add(7*day)),
		row("next", entitle.Limited(100), now.Add(-day), now.Add(30*day)),
	}, now)
	x.Equal(entitle.Limited(100), got.Value)
	x.Equal("next", string(got.Grant.Grant))

	// One that has not begun, one that has ended, and one of another key say
	// nothing.
	got = entitle.Decide("acme.robots", []entitle.Row{
		row("later", entitle.Unlimited, now.Add(day), now.Add(30*day)),
		row("over", entitle.Unlimited, now.Add(-30*day), now.Add(-day)),
		{Key: "acme.export", Value: entitle.On, From: now.Add(-day), Until: now.Add(day), Grant: []byte("x")},
	}, now)
	x.Equal(entitle.None, got.Value)
	x.Nil(got.Grant)
	x.Equal("acme.robots", got.Key)

	// The window is from, inclusive, to until, exclusive.
	r := row("edge", entitle.On, now, now.Add(time.Second))
	x.True(r.In(now))
	x.False(r.In(now.Add(time.Second)))
	x.True(r.Ended(now.Add(time.Second)))
	x.False(r.Ended(now.Add(-time.Hour)), "one that has not begun has not ended")

	// Two that began at once: the greater identifier, so that every reader
	// says the same.
	got = entitle.Decide("acme.robots", []entitle.Row{
		row("b", entitle.Limited(2), now.Add(-day), now.Add(day)),
		row("a", entitle.Limited(1), now.Add(-day), now.Add(day)),
	}, now)
	x.Equal("b", string(got.Grant.Grant))
}

func TestAKeyIsTheShapeAMetersIs(t *testing.T) {
	x := require.New(t)

	for _, v := range []string{"acme.export", "acme.robots.running", "a1.b_2"} {
		x.NoError(entitle.CheckKey(v), v)
	}
	for _, v := range []string{"", "acme", "Acme.export", "acme..export", "acme.export.", "1acme.export", "acme.ex-port", string(make([]byte, 256))} {
		x.Error(entitle.CheckKey(v), "%q", v)
	}
}

// reader is a [entitle.Reader] over rows held in memory, whose driver says
// whether it is a transaction.
type reader struct {
	rows   []entitle.Row
	tx     bool
	locked []string
}

func (r *reader) Driver() dialect.Driver { return driver{tx: r.tx}.or() }

func (r *reader) Rows(_ context.Context, tenant pdid.Id, key string, lock bool) ([]entitle.Row, error) {
	if lock {
		r.locked = append(r.locked, key)
	}

	vs := []entitle.Row{}
	for _, v := range r.rows {
		if v.Tenant == tenant && (key == "" || v.Key == key) {
			vs = append(vs, v)
		}
	}

	return vs, nil
}

func TestReadsAnswerForTheFramesTenant(t *testing.T) {
	x := require.New(t)

	now := time.Now()
	mine, theirs := pdid.New(1), pdid.New(1)
	in := func(tenant pdid.Id, key string, v entitle.Value) entitle.Row {
		return entitle.Row{Tenant: tenant, Key: key, Value: v, From: now.Add(-time.Hour), Until: now.Add(time.Hour), Grant: []byte(key + tenant.String()), Version: 1}
	}
	r := &reader{tx: true, rows: []entitle.Row{
		in(mine, "acme.export", entitle.On),
		in(mine, "acme.robots", entitle.Limited(2)),
		in(theirs, "acme.seats", entitle.Unlimited),
	}}
	ctx := frame.Into(t.Context(), frame.New(pdid.New(2), mine, frame.Whole()))

	has, err := entitle.Has(ctx, r, "acme.export")
	x.NoError(err)
	x.True(has)

	// Not granted is not entitled, and never an error -- another tenant's
	// grant included.
	has, err = entitle.Has(ctx, r, "acme.seats")
	x.NoError(err)
	x.False(has)

	limit, err := entitle.Limit(ctx, r, "acme.robots")
	x.NoError(err)
	x.Equal(entitle.Limited(2), limit)
	x.Equal([]string{"acme.robots"}, r.locked, "a limit is read locked, and a switch is not")

	limit, err = entitle.Limit(ctx, r, "acme.seats")
	x.NoError(err)
	x.Equal(entitle.None, limit)
	x.False(limit.Allows(1))

	// A switch read as a limit is the source and the app disagreeing.
	_, err = entitle.Limit(ctx, r, "acme.export")
	x.ErrorContains(err, "switch")

	// A key that is no key is a typo, said rather than read as none.
	_, err = entitle.Has(ctx, r, "acme.Export")
	x.Error(err)

	got, err := entitle.Get(ctx, r, "acme.robots")
	x.NoError(err)
	x.Equal(entitle.Limited(2), got.Value)

	all, err := entitle.All(ctx, r)
	x.NoError(err)
	x.Len(all, 2)
	x.Equal("acme.export", all[0].Key)

	// A limit outside a transaction is the race the copy is there to close.
	_, err = entitle.Limit(ctx, &reader{rows: r.rows}, "acme.robots")
	x.ErrorIs(err, entitle.ErrNotInTx)

	// And whose entitlement it is, is the frame's to say.
	for _, ctx := range []context.Context{
		t.Context(),
		frame.Into(t.Context(), frame.New(pdid.New(2), pdid.Nil, frame.Whole())),
	} {
		_, err = entitle.Has(ctx, r, "acme.export")
		x.ErrorIs(err, entitle.ErrNoTenant)
		_, err = entitle.Limit(ctx, r, "acme.robots")
		x.ErrorIs(err, entitle.ErrNoTenant)
		_, err = entitle.All(ctx, r)
		x.ErrorIs(err, entitle.ErrNoTenant)
	}
}
