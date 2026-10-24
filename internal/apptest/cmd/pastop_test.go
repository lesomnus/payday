package cmd_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/payday/internal/apptest"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	entevent "github.com/lesomnus/payday/internal/apptest/internal/ent/event"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// The past, as the `past` package writes it through `pd.Past` (payday#151):
// an operation's event in its transaction, the children it is made of, and
// the facts it writes -- each through the app's own servers, so that the wall,
// the trail and every recorder meet it as they meet any other write.

// pastOf is the app's past on `drv`, through the stack a caller reaches.
func (b *built) pastOf(t *testing.T, drv dialect.Driver) past.Store {
	t.Helper()

	s, err := pd.Past(b.Walled, drv, b.Contents)
	require.NoError(t, err)

	return s
}

// operate runs `f` as one operation in a transaction of its own, committed
// when `f` answers nothing and rolled back when it answers anything.
func (b *built) operate(t *testing.T, ctx context.Context, f func(ctx context.Context, s past.Store) error) error {
	t.Helper()

	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	require.NoError(t, err)
	defer tx.Rollback()

	if err := f(ctx, b.pastOf(t, drv)); err != nil {
		return err
	}

	return tx.Commit()
}

// arm puts a robot in the tenant, as the deployment would.
func (b *built) arm(t *testing.T, ctx context.Context, alias string) *app.Robot {
	t.Helper()

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	require.NoError(t, err)

	return v
}

func eventOf(t *testing.T, ctx context.Context, s past.Store, id pdid.Id) past.EventRow {
	t.Helper()

	vs, err := past.Events(ctx, s, past.EventQuery{Ids: []pdid.Id{id}})
	require.NoError(t, err)
	require.Len(t, vs, 1)

	return vs[0]
}

func moved(robot []byte) *app.RobotMoved { return app.RobotMoved_builder{Robot: robot}.Build() }

// TestAnEventAndWhatItIsAboutCommitTogether: an event is written in the
// transaction of its operation and nowhere else -- refused outside one -- so
// a refusal after it takes it along with every fact it wrote.
func TestAnEventAndWhatItIsAboutCommitTogether(t *testing.T) {
	b, ctx := build(t)
	as := b.as(ctx)
	r := b.arm(t, ctx, "arm-01")
	id := idOf(t, r.GetId())
	alias := r.ProtoReflect().Descriptor().Fields().ByName("alias")

	t.Run("refused outside a transaction", func(t *testing.T) {
		_, _, err := past.Begin(as, b.pastOf(t, b.Drv), past.Event{Value: moved(r.GetId())})
		require.ErrorIs(t, err, past.ErrNotInTx)
	})

	t.Run("refused with nobody to file it under", func(t *testing.T) {
		x := pdtest.NewX(t)
		begin := func(ctx context.Context, s past.Store) error {
			_, _, err := past.Begin(ctx, s, past.Event{Value: moved(r.GetId())})
			return err
		}

		x.ErrCode(codes.Unauthenticated, b.operate(t, ctx, begin))

		// A principal of the configuration's, which is of no tenant.
		f := frame.New(pdid.Nil, pdid.Nil, frame.Whole()).WithScope(frame.Everything)
		x.ErrCode(codes.FailedPrecondition, b.operate(t, frame.Into(ctx, f), begin))
	})

	t.Run("and a refusal takes the event and its facts with it", func(t *testing.T) {
		x := require.New(t)

		refused := errors.New("refused")
		var ev pdid.Id
		err := b.operate(t, as, func(ctx context.Context, s past.Store) error {
			ctx, op, err := past.Begin(ctx, s, past.Event{Subject: id, Value: moved(r.GetId())})
			x.NoError(err)
			ev = op.Id()

			x.NoError(op.Set(ctx, past.Fact{Subject: id, Values: []past.Value{past.Cleared(alias)}}))
			return refused
		})
		x.ErrorIs(err, refused)

		n, err := b.Ent.Event.Query().Where(entevent.Id(ev.Uuid())).Count(ctx)
		x.NoError(err)
		x.Zero(n)
		sts, err := past.Facts(as, b.pastOf(t, b.Drv), past.FactQuery{Subjects: []pdid.Id{id}})
		x.NoError(err)
		x.Empty(sts)
	})

	t.Run("and one the past refuses, as much", func(t *testing.T) {
		x := require.New(t)

		var ev pdid.Id
		err := b.operate(t, as, func(ctx context.Context, s past.Store) error {
			ctx, op, err := past.Begin(ctx, s, past.Event{Subject: id, Value: moved(r.GetId())})
			x.NoError(err)
			ev = op.Id()

			// A fact of a row that is not there.
			return op.Set(ctx, past.Fact{Subject: pdid.New(pd.RobotDomain), Values: []past.Value{past.Cleared(alias)}})
		})
		pdtest.NewX(t).ErrCode(codes.NotFound, err)

		n, err := b.Ent.Event.Query().Where(entevent.Id(ev.Uuid())).Count(ctx)
		x.NoError(err)
		x.Zero(n)
	})

	t.Run("and committed, both are there", func(t *testing.T) {
		x := require.New(t)

		var ev pdid.Id
		x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
			ctx, op, err := past.Begin(ctx, s, past.Event{Subject: id, Value: moved(r.GetId())})
			if err != nil {
				return err
			}
			ev = op.Id()

			return op.Set(ctx, past.Fact{Subject: id, Values: []past.Value{past.Cleared(alias)}})
		}))

		sts, err := past.Facts(as, b.pastOf(t, b.Drv), past.FactQuery{Subjects: []pdid.Id{id}})
		x.NoError(err)
		x.Len(sts, 1)
		x.Equal(ev, sts[0].Event)
	})
}

// TestAnEventSaysWhatItIs: its kind from what its message declares -- the
// full name of one that declares none -- who did it and in which tenant from
// the frame, the trace from the span, and the message itself read back whole
// from the column it is kept in, jsonb on PostgreSQL.
func TestAnEventSaysWhatItIs(t *testing.T) {
	b, ctx := build(t)
	as := b.as(ctx)
	m := machine(t, b, as)
	mid := idOf(t, m.GetId())

	traceId := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	traced := trace.ContextWithSpanContext(as, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceId,
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	}))

	sent := app.MachineServiced_builder{
		Machine: m.GetId(),
		By:      b.Holder.Bytes(),
		Mode:    app.Mode_MODE_SERVICE,
		Parts:   []*app.MachineServiced_Part{app.MachineServiced_Part_builder{Serial: "P-1", Count: 2}.Build()},
		Swapped: map[string]*app.MachineServiced_Part{"left": app.MachineServiced_Part_builder{Serial: "P-0", Count: 1}.Build()},
		Due:     timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
	}.Build()
	happened := time.Date(2026, 10, 1, 9, 0, 0, 123456789, time.UTC)

	var serviced, started pdid.Id
	require.NoError(t, b.operate(t, traced, func(ctx context.Context, s past.Store) error {
		_, op, err := past.Begin(ctx, s, past.Event{Subject: mid, At: happened, Reason: "due", Value: sent})
		if err != nil {
			return err
		}
		serviced = op.Id()

		_, op, err = past.Begin(past.Detach(ctx), s, past.Event{Value: app.MachineStarted_builder{Machine: m.GetId()}.Build()})
		if err != nil {
			return err
		}
		started = op.Id()

		return nil
	}))

	s := b.pastOf(t, b.Drv)

	t.Run("what the declaration says, and the frame, and the span", func(t *testing.T) {
		x := require.New(t)

		v := eventOf(t, as, s, serviced)
		x.Equal("machine.service", v.Kind)
		x.Equal(b.Tenant, v.Tenant)
		x.Equal(b.Holder, v.Actor)
		x.Equal(mid, v.Subject)
		x.Equal("due", v.Reason)
		x.Equal(traceId[:], v.Trace)
		x.True(happened.Truncate(time.Microsecond).Equal(v.At), "%v, to the microsecond every database keeps", v.At)
		x.False(v.Recorded.Before(v.At))

		typ, ok := pd.EventKinds[v.Kind]
		x.True(ok)
		got := typ.New().Interface()
		x.NoError(v.Value.UnmarshalTo(got))
		x.True(proto.Equal(sent, got), "%v", got)

		x.Equal("app.MachineStarted", eventOf(t, as, s, started).Kind, "a message that names no kind is written under its full name")
	})

	t.Run("and a message that is no event is refused", func(t *testing.T) {
		err := b.operate(t, as, func(ctx context.Context, s past.Store) error {
			_, _, err := past.Begin(ctx, s, past.Event{Value: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build()})
			return err
		})
		pdtest.NewX(t).ErrCode(codes.InvalidArgument, err)
	})
}

// TestAnEventOfAMessageOfSeveralKindsSaysWhichItIs: a correction and a
// retraction hold one message, and an event of it is written under the kind
// it says -- begun, as a child and in a batch alike -- and read back by that
// kind into the message both hold. One that says none of its kinds, or a
// kind its message does not hold, is refused, and a batch with one such child
// writes none of the others. A message of one kind may say it, and may not say
// another.
func TestAnEventOfAMessageOfSeveralKindsSaysWhichItIs(t *testing.T) {
	b, ctx := build(t)
	as := b.as(ctx)
	s := b.pastOf(t, b.Drv)

	of := func(kind string) past.Event {
		return past.Event{Kind: kind, Value: app.Corrected_builder{Event: pdid.New(pd.EventDomain).Bytes()}.Build()}
	}

	t.Run("begun, as a child and in a batch", func(t *testing.T) {
		x := require.New(t)

		var op, child pdid.Id
		var batch []*past.Op
		x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
			_, o, err := past.Begin(ctx, s, of("correct"))
			if err != nil {
				return err
			}
			op = o.Id()

			c, err := o.Child(ctx, of("retract"))
			if err != nil {
				return err
			}
			child = c.Id()

			batch, err = o.Batch(ctx, []past.Child{
				{Event: of("retract")},
				{Event: of("correct")},
				{Event: past.Event{Kind: "robot.move", Value: moved(nil)}},
				{Event: past.Event{Value: moved(nil)}},
			})
			return err
		}))

		x.Equal("correct", eventOf(t, as, s, op).Kind)
		x.Equal("retract", eventOf(t, as, s, child).Kind)
		x.Equal("retract", eventOf(t, as, s, batch[0].Id()).Kind)
		x.Equal("correct", eventOf(t, as, s, batch[1].Id()).Kind)
		x.Equal("robot.move", eventOf(t, as, s, batch[2].Id()).Kind, "the one kind of a message of one, said")
		x.Equal("robot.move", eventOf(t, as, s, batch[3].Id()).Kind, "and not said")

		for _, id := range []pdid.Id{op, child} {
			v := eventOf(t, as, s, id)
			typ, ok := pd.EventKinds[v.Kind]
			x.True(ok)
			got := typ.New().Interface()
			x.NoError(v.Value.UnmarshalTo(got))
			x.Equal(protoreflect.FullName("app.Corrected"), got.ProtoReflect().Descriptor().FullName(), "read back by its kind")
		}
	})

	for name, c := range map[string]struct {
		ev   past.Event
		says string
	}{
		"none of its kinds": {of(""),
			`app.Corrected is what events of kinds "correct" and "retract" hold, and an event of it says which it is: past.Event{Kind: "correct", ...}`},
		"a kind of another message": {of("robot.move"),
			`app.Corrected is what events of kinds "correct" and "retract" hold, and not of "robot.move"`},
		"a kind of no message": {of("correct.later"),
			`app.Corrected is what events of kinds "correct" and "retract" hold, and not of "correct.later"`},
		"another kind than its one": {past.Event{Kind: "retract", Value: moved(nil)},
			`app.RobotMoved is what events of kind "robot.move" hold, and not of "retract"`},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			pdtest.NewX(t).ErrCode(codes.InvalidArgument, b.operate(t, as, func(ctx context.Context, s past.Store) error {
				_, _, err := past.Begin(ctx, s, c.ev)
				require.ErrorContains(t, err, c.says)
				return err
			}))

			pdtest.NewX(t).ErrCode(codes.InvalidArgument, b.operate(t, as, func(ctx context.Context, s past.Store) error {
				_, o, err := past.Begin(ctx, s, past.Event{Value: moved(nil)})
				require.NoError(t, err)

				_, err = o.Child(ctx, c.ev)
				require.ErrorContains(t, err, c.says)
				return err
			}))

			pdtest.NewX(t).ErrCode(codes.InvalidArgument, b.operate(t, as, func(ctx context.Context, s past.Store) error {
				_, o, err := past.Begin(ctx, s, past.Event{Value: moved(nil)})
				require.NoError(t, err)

				_, err = o.Batch(ctx, []past.Child{{Event: of("retract")}, {Event: c.ev}})
				require.ErrorContains(t, err, "child 1: ")
				require.ErrorContains(t, err, c.says)

				// In the transaction it was refused in: the child that said
				// its kind was not written either.
				vs, rerr := past.Events(ctx, s, past.EventQuery{Causes: []pdid.Id{o.Id()}})
				require.NoError(t, rerr)
				require.Empty(t, vs, "a batch with a child refused wrote the others")

				return err
			}))
		})
	}
}

// TestAnOperationCalledInsideAnotherIsPartOfIt: in the transaction of the
// operation that called it, an operation's event is caused by that one's; a
// child is, by its own making; one detached is part of nothing; and the
// context an operation left behind once it committed makes nothing part of it.
func TestAnOperationCalledInsideAnotherIsPartOfIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)
	r := b.arm(t, ctx, "arm-01")
	rid := idOf(t, r.GetId())

	var outer, inner, alone, child pdid.Id
	var left context.Context
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		ctx, op, err := past.Begin(ctx, s, past.Event{Value: moved(nil)})
		x.NoError(err)
		outer = op.Id()
		left = ctx

		_, in, err := past.Begin(ctx, s, past.Event{Value: moved(nil)})
		x.NoError(err)
		inner = in.Id()

		_, a, err := past.Begin(past.Detach(ctx), s, past.Event{Value: moved(nil)})
		x.NoError(err)
		alone = a.Id()

		c, err := op.Child(ctx, past.Event{Subject: rid, Value: moved(r.GetId())})
		x.NoError(err)
		child = c.Id()
		x.Equal(op.At(), c.At(), "when the operation was, as it said nothing of its own")

		return nil
	}))

	var later pdid.Id
	x.NoError(b.operate(t, left, func(ctx context.Context, s past.Store) error {
		_, op, err := past.Begin(ctx, s, past.Event{Value: moved(nil)})
		x.NoError(err)
		later = op.Id()

		return nil
	}))

	s := b.pastOf(t, b.Drv)
	x.Equal(outer, eventOf(t, as, s, inner).Cause)
	x.Equal(outer, eventOf(t, as, s, child).Cause)
	x.Equal(rid, eventOf(t, as, s, child).Subject)
	x.Equal(b.Holder, eventOf(t, as, s, child).Actor)
	x.True(eventOf(t, as, s, alone).Cause.IsZero(), "detached")
	x.True(eventOf(t, as, s, outer).Cause.IsZero())
	x.True(eventOf(t, as, s, later).Cause.IsZero(), "an operation of another transaction, on a context the first left behind")

	parts, err := past.Events(as, s, past.EventQuery{Causes: []pdid.Id{outer}})
	x.NoError(err)
	x.ElementsMatch([]pdid.Id{inner, child}, []pdid.Id{parts[0].Id, parts[1].Id})
}

// TestAnOperationSentTwiceHappensOnce: an operation that names itself is
// done once. A copy sent after it is told it happened; one sent while it runs
// waits for it -- at the advisory lock on PostgreSQL, at SQLite's one writer
// -- and is then told the same, rather than failing on the event's key.
func TestAnOperationSentTwiceHappensOnce(t *testing.T) {
	b, ctx := build(t)
	as := b.as(ctx)

	t.Run("after it", func(t *testing.T) {
		x := require.New(t)

		id := pdid.New(pd.EventDomain)
		begin := func(ctx context.Context, s past.Store) error {
			_, op, err := past.Begin(ctx, s, past.Event{Id: id, Value: moved(nil)})
			if err == nil {
				x.Equal(id, op.Id(), "named as it asked")
			}
			return err
		}

		x.NoError(b.operate(t, as, begin))
		x.ErrorIs(b.operate(t, as, begin), past.ErrDone)
	})

	t.Run("while it runs", func(t *testing.T) {
		x := require.New(t)

		id := pdid.New(pd.EventDomain)
		drv, tx, err := dialect.BeginTx(as, b.Drv)
		x.NoError(err)
		defer tx.Rollback()

		_, _, err = past.Begin(as, b.pastOf(t, drv), past.Event{Id: id, Value: moved(nil)})
		x.NoError(err)

		done := make(chan error, 1)
		go func() {
			done <- b.operate(t, as, func(ctx context.Context, s past.Store) error {
				_, _, err := past.Begin(ctx, s, past.Event{Id: id, Value: moved(nil)})
				return err
			})
		}()

		select {
		case err := <-done:
			x.Failf("the copy did not wait for the first", "%v", err)
		case <-time.After(300 * time.Millisecond):
		}

		x.NoError(tx.Commit())
		select {
		case err := <-done:
			x.ErrorIs(err, past.ErrDone)
		case <-time.After(10 * time.Second):
			x.Fail("the copy never came back")
		}
	})

	t.Run("and a name that is not an event's is refused", func(t *testing.T) {
		err := b.operate(t, as, func(ctx context.Context, s past.Store) error {
			_, _, err := past.Begin(ctx, s, past.Event{Id: pdid.New(pd.RobotDomain), Value: moved(nil)})
			return err
		})
		pdtest.NewX(t).ErrCode(codes.InvalidArgument, err)
	})
}

// TestManyChildrenAreWrittenInPages: an operation over fifteen hundred
// robots records an event of its own, a child for each robot and a fact of
// two fields of each, in one transaction -- more than one AddMany carries, so
// in pages -- and a child refused anywhere in it leaves nothing of any.
func TestManyChildrenAreWrittenInPages(t *testing.T) {
	b, ctx := build(t)
	as := b.as(ctx)
	const n = 1500

	reqs := make([]*app.RobotAddRequest, n)
	for i := range reqs {
		reqs[i] = app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Keeper: "keeper@example.com",
		}.Build()
	}
	var robots []*app.Robot
	for lo := 0; lo < n; lo += 1000 {
		vs, err := b.Ungated.Robot().AddMany(ctx, app.RobotAddManyRequest_builder{Items: reqs[lo:min(lo+1000, n)]}.Build())
		require.NoError(t, err)
		robots = append(robots, vs.GetItems()...)
	}

	md := (*app.Robot)(nil).ProtoReflect().Descriptor()
	alias, keeper := md.Fields().ByName("alias"), md.Fields().ByName("keeper")
	children := func(last pdid.Id) []past.Child {
		vs := make([]past.Child, n)
		for i, r := range robots {
			id := idOf(t, r.GetId())
			if i == n-1 && !last.IsZero() {
				id = last
			}
			vs[i] = past.Child{
				Event: past.Event{Subject: id, Value: moved(id.Bytes())},
				Facts: []past.Fact{{Subject: id, Values: []past.Value{
					past.To(alias, protoreflectString("moved")),
					past.To(keeper, protoreflectString("another@example.com")),
				}}},
			}
		}
		return vs
	}

	t.Run("in pages, in one transaction", func(t *testing.T) {
		x := require.New(t)

		var op pdid.Id
		var kids []*past.Op
		x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
			_, o, err := past.Begin(ctx, s, past.Event{Value: moved(nil)})
			if err != nil {
				return err
			}
			op = o.Id()

			kids, err = o.Batch(ctx, children(pdid.Nil))
			return err
		}))
		x.Len(kids, n)

		s := b.pastOf(t, b.Drv)
		evs, err := past.Events(as, s, past.EventQuery{Causes: []pdid.Id{op}})
		x.NoError(err)
		x.Len(evs, n)

		ids := make([]pdid.Id, n)
		for i, v := range kids {
			ids[i] = v.Id()
		}
		sts, err := past.Facts(as, s, past.FactQuery{Events: ids})
		x.NoError(err)
		x.Len(sts, n, "a statement for each child")
		for _, st := range sts {
			x.Len(st.Rows, 2)
			x.Equal(`"moved"`, st.Rows[0].Value)
			x.Equal(past.Withheld, st.Rows[1].Value, "the keeper is a person's")
		}
	})

	t.Run("and one refused takes the rest with it", func(t *testing.T) {
		x := require.New(t)

		other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
		x.NoError(err)
		theirs, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		var op pdid.Id
		err = b.operate(t, as, func(ctx context.Context, s past.Store) error {
			_, o, err := past.Begin(ctx, s, past.Event{Value: moved(nil)})
			x.NoError(err)
			op = o.Id()

			_, err = o.Batch(ctx, children(idOf(t, theirs.GetId())))
			return err
		})
		pdtest.NewX(t).ErrCode(codes.NotFound, err)

		evs, err := past.Events(as, b.pastOf(t, b.Drv), past.EventQuery{Ids: []pdid.Id{op}, Causes: []pdid.Id{op}})
		x.NoError(err)
		x.Empty(evs)
	})
}

// TestASubjectIsOfTheOperationsTenant: an event about a row of another tenant,
// and a fact of one, are refused as not found, whatever the shape the row
// reaches its tenant by -- an edge, a path of them, a stamp, or the tenant
// itself. A row behind no wall is everybody's to be about. An identifier that
// names no row is an event's to be about -- one is recorded before the row it
// makes -- and no fact's, which is a row's.
func TestASubjectIsOfTheOperationsTenant(t *testing.T) {
	b, ctx := build(t)
	as := b.as(ctx)
	tenant := func(id []byte) *app.TenantRef { return app.TenantRef_builder{Id: id}.Build() }
	robotRef := func(id []byte) *app.RobotRef { return app.RobotRef_builder{Id: id}.Build() }

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	require.NoError(t, err)

	type row struct {
		name string
		id   []byte
		v    proto.Message
	}
	must := func(v proto.Message, err error) proto.Message {
		t.Helper()
		require.NoError(t, err)
		return v
	}
	of := func(name string, v proto.Message) row {
		return row{name, v.ProtoReflect().Get(v.ProtoReflect().Descriptor().Fields().ByNumber(1)).Bytes(), v}
	}

	mine := must(b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: tenant(b.Tenant.Bytes()), Alias: "mine"}.Build())).(*app.Robot)
	theirs := must(b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: tenant(other.GetId()), Alias: "theirs"}.Build())).(*app.Robot)

	for _, c := range []struct {
		shape        string
		mine, theirs row
	}{
		{"by an edge", of("robot", mine), of("robot", theirs)},
		{"by a path of them",
			of("joint", must(b.Ungated.Joint().Add(ctx, app.JointAddRequest_builder{Robot: robotRef(mine.GetId()), Alias: "j"}.Build()))),
			of("joint", must(b.Ungated.Joint().Add(ctx, app.JointAddRequest_builder{Robot: robotRef(theirs.GetId()), Alias: "j"}.Build())))},
		{"by a stamp",
			of("reading", must(b.Ungated.Reading().Add(ctx, app.ReadingAddRequest_builder{Robot: robotRef(mine.GetId()), Celsius: 1}.Build()))),
			of("reading", must(b.Ungated.Reading().Add(ctx, app.ReadingAddRequest_builder{Robot: robotRef(theirs.GetId()), Celsius: 1}.Build())))},
		{"a holder",
			of("holder", must(b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: tenant(b.Tenant.Bytes()), Alias: "h"}.Build()))),
			of("holder", must(b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: tenant(other.GetId()), Alias: "h"}.Build())))},
		{"the tenant itself",
			of("tenant", must(b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: tenant(b.Tenant.Bytes())}.Build()))),
			of("tenant", other)},
	} {
		t.Run(c.shape, func(t *testing.T) {
			x := pdtest.NewX(t)

			x.NoError(b.about(t, as, c.mine.id, c.mine.v))
			x.Equal(codes.NotFound, status.Code(b.about(t, as, c.theirs.id, nil)), "an event about it")
			x.Equal(codes.NotFound, status.Code(b.about(t, as, nil, c.theirs.v)), "a fact of it")
		})
	}

	t.Run("behind no wall, everybody's", func(t *testing.T) {
		x := pdtest.NewX(t)

		fleet := must(b.Ungated.Fleet().Add(ctx, app.FleetAddRequest_builder{Alias: "fleet-1"}.Build()))
		thing := must(b.Ungated.Thing().Add(ctx, app.ThingAddRequest_builder{Alias: "thing-1"}.Build()))
		x.NoError(b.about(t, as, of("fleet", fleet).id, fleet))
		x.NoError(b.about(t, as, of("thing", thing).id, thing))
	})

	t.Run("no row", func(t *testing.T) {
		x := pdtest.NewX(t)

		none := app.Robot_builder{Id: pdid.New(pd.RobotDomain).Bytes(), Alias: "none"}.Build()
		x.NoError(b.about(t, as, none.GetId(), nil), "an event of what an operation is about to make")
		x.Equal(codes.NotFound, status.Code(b.about(t, as, nil, none)), "and no fact of it")

		// A joint whose first hop was left empty reaches no tenant, and the
		// wall shows it to nobody: it is no row to be about.
		orphan := must(b.Ungated.Joint().Add(ctx, app.JointAddRequest_builder{Alias: "orphan"}.Build()))
		x.ErrCode(codes.NotFound, b.about(t, as, nil, orphan))
	})

	t.Run("and a kind nothing can say the tenant of is no subject", func(t *testing.T) {
		pdtest.NewX(t).ErrCode(codes.InvalidArgument, b.about(t, as, pdid.New(pd.AuditDomain).Bytes(), nil))
	})
}

// about is an operation whose event is about `subject`, when there is one,
// and that writes a fact of every field `row` holds, when there is one.
func (b *built) about(t *testing.T, ctx context.Context, subject []byte, row proto.Message) error {
	t.Helper()

	return b.operate(t, ctx, func(ctx context.Context, s past.Store) error {
		ev := past.Event{Value: moved(nil)}
		if subject != nil {
			ev.Subject = idOf(t, subject)
		}
		ctx, op, err := past.Begin(ctx, s, ev)
		if err != nil {
			return err
		}
		if row == nil {
			return nil
		}

		m := row.ProtoReflect()
		return op.Set(ctx, past.Fact{
			Subject: idOf(t, m.Get(m.Descriptor().Fields().ByNumber(1)).Bytes()),
			Values:  past.Changes(nil, row),
		})
	})
}

// TestMoveRecordsItsEventAndItsFact: the operation this app wrote by hand
// records what it was -- a robot's move, from a cell to another -- and the
// fact of the field it changed, the robot's cell, in the transaction of the
// write; a move refused records nothing.
func TestMoveRecordsItsEventAndItsFact(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)

	cell := func(alias string) *app.Cell {
		v, err := b.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  alias,
		}.Build())
		x.NoError(err)
		return v
	}
	floor1, floor2 := cell("floor-1"), cell("floor-2")

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
		Cell:   app.CellRef_builder{Id: floor1.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	rid := idOf(t, v.GetId())
	ref := app.RobotRef_builder{Id: v.GetId()}.Build()

	_, err = b.Walled.Robot().Move(as, app.RobotMoveRequest_builder{Ref: ref, To: app.CellRef_builder{Id: floor2.GetId()}.Build()}.Build())
	x.NoError(err)
	_, err = b.Walled.Robot().Move(as, app.RobotMoveRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	_, err = b.Walled.Robot().Move(as, app.RobotMoveRequest_builder{Ref: ref, To: app.CellRef_builder{Id: b.Holder.Bytes()}.Build()}.Build())
	pdtest.NewX(t).ErrCode(codes.NotFound, err)

	s := b.pastOf(t, b.Drv)
	evs, err := past.Events(as, s, past.EventQuery{Subjects: []pdid.Id{rid}})
	x.NoError(err)
	x.Len(evs, 2, "the refused move recorded nothing")

	got := &app.RobotMoved{}
	x.Equal("robot.move", evs[0].Kind)
	x.Equal(b.Holder, evs[0].Actor)
	x.NoError(evs[0].Value.UnmarshalTo(got))
	x.Equal(v.GetId(), got.GetRobot())
	x.Equal(floor1.GetId(), got.GetFrom())
	x.Equal(floor2.GetId(), got.GetTo())
	x.NoError(evs[1].Value.UnmarshalTo(got))
	x.Equal(floor2.GetId(), got.GetFrom())
	x.Empty(got.GetTo(), "out of every cell")

	sts, err := past.Facts(as, s, past.FactQuery{Subjects: []pdid.Id{rid}})
	x.NoError(err)
	x.Len(sts, 2)
	for i, want := range []string{`"` + idOf(t, floor2.GetId()).String() + `"`, past.Null} {
		x.Equal(evs[i].Id, sts[i].Event)
		x.Len(sts[i].Rows, 1, "the cell, and not the version that moved with it")
		x.Equal(uint32(3), uint32(sts[i].Rows[0].Field))
		x.Equal(want, sts[i].Rows[0].Value)
	}
}

// TestThePastIsOnTheTrail: every write of the past is a write through the
// servers, so a check of the rows after events, children, facts and their
// corrections finds every one as the trail says it is -- the events sealed,
// since their reason is personal.
func TestThePastIsOnTheTrail(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)
	as := b.as(ctx)

	m := machine(t, b, as)
	mid := idOf(t, m.GetId())
	model := m.ProtoReflect().Descriptor().Fields().ByName("model")

	var fact pdid.Id
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		ctx, op, err := past.Begin(ctx, s, past.Event{Subject: mid, Reason: "a reason, which is a person's", Value: moved(nil)})
		if err != nil {
			return err
		}
		if err := op.Set(ctx, past.Fact{Subject: mid, Values: past.Changes(nil, m)}); err != nil {
			return err
		}
		if _, err := op.Child(ctx, past.Event{Subject: mid, Value: moved(nil)}); err != nil {
			return err
		}

		sts, err := past.Facts(ctx, s, past.FactQuery{Subjects: []pdid.Id{mid}})
		for _, r := range sts[0].Rows {
			if r.Field == model.Number() {
				fact = r.Id
			}
		}
		return err
	}))
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		_, op, err := past.Begin(ctx, s, past.Event{Value: moved(nil)})
		if err != nil {
			return err
		}
		return op.Correct(ctx, fact, time.Now().Add(-time.Hour))
	}))

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)
	x.Positive(got.Matches)
}

// TestAReasonIsTakenOut: an event's reason blanked at a person's request is
// a write beneath the servers -- an event has no Patch -- and on the trail as
// the act it is, so a check of the rows finds the event as the trail says it
// is: its line's digest of the row now, and its seal made again of it. The
// same blanking done past the servers is what the check is there to find.
func TestAReasonIsTakenOut(t *testing.T) {
	b, ctx, d := digesting(t)
	as := b.as(ctx)

	said := func(t *testing.T, reason string) pdid.Id {
		t.Helper()

		var ev pdid.Id
		require.NoError(t, b.operate(t, as, func(ctx context.Context, s past.Store) error {
			_, op, err := past.Begin(ctx, s, past.Event{Reason: reason, Value: moved(nil)})
			if err != nil {
				return err
			}
			ev = op.Id()
			return nil
		}))
		return ev
	}
	check := func(t *testing.T) trailRows {
		t.Helper()

		got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
		require.NoError(t, err)
		return trailRows{got.Ok(), got.Differs}
	}

	ev := said(t, "Kim asked, and Kim's mother agreed")

	t.Run("blanked, and on the trail as the act it is", func(t *testing.T) {
		x := require.New(t)

		x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
			return past.Redact(ctx, s, ev)
		}))
		x.Empty(eventOf(t, as, b.pastOf(t, b.Drv), ev).Reason)
		x.Equal(trailRows{Ok: true}, check(t), "the event as the trail says it is")

		lines, err := b.Ent.Audit.Query().
			Where(entaudit.ObjectId(ev.Uuid())).
			Order(entaudit.ByDateCreated(), entaudit.ById()).
			All(ctx)
		x.NoError(err)
		last := lines[len(lines)-1]
		x.Equal(past.RedactAction, last.Action)
		x.Equal(b.Holder.Uuid(), last.ActorId, "by whoever asked")
		x.Empty(last.Patch, "and nothing of what it said")

		x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
			return past.Redact(ctx, s, ev)
		}), "and blank, it is left as it is")
	})

	t.Run("and done past the servers, a row that differs", func(t *testing.T) {
		x := require.New(t)

		ev := said(t, "Lee asked")
		k, err := entuuid.FromBytes(ev.Bytes())
		x.NoError(err)
		x.NoError(b.Ent.Event.UpdateOneId(k).SetReason("").Exec(ctx))

		x.Equal(trailRows{Differs: []pdid.Id{ev}}, check(t), "the reason is outside the digest, and inside the seal")
	})

	t.Run("not outside a transaction, and not of another tenant's", func(t *testing.T) {
		x := pdtest.NewX(t)

		x.ErrorIs(past.Redact(as, b.pastOf(t, b.Drv), ev), past.ErrNotInTx)
		x.ErrCode(codes.NotFound, b.operate(t, b.elsewhere(t, ctx), func(ctx context.Context, s past.Store) error {
			return past.Redact(ctx, s, ev)
		}))
	})
}

type trailRows struct {
	Ok      bool
	Differs []pdid.Id
}

// elsewhere is a context framed as a holder of another tenant, who may see it
// and nothing else.
func (b *built) elsewhere(t *testing.T, ctx context.Context) context.Context {
	t.Helper()

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "elsewhere"}.Build())
	if err != nil {
		other, err = b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref: app.TenantRef_builder{Alias: proto.String("elsewhere")}.Build(),
		}.Build())
	}
	require.NoError(t, err)
	h, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
	}.Build())
	require.NoError(t, err)

	tid := idOf(t, other.GetId())
	return frame.Into(ctx, frame.New(idOf(t, h.GetId()), tid, frame.Whole()).WithScope(frame.Only(tid)))
}
