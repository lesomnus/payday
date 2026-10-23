package cmd_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	entfact "github.com/lesomnus/payday/internal/apptest/internal/ent/fact"
)

// The facts of the past: one statement of any number of a resource's fields,
// a row for each, typed by the resource's own descriptor; read back at a
// moment, as known then, as the statements they were, and corrected without
// ever being changed (payday#151).

func protoreflectString(v string) protoreflect.Value { return protoreflect.ValueOfString(v) }

// machine is a machine holding one value of every kind a field holds, as a
// caller of the tenant writes one.
func machine(t *testing.T, b *built, ctx context.Context) *app.Machine {
	t.Helper()

	r := b.arm(t, ctx, "")
	maker := pdid.New(9)
	v, err := b.Walled.Machine().Add(ctx, app.MachineAddRequest_builder{
		Tenant:   app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:    "m-01",
		Model:    "X-1",
		Hours:    1<<53 + 1,
		Load:     0.5,
		Running:  true,
		Firmware: []byte{1, 2, 3},
		// Past what a double holds, and short of what a database's integer
		// does: the column is a signed 64 bits, whatever the field says.
		Cycles:       math.MaxInt64,
		MakerId:      maker.Bytes(),
		Robot:        app.RobotRef_builder{Id: r.GetId()}.Build(),
		Mode:         app.Mode_MODE_SERVICE,
		DateServiced: timestamppb.New(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)),
		Spec:         app.Machine_Spec_builder{Axes: 3, Reach: 1.5}.Build(),
		Tags:         []string{"b", "a"},
		Settings:     map[string]string{"speed": "slow", "é": " "},
		Operator:     "Kim",
	}.Build())
	require.NoError(t, err)

	return v
}

// TestEveryKindOfValueIsAFact: a machine made, as the facts of every field it
// holds -- a row of each, one of each entry of its map -- each the canonical
// JSON of its value, through the database and back into a machine.
func TestEveryKindOfValueIsAFact(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)

	m := machine(t, b, as)
	mid := idOf(t, m.GetId())

	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		ctx, op, err := past.Begin(ctx, s, past.Event{Subject: mid, Value: app.MachineStarted_builder{Machine: m.GetId()}.Build()})
		if err != nil {
			return err
		}
		return op.Set(ctx, past.Fact{Subject: mid, Values: past.Changes(nil, m)})
	}))

	rows, err := b.Ent.Fact.Query().Where(entfact.SubjectId(mid.Uuid())).All(ctx)
	x.NoError(err)
	got := map[past.Slot]string{}
	for _, r := range rows {
		x.Equal(uint32(mid.Domain()), r.Domain, "the subject's entity")
		got[past.Slot{Field: protoreflect.FieldNumber(r.FieldNumber), Key: r.Key}] = r.Value
	}
	x.Equal(map[past.Slot]string{
		{Field: 4}:                `"m-01"`,
		{Field: 8}:                `"X-1"`,
		{Field: 9}:                `9007199254740993`,
		{Field: 10}:               `0.5`,
		{Field: 11}:               `true`,
		{Field: 12}:               `"AQID"`,
		{Field: 16}:               `9223372036854775807`,
		{Field: 17}:               `"` + idOf(t, m.GetMakerId()).String() + `"`,
		{Field: 18}:               `"` + idOf(t, m.GetRobot().GetId()).String() + `"`,
		{Field: 19}:               `3`,
		{Field: 20}:               `"2026-10-01T09:00:00Z"`,
		{Field: 21}:               `{"1":3,"2":1.5}`,
		{Field: 22}:               `["b","a"]`,
		{Field: 23, Key: "speed"}: `"slow"`,
		{Field: 23, Key: "é"}:     "\" \"",
		{Field: 24}:               past.Withheld,
	}, got, "not the key, the tenant, the version or date_created; the operator a person's")

	sts, err := past.StateAt(as, b.pastOf(t, b.Drv), []pdid.Id{mid}, time.Now(), nil)
	x.NoError(err)
	was := &app.Machine{}
	x.NoError(sts[mid].Into(was))

	want := proto.Clone(m).(*app.Machine)
	want.ClearTenant()
	want.ClearDateUpdated()
	want.ClearDateCreated()
	want.SetOperator("")
	x.True(proto.Equal(want, was), "read back:\n%v\nwant:\n%v", was, want)
}

// TestAStatementIsReadBackAsWritten: a fact of three fields is three rows,
// read back as the one statement they were; a correction of one of them
// supersedes its row alone, and the row it writes is a statement of its own,
// the correcting event's.
func TestAStatementIsReadBackAsWritten(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)
	s := b.pastOf(t, b.Drv)

	m := machine(t, b, as)
	mid := idOf(t, m.GetId())
	fs := m.ProtoReflect().Descriptor().Fields()
	model, hours, mode := fs.ByName("model"), fs.ByName("hours"), fs.ByName("mode")
	from := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	var first pdid.Id
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		ctx, op, err := past.Begin(ctx, s, past.Event{Subject: mid, Value: app.MachineStarted_builder{Machine: m.GetId()}.Build()})
		if err != nil {
			return err
		}
		first = op.Id()

		return op.Set(ctx, past.Fact{Subject: mid, From: from, Values: []past.Value{
			past.To(model, protoreflect.ValueOfString("X-2")),
			past.To(hours, protoreflect.ValueOfInt64(10)),
			past.To(mode, protoreflect.ValueOfEnum(protoreflect.EnumNumber(app.Mode_MODE_RUN))),
		}})
	}))

	sts, err := past.Facts(as, s, past.FactQuery{Subjects: []pdid.Id{mid}})
	x.NoError(err)
	x.Len(sts, 1)
	x.Equal(first, sts[0].Event)
	x.True(from.Equal(sts[0].From))
	x.Len(sts[0].Rows, 3)
	before := sts[0].Rows[1].Recorded

	corrected := from.AddDate(0, 0, -3)
	var second pdid.Id
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		_, op, err := past.Begin(ctx, s, past.Event{Value: app.Corrected_builder{Event: first.Bytes(), At: timestamppb.New(corrected)}.Build()})
		if err != nil {
			return err
		}
		second = op.Id()

		return op.Correct(ctx, sts[0].Rows[1].Id, corrected)
	}))

	t.Run("as known now", func(t *testing.T) {
		x := require.New(t)

		got, err := past.Facts(as, s, past.FactQuery{Subjects: []pdid.Id{mid}})
		x.NoError(err)
		x.Len(got, 2)
		x.Equal(second, got[0].Event, "the correction holds from earlier")
		x.True(corrected.Equal(got[0].From))
		x.Len(got[0].Rows, 1)
		x.Equal(`10`, got[0].Rows[0].Value)

		x.Equal(first, got[1].Event)
		x.Len(got[1].Rows, 2, "model and mode stand as they were")
	})

	t.Run("every fact ever written", func(t *testing.T) {
		x := require.New(t)

		got, err := past.Facts(as, s, past.FactQuery{Subjects: []pdid.Id{mid}, Known: past.Known{All: true}})
		x.NoError(err)
		x.Len(got, 2)
		x.Len(got[1].Rows, 3, "the statement as it was written, the corrected row in it")
		x.Equal(second, got[1].Rows[1].SupersededBy)
		x.NotNil(got[1].Rows[1].Superseded)
	})

	t.Run("as known before the correction", func(t *testing.T) {
		x := require.New(t)

		got, err := past.Facts(as, s, past.FactQuery{Subjects: []pdid.Id{mid}, Known: past.Known{At: &before}})
		x.NoError(err)
		x.Len(got, 1)
		x.Len(got[0].Rows, 3)
	})

	t.Run("and a fact that never held is retracted", func(t *testing.T) {
		x := require.New(t)

		x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
			_, op, err := past.Begin(ctx, s, past.Event{Value: app.Corrected_builder{Event: first.Bytes()}.Build()})
			if err != nil {
				return err
			}
			return op.Retract(ctx, sts[0].Rows[0].Id)
		}))

		got, err := past.Facts(as, s, past.FactQuery{Events: []pdid.Id{first}})
		x.NoError(err)
		x.Len(got, 1)
		x.Len(got[0].Rows, 1, "the mode, and nothing in the model's place")

		// A fact that no longer stands is no fact to retract.
		pdtest.NewX(t).ErrCode(codes.NotFound, b.operate(t, as, func(ctx context.Context, s past.Store) error {
			_, op, err := past.Begin(ctx, s, past.Event{Value: app.Corrected_builder{Event: first.Bytes()}.Build()})
			if err != nil {
				return err
			}
			return op.Retract(ctx, sts[0].Rows[0].Id)
		}))
	})
}

// TestAWholeEventIsCorrected: "that operation happened on 9/28" -- every fact
// it wrote that still stands is written again from then, as the correcting
// operation's, and superseded by it; and an operation that never happened is
// retracted whole.
func TestAWholeEventIsCorrected(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)
	s := b.pastOf(t, b.Drv)

	a, c := machine(t, b, as), machine(t, b, as)
	aid, cid := idOf(t, a.GetId()), idOf(t, c.GetId())
	fs := a.ProtoReflect().Descriptor().Fields()
	model, hours := fs.ByName("model"), fs.ByName("hours")
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }

	var serviced pdid.Id
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		ctx, op, err := past.Begin(ctx, s, past.Event{At: day(15), Value: app.MachineServiced_builder{Machine: a.GetId()}.Build()})
		if err != nil {
			return err
		}
		serviced = op.Id()

		return op.Set(ctx,
			past.Fact{Subject: aid, Values: []past.Value{past.To(model, protoreflect.ValueOfString("A-2"))}},
			past.Fact{Subject: cid, Values: []past.Value{
				past.To(model, protoreflect.ValueOfString("C-2")),
				past.To(hours, protoreflect.ValueOfInt64(7)),
			}},
		)
	}))

	var correction pdid.Id
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		_, op, err := past.Begin(ctx, s, past.Event{Value: app.Corrected_builder{Event: serviced.Bytes(), At: timestamppb.New(day(28))}.Build()})
		if err != nil {
			return err
		}
		correction = op.Id()

		return op.CorrectEvent(ctx, serviced, day(28))
	}))

	got, err := past.Facts(as, s, past.FactQuery{Events: []pdid.Id{serviced}})
	x.NoError(err)
	x.Empty(got, "nothing it wrote stands")

	got, err = past.Facts(as, s, past.FactQuery{Events: []pdid.Id{correction}})
	x.NoError(err)
	x.Len(got, 2, "a statement for each machine")
	for _, st := range got {
		x.True(day(28).Equal(st.From))
	}

	all, err := past.Facts(as, s, past.FactQuery{Events: []pdid.Id{serviced}, Known: past.Known{All: true}})
	x.NoError(err)
	for _, st := range all {
		for _, r := range st.Rows {
			x.Equal(correction, r.SupersededBy)
		}
	}

	states := func(at time.Time) map[pdid.Id]*past.State {
		v, err := past.StateAt(as, s, []pdid.Id{aid, cid}, at, nil)
		x.NoError(err)
		return v
	}
	x.Equal(`"C-2"`, states(day(29))[cid].Values[past.Slot{Field: model.Number()}].Value)
	_, ok := states(day(20))[cid].Values[past.Slot{Field: model.Number()}]
	x.False(ok, "it was not so on 9/20, by what is known now")

	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		_, op, err := past.Begin(ctx, s, past.Event{Value: app.Corrected_builder{Event: correction.Bytes()}.Build()})
		if err != nil {
			return err
		}
		return op.RetractEvent(ctx, correction)
	}))
	_, ok = states(day(29))[aid].Values[past.Slot{Field: model.Number()}]
	x.False(ok, "and retracted, never")

	// An operation of another tenant's is none to correct.
	pdtest.NewX(t).ErrCode(codes.NotFound, b.operate(t, b.elsewhere(t, ctx), func(ctx context.Context, s past.Store) error {
		_, op, err := past.Begin(ctx, s, past.Event{Value: app.Corrected_builder{Event: serviced.Bytes()}.Build()})
		if err != nil {
			return err
		}
		return op.CorrectEvent(ctx, serviced, day(1))
	}))
}

// TestAStateIsWhatWasKnownThen is the example rove's design gives (§3.3), on
// the database: where a robot was, recorded on 10/1, moved on 10/5, and the
// move corrected on 10/9 -- read at a moment as known now, and as known then.
func TestAStateIsWhatWasKnownThen(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
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
	rid := idOf(t, r.GetId())
	cellFd := r.ProtoReflect().Descriptor().Fields().ByName("cell")
	in := func(id []byte) past.Value {
		return past.To(cellFd, protoreflect.ValueOfMessage(app.Cell_builder{Id: id}.Build().ProtoReflect()))
	}
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }

	// Each operation on the day it was recorded.
	on := func(t *testing.T, at time.Time, f func(ctx context.Context, op *past.Op) error) {
		t.Helper()
		require.NoError(t, b.operate(t, as, func(ctx context.Context, s past.Store) error {
			ctx, op, err := past.Begin(ctx, past.WithClock(s, func() time.Time { return at }), past.Event{Subject: rid, Value: moved(r.GetId())})
			if err != nil {
				return err
			}
			return f(ctx, op)
		}))
	}

	on(t, day(10, 1), func(ctx context.Context, op *past.Op) error {
		return op.Set(ctx, past.Fact{Subject: rid, From: day(9, 1), Values: []past.Value{in(roomA)}})
	})
	on(t, day(10, 5), func(ctx context.Context, op *past.Op) error {
		return op.Set(ctx, past.Fact{Subject: rid, From: day(9, 15), Values: []past.Value{in(roomB)}})
	})
	s := b.pastOf(t, b.Drv)
	sts, err := past.Facts(as, s, past.FactQuery{Subjects: []pdid.Id{rid}})
	x.NoError(err)
	x.Len(sts, 2)
	on(t, day(10, 9), func(ctx context.Context, op *past.Op) error {
		return op.Correct(ctx, sts[1].Rows[0].Id, day(9, 12))
	})

	where := func(at time.Time, known *time.Time) string {
		t.Helper()
		v, err := past.StateAt(as, s, []pdid.Id{rid}, at, known)
		require.NoError(t, err)
		f, ok := v[rid].Values[past.Slot{Field: cellFd.Number()}]
		if !ok {
			return "nowhere"
		}
		return map[string]string{
			`"` + idOf(t, roomA).String() + `"`: "A",
			`"` + idOf(t, roomB).String() + `"`: "B",
		}[f.Value]
	}
	known := func(m time.Month, d int) *time.Time { v := day(m, d); return &v }

	x.Equal("B", where(day(9, 13), nil), "QueryAt(9/13)")
	x.Equal("A", where(day(9, 13), known(10, 6)), "AsOf(9/13, 10/6): what the system showed on 10/6")
	x.Equal("A", where(day(9, 20), known(10, 2)), "AsOf(9/20, 10/2)")
	x.Equal("B", where(day(9, 20), known(10, 6)))
	x.Equal("nowhere", where(day(8, 31), nil))

	t.Run("and what changed between two moments", func(t *testing.T) {
		x := require.New(t)

		a, err := past.StateAt(as, s, []pdid.Id{rid}, day(9, 10), nil)
		x.NoError(err)
		c, err := past.StateAt(as, s, []pdid.Id{rid}, day(9, 20), nil)
		x.NoError(err)
		x.Equal([]past.Difference{{
			Slot:   past.Slot{Field: cellFd.Number()},
			Before: `"` + idOf(t, roomA).String() + `"`,
			After:  `"` + idOf(t, roomB).String() + `"`,
		}}, past.Diff(a[rid], c[rid]))
	})

	t.Run("and its history, newest first", func(t *testing.T) {
		x := require.New(t)

		lines, err := past.Timeline(as, s, rid, past.Known{})
		x.NoError(err)
		var got []string
		for _, l := range lines {
			if l.Statement != nil {
				got = append(got, "fact "+l.At.Format("01/02"))
				x.NotNil(l.Event, "with the event that wrote it")
				continue
			}
			got = append(got, "event "+l.At.Format("01/02"))
		}
		x.Equal([]string{"event 10/09", "event 10/05", "event 10/01", "fact 09/12", "fact 09/01"}, got)

		lines, err = past.Timeline(as, s, rid, past.Known{At: known(10, 6)})
		x.NoError(err)
		got = got[:0]
		for _, l := range lines {
			got = append(got, l.At.Format("01/02"))
		}
		x.Equal([]string{"10/05", "10/01", "09/15", "09/01"}, got, "as it read on 10/6")
	})
}

// TestAPersonalValueIsNotWrittenDown: a fact of a field its subject's entity
// declares personal or secret says that it changed, and never what to -- nor,
// of a map, which entry -- so neither the database's rows nor what is read
// back hold it.
func TestAPersonalValueIsNotWrittenDown(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)
	s := b.pastOf(t, b.Drv)

	r, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
		Keeper: "kim@example.com",
		Secret: []byte("hunter2"),
	}.Build())
	x.NoError(err)
	h, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: b.Holder.Bytes()}.Build()}.Build())
	x.NoError(err)
	labelled := proto.Clone(h).(*app.Holder)
	labelled.SetLabels(map[string]string{"Kim's mother": "010-0000-0000"})

	rid, hid := idOf(t, r.GetId()), idOf(t, h.GetId())
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		ctx, op, err := past.Begin(ctx, s, past.Event{Value: moved(nil)})
		if err != nil {
			return err
		}
		return op.Set(ctx,
			past.Fact{Subject: rid, Values: past.Changes(nil, r)},
			past.Fact{Subject: hid, Values: past.Changes(h, labelled)},
		)
	}))

	rows, err := b.Ent.Fact.Query().Where(entfact.SubjectIdIn(rid.Uuid(), hid.Uuid())).All(ctx)
	x.NoError(err)
	got := map[pdid.Id]map[past.Slot]string{}
	for _, v := range rows {
		k := pdid.Id(v.SubjectId)
		if got[k] == nil {
			got[k] = map[past.Slot]string{}
		}
		got[k][past.Slot{Field: protoreflect.FieldNumber(v.FieldNumber), Key: v.Key}] = v.Value
	}
	x.Equal(map[past.Slot]string{
		{Field: 4}:  `"arm-01"`,
		{Field: 8}:  past.Withheld,
		{Field: 32}: past.Withheld,
	}, got[rid], "the secret and the keeper changed, and not what to")
	x.Equal(map[past.Slot]string{{Field: 7}: past.Withheld}, got[hid], "a label, and not which")

	v, err := past.StateAt(as, s, []pdid.Id{rid}, time.Now(), nil)
	x.NoError(err)
	read := &app.Robot{}
	x.NoError(v[rid].Into(read))
	x.Equal("arm-01", read.GetAlias())
	x.Empty(read.GetKeeper())
	x.Empty(read.GetSecret())
	x.True(v[rid].Values[past.Slot{Field: 32}].Withheld())
}

// TestThePastIsReadBehindTheWall: every read is narrowed to the tenants the
// caller may see, as a server's are -- a caller of another tenant reads
// nothing of this one's, one who may see every tenant reads it all, and a
// call with no frame is refused.
func TestThePastIsReadBehindTheWall(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)
	s := b.pastOf(t, b.Drv)

	m := machine(t, b, as)
	mid := idOf(t, m.GetId())
	var ev pdid.Id
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		ctx, op, err := past.Begin(ctx, s, past.Event{Subject: mid, Value: app.MachineStarted_builder{Machine: m.GetId()}.Build()})
		if err != nil {
			return err
		}
		ev = op.Id()
		return op.Set(ctx, past.Fact{Subject: mid, Values: past.Changes(nil, m)})
	}))

	sees := func(t *testing.T, ctx context.Context) (events, facts, held, lines int) {
		t.Helper()
		x := require.New(t)

		evs, err := past.Events(ctx, s, past.EventQuery{Ids: []pdid.Id{ev}, Subjects: []pdid.Id{mid}})
		x.NoError(err)
		sts, err := past.Facts(ctx, s, past.FactQuery{Subjects: []pdid.Id{mid}})
		x.NoError(err)
		st, err := past.StateAt(ctx, s, []pdid.Id{mid}, time.Now(), nil)
		x.NoError(err)
		tl, err := past.Timeline(ctx, s, mid, past.Known{})
		x.NoError(err)

		return len(evs), len(sts), len(st[mid].Values), len(tl)
	}

	events, facts, held, lines := sees(t, as)
	x.Equal(1, events)
	x.Equal(1, facts)
	x.Positive(held)
	x.Equal(2, lines)

	events, facts, held, lines = sees(t, b.elsewhere(t, ctx))
	x.Zero(events+facts+held+lines, "another tenant's caller reads nothing of this one's")

	f := frame.New(b.Holder, b.Tenant, frame.Whole()).WithScope(frame.Everything)
	events, facts, _, _ = sees(t, frame.Into(ctx, f))
	x.Equal(1, events)
	x.Equal(1, facts)

	_, err := past.Facts(ctx, s, past.FactQuery{Subjects: []pdid.Id{mid}})
	pdtest.NewX(t).ErrCode(codes.Unauthenticated, err)
}

// TestAReasonReadIsOnTheAccessRecord: an event's reason is personal, and the
// past answers with it through no RPC whose declarations the access record
// reads -- so an event read back with one is a row of the access record of
// the call that read it, filed under its tenant, and one with none is not.
func TestAReasonReadIsOnTheAccessRecord(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)

	var said, silent pdid.Id
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		_, op, err := past.Begin(ctx, s, past.Event{Reason: "Kim asked", Value: moved(nil)})
		if err != nil {
			return err
		}
		said = op.Id()

		_, op, err = past.Begin(ctx, s, past.Event{Value: moved(nil)})
		if err != nil {
			return err
		}
		silent = op.Id()
		return nil
	}))

	in, call := access.Into(as, "/app.Example/History")
	_, err := past.Events(in, b.pastOf(t, b.Drv), past.EventQuery{Ids: []pdid.Id{said, silent}})
	x.NoError(err)

	_, saw := call.Unsaid()
	x.Equal([]access.Subject{{Id: said, Tenant: b.Tenant}}, saw)
}

// TestAValueSaidAgainForAMomentReplacesIt: a fact of a field from exactly the
// moment another stands from supersedes it -- by the operation that said it
// again -- and one from another moment does not: it says what the field was
// from then.
func TestAValueSaidAgainForAMomentReplacesIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)
	s := b.pastOf(t, b.Drv)

	m := machine(t, b, as)
	mid := idOf(t, m.GetId())
	model := m.ProtoReflect().Descriptor().Fields().ByName("model")
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	say := func(v string, from time.Time) pdid.Id {
		t.Helper()

		var ev pdid.Id
		x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
			ctx, op, err := past.Begin(ctx, s, past.Event{Subject: mid, Value: app.MachineStarted_builder{Machine: m.GetId()}.Build()})
			if err != nil {
				return err
			}
			ev = op.Id()
			return op.Set(ctx, past.Fact{Subject: mid, From: from, Values: []past.Value{past.To(model, protoreflect.ValueOfString(v))}})
		}))
		return ev
	}

	first := say("X-1", from)
	again := say("X-2", from)
	later := say("X-3", from.AddDate(0, 1, 0))

	got, err := past.Facts(as, s, past.FactQuery{Subjects: []pdid.Id{mid}})
	x.NoError(err)
	x.Len(got, 2)
	x.Equal(again, got[0].Event, "said again for the moment")
	x.Equal(later, got[1].Event, "and from another, which stands beside it")

	all, err := past.Facts(as, s, past.FactQuery{Events: []pdid.Id{first}, Known: past.Known{All: true}})
	x.NoError(err)
	x.Len(all, 1)
	x.Equal(again, all[0].Rows[0].SupersededBy)
}

// TestASupersededFactIsNotSupersededAgain: a supersession is held to the
// version of the row it read, so two operations that read one standing fact
// cannot both replace it -- the second is refused rather than overwriting who
// superseded it.
func TestASupersededFactIsNotSupersededAgain(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)

	m := machine(t, b, as)
	mid := idOf(t, m.GetId())
	model := m.ProtoReflect().Descriptor().Fields().ByName("model")

	var row past.FactRow
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		ctx, op, err := past.Begin(ctx, s, past.Event{Subject: mid, Value: app.MachineStarted_builder{Machine: m.GetId()}.Build()})
		if err != nil {
			return err
		}
		if err := op.Set(ctx, past.Fact{Subject: mid, Values: []past.Value{past.To(model, protoreflect.ValueOfString("X-2"))}}); err != nil {
			return err
		}

		sts, err := past.Facts(ctx, s, past.FactQuery{Subjects: []pdid.Id{mid}})
		row = sts[0].Rows[0]
		return err
	}))

	retract := func(ctx context.Context, s past.Store) error {
		_, op, err := past.Begin(ctx, s, past.Event{Value: app.Corrected_builder{Event: row.Event.Bytes()}.Build()})
		if err != nil {
			return err
		}
		return s.Supersede(ctx, []past.FactRow{row}, op.Recorded(), op.Id())
	}
	x.NoError(b.operate(t, as, retract))
	pdtest.NewX(t).ErrCode(codes.FailedPrecondition, b.operate(t, as, retract))
}
