package past

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
)

// EventQuery is which events [Events] reads: of these identifiers, about
// these subjects, or part of these operations. At least one of them; a read
// of every event is a List's.
type EventQuery struct {
	Ids      []pdid.Id
	Subjects []pdid.Id
	Causes   []pdid.Id
}

// Events reads events back, oldest first, of the tenants the caller of `ctx`
// may see.
//
// An event's reason is personal, so one read back with a reason is a row of
// the access record of the call that read it (`access.Saw`): no RPC answered
// with it, so no declaration sees it on its way out.
func Events(ctx context.Context, s Store, q EventQuery) ([]EventRow, error) {
	if len(q.Ids)+len(q.Subjects)+len(q.Causes) == 0 {
		return nil, status.Error(codes.InvalidArgument, "a read of events names which: by identifier, subject or cause")
	}

	wall, err := walled(ctx)
	if err != nil {
		return nil, err
	}

	var out []EventRow
	seen := map[pdid.Id]bool{}
	for _, by := range []struct {
		col string
		ids []pdid.Id
	}{{colId, q.Ids}, {colSubject, q.Subjects}, {colCause, q.Causes}} {
		for _, ids := range pages(by.ids) {
			vs, err := s.Events(ctx, Select{
				Where: append(slices.Clone(wall), in(by.col, ids)),
				Order: order(colRecorded, colId),
			})
			if err != nil {
				return nil, err
			}
			for _, v := range vs {
				if !seen[v.Id] {
					seen[v.Id] = true
					out = append(out, v)
				}
			}
		}
	}
	slices.SortStableFunc(out, func(a, b EventRow) int {
		if c := a.Recorded.Compare(b.Recorded); c != 0 {
			return c
		}
		return a.Id.Uuid().Compare(b.Id.Uuid())
	})

	var saw []access.Subject
	for _, v := range out {
		if v.Reason != "" {
			saw = append(saw, access.Subject{Id: v.Id, Tenant: v.Tenant})
		}
	}
	access.Saw(ctx, saw...)

	return out, nil
}

// FactQuery is which facts [Facts] reads: of these subjects, written by these
// events, or both, as `Known` takes them.
type FactQuery struct {
	Subjects []pdid.Id
	Events   []pdid.Id
	Known    Known
}

// Statement is facts read back as they were written: the rows one event wrote
// of one subject from one moment on, in the order of their fields.
//
// A correction of one field supersedes that field's row alone, and the row it
// writes in its place is a statement of its own, the correcting event's.
type Statement struct {
	Event   pdid.Id
	Subject pdid.Id
	From    time.Time
	Rows    []FactRow
}

// Facts reads facts back as the statements they were written in, regrouped
// by event, subject and moment -- every row carries those three -- oldest
// first, of the tenants the caller of `ctx` may see.
func Facts(ctx context.Context, s Store, q FactQuery) ([]Statement, error) {
	if len(q.Subjects)+len(q.Events) == 0 {
		return nil, status.Error(codes.InvalidArgument, "a read of facts names which: by subject or by event")
	}

	wall, err := walled(ctx)
	if err != nil {
		return nil, err
	}

	var rows []FactRow
	read := func(where []func(*sql.Selector)) error {
		vs, err := s.Facts(ctx, Select{
			Where: append(append(slices.Clone(wall), known(q.Known)...), where...),
			Order: order(colSubject, colFrom, colRecorded, colId),
		})
		rows = append(rows, vs...)
		return err
	}

	switch {
	case len(q.Subjects) > 0:
		events := pages(q.Events)
		for _, ids := range pages(q.Subjects) {
			if len(events) == 0 {
				if err := read([]func(*sql.Selector){in(colSubject, ids)}); err != nil {
					return nil, err
				}
				continue
			}
			for _, evs := range events {
				if err := read([]func(*sql.Selector){in(colSubject, ids), in(colEvent, evs)}); err != nil {
					return nil, err
				}
			}
		}
	default:
		for _, evs := range pages(q.Events) {
			if err := read([]func(*sql.Selector){in(colEvent, evs)}); err != nil {
				return nil, err
			}
		}
	}

	return regroup(rows), nil
}

// regroup is `rows` as the statements they were written in: by event,
// subject and moment, each in the order of its fields, the statements in the
// order they hold from, then were written down, then of their events.
func regroup(rows []FactRow) []Statement {
	type key struct {
		event, subject pdid.Id
		from           int64
	}
	at := map[key]int{}
	var out []Statement
	for _, r := range rows {
		k := key{r.Event, r.Subject, r.From.UnixMicro()}
		i, ok := at[k]
		if !ok {
			i = len(out)
			at[k] = i
			out = append(out, Statement{Event: r.Event, Subject: r.Subject, From: r.From})
		}
		out[i].Rows = append(out[i].Rows, r)
	}

	for i := range out {
		slices.SortFunc(out[i].Rows, func(a, b FactRow) int {
			if c := a.Slot().compare(b.Slot()); c != 0 {
				return c
			}
			return a.Recorded.Compare(b.Recorded)
		})
	}
	first := func(st Statement) time.Time {
		v := st.Rows[0].Recorded
		for _, r := range st.Rows[1:] {
			if r.Recorded.Before(v) {
				v = r.Recorded
			}
		}
		return v
	}
	slices.SortStableFunc(out, func(a, b Statement) int {
		if c := a.From.Compare(b.From); c != 0 {
			return c
		}
		if c := first(a).Compare(first(b)); c != 0 {
			return c
		}
		if c := a.Event.Uuid().Compare(b.Event.Uuid()); c != 0 {
			return c
		}
		return a.Subject.Uuid().Compare(b.Subject.Uuid())
	})

	return out
}

// State is what one subject was at a moment: for each of its fields that held
// something then, the fact that says what.
type State struct {
	Subject pdid.Id

	// Message is what the subject's rows are, and nil for an identifier of
	// no entity of the app's.
	Message protoreflect.MessageDescriptor

	// Values is the fact each field held by, a map's by entry. A field that
	// held nothing -- cleared, or never said -- is not in it, and one whose
	// value is withheld is, with no value.
	Values map[Slot]FactRow
}

// StateAt is what each of `subjects` was at `at`, by what is known now -- the
// facts no correction or later fact replaced -- or, with `known`, by what was
// known then: the facts written down by then that had not yet been replaced.
// The first is what a thing was; the second, what the system said it was, at
// the moment somebody looked.
//
// Every subject asked about has a state, with no values for one nothing is
// known of. Of the tenants the caller of `ctx` may see.
func StateAt(ctx context.Context, s Store, subjects []pdid.Id, at time.Time, known *time.Time) (map[pdid.Id]*State, error) {
	wall, err := walled(ctx)
	if err != nil {
		return nil, err
	}

	at = moment(at)
	k := Known{}
	if known != nil {
		v := moment(*known)
		k.At = &v
	}

	out := map[pdid.Id]*State{}
	for _, ids := range pages(subjects) {
		rows, err := s.Facts(ctx, Select{
			Where: append(append(slices.Clone(wall), knownOf(k)...), in(colSubject, ids), sql.FieldLTE(colFrom, at)),
			Order: order(colSubject, colFrom, colRecorded, colId),
		})
		if err != nil {
			return nil, err
		}

		by := map[pdid.Id][]FactRow{}
		for _, r := range rows {
			by[r.Subject] = append(by[r.Subject], r)
		}
		for _, id := range ids {
			st := &State{Subject: id, Values: fold(by[id], at, k)}
			if d, ok := s.Describe(id); ok {
				st.Message = d.Message
			}
			out[id] = st
		}
	}

	return out, nil
}

// knownOf is [known], which [StateAt] reads by: never every fact ever
// written, which no moment's state is made of.
func knownOf(k Known) []func(*sql.Selector) {
	k.All = false
	return known(k)
}

// fold is what one subject's facts say it was at `at`, as `k` takes them: for
// each field, the value of the fact of it that holds from latest, by `at`, of
// the ones `k` takes -- written down last, of two from one moment. A field
// cleared holds nothing, and is not in what it answers.
//
// The facts are asked of the database by the same rules; they are asked
// again here, of each row, so that what a state is is said in one place a
// test can hold it to.
func fold(rows []FactRow, at time.Time, k Known) map[Slot]FactRow {
	rows = slices.Clone(rows)
	slices.SortStableFunc(rows, func(a, b FactRow) int {
		if c := a.From.Compare(b.From); c != 0 {
			return c
		}
		if c := a.Recorded.Compare(b.Recorded); c != 0 {
			return c
		}
		return a.Id.Uuid().Compare(b.Id.Uuid())
	})

	out := map[Slot]FactRow{}
	for _, r := range rows {
		if r.From.After(at) || !visible(r, k) {
			continue
		}
		if r.Value == Null {
			delete(out, r.Slot())
			continue
		}
		out[r.Slot()] = r
	}

	return out
}

// Into puts what the subject was into `m`, a message of what its rows are:
// `m` is reset, and holds the subject's identifier and each field the state
// holds a value of. A field withheld is left empty, as is one the state holds
// nothing of; a field this build's message no longer has is passed over.
func (st *State) Into(m proto.Message) error {
	r := m.ProtoReflect()
	md := r.Descriptor()
	if st.Message != nil && md.FullName() != st.Message.FullName() {
		return fmt.Errorf("past: %s is a %s, and this is a %s", st.Subject, st.Message.FullName(), md.FullName())
	}

	proto.Reset(m)
	if k := keyOf(md); k != nil && k.Kind() == protoreflect.BytesKind && !st.Subject.IsZero() {
		r.Set(k, protoreflect.ValueOfBytes(st.Subject.Bytes()))
	}

	for _, slot := range slices.SortedFunc(maps.Keys(st.Values), Slot.compare) {
		row := st.Values[slot]
		fd := md.Fields().ByNumber(slot.Field)
		if fd == nil || row.Withheld() || row.Value == Null {
			continue
		}

		raw, err := parse(row.Value)
		if err != nil {
			return fmt.Errorf("past: fact %s: %w", row.Id, err)
		}
		if !fd.IsMap() {
			if err := decodeInto(r, fd, raw); err != nil {
				return fmt.Errorf("past: fact %s: %w", row.Id, err)
			}
			continue
		}

		key, err := decodeKey(fd.MapKey(), slot.Key)
		if err != nil {
			return fmt.Errorf("past: fact %s: %w", row.Id, err)
		}
		mp := r.Mutable(fd).Map()
		v, err := decodeSingular(fd.MapValue(), raw, func() protoreflect.Message { return mp.NewValue().Message() })
		if err != nil {
			return fmt.Errorf("past: fact %s: %w", row.Id, err)
		}
		mp.Set(key, v)
	}

	return nil
}

// Difference is one field of a subject that held one thing at one moment and
// another at the other: [Null] for nothing, and [Withheld] for a value not
// written down.
type Difference struct {
	Slot
	Before, After string
}

// Diff is what changed from `before` to `after`, two states of one subject,
// field by field in the order of their numbers. A field withheld in both
// changed when a fact of it was written between the two, which is all that
// is known of it.
func Diff(before, after *State) []Difference {
	slots := map[Slot]bool{}
	for k := range before.Values {
		slots[k] = true
	}
	for k := range after.Values {
		slots[k] = true
	}

	var out []Difference
	for _, k := range slices.SortedFunc(maps.Keys(slots), Slot.compare) {
		a, had := before.Values[k]
		b, has := after.Values[k]
		x, y := Null, Null
		if had {
			x = a.Value
		}
		if has {
			y = b.Value
		}

		switch {
		case x != y:
		case x == Withheld && a.Id != b.Id:
		default:
			continue
		}
		out = append(out, Difference{Slot: k, Before: x, After: y})
	}

	return out
}

// Line is one line of a [Timeline]: an event about the subject, or a
// statement of its facts and the event that wrote it -- nil where that event
// is gone, or is of a tenant the caller may not see.
type Line struct {
	// At is when it held: a statement's moment, an event's.
	At time.Time

	Event     *EventRow
	Statement *Statement
}

// Timeline is one subject's history: its facts, as `known` takes them, as
// the statements they were written in, and the events about it -- written
// down by `known.At`, for a read as known then -- newest first, by when each
// held and then by when it was written down. Of the tenants the caller of
// `ctx` may see.
func Timeline(ctx context.Context, s Store, subject pdid.Id, known Known) ([]Line, error) {
	sts, err := Facts(ctx, s, FactQuery{Subjects: []pdid.Id{subject}, Known: known})
	if err != nil {
		return nil, err
	}
	evs, err := Events(ctx, s, EventQuery{Subjects: []pdid.Id{subject}})
	if err != nil {
		return nil, err
	}
	if known.At != nil {
		evs = slices.DeleteFunc(evs, func(v EventRow) bool { return v.Recorded.After(*known.At) })
	}

	byId := map[pdid.Id]*EventRow{}
	for i := range evs {
		byId[evs[i].Id] = &evs[i]
	}
	var others []pdid.Id
	for _, st := range sts {
		if _, ok := byId[st.Event]; !ok {
			others = append(others, st.Event)
		}
	}
	if len(others) > 0 {
		vs, err := Events(ctx, s, EventQuery{Ids: others})
		if err != nil {
			return nil, err
		}
		for i := range vs {
			byId[vs[i].Id] = &vs[i]
		}
	}

	out := make([]Line, 0, len(sts)+len(evs))
	for i := range evs {
		out = append(out, Line{At: evs[i].At, Event: &evs[i]})
	}
	for i := range sts {
		out = append(out, Line{At: sts[i].From, Event: byId[sts[i].Event], Statement: &sts[i]})
	}

	recorded := func(e Line) time.Time {
		if e.Statement != nil {
			return e.Statement.Rows[0].Recorded
		}
		return e.Event.Recorded
	}
	slices.SortStableFunc(out, func(a, b Line) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		if c := recorded(b).Compare(recorded(a)); c != 0 {
			return c
		}
		// An event before what it wrote, as it is read.
		return cmp.Compare(btoi(a.Statement != nil), btoi(b.Statement != nil))
	})

	return out, nil
}

func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}
