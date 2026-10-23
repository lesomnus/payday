package past

import (
	"context"
	"errors"
	"time"

	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
)

// Expirer is [Expire] in every tenant with a past, every so often: a
// [spin.Spinner] called `payday.past.expire`, for an app whose past is what it
// expires.
//
//	s.Spin = append(s.Spin, past.Expirer{
//		Tenants: pd.PastTenants(client),
//		Window:  windows.Of, // the app's, and the trail's from the same source
//		In:      pd.PastIn(ungated, drv, content),
//		Expiry:  past.Expiry{KeepKinds: []string{"robot.add"}},
//	})
//
// An app with rows of its own that name events -- time rows, stock movements,
// `(payday.field) = {event: true}` -- expires them before the past and in the
// same transaction, so that an event only they named goes with them: its own
// loop does both, calling [Expire] once its own rows are gone. Run beside this
// one instead, the event stays until the pass after they went.
//
// A pass logs what fails and goes on: a tenant whose window cannot be read
// keeps everything that pass, as the trail's does, and one whose expiry fails
// keeps what it would have taken, since a transaction is all or nothing. What
// a pass took is not there for the next to take, so a pass run twice -- one
// process taking over another's lease -- takes nothing the second time.
type Expirer struct {
	// Tenants is the tenants with a past, a page of at most `limit` after
	// `after` in the order of their identifiers: `pd.PastTenants(client)`.
	Tenants func(ctx context.Context, after pdid.Id, limit int) ([]pdid.Id, error)

	// Window is how long `tenant` keeps its past, and what holds are on it,
	// as of now. An error keeps everything of the tenant's this pass.
	Window func(ctx context.Context, tenant pdid.Id) (Window, error)

	// In runs `f` in a transaction of its own on a store of the past, the
	// context framed as the deployment's work in `tenant`, and commits when
	// `f` answers nil: `pd.PastIn(ungated, drv, content)`.
	In func(ctx context.Context, tenant pdid.Id, f func(ctx context.Context, s Store) error) error

	// Every is the wait between passes: an hour when it is zero.
	Every time.Duration

	// Expiry is what no window ends, and what is said of what one took.
	Expiry Expiry
}

var (
	_ spin.Spinner = Expirer{}
	_ spin.Named   = Expirer{}
)

// ExpirerName is what an [Expirer] is called, under a lease too.
const ExpirerName = "payday.past.expire"

// tenantsPage is how many tenants a pass asks for at a time.
const tenantsPage = 100

func (e Expirer) SpinName() string { return ExpirerName }

// Spin runs [Expirer.Pass] every [Expirer.Every] until the context is done:
// once as it starts, and then after each wait.
//
// An expirer missing what it is made of gives up at once, which ends the
// process: a past that was meant to expire and does not is a deployment
// keeping what it said it would not, and nothing else would say so.
func (e Expirer) Spin(ctx context.Context) error {
	switch {
	case e.Tenants == nil:
		return errors.New("past: an expirer has no Tenants to expire")
	case e.Window == nil:
		return errors.New("past: an expirer has no Window to ask of each tenant")
	case e.In == nil:
		return errors.New("past: an expirer has no In to do a tenant's in")
	}

	every := e.Every
	if every <= 0 {
		every = time.Hour
	}

	return spin.Every(every, e.Pass).Spin(ctx)
}

// Pass expires every tenant [Expirer.Tenants] answers, once. It answers nil
// whatever went wrong, which it logs: a pass that fails is tried again by the
// next, and the process goes on serving meanwhile.
func (e Expirer) Pass(ctx context.Context) error {
	after := pdid.Nil
	for {
		vs, err := e.Tenants(ctx, after, tenantsPage)
		if err != nil {
			log.From(ctx).WarnContext(ctx, "past: the tenants to expire", "err", err, "after", after)
			return nil
		}
		for _, v := range vs {
			if ctx.Err() != nil {
				return nil
			}
			e.expire(ctx, v)
		}
		if len(vs) < tenantsPage {
			return nil
		}

		after = vs[len(vs)-1]
	}
}

// expire is one tenant's expiry, logged.
func (e Expirer) expire(ctx context.Context, tenant pdid.Id) {
	w, err := e.Window(ctx, tenant)
	if err != nil {
		log.From(ctx).WarnContext(ctx, "past: a tenant has no window, so nothing of theirs expires this pass",
			"tenant", tenant, "err", err)
		return
	}
	if w.Keep <= 0 || w.Held() {
		// Nothing goes, and a transaction would say so.
		return
	}

	var x Expired
	err = e.In(ctx, tenant, func(ctx context.Context, s Store) error {
		var err error
		x, err = Expire(ctx, s, tenant, w, time.Now(), e.Expiry)
		return err
	})
	switch {
	case err != nil:
		log.From(ctx).WarnContext(ctx, "past: an expiry failed, and nothing of it was taken",
			"tenant", tenant, "err", err)
	case x.Total() > 0:
		log.From(ctx).InfoContext(ctx, "past: expired",
			"tenant", tenant, "before", x.Before.Format(time.RFC3339), "facts", x.Facts, "events", x.Events)
	}
}
