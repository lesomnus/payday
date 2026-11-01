package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lesomnus/payday/notify"
	"github.com/lesomnus/payday/pdid"
)

// erasing is an Erase of `left` notifications, at most `limit` a call, that
// records what each call was asked.
type erasing struct {
	left    []notify.Notification
	befores []time.Time
	fail    error
}

func (e *erasing) erase(_ context.Context, before time.Time, limit int) ([]notify.Notification, error) {
	e.befores = append(e.befores, before)
	if e.fail != nil {
		return nil, e.fail
	}
	n := min(limit, len(e.left))
	vs := e.left[:n]
	e.left = e.left[n:]
	return vs, nil
}

func TestAnExpirerErasesWhatItKeepsNoLongerABatchAtATime(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 11, 2, 3, 4, 5, 0, time.UTC)
	other := pdid.New(tenantDomain)
	var vs []notify.Notification
	for i := range 7 {
		tenant := f.tenant
		if i%2 == 1 {
			tenant = other
		}
		vs = append(vs, notify.Notification{ID: pdid.New(noticeDomain), Tenant: tenant, Topic: notify.Named(fmt.Sprintf("t%d", i%3))})
	}
	e := &erasing{left: vs}
	changes, stop := f.watch.Subscribe()
	defer stop()

	x := notify.Expirer{Keep: 90 * 24 * time.Hour, Erase: e.erase, Watch: f.watch, Batch: 3, Clock: func() time.Time { return now }}
	if x.SpinName() != notify.ExpireName {
		t.Fatalf("an expirer is called %q", x.SpinName())
	}
	n, err := x.Pass(f.ctx)
	if err != nil || n != 7 {
		t.Fatalf("a pass took %d: %v", n, err)
	}
	if len(e.befores) != 3 {
		t.Fatalf("seven in batches of three took %d batches, not three", len(e.befores))
	}
	for _, b := range e.befores {
		if !b.Equal(now.Add(-90 * 24 * time.Hour)) {
			t.Fatalf("a batch was asked for what was published before %s", b)
		}
	}

	// Each batch says which topics of which namespace it took from, once: two
	// namespaces in each of the first two batches, and one in the last.
	said := map[pdid.Id]map[notify.Topic]int{}
	for range 5 {
		select {
		case event := <-changes:
			for _, c := range event.Changes {
				if c.By != notify.InvalidationMethod {
					t.Fatalf("an expiry said %+v", c)
				}
				var h notify.Hint
				if err := json.Unmarshal(c.Signal, &h); err != nil {
					t.Fatal(err)
				}
				if !h.Reader.IsZero() {
					t.Fatal("an expiry's hint is one reader's")
				}
				if said[h.Tenant] == nil {
					said[h.Tenant] = map[notify.Topic]int{}
				}
				for _, v := range h.Topics {
					said[h.Tenant][v]++
				}
			}
		case <-time.After(time.Second):
			t.Fatalf("the expiry said %d tenants' topics", len(said))
		}
	}
	if len(said) != 2 || len(said[f.tenant]) != 3 || len(said[other]) != 3 {
		t.Fatalf("the expiry said %+v", said)
	}

	n, err = x.Pass(f.ctx)
	if err != nil || n != 0 {
		t.Fatalf("a second pass took %d: %v", n, err)
	}
}

func TestAnExpirerStopsAtAFailureAndKeepsWhatItTook(t *testing.T) {
	f := setup(t)
	e := &erasing{fail: errors.New("the database blinked")}
	x := notify.Expirer{Keep: time.Hour, Erase: e.erase}
	n, err := x.Pass(f.ctx)
	if !errors.Is(err, e.fail) || n != 0 {
		t.Fatalf("a failed pass answered %d, %v", n, err)
	}

	// And a pass of what is done is over before it asks anything.
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	e.fail = nil
	e.befores = nil
	if n, err := x.Pass(ctx); err != nil || n != 0 || len(e.befores) != 0 {
		t.Fatalf("a pass of a done context took %d, asked %d times: %v", n, len(e.befores), err)
	}
}

func TestAnExpirerMissingWhatItIsMadeOfGivesUp(t *testing.T) {
	for _, x := range []notify.Expirer{
		{Erase: (&erasing{}).erase},
		{Keep: -time.Hour, Erase: (&erasing{}).erase},
		{Keep: time.Hour},
	} {
		if err := x.Spin(context.Background()); err == nil {
			t.Fatalf("%+v ran", x)
		}
		if _, err := x.Pass(context.Background()); err == nil {
			t.Fatalf("%+v passed", x)
		}
	}
}

// A hundred topics to a change, as a request names at most.
func TestAnExpirerSaysAHundredTopicsAtATime(t *testing.T) {
	f := setup(t)
	var vs []notify.Notification
	for i := range 250 {
		vs = append(vs, notify.Notification{ID: pdid.New(noticeDomain), Tenant: f.tenant, Topic: notify.Named(fmt.Sprint(i))})
	}
	changes, stop := f.watch.Subscribe()
	defer stop()

	x := notify.Expirer{Keep: time.Hour, Erase: (&erasing{left: vs}).erase, Watch: f.watch, Batch: 500}
	if n, err := x.Pass(f.ctx); err != nil || n != 250 {
		t.Fatalf("a pass took %d: %v", n, err)
	}
	total := 0
	for range 3 {
		event := <-changes
		var h notify.Hint
		if err := json.Unmarshal(event.Changes[0].Signal, &h); err != nil {
			t.Fatal(err)
		}
		if len(h.Topics) > 100 {
			t.Fatalf("one change said %d topics", len(h.Topics))
		}
		total += len(h.Topics)
	}
	if total != 250 {
		t.Fatalf("the expiry said %d topics of 250", total)
	}
	select {
	case event := <-changes:
		t.Fatalf("and then said %+v", event)
	default:
	}
}
