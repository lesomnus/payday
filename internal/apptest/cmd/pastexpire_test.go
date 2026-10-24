package cmd_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/internal/ent"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	entevent "github.com/lesomnus/payday/internal/apptest/internal/ent/event"
	entfact "github.com/lesomnus/payday/internal/apptest/internal/ent/fact"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/predicate"
	entstint "github.com/lesomnus/payday/internal/apptest/internal/ent/stint"
	"github.com/lesomnus/payday/internal/apptest/server/core"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// The past's expiry (payday#151): what a tenant's window no longer reaches,
// taken in one transaction after the app's own rows that name events, each row
// on the trail as taken -- tried on a history written over September and
// October of 2026, with the window drawn on 10/10.

func sep(d int) time.Time { return time.Date(2026, time.September, d, 0, 0, 0, 0, time.UTC) }
func oct(d int) time.Time { return time.Date(2026, time.October, d, 0, 0, 0, 0, time.UTC) }

// The window every test here expires by: ten days, on 10/20, so that what was
// over before 10/10 goes.
var (
	expiredOn = oct(20)
	window    = past.Window{Keep: 10 * 24 * time.Hour}
	cutoff    = oct(10)
)

// exceptions is this app's exceptions to every window: a machine's mode of
// service, which a later mode does not make history, and the event of a
// service.
func exceptions() past.Expiry {
	return past.Expiry{
		KeepFacts: []past.FactKeep{{Domain: pd.MachineDomain, Field: 19, Values: []string{"3"}}},
		KeepKinds: []string{"machine.service"},
	}
}

// stintOver is a stint that ended by `c`: what this app's own expiry takes, and
// what a preview is told goes of its table.
func stintOver(c time.Time) predicate.Stint {
	return entstint.And(entstint.ValidToNotNil(), entstint.ValidToLTE(c))
}

// going is what this app says goes of its own tables that name events, for a
// preview, which takes nothing first.
var going = map[pdid.Domain]past.Going{
	pd.StintDomain: func(c time.Time) func(*sql.Selector) { return stintOver(c) },
}

// step is what an operation of a history writes beside its event: `next` is
// the stack it writes through, and `s` its past, both on its transaction.
type step func(ctx context.Context, next app.Server, s past.Store, op *past.Op) error

// done is one operation of a history, recorded at `at`, in a transaction of
// its own through the stack a caller reaches: its event, and what `f` writes
// as it. It answers the event.
func (b *built) done(t *testing.T, ctx context.Context, at time.Time, ev past.Event, f step) pdid.Id {
	t.Helper()

	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	require.NoError(t, err)
	defer tx.Rollback()

	next, err := enttx.Rebind(b.Walled, drv)
	require.NoError(t, err)
	s, err := pd.Past(next, drv, b.Contents)
	require.NoError(t, err)
	s = past.WithClock(s, func() time.Time { return at })

	ctx, op, err := past.Begin(ctx, s, ev)
	require.NoError(t, err)
	if f != nil {
		require.NoError(t, f(ctx, next, s, op))
	}
	require.NoError(t, tx.Commit())

	return op.Id()
}

// sets is a step that says `vs` of `subject` from `from`.
func sets(subject pdid.Id, from time.Time, vs ...past.Value) step {
	return func(ctx context.Context, _ app.Server, _ past.Store, op *past.Op) error {
		return op.Set(ctx, past.Fact{Subject: subject, From: from, Values: vs})
	}
}

// factOf is the one fact `event` wrote of the field `fd`.
func (b *built) factOf(t *testing.T, ctx context.Context, event pdid.Id, fd protoreflect.FieldDescriptor) pdid.Id {
	t.Helper()

	vs, err := b.Ent.Fact.Query().
		Where(entfact.EventId(event.Uuid()), entfact.FieldNumber(uint32(fd.Number()))).
		Ids(ctx)
	require.NoError(t, err)
	require.Len(t, vs, 1, "the facts %s wrote of %s", event, fd.Name())

	return pdid.Id(vs[0])
}

var errDry = errors.New("a dry run, rolled back")

// expired is this app's expiry of `tenant`'s history as the deployment runs
// it, one transaction through `pd.PastIn`: the stints that ended before the
// window first, through the server so that the trail has each as the erase it
// is, then the past's -- and the receipt, this app's own, of both. Rolled back
// when `dry`, so that what it says is all it did. It answers what the past
// took, and how many stints.
func (b *built) expired(t *testing.T, ctx context.Context, tenant pdid.Id, w past.Window, now time.Time, x past.Expiry, dry bool) (past.Expired, int, error) {
	t.Helper()

	var out past.Expired
	stints := 0
	if x.Receipt == nil {
		x.Receipt = func(v past.Expired) proto.Message {
			if v.Total()+stints == 0 {
				return nil
			}
			return app.PastExpired_builder{
				Before: timestamppb.New(v.Before),
				Facts:  uint32(v.Facts),
				Events: uint32(v.Events),
				Stints: uint32(stints),
			}.Build()
		}
	}

	err := pd.PastIn(b.Ungated, b.Drv, b.Contents)(ctx, tenant, func(ctx context.Context, s past.Store) error {
		if w.Keep > 0 && !w.Held() {
			next, err := enttx.Rebind(b.Ungated, s.Driver())
			if err != nil {
				return err
			}
			ids, err := ent.NewClient(ent.Driver(s.Driver())).Stint.Query().
				Where(entstint.TenantId(tenant.Uuid()), stintOver(now.Add(-w.Keep))).
				Ids(ctx)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if _, err := next.Stint().Erase(ctx, app.StintRef_builder{Id: id[:]}.Build()); err != nil {
					return err
				}
			}
			stints = len(ids)
		}

		var err error
		if out, err = past.Expire(ctx, s, tenant, w, now, x); err != nil {
			return err
		}
		if dry {
			return errDry
		}
		return nil
	})
	if errors.Is(err, errDry) {
		err = nil
	}

	return out, stints, err
}

// census is how many rows of the past and stints there are, and lines of the
// trail: what a dry run leaves as it found it.
func (b *built) census(t *testing.T, ctx context.Context) [4]int {
	t.Helper()

	var out [4]int
	var err error
	for i, f := range []func(context.Context) (int, error){
		b.Ent.Fact.Query().Count,
		b.Ent.Event.Query().Count,
		b.Ent.Stint.Query().Count,
		b.Ent.Audit.Query().Count,
	} {
		out[i], err = f(ctx)
		require.NoError(t, err)
	}

	return out
}

// history is a tenant's past as [writeHistory] wrote it: a robot and a
// machine, and of each of its facts and events whether an expiry on
// [expiredOn] by [window] with [exceptions] takes it, and why.
type history struct {
	robot, machine pdid.Id

	facts, events map[pdid.Id]kept
}

type kept struct {
	stays bool
	why   string
}

func (h *history) fact(id pdid.Id, stays bool, why string)  { h.facts[id] = kept{stays, why} }
func (h *history) event(id pdid.Id, stays bool, why string) { h.events[id] = kept{stays, why} }

// gone is how many of `vs` go.
func gone(vs map[pdid.Id]kept) int {
	n := 0
	for _, v := range vs {
		if !v.stays {
			n++
		}
	}

	return n
}

// holds asserts what is there is what an expiry leaves of the history.
func (h *history) holds(t *testing.T, b *built, ctx context.Context) {
	t.Helper()
	x := require.New(t)

	for id, v := range h.facts {
		n, err := b.Ent.Fact.Query().Where(entfact.Id(id.Uuid())).Count(ctx)
		x.NoError(err)
		x.Equal(v.stays, n == 1, "the fact of %s: stays %v, and is there %v", v.why, v.stays, n == 1)
	}
	for id, v := range h.events {
		n, err := b.Ent.Event.Query().Where(entevent.Id(id.Uuid())).Count(ctx)
		x.NoError(err)
		x.Equal(v.stays, n == 1, "the event of %s: stays %v, and is there %v", v.why, v.stays, n == 1)
	}
}

// writeHistory writes the history every test here expires, in the tenant of
// `b`: each line below is an operation, on the day it was recorded, and says
// what an expiry with the window drawn on 10/10 leaves of it.
func writeHistory(t *testing.T, b *built, ctx context.Context) *history {
	t.Helper()
	x := require.New(t)
	as := b.as(ctx)

	cell := func(alias string) []byte {
		v, err := b.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  alias,
		}.Build())
		x.NoError(err)
		return v.GetId()
	}
	roomA, roomB := cell("a"), cell("b")

	r := b.arm(t, ctx, "arm-01")
	m := machine(t, b, as)
	h := &history{robot: idOf(t, r.GetId()), machine: idOf(t, m.GetId()), facts: map[pdid.Id]kept{}, events: map[pdid.Id]kept{}}
	rid, mid := h.robot, h.machine

	cellFd := r.ProtoReflect().Descriptor().Fields().ByName("cell")
	mf := m.ProtoReflect().Descriptor().Fields()
	model, hours, load, mode := mf.ByName("model"), mf.ByName("hours"), mf.ByName("load"), mf.ByName("mode")
	tags, firmware := mf.ByName("tags"), mf.ByName("firmware")

	in := func(id []byte) past.Value {
		return past.To(cellFd, protoreflect.ValueOfMessage(app.Cell_builder{Id: id}.Build().ProtoReflect()))
	}
	modeOf := func(v app.Mode) past.Value {
		return past.To(mode, protoreflect.ValueOfEnum(protoreflect.EnumNumber(v)))
	}
	about := func(subject pdid.Id, v proto.Message) past.Event { return past.Event{Subject: subject, Value: v} }
	moveR := moved(r.GetId())
	started := app.MachineStarted_builder{Machine: m.GetId()}.Build()
	serviced := app.MachineServiced_builder{Machine: m.GetId()}.Build()
	// A correction of when an event happened, and a retraction of one that
	// never did: one message, of two kinds.
	correcting := func(subject, ev pdid.Id, at time.Time) past.Event {
		return past.Event{Subject: subject, Kind: "correct", Value: app.Corrected_builder{Event: ev.Bytes(), At: timestamppb.New(at)}.Build()}
	}
	retracting := func(subject, ev pdid.Id) past.Event {
		return past.Event{Subject: subject, Kind: "retract", Value: app.Corrected_builder{Event: ev.Bytes()}.Build()}
	}

	// The robot's cell: A from 9/1, B from 9/5, that move corrected on 9/8 to
	// have been on 9/4, and A again from 10/15. The window begins in B, by the
	// correction; the two before it go, and with them the events that wrote
	// nothing else.
	e1 := b.done(t, as, sep(1), about(rid, moveR), sets(rid, sep(1), in(roomA)))
	e2 := b.done(t, as, sep(5), about(rid, moveR), sets(rid, sep(5), in(roomB)))
	f1, f2 := b.factOf(t, ctx, e1, cellFd), b.factOf(t, ctx, e2, cellFd)
	e3 := b.done(t, as, sep(8), correcting(rid, e2, sep(4)), func(ctx context.Context, _ app.Server, _ past.Store, op *past.Op) error {
		return op.Correct(ctx, f2, sep(4))
	})
	e4 := b.done(t, as, oct(15), about(rid, moveR), sets(rid, oct(15), in(roomA)))
	h.fact(f1, false, "the first cell, which a cell known by the cut replaced")
	h.fact(f2, false, "the move the correction superseded")
	h.fact(b.factOf(t, ctx, e3, cellFd), true, "the cell the window begins in")
	h.fact(b.factOf(t, ctx, e4, cellFd), true, "the cell now")
	h.event(e1, false, "the first move")
	h.event(e2, false, "the move that was corrected")
	h.event(e3, true, "the correction, which wrote the cell the window begins in")
	h.event(e4, true, "the move after the cut")

	// A model replaced by one written down only after the cut, from a moment
	// before it: as known at the cut, the machine was still the first.
	m1 := b.done(t, as, sep(1), about(mid, started), sets(mid, sep(1), past.To(model, protoreflect.ValueOfString("X-1"))))
	m2 := b.done(t, as, oct(15), about(mid, started), sets(mid, oct(1), past.To(model, protoreflect.ValueOfString("X-2"))))
	h.fact(b.factOf(t, ctx, m1, model), true, "a model whose next was known only after the cut")
	h.fact(b.factOf(t, ctx, m2, model), true, "the model known after the cut")
	h.event(m1, true, "the first model")
	h.event(m2, true, "the second model")

	// Hours whose next was retracted after the cut: as known now, the first
	// hours are what the window begins in, and as known at the cut the
	// second.
	h1 := b.done(t, as, sep(1), about(mid, started), sets(mid, sep(1), past.To(hours, protoreflect.ValueOfInt64(1))))
	h2 := b.done(t, as, sep(3), about(mid, started), sets(mid, sep(3), past.To(hours, protoreflect.ValueOfInt64(2))))
	h2f := b.factOf(t, ctx, h2, hours)
	h3 := b.done(t, as, oct(15), retracting(mid, h2), func(ctx context.Context, _ app.Server, _ past.Store, op *past.Op) error {
		return op.Retract(ctx, h2f)
	})
	h.fact(b.factOf(t, ctx, h1, hours), true, "hours whose next was retracted after the cut")
	h.fact(h2f, true, "hours retracted after the cut")
	h.event(h1, true, "the first hours")
	h.event(h2, true, "the second hours")
	h.event(h3, true, "the retraction, after the cut")

	// A load whose next begins after the cut, though it was known long before.
	l1 := b.done(t, as, sep(1), about(mid, started), sets(mid, sep(1), past.To(load, protoreflect.ValueOfFloat64(0.5))))
	l2 := b.done(t, as, sep(2), about(mid, started), sets(mid, oct(15), past.To(load, protoreflect.ValueOfFloat64(0.7))))
	h.fact(b.factOf(t, ctx, l1, load), true, "a load whose next begins after the cut")
	h.fact(b.factOf(t, ctx, l2, load), true, "the load from after the cut")
	h.event(l1, true, "the first load")
	h.event(l2, true, "the second load")

	// Modes: idle, then in service -- which is kept, though run replaced it --
	// then running; and in service again, retracted before the cut, which a
	// correction says never held and so is no record of anything.
	d1 := b.done(t, as, sep(1), about(mid, started), sets(mid, sep(1), modeOf(app.Mode_MODE_IDLE)))
	d2 := b.done(t, as, sep(2), about(mid, started), sets(mid, sep(2), modeOf(app.Mode_MODE_SERVICE)))
	d3 := b.done(t, as, sep(3), about(mid, started), sets(mid, sep(3), modeOf(app.Mode_MODE_RUN)))
	d4 := b.done(t, as, sep(4), about(mid, started), sets(mid, sep(4), modeOf(app.Mode_MODE_SERVICE)))
	d4f := b.factOf(t, ctx, d4, mode)
	d5 := b.done(t, as, sep(6), retracting(mid, d4), func(ctx context.Context, _ app.Server, _ past.Store, op *past.Op) error {
		return op.Retract(ctx, d4f)
	})
	h.fact(b.factOf(t, ctx, d1, mode), false, "idle, which service replaced")
	h.fact(b.factOf(t, ctx, d2, mode), true, "service, which no window ends")
	h.fact(b.factOf(t, ctx, d3, mode), true, "the mode the window begins in")
	h.fact(d4f, false, "service retracted before the cut")
	h.event(d1, false, "idle")
	h.event(d2, true, "service, kept")
	h.event(d3, true, "running")
	h.event(d4, false, "the service that never held")
	h.event(d5, false, "its retraction, which only the retracted fact named")

	// An event of a kind no window ends, and one of a kind any does, neither
	// naming anything.
	h.event(b.done(t, as, sep(1), about(mid, serviced), nil), true, "a service, a kind kept")
	h.event(b.done(t, as, sep(1), about(mid, started), nil), false, "a start that wrote nothing")

	// Stints: one that ended before the cut, which the app takes first; one
	// still open, and one that ends after the cut.
	stint := func(from time.Time, to *time.Time) step {
		return func(ctx context.Context, next app.Server, _ past.Store, op *past.Op) error {
			req := app.StintAddRequest_builder{
				Tenant:    app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				RobotId:   r.GetId(),
				ValidFrom: timestamppb.New(from),
				EventId:   op.Id().Bytes(),
			}.Build()
			if to != nil {
				req.SetValidTo(timestamppb.New(*to))
			}
			_, err := next.Stint().Add(ctx, req)
			return err
		}
	}
	h.event(b.done(t, as, sep(1), about(rid, moveR), stint(sep(1), ptr(sep(5)))), false, "a stint that ended before the cut")
	h.event(b.done(t, as, sep(5), about(rid, moveR), stint(sep(5), nil)), true, "a stint still open")
	h.event(b.done(t, as, sep(6), about(rid, moveR), stint(sep(6), ptr(oct(15)))), true, "a stint that ends after the cut")

	// An operation over two things, one of which wrote what the machine is
	// tagged now: that one stays, and the operation with it; the other goes.
	var c1, c2 pdid.Id
	p := b.done(t, as, sep(2), about(rid, moveR), func(ctx context.Context, _ app.Server, _ past.Store, op *past.Op) error {
		v := m.ProtoReflect().NewField(tags)
		v.List().Append(protoreflect.ValueOfString("x"))
		cs, err := op.Batch(ctx, []past.Child{
			{Event: about(mid, started), Facts: []past.Fact{{Subject: mid, Values: []past.Value{past.To(tags, v)}}}},
			{Event: about(mid, started)},
		})
		if err != nil {
			return err
		}
		c1, c2 = cs[0].Id(), cs[1].Id()
		return nil
	})
	h.fact(b.factOf(t, ctx, c1, tags), true, "the tags now")
	h.event(p, true, "an operation one of whose children stays")
	h.event(c1, true, "the child that wrote the tags")
	h.event(c2, false, "the child that wrote nothing")

	// An operation that called another, whose child wrote the firmware the
	// machine has now: all three stay, the chain kept up from its last link.
	var n, g pdid.Id
	q := b.done(t, as, sep(3), about(mid, started), func(ctx context.Context, _ app.Server, s past.Store, _ *past.Op) error {
		ctx, inner, err := past.Begin(ctx, s, about(mid, started))
		if err != nil {
			return err
		}
		child, err := inner.Child(ctx, about(mid, started))
		if err != nil {
			return err
		}
		n, g = inner.Id(), child.Id()
		return child.Set(ctx, past.Fact{Subject: mid, Values: []past.Value{past.To(firmware, protoreflect.ValueOfBytes([]byte{9}))}})
	})
	h.fact(b.factOf(t, ctx, g, firmware), true, "the firmware now")
	h.event(q, true, "an operation a chain of others kept")
	h.event(n, true, "the operation it called")
	h.event(g, true, "that one's child, which wrote the firmware")

	// And a chain that wrote nothing, each link written down a minute after
	// the one it is part of: the first is read while the second still names
	// it, and goes once the last has.
	var y, w pdid.Id
	z := b.done(t, as, sep(4), about(mid, started), func(ctx context.Context, _ app.Server, s past.Store, _ *past.Op) error {
		ctx, inner, err := past.Begin(ctx, past.WithClock(s, func() time.Time { return sep(4).Add(time.Minute) }), about(mid, started))
		if err != nil {
			return err
		}
		_, last, err := past.Begin(ctx, past.WithClock(s, func() time.Time { return sep(4).Add(2 * time.Minute) }), about(mid, started))
		if err != nil {
			return err
		}
		y, w = inner.Id(), last.Id()
		return nil
	})
	h.event(z, false, "an operation of a chain that wrote nothing")
	h.event(y, false, "the operation it called")
	h.event(w, false, "the one that called")

	// An operation whose child is of a kind no window ends.
	var kc pdid.Id
	kp := b.done(t, as, sep(5), about(rid, moveR), func(ctx context.Context, _ app.Server, _ past.Store, op *past.Op) error {
		c, err := op.Child(ctx, about(mid, serviced))
		if err != nil {
			return err
		}
		kc = c.Id()
		return nil
	})
	h.event(kp, true, "an operation whose child is kept")
	h.event(kc, true, "its child, a service")

	// Before the cut on one side and after it on the other.
	h.event(b.done(t, as, oct(15), past.Event{At: sep(1), Value: started}, nil), true, "a start written down after the cut")
	h.event(b.done(t, as, sep(1), past.Event{At: oct(15), Value: started}, nil), true, "a start that happens after the cut")

	return h
}

func ptr[T any](v T) *T { return &v }

// sameExpired asserts two answers of what an expiry took are one answer.
func sameExpired(t *testing.T, want, got past.Expired, msg string) {
	t.Helper()

	require.True(t, want.Before.Equal(got.Before), "%s: before %s, and %s", msg, want.Before, got.Before)
	want.Before, got.Before = time.Time{}, time.Time{}
	require.Equal(t, want, got, msg)
}

// TestAnExpiryIsWhatItsDryRunSays: what a preview counts, taking nothing, is
// what an expiry in a transaction rolled back takes, which is what one
// committed takes -- the rows and the closure of events kept by events that
// stay -- and the dry run leaves everything as it found it.
func TestAnExpiryIsWhatItsDryRunSays(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	h := writeHistory(t, b, ctx)

	want := past.Expired{Tenant: b.Tenant, Before: cutoff, Facts: gone(h.facts), Events: gone(h.events)}

	seen, err := past.Preview(b.as(ctx), b.pastOf(t, b.Drv), b.Tenant, window, expiredOn, exceptions(), going)
	x.NoError(err)
	sameExpired(t, want, seen, "what a preview counts")

	before := b.census(t, ctx)
	dry, stints, err := b.expired(t, ctx, b.Tenant, window, expiredOn, exceptions(), true)
	x.NoError(err)
	sameExpired(t, want, dry, "what a dry run took")
	x.Equal(1, stints)
	x.Equal(before, b.census(t, ctx), "a dry run left something changed")

	done, stints, err := b.expired(t, ctx, b.Tenant, window, expiredOn, exceptions(), false)
	x.NoError(err)
	sameExpired(t, want, done, "what the run took")
	x.Equal(1, stints)
	h.holds(t, b, ctx)
}

// TestTheStateTheWindowBeginsInStays: what the robot and the machine were at
// any moment inside the window reads as it did before the expiry, as known
// now and as known at any moment inside it -- what went is only what nothing
// inside the window reads -- and what they were before the window does not.
func TestTheStateTheWindowBeginsInStays(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	h := writeHistory(t, b, ctx)
	as, s := b.as(ctx), b.pastOf(t, b.Drv)

	state := func(at time.Time, known *time.Time) map[string]string {
		t.Helper()

		sts, err := past.StateAt(as, s, []pdid.Id{h.robot, h.machine}, at, known)
		require.NoError(t, err)
		out := map[string]string{}
		for id, st := range sts {
			for slot, r := range st.Values {
				out[fmt.Sprintf("%s %d %q", id, slot.Field, slot.Key)] = r.Id.String() + " " + r.Value
			}
		}
		return out
	}
	inside := []time.Time{cutoff, oct(12), oct(16), expiredOn}
	read := func() map[string]map[string]string {
		out := map[string]map[string]string{}
		for _, at := range inside {
			out[at.Format("01/02")] = state(at, nil)
			for _, known := range inside {
				out[at.Format("01/02")+" as known "+known.Format("01/02")] = state(at, &known)
			}
		}
		return out
	}

	was, before := read(), state(sep(2), nil)
	_, _, err := b.expired(t, ctx, b.Tenant, window, expiredOn, exceptions(), false)
	x.NoError(err)

	got := read()
	for _, k := range slices.Sorted(maps.Keys(was)) {
		x.Equal(was[k], got[k], "at %s", k)
	}
	x.NotEqual(before, state(sep(2), nil), "nothing before the window went")
}

// TestASecondPassTakesNothing: what a pass took is not there to take again,
// and what it left is what the next leaves -- past more than a page of facts
// and of events, each of which goes a page at a time, and the chains an event
// is kept by, which go a link a time.
func TestASecondPassTakesNothing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	h := writeHistory(t, b, ctx)
	as := b.as(ctx)

	// Six hundred settings, said twice, and six hundred children of an
	// operation that wrote nothing: the first six hundred facts go, and every
	// one of those events, and the ones that wrote what went.
	settings := machineField(t, "settings")
	entries := func(v string) []past.Value {
		var vs []past.Value
		for i := range 600 {
			vs = append(vs, past.Entry(settings, protoreflect.ValueOfString(fmt.Sprintf("k%03d", i)).MapKey(), protoreflect.ValueOfString(v)))
		}
		return vs
	}
	started := app.MachineStarted_builder{Machine: h.machine.Bytes()}.Build()
	b.done(t, as, sep(1), past.Event{Value: started}, sets(h.machine, sep(1), entries("slow")...))
	b.done(t, as, sep(2), past.Event{Value: started}, sets(h.machine, sep(2), entries("fast")...))
	b.done(t, as, sep(1), past.Event{Value: started}, func(ctx context.Context, _ app.Server, _ past.Store, op *past.Op) error {
		cs := make([]past.Child, 600)
		for i := range cs {
			cs[i] = past.Child{Event: past.Event{Value: started}}
		}
		_, err := op.Batch(ctx, cs)
		return err
	})

	first, _, err := b.expired(t, ctx, b.Tenant, window, expiredOn, exceptions(), false)
	x.NoError(err)
	x.Equal(gone(h.facts)+600, first.Facts)
	x.Equal(gone(h.events)+1+601, first.Events)
	h.holds(t, b, ctx)

	left := b.census(t, ctx)
	second, stints, err := b.expired(t, ctx, b.Tenant, window, expiredOn, exceptions(), false)
	x.NoError(err)
	sameExpired(t, past.Expired{Tenant: b.Tenant, Before: cutoff}, second, "the second pass")
	x.Zero(stints)
	x.Equal(left, b.census(t, ctx), "and wrote nothing, a receipt among it")
}

// machineField is the field `name` of a machine.
func machineField(t *testing.T, name string) protoreflect.FieldDescriptor {
	t.Helper()

	fd := (&app.Machine{}).ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd)

	return fd
}

// TestTheReceiptSaysHowManyAndNoMore: an expiry that took something records
// the app's receipt of it, once, as the deployment's own work: how many of
// what, and from before when -- the past's rows and the app's own stints --
// and nothing of what any of them said.
func TestTheReceiptSaysHowManyAndNoMore(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	writeHistory(t, b, ctx)

	receipts := func() []*ent.Event {
		t.Helper()
		vs, err := b.Ent.Event.Query().Where(entevent.Kind("past.expire")).All(ctx)
		x.NoError(err)
		return vs
	}

	done, stints, err := b.expired(t, ctx, b.Tenant, window, expiredOn, exceptions(), false)
	x.NoError(err)
	vs := receipts()
	x.Len(vs, 1)

	v := vs[0]
	x.Nil(v.ActorId, "by the deployment, and nobody of the tenant's")
	x.Equal(b.Tenant.Uuid(), v.TenantId)
	x.Nil(v.SubjectId)
	x.Empty(v.Reason)

	got := &app.PastExpired{}
	x.NoError(v.Value.UnmarshalTo(got))
	want := app.PastExpired_builder{
		Before: timestamppb.New(cutoff),
		Facts:  uint32(done.Facts),
		Events: uint32(done.Events),
		Stints: uint32(stints),
	}.Build()
	x.True(proto.Equal(want, got), "the receipt: %v, and it says %v", want, got)
	x.Positive(got.GetFacts())
	x.Positive(got.GetEvents())

	// The whole of it, as its row keeps it.
	js, err := protojson.Marshal(v.Value)
	x.NoError(err)
	x.JSONEq(fmt.Sprintf(`{"@type": "type.googleapis.com/app.PastExpired", "before": %q, "facts": %d, "events": %d, "stints": %d}`,
		cutoff.Format(time.RFC3339), done.Facts, done.Events, stints), string(js))

	// Said when it is read, as every event is.
	x.Equal(fmt.Sprintf("The deployment let go of what was over before Oct 10 00:00 (facts: %d, events: %d, stints: %d)", done.Facts, done.Events, stints),
		b.said(t, b.as(ctx), core.Sentences(b.Walled.Event()), "en", time.UTC, pdid.Id(v.Id)))

	// And a pass that took nothing says so with nothing.
	_, _, err = b.expired(t, ctx, b.Tenant, window, expiredOn, exceptions(), false)
	x.NoError(err)
	x.Len(receipts(), 1)
}

// TestAHoldKeepsEverything: while a legal hold is on, whatever the window
// says, nothing goes -- not a row, and no receipt is asked for -- and a window
// of forever takes nothing either; a preview says the same, with the hold.
func TestAHoldKeepsEverything(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	writeHistory(t, b, ctx)

	asked := false
	saying := exceptions()
	saying.Receipt = func(past.Expired) proto.Message {
		asked = true
		return nil
	}
	held := past.Window{Keep: window.Keep, Holds: []string{"case 7"}}

	before := b.census(t, ctx)
	got, _, err := b.expired(t, ctx, b.Tenant, held, expiredOn, saying, false)
	x.NoError(err)
	sameExpired(t, past.Expired{Tenant: b.Tenant, Before: cutoff, Held: []string{"case 7"}}, got, "under a hold")
	x.Equal(before, b.census(t, ctx), "something went under a hold")
	x.False(asked, "a receipt was asked for under a hold")

	got, _, err = b.expired(t, ctx, b.Tenant, past.Window{}, expiredOn, saying, false)
	x.NoError(err)
	sameExpired(t, past.Expired{Tenant: b.Tenant}, got, "kept forever")
	x.Equal(before, b.census(t, ctx), "something went of a past kept forever")
	x.False(asked)

	seen, err := past.Preview(b.as(ctx), b.pastOf(t, b.Drv), b.Tenant, held, expiredOn, exceptions(), going)
	x.NoError(err)
	sameExpired(t, past.Expired{Tenant: b.Tenant, Before: cutoff, Held: []string{"case 7"}}, seen, "a preview under a hold")
}

// TestAnExceptionOutlivesTheWindow: a fact of a value no window ends stays
// when a later one replaces it, and an event of a kind no window ends stays
// though nothing names it -- and keeps the operation it is part of -- while
// one the app keeps nothing of goes. A value an exception is written with is
// held to how a fact writes it.
func TestAnExceptionOutlivesTheWindow(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	h := writeHistory(t, b, ctx)

	plain, _, err := b.expired(t, ctx, b.Tenant, window, expiredOn, past.Expiry{}, true)
	x.NoError(err)
	x.Equal(gone(h.facts)+1, plain.Facts, "service, which run replaced")
	x.Equal(gone(h.events)+4, plain.Events, "the service it wrote, the two services, and the operation one is part of")

	done, _, err := b.expired(t, ctx, b.Tenant, window, expiredOn, exceptions(), false)
	x.NoError(err)
	x.Equal(gone(h.facts), done.Facts)
	x.Equal(gone(h.events), done.Events)
	h.holds(t, b, ctx)

	t.Run("and one written as a fact does not write its value is refused", func(t *testing.T) {
		x := pdtest.NewX(t)

		named := exceptions()
		named.KeepFacts[0].Values = []string{`"MODE_SERVICE"`}
		_, _, err := b.expired(t, ctx, b.Tenant, window, expiredOn, named, true)
		x.ErrCode(codes.InvalidArgument, err)
		x.ErrorContains(err, "app.Machine.mode")
	})
}

// TestAnEventARowStillNamesStays: an event is taken only once no row that
// stays names it -- a stint of the app's, a fact, an event that is part of it
// -- so the app takes its own rows first, in the same transaction, and an event
// only they named goes with them; done the other way round, it stays a pass
// longer. A preview is told what of the app's rows goes, and counts the event
// with them.
func TestAnEventARowStillNamesStays(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	h := writeHistory(t, b, ctx)

	var first past.Expired
	err := pd.PastIn(b.Ungated, b.Drv, b.Contents)(ctx, b.Tenant, func(ctx context.Context, s past.Store) error {
		var err error
		first, err = past.Expire(ctx, s, b.Tenant, window, expiredOn, exceptions())
		if err != nil {
			return err
		}
		return errDry
	})
	x.ErrorIs(err, errDry)
	x.Equal(gone(h.events)-1, first.Events, "the stint that ended was not taken first, and still named its event")

	seen, err := past.Preview(b.as(ctx), b.pastOf(t, b.Drv), b.Tenant, window, expiredOn, exceptions(), nil)
	x.NoError(err)
	x.Equal(gone(h.events)-1, seen.Events, "a preview told nothing of the stints counts every one as staying")

	done, _, err := b.expired(t, ctx, b.Tenant, window, expiredOn, exceptions(), false)
	x.NoError(err)
	x.Equal(gone(h.events), done.Events)
	h.holds(t, b, ctx)
}

// TestWhatAnExpiryTookIsOnTheTrail: every row an expiry took was taken past
// the servers and is on the trail as taken -- by the deployment, filed under
// the tenant, as `payday.past.expire` -- so a check of the rows finds each as
// the trail says it is: gone.
func TestWhatAnExpiryTookIsOnTheTrail(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)
	h := writeHistory(t, b, ctx)

	_, _, err := b.expired(t, ctx, b.Tenant, window, expiredOn, exceptions(), false)
	x.NoError(err)

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)
	x.Positive(got.Matches)

	for _, vs := range []map[pdid.Id]kept{h.facts, h.events} {
		for id, v := range vs {
			if v.stays {
				continue
			}

			lines, err := b.Ent.Audit.Query().
				Where(entaudit.ObjectId(id.Uuid())).
				Order(entaudit.ByDateCreated(), entaudit.ById()).
				All(ctx)
			x.NoError(err)
			x.NotEmpty(lines, v.why)
			last := lines[len(lines)-1]
			x.Equal(past.ExpireAction, last.Action, "the last word of %s", v.why)
			x.Equal(uuid.Nil(), last.ActorId, "by the deployment")
			x.Equal(b.Tenant.Uuid(), last.TenantId, "filed under the tenant")
		}
	}
}

// TestAnExpiryIsOfOneTenantAndInATransaction: an expiry is refused outside a
// transaction -- what it deletes and what it puts on the trail commit
// together -- and framed in another tenant than the one it expires, since that
// is what the trail files what it took under. A preview is of a tenant the
// caller may see.
func TestAnExpiryIsOfOneTenantAndInATransaction(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)
	writeHistory(t, b, ctx)
	before := b.census(t, ctx)

	_, err := past.Expire(b.as(ctx), b.pastOf(t, b.Drv), b.Tenant, window, expiredOn, exceptions())
	x.ErrorIs(err, past.ErrNotInTx)

	elsewhere := b.elsewhere(t, ctx)
	before[3], _ = b.Ent.Audit.Query().Count(ctx) // the other tenant and its holder, on the trail
	x.ErrCode(codes.FailedPrecondition, b.operate(t, elsewhere, func(ctx context.Context, s past.Store) error {
		_, err := past.Expire(ctx, s, b.Tenant, window, expiredOn, exceptions())
		return err
	}))
	x.Equal(before, b.census(t, ctx), "something was taken")

	_, err = past.Preview(elsewhere, b.pastOf(t, b.Drv), b.Tenant, window, expiredOn, exceptions(), going)
	x.ErrCode(codes.NotFound, err)
}

// TestTwoExpiriesOfATenantTakeTurns: an expiry holds the tenant until its
// transaction ends, so one begun while another runs waits for it, and then
// reads what it left -- nothing to take -- rather than taking the same rows
// beside it and putting each on the trail twice. On PostgreSQL that is the
// expiry's lock; SQLite has one writer at a time.
func TestTwoExpiriesOfATenantTakeTurns(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	writeHistory(t, b, ctx)

	as := frame.Into(ctx, frame.New(pdid.Nil, b.Tenant, frame.Grant{}).WithScope(frame.Only(b.Tenant)))
	drv, tx, err := dialect.BeginTx(as, b.Drv)
	x.NoError(err)
	defer tx.Rollback()
	s, err := pd.Past(b.Ungated, drv, b.Contents)
	x.NoError(err)
	first, err := past.Expire(as, s, b.Tenant, window, expiredOn, exceptions())
	x.NoError(err)
	x.Positive(first.Total())

	type answer struct {
		v   past.Expired
		err error
	}
	done := make(chan answer, 1)
	go func() {
		var v past.Expired
		err := pd.PastIn(b.Ungated, b.Drv, b.Contents)(ctx, b.Tenant, func(ctx context.Context, s past.Store) error {
			var err error
			v, err = past.Expire(ctx, s, b.Tenant, window, expiredOn, exceptions())
			return err
		})
		done <- answer{v, err}
	}()

	select {
	case v := <-done:
		x.Failf("the second did not wait for the first", "%+v", v)
	case <-time.After(300 * time.Millisecond):
	}
	x.NoError(tx.Commit())

	select {
	case v := <-done:
		x.NoError(v.err)
		x.Zero(v.v.Total(), "the second took again what the first took")
	case <-time.After(10 * time.Second):
		x.Fail("the second never came back")
	}

	n, err := b.Ent.Audit.Query().Where(entaudit.Action(past.ExpireAction)).Count(ctx)
	x.NoError(err)
	x.Equal(first.Total(), n, "a row taken is on the trail once")
}

// TestTheExpirerExpiresEveryTenantWithAPast: the loop a deployment runs, on
// what `pd gen` writes for it -- every tenant with a past, each in a
// transaction of its own as the deployment's work in it -- takes what each
// tenant's window no longer reaches, and nothing of a tenant whose window
// cannot be read.
func TestTheExpirerExpiresEveryTenantWithAPast(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	writeHistory(t, b, ctx)

	// Two tenants more, each with an event of long ago that names nothing.
	others := map[pdid.Id]bool{}
	for _, alias := range []string{"other", "broken"} {
		v, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias}.Build())
		x.NoError(err)
		tid := idOf(t, v.GetId())
		hv, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: app.TenantRef_builder{Id: v.GetId()}.Build()}.Build())
		x.NoError(err)
		as := frame.Into(ctx, frame.New(idOf(t, hv.GetId()), tid, frame.Whole()).WithScope(frame.Only(tid)))
		b.done(t, as, sep(1), past.Event{Value: app.MachineStarted_builder{}.Build()}, nil)
		others[tid] = alias == "broken"
	}
	count := func(tenant pdid.Id) int {
		t.Helper()
		n, err := b.Ent.Event.Query().Where(entevent.TenantId(tenant.Uuid())).Count(ctx)
		x.NoError(err)
		return n
	}
	before := map[pdid.Id]int{b.Tenant: count(b.Tenant)}
	for tid := range others {
		before[tid] = count(tid)
	}

	e := past.Expirer{
		Tenants: pd.PastTenants(b.Ent),
		Window: func(_ context.Context, tenant pdid.Id) (past.Window, error) {
			if others[tenant] {
				return past.Window{}, errors.New("its contract could not be read")
			}
			// A day, from now: everything of 2026 written above is long over.
			return past.Window{Keep: 24 * time.Hour}, nil
		},
		In:     pd.PastIn(b.Ungated, b.Drv, b.Contents),
		Expiry: exceptions(),
	}
	x.Equal("payday.past.expire", e.SpinName())
	x.NoError(e.Pass(ctx))

	for tid, broken := range others {
		if broken {
			x.Equal(before[tid], count(tid), "a tenant whose window could not be read lost something")
			continue
		}
		x.Less(count(tid), before[tid], "a tenant was not expired")
	}
	x.Less(count(b.Tenant), before[b.Tenant], "the first tenant was not expired")
}
