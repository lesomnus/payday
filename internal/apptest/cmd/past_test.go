package cmd_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	entevent "github.com/lesomnus/payday/internal/apptest/internal/ent/event"
	entfact "github.com/lesomnus/payday/internal/apptest/internal/ent/fact"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// beforeThePast is this app's servers as an app that kept events of its own
// had them before it declared a past: written through, on the trail, and with
// none of the layers above -- the Audit layer among them, which refuses a row
// of the past's tables that the `past` package did not write. What such an
// app's table already held when it adopted payday's is written with it.
func (b *built) beforeThePast(t *testing.T) app.Server {
	t.Helper()

	s, err := pd.NewSink(b.Ent, bare.WithMinter(pd.Minter()), bare.WithRecorder(pd.Recorder(b.Contents)))
	require.NoError(t, err)

	return s
}

// TestThePastIsWrittenAndReadBack: payday's Event and Fact, which are in this
// app because it declares a past (payday#151), written by the `past` package
// through the servers generated for them -- the one writer those servers take
// (TestThePastIsWrittenByThePackageAlone) -- and read back through the same
// servers, and by their columns.
//
// Each has a column nothing else in the app has. An event's `value` is an Any,
// kept as the canonical protobuf JSON of the message it holds -- jsonb, on
// PostgreSQL, which keeps an object's keys in an order of its own, so a field
// with a name shorter than `@type`, as a move's `to` and `from` are, comes back
// before it. A fact's `key`, `value` and `domain` are words a database or a
// generator could have taken for itself, and its `field_number` is not `field`
// because ent did: a column of that name does not compile.
func TestThePastIsWrittenAndReadBack(t *testing.T) {
	b, ctx := build(t)
	as := b.as(ctx)

	m := machine(t, b, as)
	mid := idOf(t, m.GetId())
	fields := m.ProtoReflect().Descriptor().Fields()
	settings, serviced, operator := fields.ByName("settings"), fields.ByName("date_serviced"), fields.ByName("operator")

	moved := app.RobotMoved_builder{
		Robot: m.GetRobot().GetId(),
		From:  bytes.Repeat([]byte{1}, 16),
		To:    bytes.Repeat([]byte{2}, 16),
	}.Build()
	started := app.MachineStarted_builder{Machine: m.GetId()}.Build()
	from := time.Now().Add(-time.Hour)

	// An operation, two children of it at once, and a statement of three of
	// the machine's fields: an entry of its map, a field cleared, and one its
	// entity declares personal.
	var ev pdid.Id
	x := require.New(t)
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		ctx, op, err := past.Begin(ctx, s, past.Event{Subject: mid, Reason: "asked to", Value: moved})
		if err != nil {
			return err
		}
		ev = op.Id()

		if _, err := op.Batch(ctx, []past.Child{
			{Event: past.Event{Subject: mid, Value: started}},
			{Event: past.Event{Subject: mid, Value: started}},
		}); err != nil {
			return err
		}

		return op.Set(ctx, past.Fact{Subject: mid, From: from, Values: []past.Value{
			past.Entry(settings, protoreflect.ValueOfString("colour").MapKey(), protoreflect.ValueOfString("red")),
			past.Cleared(serviced),
			past.To(operator, protoreflect.ValueOfString("Kim")),
		}})
	}))

	get := func(t *testing.T, id []byte) *app.Event {
		t.Helper()

		v, err := b.Walled.Event().Get(as, app.EventGetRequest_builder{
			Ref:    app.EventRef_builder{Id: id}.Build(),
			Select: app.EventSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		require.NoError(t, err)

		return v
	}

	t.Run("by hand, through the servers, refused", func(t *testing.T) {
		_, err := b.Walled.Event().Add(as, app.EventAddRequest_builder{
			Tenant:     app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Kind:       "robot.move",
			OccurredAt: timestamppb.Now(),
		}.Build())
		pdtest.NewX(t).ErrCode(codes.PermissionDenied, err)
	})

	t.Run("an event, and the message it holds", func(t *testing.T) {
		x := require.New(t)

		v := get(t, ev.Bytes())
		x.Equal("robot.move", v.GetKind())
		x.Equal("asked to", v.GetReason())
		x.Equal(b.Holder.Bytes(), v.GetActorId(), "who asked, read off the frame")
		x.Equal(mid.Bytes(), v.GetSubjectId())

		x.Equal("type.googleapis.com/app.RobotMoved", v.GetValue().GetTypeUrl())
		got := &app.RobotMoved{}
		x.NoError(v.GetValue().UnmarshalTo(got))
		x.True(proto.Equal(moved, got), "%v", got)
	})

	t.Run("many at once, each with the message it holds", func(t *testing.T) {
		x := require.New(t)

		// Found by a filter, and in SQL by the column.
		ls, err := b.Walled.Event().List(as, app.EventListRequest_builder{Filters: []*app.EventFilter{
			app.EventFilter_builder{CauseId: ev.Bytes()}.Build(),
		}}.Build())
		x.NoError(err)
		x.Len(ls.GetItems(), 2)
		for _, v := range ls.GetItems() {
			got := &app.MachineStarted{}
			x.NoError(v.GetValue().UnmarshalTo(got))
			x.True(proto.Equal(started, got), "%v", got)
		}

		n, err := b.Ent.Event.Query().Where(entevent.ValueNotNil()).Count(ctx)
		x.NoError(err)
		x.Equal(3, n)
	})

	t.Run("and one from before the past, which holds none", func(t *testing.T) {
		x := require.New(t)

		v, err := b.beforeThePast(t).Event().Add(ctx, app.EventAddRequest_builder{
			Tenant:     app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Kind:       "robot.oil",
			OccurredAt: timestamppb.Now(),
			Desc:       "oiled",
		}.Build())
		x.NoError(err)

		got := get(t, v.GetId())
		x.False(got.HasValue(), "an event written with no value is read with one: %v", got.GetValue())
		x.Equal("oiled", got.GetDesc(), "the overlay's field")
	})

	t.Run("facts, one of them superseded by a patch", func(t *testing.T) {
		x := require.New(t)

		colour, err := b.Ent.Fact.Query().Where(entfact.EventId(ev.Uuid()), entfact.Key("colour")).OnlyId(ctx)
		x.NoError(err)

		// Said again for the same moment, which supersedes it: a Patch of the
		// row, by the operation that said it again.
		var again pdid.Id
		x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
			ctx, op, err := past.Begin(ctx, s, past.Event{Subject: mid, Value: started})
			if err != nil {
				return err
			}
			again = op.Id()

			return op.Set(ctx, past.Fact{Subject: mid, From: from, Values: []past.Value{
				past.Entry(settings, protoreflect.ValueOfString("colour").MapKey(), protoreflect.ValueOfString("blue")),
			}})
		}))

		got, err := b.Walled.Fact().Get(as, app.FactGetRequest_builder{
			Ref:    app.FactRef_builder{Id: colour[:]}.Build(),
			Select: app.FactSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("colour", got.GetKey())
		x.Equal(`"red"`, got.GetValue())
		x.Equal(uint32(settings.Number()), got.GetFieldNumber())
		x.Equal(uint32(mid.Domain()), got.GetDomain())
		x.Equal(from.UnixMicro(), got.GetValidFrom().AsTime().UnixMicro())
		x.True(got.HasSupersededAt())
		x.Equal(again.Bytes(), got.GetSupersededBy())

		ls, err := b.Walled.Fact().List(as, app.FactListRequest_builder{Filters: []*app.FactFilter{
			app.FactFilter_builder{SubjectId: mid.Bytes(), FieldNumber: z.Ptr(uint32(operator.Number()))}.Build(),
		}}.Build())
		x.NoError(err)
		x.Len(ls.GetItems(), 1)
		x.Equal(past.Withheld, ls.GetItems()[0].GetValue())

		// The columns as SQL names them: a predicate on each, and an order.
		n, err := b.Ent.Fact.Query().
			Where(entfact.Key("colour"), entfact.Value(`"red"`), entfact.Domain(uint32(mid.Domain())), entfact.FieldNumber(uint32(settings.Number()))).
			Count(ctx)
		x.NoError(err)
		x.Equal(1, n)

		fs, err := b.Ent.Fact.Query().
			Where(entfact.EventId(ev.Uuid())).
			Order(entfact.ByFieldNumber(), entfact.ByKey()).
			All(ctx)
		x.NoError(err)
		x.Len(fs, 3)
		x.Equal(uint32(serviced.Number()), fs[0].FieldNumber)
		x.Equal(past.Null, fs[0].Value)
	})
}

// TestAnEventIsReadBackByItsKind: `pd.EventKinds` names the message each of
// this app's kinds of event holds -- a message that names no kind, by its full
// name, and one of two kinds by each -- and an event holding one, written
// through the server and kept as JSON in the database, is read back into the
// message its kind names, whole: an enum by its name, a list and a map of a
// message of its own, a time.
func TestAnEventIsReadBackByItsKind(t *testing.T) {
	x := require.New(t)

	names := map[string]protoreflect.FullName{}
	for kind, v := range pd.EventKinds {
		names[kind] = v.Descriptor().FullName()
	}
	x.Equal(map[string]protoreflect.FullName{
		"machine.service":    "app.MachineServiced",
		"app.MachineStarted": "app.MachineStarted",
		"robot.move":         "app.RobotMoved",
		"correct":            "app.Corrected",
		"retract":            "app.Corrected",
		"past.expire":        "app.PastExpired",
	}, names)

	b, ctx := build(t)
	as := b.as(ctx)

	part := func(serial string, count uint32) *app.MachineServiced_Part {
		return app.MachineServiced_Part_builder{Serial: serial, Count: count}.Build()
	}
	sent := app.MachineServiced_builder{
		Machine: bytes.Repeat([]byte{7}, 16),
		By:      b.Holder.Bytes(),
		Mode:    app.Mode_MODE_SERVICE,
		Parts:   []*app.MachineServiced_Part{part("P-1", 2), part("P-2", 1)},
		Swapped: map[string]*app.MachineServiced_Part{"left": part("P-0", 1)},
		Due:     timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
	}.Build()

	var ev pdid.Id
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		_, op, err := past.Begin(ctx, s, past.Event{Value: sent})
		if err != nil {
			return err
		}
		ev = op.Id()

		return nil
	}))

	got, err := b.Walled.Event().Get(as, app.EventGetRequest_builder{
		Ref:    app.EventRef_builder{Id: ev.Bytes()}.Build(),
		Select: app.EventSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)

	typ, ok := pd.EventKinds[got.GetKind()]
	x.True(ok, "no event message for kind %q", got.GetKind())

	v := typ.New().Interface()
	x.NoError(got.GetValue().UnmarshalTo(v))
	x.True(proto.Equal(sent, v), "%v", v)
}
