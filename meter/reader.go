package meter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
)

// Reader measures every tenant's meters on a schedule, and one tenant's when it
// is asked to, and hands each tenant's readings to the sink: a [spin.Spinner]
// called `payday.meter.read`, which a lease runs in one process of the
// deployment.
//
//	s.Meters = &meter.Reader{
//		Tenants: pd.MeterTenants(client),
//		Meters:  append(pd.Meters(client), kept, stored), // the schema's, then the app's own
//		Sink:    sink,                                    // the billing service's client
//	}
//	s.Spin = append(s.Spin, s.Meters)
//
// # From a replica
//
// It reads what it is built on. A deployment with a replica of its database
// builds `pd.Meters` and `pd.MeterTenants` on a client of the replica, and
// every pass reads there and not where requests are answered. A reading is then
// as old as the replica is behind, and stamped when it was read, which is what
// a reading is. [Reader.Read] reads there too; a reader built on the primary
// beside it is how an app that wants otherwise has both.
//
// # When
//
// A pass as it starts, and then one at each multiple of [Reader.Every] since
// the Unix epoch: each midnight UTC for a day, each hour on the hour for an
// hour. On the clock rather than an interval after the last, so a pass that
// takes a while does not move the next one later, day after day, until a day
// has none. A pass that takes longer than Every does not double up, and the
// boundaries it ran past are not made up.
//
// One schedule for every meter, and not one each: a tenant is measured at one
// moment, every meter of it, so its readings are one picture of it, and a
// meter that wants to be read more often than the others is one reading too
// many of the rest -- which a sink keeps and a bill does not mind.
//
// # What fails
//
// A pass logs what fails and goes on, and then tries it again, a minute later
// and longer each time, until it is done or the next pass begins: the readings
// the sink would not take are handed it again as they were, so that one which
// did reach it counts once; a tenant a meter could not be measured of is
// measured again; and a pass that could not list the rest of the tenants goes
// on from the last it measured. What is still not done when the next pass
// begins is let go, and said in the log as an error -- those tenants have no
// reading from that pass. Nothing is written down meanwhile: a process that
// stops loses what it had not handed over, and the next pass, in whichever
// process holds the lease then, measures again.
//
// A reader missing what it is made of -- a sink, its meters, a key that is not
// one, two meters of one key -- gives up as it starts, which ends the process:
// a deployment that bills by these and reads none of them is one nothing else
// would mention.
type Reader struct {
	// Tenants is every tenant to measure, a page of at most `limit` after
	// `after` in the order of their identifiers: `pd.MeterTenants(client)`,
	// which is every tenant there is.
	Tenants func(ctx context.Context, after pdid.Id, limit int) ([]pdid.Id, error)

	// Meters are what is measured of each tenant: `pd.Meters(client)`, which
	// are the ones the schema declares, and the app's own beside them.
	Meters []Meter

	// Sink takes each tenant's readings.
	Sink Sink

	// Every is how often every tenant is measured: a day when zero. See
	// [Reader] for when a pass begins.
	Every time.Duration
}

var (
	_ spin.Spinner = Reader{}
	_ spin.Named   = Reader{}
)

// ReaderName is what a [Reader] is called, under a lease too.
const ReaderName = "payday.meter.read"

// tenantsPage is how many tenants a pass asks for at a time.
const tenantsPage = 100

// retryFirst and retryMost are how long a pass waits before it tries again
// what it could not do, at first and at most: doubling from the one to the
// other, so a sink that is down for a minute costs nothing and one that is down
// for the day is asked once an hour.
const (
	retryFirst = time.Minute
	retryMost  = time.Hour
)

func (r Reader) SpinName() string { return ReaderName }

// Spin measures every tenant at each boundary of [Reader.Every] until the
// context is done; see [Reader] for when, and for what it does with a failure.
func (r Reader) Spin(ctx context.Context) error {
	if err := r.check(); err != nil {
		return err
	}
	if r.Tenants == nil {
		return errors.New("meter: a reader has no Tenants to measure")
	}

	every := r.every()
	for {
		p := &pass{r: r}
		p.run(ctx)

		next := boundary(time.Now(), every)
		p.settle(ctx, next)
		log.From(ctx).InfoContext(ctx, "meter: measured",
			"tenants", p.tenants, "readings", p.readings, "owed", len(p.owed), "again", len(p.again), "listed", p.listed)
		switch {
		case ctx.Err() != nil:
			p.lost(ctx, slog.LevelWarn, "meter: the reader stopped with a pass not done; the next one measures these tenants again")
			return nil
		case !p.done():
			p.lost(ctx, slog.LevelError, "meter: a pass was not done when the next began, so these tenants have no reading from it")
		}

		if !sleep(ctx, next) {
			return nil
		}
	}
}

// Read measures every meter of `tenant` now and hands the readings to the sink:
// what an app calls before it destroys a tenant, so that the last period is
// measured while what it is measured from is there.
//
// It answers an error for any meter it could not measure and for a sink that
// did not take the rest -- which a destruction waits on, and tries again: the
// readings it did hand over are the sink's, and one handed over again counts
// once. It reads where the reader was built to read; see [Reader].
func (r Reader) Read(ctx context.Context, tenant pdid.Id) error {
	if err := r.check(); err != nil {
		return err
	}

	rs, err := r.measure(ctx, tenant)
	if len(rs) > 0 {
		if serr := r.Sink.Send(ctx, rs); serr != nil {
			err = errors.Join(err, fmt.Errorf("meter: the sink did not take the readings of %s: %w", tenant, serr))
		}
	}

	return err
}

// check refuses a reader that cannot measure anything, before it measures:
// what pd gen holds a declared meter to, held here to the app's own too.
func (r Reader) check() error {
	switch {
	case r.Sink == nil:
		return errors.New("meter: a reader has no Sink to hand its readings to")
	case len(r.Meters) == 0:
		return errors.New("meter: a reader has no Meters to measure")
	case r.Every < 0:
		return fmt.Errorf("meter: a reader is told to measure every %s", r.Every)
	}

	seen := map[string]bool{}
	for _, m := range r.Meters {
		if err := CheckKey(m.Key); err != nil {
			return err
		}
		if err := CheckUnit(m.Unit); err != nil {
			return fmt.Errorf("%s: %w", m.Key, err)
		}
		if m.Measure == nil {
			return fmt.Errorf("meter: %s has no Measure", m.Key)
		}
		if seen[m.Key] {
			return fmt.Errorf("meter: two meters are called %q, and a reading of one would be taken for the other's", m.Key)
		}
		seen[m.Key] = true
	}

	return nil
}

func (r Reader) every() time.Duration {
	if r.Every > 0 {
		return r.Every
	}

	return 24 * time.Hour
}

// measure is every meter of `tenant`, at one moment, and what could not be
// measured.
func (r Reader) measure(ctx context.Context, tenant pdid.Id) ([]Reading, error) {
	at := time.Now().UTC().Truncate(time.Microsecond)

	rs := make([]Reading, 0, len(r.Meters))
	var errs []error
	for _, m := range r.Meters {
		v, err := m.Measure(ctx, tenant)
		if err != nil {
			errs = append(errs, fmt.Errorf("meter: %s of %s: %w", m.Key, tenant, err))
			continue
		}
		rs = append(rs, Reading{Tenant: tenant, Meter: m.Key, MeasuredAt: at, Value: v, Unit: m.Unit})
	}

	return rs, errors.Join(errs...)
}

// pass is one measuring of every tenant, and what of it is not done yet.
type pass struct {
	r Reader

	// after is the last tenant listed, and listed whether every one has been.
	after  pdid.Id
	listed bool

	// owed are readings the sink did not take, a tenant's to an element, and
	// again the tenants a meter could not be measured of.
	owed  [][]Reading
	again []pdid.Id

	tenants, readings int
}

// done is whether every tenant was measured and every reading taken.
func (p *pass) done() bool { return p.listed && len(p.owed) == 0 && len(p.again) == 0 }

// run measures the tenants from where the pass got to, until every one is
// listed or listing them fails.
func (p *pass) run(ctx context.Context) {
	for !p.listed {
		vs, err := p.r.Tenants(ctx, p.after, tenantsPage)
		if err != nil {
			if ctx.Err() == nil {
				log.From(ctx).WarnContext(ctx, "meter: the tenants to measure", "err", err, "after", p.after)
			}
			return
		}
		for _, tenant := range vs {
			if ctx.Err() != nil {
				return
			}
			p.one(ctx, tenant)
			p.after = tenant
		}

		p.listed = len(vs) < tenantsPage
	}
}

// one measures `tenant` and hands the readings over, and keeps what is not
// done.
func (p *pass) one(ctx context.Context, tenant pdid.Id) {
	rs, err := p.r.measure(ctx, tenant)
	if err != nil {
		if ctx.Err() == nil {
			log.From(ctx).WarnContext(ctx, "meter: a tenant's meters were not all measured, and are measured again",
				"tenant", tenant, "err", err)
		}
		p.again = append(p.again, tenant)
	}
	if len(rs) > 0 {
		p.send(ctx, rs)
	}
}

// send hands one tenant's readings over, and keeps them when the sink does not
// take them.
func (p *pass) send(ctx context.Context, rs []Reading) {
	if err := p.r.Sink.Send(ctx, rs); err != nil {
		if ctx.Err() == nil {
			log.From(ctx).WarnContext(ctx, "meter: the sink did not take a tenant's readings, and is handed them again",
				"tenant", rs[0].Tenant, "err", err)
		}
		p.owed = append(p.owed, rs)
		return
	}

	p.tenants++
	p.readings += len(rs)
}

// settle tries again what the pass did not do, waiting longer each time, until
// it is done or `until`.
func (p *pass) settle(ctx context.Context, until time.Time) {
	wait := retryFirst
	for !p.done() {
		at := time.Now().Add(wait)
		if !at.Before(until) || !sleep(ctx, at) {
			return
		}

		owed, again := p.owed, p.again
		p.owed, p.again = nil, nil
		for _, rs := range owed {
			p.send(ctx, rs)
		}
		for _, tenant := range again {
			p.one(ctx, tenant)
		}
		p.run(ctx)

		wait = min(2*wait, retryMost)
	}
}

// lost says which tenants of the pass are not done, at `level`.
func (p *pass) lost(ctx context.Context, level slog.Level, msg string) {
	tenants := []string{}
	for _, rs := range p.owed {
		tenants = append(tenants, rs[0].Tenant.String())
	}
	for _, tenant := range p.again {
		tenants = append(tenants, tenant.String())
	}
	if len(tenants) == 0 && p.listed {
		return
	}

	// Some of them, and how many: a list of every tenant of a deployment is
	// not a line anybody reads.
	some := tenants[:min(len(tenants), 20)]
	args := []any{"tenants", len(tenants), "some", some}
	if !p.listed {
		args = append(args, "unlisted_after", p.after.String())
	}
	log.From(ctx).Log(context.WithoutCancel(ctx), level, msg, args...)
}

// boundary is the first multiple of `every` since the Unix epoch after `now`.
//
// Since the epoch rather than since Go's zero time, which `Truncate` counts
// from: the two agree for a day and an hour, and not for a week, whose
// multiples since the epoch are Thursdays -- which is a thing a person can be
// told.
func boundary(now time.Time, every time.Duration) time.Time {
	n := now.UnixNano()
	d := every.Nanoseconds()

	return time.Unix(0, (n/d+1)*d).UTC()
}

// sleep waits until `at`, and answers false if the context ended first.
func sleep(ctx context.Context, at time.Time) bool {
	t := time.NewTimer(time.Until(at))
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
