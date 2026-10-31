package meter_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/meter"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
)

// quiet is a context whose log goes nowhere: a pass says what it did, and a
// test reads what it handed over instead.
func quiet(ctx context.Context) context.Context {
	return log.Into(ctx, slog.New(slog.DiscardHandler))
}

// kept is a sink that keeps what it takes, and refuses while `refuse` says so.
type kept struct {
	mu     sync.Mutex
	got    [][]meter.Reading
	tries  int
	refuse func(rs []meter.Reading) bool
}

func (k *kept) Send(_ context.Context, rs []meter.Reading) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.tries++
	if k.refuse != nil && k.refuse(rs) {
		return errors.New("not now")
	}
	k.got = append(k.got, slices.Clone(rs))

	return nil
}

func (k *kept) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()

	return k.tries
}

func (k *kept) taken() [][]meter.Reading {
	k.mu.Lock()
	defer k.mu.Unlock()

	return slices.Clone(k.got)
}

// tenants is a fixed list of tenants, paged as `pd.MeterTenants` pages them.
func tenants(vs ...pdid.Id) func(context.Context, pdid.Id, int) ([]pdid.Id, error) {
	return func(_ context.Context, after pdid.Id, limit int) ([]pdid.Id, error) {
		out := []pdid.Id{}
		for _, v := range vs {
			if len(out) == limit {
				break
			}
			if after.IsZero() || v.Uuid().String() > after.Uuid().String() {
				out = append(out, v)
			}
		}

		return out, nil
	}
}

// constant is a meter every tenant holds `v` of.
func constant(key string, v int64) meter.Meter {
	return meter.Meter{Key: key, Unit: "{thing}", Measure: func(context.Context, pdid.Id) (int64, error) { return v, nil }}
}

// sorted is `n` tenants in the order of their identifiers.
func sorted(n int) []pdid.Id {
	vs := make([]pdid.Id, n)
	for i := range vs {
		vs[i] = pdid.New(pdid.Domain(1))
	}
	slices.SortFunc(vs, func(a, b pdid.Id) int {
		switch x, y := a.Uuid().String(), b.Uuid().String(); {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	})

	return vs
}

func TestAKeyIsTheAppsNameAndWords(t *testing.T) {
	for _, v := range []string{"acme.robots", "acme.robots.running", "acme.robots_2.v1"} {
		require.NoError(t, meter.CheckKey(v), v)
	}
	for _, v := range []string{
		"", "robots", "Acme.robots", "acme..robots", "acme.robots.", ".acme.robots",
		"acme.2robots", "acme.robots-running", "acme robots", "acme.róbots",
	} {
		require.Error(t, meter.CheckKey(v), v)
	}
}

func TestAUnitIsWhatOpenTelemetryWritesOneIn(t *testing.T) {
	for _, v := range []string{"By", "{robot}", "d", "1", "GBy", "By/s"} {
		require.NoError(t, meter.CheckUnit(v), v)
	}
	for _, v := range []string{"", "a robot", "°C", "\t", string(make([]byte, 64))} {
		require.Error(t, meter.CheckUnit(v), v)
	}
}

// TestAReaderMissingWhatItIsMadeOfGivesUp: before it measures anything, and so
// at the start of the process, rather than with a bill short of a meter.
func TestAReaderMissingWhatItIsMadeOfGivesUp(t *testing.T) {
	ctx := quiet(t.Context())
	sink := &kept{}
	good := constant("acme.robots", 1)

	for name, r := range map[string]meter.Reader{
		"no sink":       {Meters: []meter.Meter{good}},
		"no meters":     {Sink: sink},
		"a bad key":     {Sink: sink, Meters: []meter.Meter{constant("robots", 1)}},
		"no unit":       {Sink: sink, Meters: []meter.Meter{{Key: "acme.robots", Measure: good.Measure}}},
		"no measure":    {Sink: sink, Meters: []meter.Meter{{Key: "acme.robots", Unit: "{robot}"}}},
		"one key twice": {Sink: sink, Meters: []meter.Meter{good, constant("acme.robots", 2)}},
		"a past every":  {Sink: sink, Meters: []meter.Meter{good}, Every: -time.Hour},
	} {
		require.Error(t, r.Read(ctx, pdid.New(1)), name)

		r.Tenants = tenants(pdid.New(1))
		require.Error(t, r.Spin(ctx), name)
	}

	// And a loop with nobody to measure.
	require.ErrorContains(t, meter.Reader{Sink: sink, Meters: []meter.Meter{good}}.Spin(ctx), "no Tenants")
	require.Empty(t, sink.taken())
}

// TestItIsCalledByTheNameALeaseHolds, since a lease is held by the name and a
// loop renamed is a loop run twice through an upgrade.
func TestItIsCalledByTheNameALeaseHolds(t *testing.T) {
	var s spin.Spinner = meter.Reader{}
	n, ok := s.(spin.Named)
	require.True(t, ok)
	require.Equal(t, "payday.meter.read", n.SpinName())
	require.Equal(t, meter.ReaderName, n.SpinName())
}

// TestReadIsOneTenantAtOneMoment: what an app calls before it destroys a
// tenant. Every meter of it, stamped with one moment -- UTC, to the
// microsecond -- and handed over at once.
func TestReadIsOneTenantAtOneMoment(t *testing.T) {
	x := require.New(t)
	ctx := quiet(t.Context())
	sink := &kept{}
	tenant := pdid.New(1)

	seen := []pdid.Id{}
	r := meter.Reader{
		Sink: sink,
		Meters: []meter.Meter{
			constant("acme.robots", 3),
			{Key: "acme.bytes", Unit: "By", Measure: func(_ context.Context, v pdid.Id) (int64, error) {
				seen = append(seen, v)
				return 1 << 40, nil
			}},
		},
	}
	before := time.Now()
	x.NoError(r.Read(ctx, tenant))

	got := sink.taken()
	x.Len(got, 1, "one tenant's readings are handed over together")
	x.Equal([]pdid.Id{tenant}, seen)

	rs := got[0]
	x.Len(rs, 2)
	x.Equal(meter.Reading{Tenant: tenant, Meter: "acme.robots", MeasuredAt: rs[0].MeasuredAt, Value: 3, Unit: "{thing}"}, rs[0])
	x.Equal(meter.Reading{Tenant: tenant, Meter: "acme.bytes", MeasuredAt: rs[0].MeasuredAt, Value: 1 << 40, Unit: "By"}, rs[1])

	at := rs[0].MeasuredAt
	x.Equal(time.UTC, at.Location())
	x.Equal(at, at.Truncate(time.Microsecond))
	x.False(at.Before(before.Truncate(time.Microsecond)))
	x.Equal(at.Round(0), at, "no monotonic reading, so that a reading compares by what it says")
}

// TestReadSaysWhatItCouldNotDo: a meter that failed and a sink that did not
// take the rest are both an error a destruction waits on, and the meters that
// were measured are handed over regardless.
func TestReadSaysWhatItCouldNotDo(t *testing.T) {
	x := require.New(t)
	ctx := quiet(t.Context())
	tenant := pdid.New(1)

	broken := meter.Meter{Key: "acme.broken", Unit: "1", Measure: func(context.Context, pdid.Id) (int64, error) {
		return 0, errors.New("the database blinked")
	}}

	sink := &kept{}
	err := meter.Reader{Sink: sink, Meters: []meter.Meter{constant("acme.robots", 3), broken}}.Read(ctx, tenant)
	x.ErrorContains(err, "acme.broken")
	x.ErrorContains(err, "the database blinked")
	x.Len(sink.taken(), 1)
	x.Len(sink.taken()[0], 1, "what was measured went")

	sink = &kept{refuse: func([]meter.Reading) bool { return true }}
	err = meter.Reader{Sink: sink, Meters: []meter.Meter{constant("acme.robots", 3)}}.Read(ctx, tenant)
	x.ErrorContains(err, "the sink did not take")
	x.ErrorContains(err, "not now")

	// And nothing at all to hand over is not handed over.
	sink = &kept{}
	err = meter.Reader{Sink: sink, Meters: []meter.Meter{broken}}.Read(ctx, tenant)
	x.Error(err)
	x.Zero(sink.count())
}

// TestAPassIsOnTheClock: one as the reader starts, and then at each boundary of
// `Every` since the Unix epoch -- not an interval after the last, which would
// drift, and not made up for when a pass ran past one.
func TestAPassIsOnTheClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		ctx, cancel := context.WithCancel(quiet(t.Context()))
		defer cancel()

		// The bubble's clock starts at midnight UTC, 1 January 2000. Ten
		// minutes in, so that the start is not on a boundary.
		time.Sleep(10 * time.Minute)
		start := time.Now()

		// A pass that takes twenty minutes, so that one finishing late is
		// seen not to move the next.
		sink := &kept{}
		slow := meter.Meter{Key: "acme.slow", Unit: "1", Measure: func(context.Context, pdid.Id) (int64, error) {
			time.Sleep(20 * time.Minute)
			return 1, nil
		}}
		r := meter.Reader{Tenants: tenants(pdid.New(1)), Meters: []meter.Meter{slow}, Sink: sink, Every: time.Hour}

		done := make(chan error)
		go func() { done <- r.Spin(ctx) }()

		time.Sleep(3*time.Hour + 15*time.Minute)
		synctest.Wait()

		ats := []time.Time{}
		for _, rs := range sink.taken() {
			ats = append(ats, rs[0].MeasuredAt)
		}
		x.Equal([]time.Time{
			start.UTC(),
			time.Date(2000, 1, 1, 1, 0, 0, 0, time.UTC),
			time.Date(2000, 1, 1, 2, 0, 0, 0, time.UTC),
			time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC),
		}, ats)

		cancel()
		x.NoError(<-done, "a reader told to stop has not failed")
	})
}

// TestADayIsMidnightUTC, which is what `Every` is when it says nothing.
func TestADayIsMidnightUTC(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		ctx, cancel := context.WithCancel(quiet(t.Context()))
		defer cancel()

		time.Sleep(15 * time.Hour)

		sink := &kept{}
		r := meter.Reader{Tenants: tenants(pdid.New(1)), Meters: []meter.Meter{constant("acme.robots", 1)}, Sink: sink}
		go r.Spin(ctx)

		time.Sleep(2 * 24 * time.Hour)
		synctest.Wait()

		ats := []time.Time{}
		for _, rs := range sink.taken() {
			ats = append(ats, rs[0].MeasuredAt)
		}
		x.Equal([]time.Time{
			time.Date(2000, 1, 1, 15, 0, 0, 0, time.UTC),
			time.Date(2000, 1, 2, 0, 0, 0, 0, time.UTC),
			time.Date(2000, 1, 3, 0, 0, 0, 0, time.UTC),
		}, ats)
	})
}

// TestEveryTenantIsMeasured, a page at a time.
func TestEveryTenantIsMeasured(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		ctx, cancel := context.WithCancel(quiet(t.Context()))
		defer cancel()

		vs := sorted(250)
		sink := &kept{}
		r := meter.Reader{Tenants: tenants(vs...), Meters: []meter.Meter{constant("acme.robots", 1)}, Sink: sink}
		go r.Spin(ctx)
		synctest.Wait()

		got := []pdid.Id{}
		for _, rs := range sink.taken() {
			got = append(got, rs[0].Tenant)
		}
		x.Equal(vs, got)
	})
}

// TestWhatTheSinkRefusedIsHandedItAgainAsItWas: the same readings, so that one
// that did reach it -- a call that timed out after it was kept -- counts once,
// a minute later and then longer each time.
func TestWhatTheSinkRefusedIsHandedItAgainAsItWas(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		ctx, cancel := context.WithCancel(quiet(t.Context()))
		defer cancel()

		vs := sorted(2)
		var down atomic.Bool
		down.Store(true)
		sink := &kept{refuse: func(rs []meter.Reading) bool { return down.Load() && rs[0].Tenant == vs[1] }}
		r := meter.Reader{Tenants: tenants(vs...), Meters: []meter.Meter{constant("acme.robots", 1)}, Sink: sink, Every: 24 * time.Hour}
		go r.Spin(ctx)

		synctest.Wait()
		x.Len(sink.taken(), 1)
		x.Equal(2, sink.count())
		first := time.Now()

		// A minute, then two more.
		time.Sleep(time.Minute)
		synctest.Wait()
		x.Equal(3, sink.count())
		time.Sleep(time.Minute)
		synctest.Wait()
		x.Equal(3, sink.count(), "the second wait is longer than the first")
		time.Sleep(time.Minute)
		synctest.Wait()
		x.Equal(4, sink.count())

		down.Store(false)
		time.Sleep(4 * time.Minute)
		synctest.Wait()
		got := sink.taken()
		x.Len(got, 2)
		x.Equal(vs[1], got[1][0].Tenant)
		x.Equal(first.UTC(), got[1][0].MeasuredAt, "handed again as it was, and not measured again")

		// And once taken, not again.
		tries := sink.count()
		time.Sleep(10 * time.Hour)
		synctest.Wait()
		x.Equal(tries, sink.count())
	})
}

// TestWhatIsNotDoneWhenTheNextPassBeginsIsLetGo: the next pass measures again,
// and memory does not fill with a day's readings for every day a sink is
// down.
func TestWhatIsNotDoneWhenTheNextPassBeginsIsLetGo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		ctx, cancel := context.WithCancel(quiet(t.Context()))
		defer cancel()

		var down atomic.Bool
		down.Store(true)
		ats := map[time.Time]int{}
		var mu sync.Mutex
		sink := meter.SinkFunc(func(_ context.Context, rs []meter.Reading) error {
			mu.Lock()
			defer mu.Unlock()
			ats[rs[0].MeasuredAt]++
			if down.Load() {
				return errors.New("down")
			}
			return nil
		})
		r := meter.Reader{Tenants: tenants(pdid.New(1)), Meters: []meter.Meter{constant("acme.robots", 1)}, Sink: sink, Every: time.Hour}
		go r.Spin(ctx)

		time.Sleep(90 * time.Minute)
		synctest.Wait()
		mu.Lock()
		x.Len(ats, 2, "the first pass's readings and the second's, each its own")
		mu.Unlock()

		down.Store(false)
		time.Sleep(30 * time.Minute)
		synctest.Wait()
		mu.Lock()
		defer mu.Unlock()
		midnight := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		x.Greater(ats[midnight], 1, "the first pass's were handed over more than once")
		x.Less(ats[midnight], 7, "and less than every minute")
		x.Contains(ats, midnight.Add(time.Hour))
	})
}

// TestATenantAMeterFailedForIsMeasuredAgain: whole, at a moment of its own,
// since a meter that failed may have failed with the database the others read.
func TestATenantAMeterFailedForIsMeasuredAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		ctx, cancel := context.WithCancel(quiet(t.Context()))
		defer cancel()

		fails := 1
		flaky := meter.Meter{Key: "acme.flaky", Unit: "1", Measure: func(context.Context, pdid.Id) (int64, error) {
			if fails > 0 {
				fails--
				return 0, errors.New("blinked")
			}
			return 7, nil
		}}
		sink := &kept{}
		r := meter.Reader{Tenants: tenants(pdid.New(1)), Meters: []meter.Meter{constant("acme.robots", 1), flaky}, Sink: sink}
		go r.Spin(ctx)

		synctest.Wait()
		got := sink.taken()
		x.Len(got, 1)
		x.Len(got[0], 1, "what was measured went at once")

		time.Sleep(time.Minute)
		synctest.Wait()
		got = sink.taken()
		x.Len(got, 2)
		x.Len(got[1], 2, "measured again, every meter of it")
		x.Equal(got[0][0].MeasuredAt.Add(time.Minute), got[1][0].MeasuredAt)
		x.Equal(int64(7), got[1][1].Value)
	})
}

// TestAPassThatCouldNotListTheTenantsGoesOn from the last it measured, rather
// than leaving the rest without a reading or measuring the first twice.
func TestAPassThatCouldNotListTheTenantsGoesOn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		ctx, cancel := context.WithCancel(quiet(t.Context()))
		defer cancel()

		vs := sorted(150)
		list := tenants(vs...)
		fails := 1
		r := meter.Reader{
			Tenants: func(ctx context.Context, after pdid.Id, limit int) ([]pdid.Id, error) {
				if !after.IsZero() && fails > 0 {
					fails--
					return nil, errors.New("blinked")
				}
				return list(ctx, after, limit)
			},
			Meters: []meter.Meter{constant("acme.robots", 1)},
			Sink:   &kept{},
		}
		sink := r.Sink.(*kept)
		go r.Spin(ctx)

		synctest.Wait()
		x.Len(sink.taken(), 100)

		time.Sleep(time.Minute)
		synctest.Wait()
		got := []pdid.Id{}
		for _, rs := range sink.taken() {
			got = append(got, rs[0].Tenant)
		}
		x.Equal(vs, got)
	})
}
