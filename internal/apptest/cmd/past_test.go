package cmd_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	app "github.com/lesomnus/payday/internal/apptest"
	entevent "github.com/lesomnus/payday/internal/apptest/internal/ent/event"
	entfact "github.com/lesomnus/payday/internal/apptest/internal/ent/fact"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// TestThePastIsWrittenAndReadBack: payday's Event and Fact, which are in this
// app because it declares a past (payday#151), through the servers generated
// for them.
//
// Each has a column nothing else in the app has. An event's `value` is an Any,
// kept as the canonical protobuf JSON of the message it holds -- jsonb, on
// PostgreSQL, which keeps an object's keys in an order of its own, so a field
// with a name shorter than `@type` comes back before it. A fact's `key`,
// `value` and `domain` are words a database or a generator could have taken
// for itself, and its `field_number` is not `field` because ent did: a column
// of that name does not compile.
func TestThePastIsWrittenAndReadBack(t *testing.T) {
	b, ctx := build(t)
	as := b.as(ctx)
	tenant := app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build()

	// A message of the app's whose one field is shorter than `@type`, and a
	// well-known one, which an Any holds by its own JSON shape.
	moved := app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build()
	held, err := anypb.New(moved)
	require.NoError(t, err)
	said, err := anypb.New(wrapperspb.String("hello"))
	require.NoError(t, err)

	get := func(t *testing.T, id []byte) *app.Event {
		t.Helper()

		v, err := b.Walled.Event().Get(as, app.EventGetRequest_builder{
			Ref:    app.EventRef_builder{Id: id}.Build(),
			Select: app.EventSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		require.NoError(t, err)

		return v
	}

	ev, err := b.Walled.Event().Add(as, app.EventAddRequest_builder{
		Tenant:     tenant,
		Kind:       "robot.move",
		Reason:     "asked to",
		OccurredAt: timestamppb.Now(),
		Value:      held,
		Desc:       "moved",
	}.Build())
	require.NoError(t, err)

	t.Run("an event, and the message it holds", func(t *testing.T) {
		x := require.New(t)

		v := get(t, ev.GetId())
		x.Equal("robot.move", v.GetKind())
		x.Equal("asked to", v.GetReason())
		x.Equal("moved", v.GetDesc(), "the overlay's field")

		x.Equal(held.GetTypeUrl(), v.GetValue().GetTypeUrl())
		got := &app.TenantRef{}
		x.NoError(v.GetValue().UnmarshalTo(got))
		x.True(proto.Equal(moved, got), "%v", got)
	})

	var children []*app.Event
	t.Run("many at once, with a value and without one", func(t *testing.T) {
		x := require.New(t)

		vs, err := b.Walled.Event().AddMany(as, app.EventAddManyRequest_builder{Items: []*app.EventAddRequest{
			app.EventAddRequest_builder{Tenant: tenant, Kind: "robot.say", OccurredAt: timestamppb.Now(), Value: said, CauseId: ev.GetId()}.Build(),
			app.EventAddRequest_builder{Tenant: tenant, Kind: "robot.wait", OccurredAt: timestamppb.Now(), SubjectId: ev.GetId()}.Build(),
		}}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 2)
		children = vs.GetItems()

		s := &wrapperspb.StringValue{}
		x.NoError(get(t, children[0].GetId()).GetValue().UnmarshalTo(s))
		x.Equal("hello", s.GetValue())

		v := get(t, children[1].GetId())
		x.False(v.HasValue(), "an event written with no value is read with one: %v", v.GetValue())

		// And found by a filter, and in SQL by the column.
		ls, err := b.Walled.Event().List(as, app.EventListRequest_builder{Filters: []*app.EventFilter{
			app.EventFilter_builder{CauseId: ev.GetId()}.Build(),
		}}.Build())
		x.NoError(err)
		x.Len(ls.GetItems(), 1)
		x.NoError(ls.GetItems()[0].GetValue().UnmarshalTo(s))

		n, err := b.Server.Ent.Event.Query().Where(entevent.ValueNotNil()).Count(ctx)
		x.NoError(err)
		x.Equal(2, n)
	})

	t.Run("facts, one of them superseded by a patch", func(t *testing.T) {
		x := require.New(t)
		from := timestamppb.New(time.Now().Add(-time.Hour))

		v, err := b.Walled.Fact().Add(as, app.FactAddRequest_builder{
			Tenant:      tenant,
			SubjectId:   ev.GetId(),
			Key:         "colour",
			Value:       `"red"`,
			FieldNumber: z.Ptr(uint32(22)),
			Domain:      z.Ptr(uint32(7)),
			ValidFrom:   from,
			EventId:     ev.GetId(),
		}.Build())
		x.NoError(err)

		vs, err := b.Walled.Fact().AddMany(as, app.FactAddManyRequest_builder{Items: []*app.FactAddRequest{
			// Cleared, and withheld.
			app.FactAddRequest_builder{Tenant: tenant, SubjectId: ev.GetId(), Value: "null", FieldNumber: z.Ptr(uint32(5)), Domain: z.Ptr(uint32(7)), ValidFrom: from, EventId: ev.GetId()}.Build(),
			app.FactAddRequest_builder{Tenant: tenant, SubjectId: ev.GetId(), Value: "", FieldNumber: z.Ptr(uint32(6)), Domain: z.Ptr(uint32(7)), ValidFrom: from, EventId: ev.GetId()}.Build(),
		}}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 2)

		at := timestamppb.Now()
		_, err = b.Walled.Fact().Patch(as, app.FactPatchRequest_builder{
			Ref:              app.FactRef_builder{Id: v.GetId()}.Build(),
			DateUpdatedForce: z.Ptr(true),
			SupersededAt:     at,
			SupersededBy:     children[0].GetId(),
		}.Build())
		x.NoError(err)

		got, err := b.Walled.Fact().Get(as, app.FactGetRequest_builder{
			Ref:    app.FactRef_builder{Id: v.GetId()}.Build(),
			Select: app.FactSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("colour", got.GetKey())
		x.Equal(`"red"`, got.GetValue())
		x.Equal(uint32(22), got.GetFieldNumber())
		x.Equal(uint32(7), got.GetDomain())
		x.Equal(at.AsTime().UnixMicro(), got.GetSupersededAt().AsTime().UnixMicro())
		x.Equal(children[0].GetId(), got.GetSupersededBy())

		ls, err := b.Walled.Fact().List(as, app.FactListRequest_builder{Filters: []*app.FactFilter{
			app.FactFilter_builder{SubjectId: ev.GetId(), FieldNumber: z.Ptr(uint32(6))}.Build(),
		}}.Build())
		x.NoError(err)
		x.Len(ls.GetItems(), 1)
		x.Equal("", ls.GetItems()[0].GetValue())

		// The columns as SQL names them: a predicate on each, and an order.
		n, err := b.Server.Ent.Fact.Query().
			Where(entfact.Key("colour"), entfact.Value(`"red"`), entfact.Domain(7), entfact.FieldNumber(22)).
			Count(ctx)
		x.NoError(err)
		x.Equal(1, n)

		fs, err := b.Server.Ent.Fact.Query().
			Where(entfact.Domain(7)).
			Order(entfact.ByFieldNumber(), entfact.ByKey()).
			All(ctx)
		x.NoError(err)
		x.Len(fs, 3)
		x.Equal(uint32(5), fs[0].FieldNumber)
	})
}

// TestAnEventIsReadBackByItsKind: `pd.EventKinds` names each of this app's
// event messages by the kind its rows are written under -- one that names no
// kind, by its full name -- and an event holding one, written through the
// server and kept as JSON in the database, is read back into the message its
// kind names, whole: an enum by its name, a list and a map of a message of its
// own, a time.
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
	held, err := anypb.New(sent)
	x.NoError(err)

	ev, err := b.Walled.Event().Add(as, app.EventAddRequest_builder{
		Tenant:     app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Kind:       "machine.service",
		OccurredAt: timestamppb.Now(),
		Value:      held,
	}.Build())
	x.NoError(err)

	got, err := b.Walled.Event().Get(as, app.EventGetRequest_builder{
		Ref:    app.EventRef_builder{Id: ev.GetId()}.Build(),
		Select: app.EventSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)

	typ, ok := pd.EventKinds[got.GetKind()]
	x.True(ok, "no event message for kind %q", got.GetKind())

	v := typ.New().Interface()
	x.NoError(got.GetValue().UnmarshalTo(v))
	x.True(proto.Equal(sent, v), "%v", v)
}
