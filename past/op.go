package past

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// Event is an operation's event as [Begin] is given it: what happened, to
// what, when and why. Who did it, in which tenant, as part of which trace and
// of which other operation are not the caller's to say; [Begin] reads them.
type Event struct {
	// Id is the operation's own identifier, an event's: for an operation a
	// client may send twice, which names itself so that the second copy is
	// told [ErrDone] rather than done again. Zero for one that does not, which
	// is given one.
	Id pdid.Id

	// Subject is the thing it is about, so that its timeline finds it; zero
	// for an operation about nothing in particular.
	Subject pdid.Id

	// At is when it happened, and the moment it is written down when zero.
	At time.Time

	// Reason is why, in the words of whoever asked: personal, and kept off
	// the trail ([Redact]).
	Reason string

	// Value is what was done: a message the app declares an event message,
	// `option (payday.event)`, whose kind the event is written under.
	Value proto.Message
}

// Op is one operation in progress, in the transaction its event is written
// in: what writes facts as its own, and the children it is made of.
//
// It is used from the goroutine of its transaction, as the transaction is.
type Op struct {
	s Store

	event  pdid.Id
	tenant pdid.Id
	actor  pdid.Id
	trace  []byte

	// at is when it happened, and now when it was written down: what the
	// facts it writes are known from, and the ones it supersedes stop being.
	at  time.Time
	now time.Time
}

// Id is the operation's event.
func (o *Op) Id() pdid.Id { return o.event }

// At is when the operation happened, which is what a fact it writes holds
// from when it says no moment of its own.
func (o *Op) At() time.Time { return o.at }

// Recorded is when the operation was written down.
func (o *Op) Recorded() time.Time { return o.now }

// causeKey is the operation in progress, on the context [Begin] answers with.
type causeKey struct{}

// cause is an operation in progress, and the transaction it is in: an
// operation begun in another -- after this one committed, say, on a context
// it left behind -- is not part of it.
type cause struct {
	event pdid.Id
	tx    *dialect.Keep
}

// causeOf is the operation `ctx` says this one is part of: one begun in the
// transaction `drv` is, and nil for any other.
func causeOf(ctx context.Context, drv dialect.Driver) pdid.Id {
	c, ok := ctx.Value(causeKey{}).(cause)
	if !ok || c.event.IsZero() {
		return pdid.Nil
	}
	if k, ok := dialect.KeepOf(drv); !ok || k != c.tx {
		return pdid.Nil
	}

	return c.event
}

// Detach is `ctx` with no operation in it: what is begun with it is part of
// nothing, even in the transaction of the operation `ctx` was part of. For an
// operation that one runs as the deployment's own work, say, rather than as a
// part of itself.
//
// A function of the context and not of an [Op]: what an operation is part of
// is written with its event, so it is said before [Begin], to what it is
// begun with.
func Detach(ctx context.Context) context.Context {
	return context.WithValue(ctx, causeKey{}, cause{})
}

// Begin records the event an operation is, and answers with the operation
// and a context for the operations it calls: in the same transaction, each is
// part of it.
//
// The store is on the operation's transaction, and Begin refuses one that is
// on none ([ErrNotInTx]): the event and every write it is about commit
// together or not at all. The rest is read rather than given -- who did it
// and in which tenant from the caller's frame, which a call without one is
// refused for; the trace from the span; what it is part of from `ctx`, when
// that is an operation in this transaction; its kind from what `ev.Value`
// declares, which the app's schema must declare as well -- so that none of it
// is a thing a caller could say otherwise.
//
// The subject is a row of the caller's tenant, or of no tenant, and one of
// another is refused as not found. An identifier that names no row is taken
// at its word: an operation records its event before the row it makes. So it
// is the check behind the one an operation makes itself, reading its subject
// through the wall, and not instead of it.
//
// With `ev.Id`, an operation a client may send twice, Begin holds that
// identifier until the transaction ends -- on PostgreSQL a copy sent while the
// first is running waits for it here -- and answers [ErrDone] when its event
// is there: the operation already happened, and the copy writes nothing.
//
// The operation's moment is taken after that wait, so one that waited is
// written down after the one it waited for. An operation that takes a lock of
// its own takes it before it begins, for the same reason.
func Begin(ctx context.Context, s Store, ev Event) (context.Context, *Op, error) {
	drv := s.Driver()
	if !dialect.InTx(drv) {
		return ctx, nil, ErrNotInTx
	}

	f, err := gate.Actor(ctx)
	if err != nil {
		return ctx, nil, err
	}
	if f.Tenant.IsZero() {
		return ctx, nil, status.Error(codes.FailedPrecondition, "an event is filed under the tenant of whoever did it, and this caller is of none")
	}

	kind, value, err := kindOf(s, ev.Value)
	if err != nil {
		return ctx, nil, err
	}

	if !ev.Id.IsZero() {
		if err := lock(ctx, drv, ev.Id); err != nil {
			return ctx, nil, err
		}

		vs, err := s.Events(ctx, Select{Where: []func(*sql.Selector){
			is(colTenant, f.Tenant),
			is(colId, ev.Id),
		}, Limit: 1})
		if err != nil {
			return ctx, nil, err
		}
		if len(vs) > 0 {
			return ctx, nil, ErrDone
		}
	}

	o := &Op{s: s, tenant: f.Tenant, actor: f.Actor, trace: traceOf(ctx), now: now(s)}
	o.at = o.now
	if !ev.At.IsZero() {
		o.at = moment(ev.At)
	}

	if err := o.agree(ctx, []pdid.Id{ev.Subject}, nil); err != nil {
		return ctx, nil, err
	}

	ids, err := s.AddEvents(ctx, []EventRow{{
		Id:       ev.Id,
		Tenant:   o.tenant,
		Kind:     kind,
		Actor:    o.actor,
		At:       o.at,
		Reason:   ev.Reason,
		Subject:  ev.Subject,
		Trace:    o.trace,
		Cause:    causeOf(ctx, drv),
		Value:    value,
		Recorded: o.now,
	}})
	if err != nil {
		return ctx, nil, err
	}
	o.event = ids[0]

	k, _ := dialect.KeepOf(drv)
	return context.WithValue(ctx, causeKey{}, cause{o.event, k}), o, nil
}

// Child is one thing an operation over many changed: an event of that
// thing's own, its cause the operation's, and the facts it wrote of it.
type Child struct {
	Event Event
	Facts []Fact
}

// Child records an event that is part of this operation -- what it did to one
// of the many things it is over -- and answers with it as an operation of its
// own, whose facts point at it. It is done by whoever did this one, when this
// one was, unless it says when.
func (o *Op) Child(ctx context.Context, ev Event) (*Op, error) {
	vs, err := o.Batch(ctx, []Child{{Event: ev}})
	if err != nil {
		return nil, err
	}

	return vs[0], nil
}

// Batch records many children of this operation at once: their events, a
// page to a statement, and then their facts the same way, each fact pointing
// at its child's event. It answers with the children, in order.
//
// What a child's facts replace is superseded by the child, as [Op.Set] does,
// and every subject is checked as [Begin] and [Op.Set] check theirs, all of
// them in one read a page at a time rather than one each.
func (o *Op) Batch(ctx context.Context, children []Child) ([]*Op, error) {
	if len(children) == 0 {
		return nil, nil
	}

	rows := make([]EventRow, len(children))
	subjects := make([]pdid.Id, 0, len(children))
	for i, c := range children {
		kind, value, err := kindOf(o.s, c.Event.Value)
		if err != nil {
			return nil, fmt.Errorf("child %d: %w", i, err)
		}

		at := o.at
		if !c.Event.At.IsZero() {
			at = moment(c.Event.At)
		}
		rows[i] = EventRow{
			Id:       c.Event.Id,
			Tenant:   o.tenant,
			Kind:     kind,
			Actor:    o.actor,
			At:       at,
			Reason:   c.Event.Reason,
			Subject:  c.Event.Subject,
			Trace:    o.trace,
			Cause:    o.event,
			Value:    value,
			Recorded: o.now,
		}
		subjects = append(subjects, c.Event.Subject)
	}

	// The facts before anything is written, so that a child refused for
	// what it says writes nothing of the others either -- their subjects in
	// the read the events' are in.
	var of []pdid.Id
	for _, c := range children {
		of = append(of, subjectsOf(c.Facts)...)
	}
	if err := o.agree(ctx, subjects, of); err != nil {
		return nil, err
	}
	facts := make([][]statement, len(children))
	for i, c := range children {
		sts, err := o.statements(pdid.Nil, rows[i].At, c.Facts)
		if err != nil {
			return nil, fmt.Errorf("child %d: %w", i, err)
		}
		facts[i] = sts
	}

	ids, err := o.s.AddEvents(ctx, rows)
	if err != nil {
		return nil, err
	}

	out := make([]*Op, len(children))
	var sts []statement
	for i, id := range ids {
		out[i] = &Op{s: o.s, event: id, tenant: o.tenant, actor: o.actor, trace: o.trace, at: rows[i].At, now: o.now}
		for _, st := range facts[i] {
			st.event = id
			sts = append(sts, st)
		}
	}

	if err := o.write(ctx, sts); err != nil {
		return nil, err
	}

	return out, nil
}

// Made is the `n`th thing of the domain `d` this operation makes, named after
// the operation: its identifier in that domain, the `n`th differing from it
// in its last two bytes, which are random. A copy of the operation sent again
// names what it would make by the same names -- an asset it adds, the
// children it records -- and so finds what the first copy made.
//
// `n` is at most 65535, and the operation's own event is (its domain, 0).
func (o *Op) Made(d pdid.Domain, n int) pdid.Id {
	if n < 0 || n > 0xFFFF {
		panic(fmt.Sprintf("past: the %dth thing an operation makes; it makes at most 65535 of a kind", n))
	}

	v := o.event
	v[14] ^= byte(n >> 8)
	v[15] ^= byte(n)

	return pdid.WithDomain(v, d)
}

// agree refuses a subject of another tenant than the operation's: as not
// found, since that a row exists in a tenant the caller is not in is itself
// something not to say. A row of no tenant agrees with every one.
//
// An identifier that names no row is an event's to be about -- `events` --
// since an operation records its event before the row it makes; and no
// fact's -- `facts` -- which is of a row's fields. Both in one read.
func (o *Op) agree(ctx context.Context, events, facts []pdid.Id) error {
	row := map[pdid.Id]bool{}
	var vs []pdid.Id
	for i, v := range slices.Concat(events, facts) {
		if v.IsZero() {
			continue
		}
		if _, ok := row[v]; !ok {
			vs = append(vs, v)
		}
		row[v] = row[v] || i >= len(events)
	}
	if len(vs) == 0 {
		return nil
	}

	tenants, err := o.s.TenantOf(ctx, vs)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "%s", err)
	}
	for _, v := range vs {
		t, ok := tenants[v]
		switch {
		case !ok && row[v]:
			return status.Errorf(codes.NotFound, "%s not found", v)
		case ok && !t.IsZero() && t != o.tenant:
			return status.Errorf(codes.NotFound, "%s not found", v)
		}
	}

	return nil
}

// kindOf is the kind an event holding `v` is written under, read off what
// `v` declares, and `v` as the event keeps it.
func kindOf(s Store, v proto.Message) (string, *anypb.Any, error) {
	if v == nil || !v.ProtoReflect().IsValid() {
		return "", nil, status.Error(codes.InvalidArgument, "an event holds what happened: a message the app declares with (payday.event)")
	}

	md := v.ProtoReflect().Descriptor()
	kind, ok := declared(md)
	if !ok {
		return "", nil, status.Errorf(codes.InvalidArgument, "%s says no (payday.event), so it is no event message", md.FullName())
	}
	if t, ok := s.Kinds()[kind]; !ok || t.Descriptor().FullName() != md.FullName() {
		return "", nil, status.Errorf(codes.InvalidArgument,
			"%s is of kind %q, which this app's schema does not declare: a row of it would be read back by nothing", md.FullName(), kind)
	}

	value, err := anypb.New(v)
	if err != nil {
		return "", nil, err
	}

	return kind, value, nil
}

// declared is the kind an event holding a message of `md` is written under --
// what its `(payday.event)` says, or its full name where that names none --
// and false for a message that declares no `(payday.event)` at all: what an
// event is written under, and what a sentence of it is said for.
func declared(md protoreflect.MessageDescriptor) (string, bool) {
	o, _ := proto.GetExtension(md.Options(), pdpb.E_Event).(*pdpb.EventMessage)
	if o == nil {
		return "", false
	}
	if kind := o.GetKind(); kind != "" {
		return kind, true
	}

	return string(md.FullName()), true
}

// lock holds `id` until the transaction `drv` is in ends, on PostgreSQL: a
// transaction's advisory lock on its last eight bytes, which are random. A
// copy of an operation sent while the first is running waits for it here,
// and then sees what it did -- without the wait, both would find nothing,
// and the second fail on the event's key rather than be told the first had
// happened. SQLite has one writer at a time, so the copy after has nothing to
// wait for.
//
// The one-key form, which PostgreSQL keeps apart from the two-key one the
// leases of `spin/spinpg` are taken in.
func lock(ctx context.Context, drv dialect.Driver, id pdid.Id) error {
	if drv.Dialect() != dialect.Postgres {
		return nil
	}

	key := int64(binary.BigEndian.Uint64(id[8:]))
	return drv.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", []any{key}, nil)
}

// traceOf is the trace `ctx` is part of, and empty for none.
func traceOf(ctx context.Context) []byte {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return []byte{}
	}

	id := sc.TraceID()
	return id[:]
}
