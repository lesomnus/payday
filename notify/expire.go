package notify

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/watch"
)

// ExpireName is what an [Expirer] is called, under a lease too, and the act
// each notification it erases is on the trail as.
const ExpireName = "payday.notify.expire"

// Expirer erases the notifications an app keeps no longer, with their
// receipts (payday#194): a [spin.Spinner] called `payday.notify.expire`, made
// by the generated `pd.NotificationExpirer` for an app whose schema keeps its
// notifications for a number of days.
//
//	s.Spin = append(s.Spin, pd.NotificationExpirer(drv, w, content))
//
// # What goes
//
// A notification published longer ago than [Expirer.Keep], every receipt of
// it, and with them its idempotency key: what the schema's `notify.keep` says
// is kept, and no longer. The oldest go first, a batch at a time, each batch
// in a transaction of its own that puts each notification on the trail as
// erased -- as payday's other erasures beneath the servers are -- so that a
// check of the rows finds nothing the trail says is a row and is not. A
// receipt is not on the trail (payday#197) and goes with nothing said.
//
// Once a batch commits, the topics its notifications were in are said to have
// changed, as an app's policy invalidation is: a stream that showed one
// queries again, finds it gone, and drops it.
//
// # Run twice
//
// A pass takes what is there to take, and a second pass of the same moment
// finds none of it: two at once -- one process taking over another's lease --
// take a batch each or wait for each other's, and never one notification
// twice. A pass that fails partway keeps what it took before it failed, and
// the next pass takes the rest.
type Expirer struct {
	// Keep is how long a notification is kept from its publication: what was
	// published before now less Keep goes. The generated expirer gives it the
	// schema's. Zero or less is a mistake, and the expirer refuses to start
	// rather than take everything.
	Keep time.Duration

	// Erase takes, in one transaction of its own, at most `limit` of the
	// notifications published before `before`, oldest first, with every
	// receipt of each, puts each on the trail as erased, and answers with what
	// it took -- or with nothing taken, when it fails.
	Erase func(ctx context.Context, before time.Time, limit int) ([]Notification, error)

	// Watch is where the topics of what a batch took are said to have
	// changed, once it commits. Nil says nothing, and a stream learns of an
	// erased notification only when it queries again for another reason.
	Watch *watch.Watch

	// Every is the wait between passes: an hour when it is zero.
	Every time.Duration

	// Batch is how many notifications one transaction takes: 500 when it is
	// zero.
	Batch int

	// Clock is now: time.Now when it is nil.
	Clock func() time.Time
}

var (
	_ spin.Spinner = Expirer{}
	_ spin.Named   = Expirer{}
)

func (e Expirer) SpinName() string { return ExpireName }

// Spin runs [Expirer.Pass] every [Expirer.Every] until the context is done:
// once as it starts, and then after each wait. A pass that fails is logged and
// tried again by the next, as the past's is: the process goes on serving
// meanwhile.
//
// An expirer missing what it is made of gives up at once, which ends the
// process: notifications meant to go that do not are a deployment keeping what
// it said it would not, and nothing else would say so.
func (e Expirer) Spin(ctx context.Context) error {
	switch {
	case e.Keep <= 0:
		return errors.New("notify: an expirer keeps notifications for no time at all; Keep is how long, from publication")
	case e.Erase == nil:
		return errors.New("notify: an expirer has no Erase to take what it does not keep")
	}

	every := e.Every
	if every <= 0 {
		every = time.Hour
	}

	return spin.Every(every, func(ctx context.Context) error {
		n, err := e.Pass(ctx)
		switch {
		case err != nil:
			log.From(ctx).WarnContext(ctx, "notify: an expiry failed partway, and the next pass takes the rest",
				"erased", n, "err", err)
		case n > 0:
			log.From(ctx).InfoContext(ctx, "notify: expired", "erased", n)
		}

		return nil
	}).Spin(ctx)
}

// Pass erases what is older than [Expirer.Keep], a batch at a time, until a
// batch takes fewer than [Expirer.Batch] or the context is done. It answers
// how many it took, and the first error, at which it stops: what it took
// before then is gone.
func (e Expirer) Pass(ctx context.Context) (int, error) {
	if e.Keep <= 0 || e.Erase == nil {
		return 0, errors.New("notify: an expirer needs a Keep and an Erase")
	}

	clock := e.Clock
	if clock == nil {
		clock = time.Now
	}
	batch := e.Batch
	if batch <= 0 {
		batch = 500
	}
	before := clock().UTC().Add(-e.Keep)

	n := 0
	for ctx.Err() == nil {
		vs, err := e.Erase(ctx, before, batch)
		if err != nil {
			return n, err
		}
		n += len(vs)

		// The batch is gone whether or not a stream is told; one that is not
		// learns of it on its next query, as after any lost hint.
		if err := e.said(ctx, vs); err != nil {
			log.From(ctx).WarnContext(ctx, "notify: expired notifications' topics were not said to have changed", "err", err)
		}
		if len(vs) < batch {
			break
		}
	}

	return n, nil
}

// said tells the watch which topics of which namespace lost a notification,
// as a policy invalidation does: every stream on one of them asks its policy
// again and queries again. A hundred topics to a change, as a request names
// at most.
func (e Expirer) said(ctx context.Context, vs []Notification) error {
	if e.Watch == nil || e.Watch.Broker() == nil || len(vs) == 0 {
		return nil
	}

	var order []pdid.Id
	topics := map[pdid.Id][]Topic{}
	for _, v := range vs {
		if _, ok := topics[v.Tenant]; !ok {
			order = append(order, v.Tenant)
		}
		if !slices.Contains(topics[v.Tenant], v.Topic) {
			topics[v.Tenant] = append(topics[v.Tenant], v.Topic)
		}
	}

	var errs []error
	for _, tenant := range order {
		for chunk := range slices.Chunk(topics[tenant], 100) {
			errs = append(errs, invalidate(ctx, e.Watch, tenant, chunk))
		}
	}

	return errors.Join(errs...)
}

// invalidate says to the broker that `topics` of `tenant`'s namespace changed
// in a way only a fresh query and the policy can say: what [Service.Invalidate]
// says after an app's policy change, and an [Expirer] after an expiry.
func invalidate(ctx context.Context, w *watch.Watch, tenant pdid.Id, topics []Topic) error {
	signal, err := json.Marshal(Hint{Tenant: tenant, Topics: topics})
	if err != nil {
		return err
	}

	return watch.Confirm(context.WithoutCancel(ctx), w.Broker(), watch.Event{Tenant: tenant,
		Method: InvalidationMethod, Changes: []watch.Change{{By: InvalidationMethod, Key: tenant, Signal: signal}}})
}
