package past

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/protobuf-orm/ent/dialect/sql"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdid"
)

// Fact is one statement about one resource, its subject: from `From` on, the
// fields `Values` name held the values they say -- `{status: "repair",
// firmware: "2.4"}` from 9/15.
//
// It is written as a row for each field, every one of them carrying the
// statement's event, subject and moment, in one statement of SQL; and read
// back as the statement it was, regrouped by those three ([Facts]).
type Fact struct {
	Subject pdid.Id

	// From is when the values became true, and the operation's own moment --
	// when it happened -- when it is zero.
	From time.Time

	Values []Value
}

// Value is one field of a [Fact]: set to a value ([To]), one entry of a map
// set or gone ([Entry]), or cleared ([Cleared]). [Changes] says the ones a
// write changed.
type Value struct {
	// Field is the field, of the subject's message.
	Field protoreflect.FieldDescriptor

	// Key is the entry of a map field, and no key for any other.
	Key protoreflect.MapKey

	// Value is what it holds, and no value for a field cleared or an entry
	// gone.
	Value protoreflect.Value

	// whole is the map an entry [Changes] found is of, as the write left it:
	// what a change to a map withheld is written as, for which there is no
	// entry to write but the field.
	whole protoreflect.Value
}

// To is the field `fd` set to `v`. A field a list is one value, the whole
// list; a map is a fact for each entry, so for a map field it is [Entry].
func To(fd protoreflect.FieldDescriptor, v protoreflect.Value) Value {
	return Value{Field: fd, Value: v}
}

// Entry is the entry `k` of the map field `fd` set to `v`, or gone for no
// value.
func Entry(fd protoreflect.FieldDescriptor, k protoreflect.MapKey, v protoreflect.Value) Value {
	return Value{Field: fd, Key: k, Value: v}
}

// Cleared is the field `fd` holding nothing. A map's entries go one by one,
// each an [Entry] with no value.
func Cleared(fd protoreflect.FieldDescriptor) Value { return Value{Field: fd} }

// Slot is which of a subject's fields a fact is about: the field, and for a
// map field the entry, by [Encode]'s text of its key.
type Slot struct {
	Field protoreflect.FieldNumber
	Key   string
}

func (s Slot) compare(t Slot) int {
	if c := cmp.Compare(s.Field, t.Field); c != 0 {
		return c
	}

	return utf16Compare(s.Key, t.Key)
}

// Changes is the values `after` holds differently from `before`, two of one
// message: each field set to what `after` holds, cleared where it holds
// nothing, and each entry of a map that changed. A nil `before` is a row made
// -- every field `after` holds -- and a nil `after` a row gone.
//
// A field is compared by its [Encode]d value, so an edge by the row it names
// and a message by what it holds. Three kinds of field are never a fact, as
// they are no part of what a thing was: the key, which is the subject, the
// version, which every write moves, and a field that cannot change once
// written -- an immutable one, the tenant among them, and `date_created`.
//
// Whether a field is withheld is not this one's to know: the values are as
// the messages hold them, and [Op.Set] withholds what the subject's entity
// declares.
func Changes(before, after proto.Message) []Value {
	switch {
	case before == nil && after == nil:
		return nil
	case before == nil:
		before = after.ProtoReflect().Type().New().Interface()
	case after == nil:
		after = before.ProtoReflect().Type().New().Interface()
	}

	b, a := before.ProtoReflect(), after.ProtoReflect()
	if b.Descriptor().FullName() != a.Descriptor().FullName() {
		panic(fmt.Sprintf("past: the changes from a %s to a %s", b.Descriptor().FullName(), a.Descriptor().FullName()))
	}

	var out []Value
	fs := a.Descriptor().Fields()
	for i := range fs.Len() {
		fd := fs.Get(i)
		if !isFact(fd) {
			continue
		}

		if fd.IsMap() {
			out = append(out, entries(fd, b.Get(fd).Map(), a.Get(fd).Map(), a.Get(fd))...)
			continue
		}

		was, is := held(b, fd), held(a, fd)
		switch {
		case !was.IsValid() && !is.IsValid():
		case !is.IsValid():
			out = append(out, Cleared(fd))
		case !was.IsValid() || !same(fd, was, is):
			out = append(out, To(fd, is))
		}
	}

	return out
}

// entries is the entries of the map field `fd` that changed from `b` to `a`,
// in the order of their keys. `whole` is `a`, as the field.
func entries(fd protoreflect.FieldDescriptor, b, a protoreflect.Map, whole protoreflect.Value) []Value {
	type entry struct {
		key string
		k   protoreflect.MapKey
	}
	var ks []entry
	seen := map[string]bool{}
	add := func(k protoreflect.MapKey, _ protoreflect.Value) bool {
		s, err := encodeKey(fd.MapKey(), k)
		if err != nil {
			// A key with no text; the value is compared by its own, and
			// [Op.Set] says what is wrong with it.
			s = k.String()
		}
		if !seen[s] {
			seen[s] = true
			ks = append(ks, entry{s, k})
		}
		return true
	}
	b.Range(add)
	a.Range(add)
	slices.SortFunc(ks, func(x, y entry) int { return utf16Compare(x.key, y.key) })

	var out []Value
	vd := fd.MapValue()
	for _, e := range ks {
		was, is := b.Get(e.k), a.Get(e.k)
		switch {
		case !was.IsValid() && !is.IsValid():
		case !is.IsValid():
			out = append(out, Value{Field: fd, Key: e.k, whole: whole})
		case !was.IsValid() || !same(vd, was, is):
			out = append(out, Value{Field: fd, Key: e.k, Value: is, whole: whole})
		}
	}

	return out
}

// isFact says a change of `fd` is a fact of what a thing was: every field but
// its key, its version, and one that cannot change once written.
func isFact(fd protoreflect.FieldDescriptor) bool {
	if o := ormField(fd); o.GetKey() || o.HasVersion() || o.GetImmutable() {
		return false
	}
	if o, _ := proto.GetExtension(fd.Options(), ormpb.E_Edge).(*ormpb.EdgeOptions); o.GetImmutable() {
		return false
	}

	return true
}

// held is the value `m` holds of `fd`, and no value where it holds nothing:
// a field it says is not set, a list with nothing in it, an identifier or an
// edge that names no row. A field with no presence, which holds its zero
// value when it is not set, holds that: a count of 0 is a count.
func held(m protoreflect.Message, fd protoreflect.FieldDescriptor) protoreflect.Value {
	if fd.HasPresence() && !m.Has(fd) {
		return protoreflect.Value{}
	}

	v := m.Get(fd)
	if empty(fd, v) {
		return protoreflect.Value{}
	}

	return v
}

// empty says `v`, a value of `fd`, is nothing: what is written as [Null].
func empty(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
	switch {
	case !v.IsValid():
		return true
	case fd.IsList():
		return v.List().Len() == 0
	case fd.IsMap():
		return v.Map().Len() == 0
	case isUuid(fd):
		return len(v.Bytes()) == 0
	case isEdge(fd):
		k := keyOf(v.Message().Descriptor())
		return k == nil || len(v.Message().Get(k).Bytes()) == 0
	}

	return false
}

// same says two values of `fd` are one value: their canonical text is.
func same(fd protoreflect.FieldDescriptor, a, b protoreflect.Value) bool {
	x, err := appendField(nil, fd, a)
	if err != nil {
		return false
	}
	y, err := appendField(nil, fd, b)
	if err != nil {
		return false
	}

	return string(x) == string(y)
}

// said is one row a statement is written as, before it is written.
type said struct {
	Slot
	value string
}

// compile is the rows `f` is written as, its subject being of `d`: for each
// value, its field's slot and the text of what it holds -- [Withheld] for a
// field `d` declares personal or secret, whatever it was set to, and [Null]
// for one cleared.
//
// A value withheld keeps no key either: a map's keys can be somebody's as
// much as its values are, so a change to a map withheld is a fact of the
// field, [Null] when it is left empty.
func compile(f Fact, d Description) ([]said, error) {
	md := d.Message
	out := make([]said, 0, len(f.Values))
	at := map[Slot]string{}
	for _, v := range f.Values {
		fd := v.Field
		if fd == nil {
			return nil, status.Errorf(codes.InvalidArgument, "a fact of %s names no field", f.Subject)
		}
		if fd.ContainingMessage().FullName() != md.FullName() || md.Fields().ByNumber(fd.Number()) == nil {
			return nil, status.Errorf(codes.InvalidArgument,
				"%s is not a field of %s, which %s is", fd.FullName(), md.FullName(), f.Subject)
		}

		var s said
		switch {
		case d.Withholds(fd):
			s = said{Slot{fd.Number(), ""}, withheld(fd, v)}

		case fd.IsMap():
			if !v.Key.IsValid() {
				return nil, status.Errorf(codes.InvalidArgument,
					"%s: a map is a fact for each entry, so it is set an entry at a time", fd.FullName())
			}
			key, err := encodeKey(fd.MapKey(), v.Key)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, err.Error())
			}
			s = said{Slot{fd.Number(), key}, Null}
			if v.Value.IsValid() {
				if s.value, err = Encode(fd.MapValue(), v.Value); err != nil {
					return nil, status.Error(codes.InvalidArgument, err.Error())
				}
			}

		default:
			if v.Key.IsValid() {
				return nil, status.Errorf(codes.InvalidArgument, "%s: a key is an entry of a map, and this is not one", fd.FullName())
			}
			s = said{Slot{fd.Number(), ""}, Null}
			if !empty(fd, v.Value) {
				var err error
				if s.value, err = Encode(fd, v.Value); err != nil {
					return nil, status.Error(codes.InvalidArgument, err.Error())
				}
			}
		}

		if was, ok := at[s.Slot]; ok {
			if fd.IsMap() && d.Withholds(fd) && was == s.value {
				// The entries of a map withheld, which are one fact of it.
				continue
			}

			return nil, status.Errorf(codes.InvalidArgument, "%s: twice in one fact", fd.FullName())
		}
		at[s.Slot] = s.value
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b said) int { return a.compare(b.Slot) })

	return out, nil
}

// withheld is what a value of a field withheld is written as: that it
// changed, [Withheld], and [Null] for a field left holding nothing.
func withheld(fd protoreflect.FieldDescriptor, v Value) string {
	switch {
	case v.whole.IsValid():
		// An entry [Changes] found: the map it left is what says.
		if v.whole.Map().Len() == 0 {
			return Null
		}
		return Withheld
	case fd.IsMap() && v.Key.IsValid():
		// An entry, set or gone, and nothing to say what the rest are.
		return Withheld
	case empty(fd, v.Value):
		return Null
	}

	return Withheld
}

// statement is a [Fact] compiled, and the event that writes it.
type statement struct {
	event   pdid.Id
	subject pdid.Id
	from    time.Time
	rows    []said
}

// Set writes `facts` as this operation's: each a row for each of its values,
// all in one AddMany, every row carrying the operation's event, the fact's
// subject and its moment.
//
// Each supersedes, field by field, the facts standing for that field of that
// subject from exactly the same moment -- a value said again for a moment
// replaces what was said for it -- and nothing else: a fact from another
// moment stands, and says what the field was from then. A fact with no
// values writes nothing.
//
// A subject is a row of the operation's tenant, or of no tenant at all; a
// row of another, and an identifier that names no row, are refused.
func (o *Op) Set(ctx context.Context, facts ...Fact) error {
	sts, err := o.compile(ctx, o.event, facts)
	if err != nil {
		return err
	}

	return o.write(ctx, sts)
}

// compile is `facts` as the statements `event` writes, each checked against
// its subject: a row of the operation's tenant, and described.
func (o *Op) compile(ctx context.Context, event pdid.Id, facts []Fact) ([]statement, error) {
	if err := o.agree(ctx, nil, subjectsOf(facts)); err != nil {
		return nil, err
	}

	return o.statements(event, o.at, facts)
}

func subjectsOf(facts []Fact) []pdid.Id {
	vs := make([]pdid.Id, 0, len(facts))
	for _, f := range facts {
		if f.Subject.IsZero() {
			// Refused by [Op.statements], which says which.
			continue
		}
		vs = append(vs, f.Subject)
	}

	return vs
}

// statements is `facts` as the statements `event` writes, each holding from
// `at` unless it says when: each compiled by what its subject's entity
// declares.
func (o *Op) statements(event pdid.Id, at time.Time, facts []Fact) ([]statement, error) {
	out := make([]statement, 0, len(facts))
	for _, f := range facts {
		if f.Subject.IsZero() {
			return nil, status.Error(codes.InvalidArgument, "a fact is of a subject, and this one names none")
		}
		d, ok := o.s.Describe(f.Subject)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "%s names no entity of this app's, so nothing says what its fields are", f.Subject)
		}

		rows, err := compile(f, d)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}

		from := at
		if !f.From.IsZero() {
			from = moment(f.From)
		}
		out = append(out, statement{event: event, subject: f.Subject, from: from, rows: rows})
	}

	return out, nil
}

// write supersedes what `sts` replace and writes them, a page of rows to a
// statement: what is superseded first, and then what supersedes it, so that
// no statement leaves two rows of one slot and one moment standing -- which a
// constraint on standing rows, an app's own, would refuse.
func (o *Op) write(ctx context.Context, sts []statement) error {
	if len(sts) == 0 {
		return nil
	}

	// Two values for one field of one subject from one moment, in one
	// operation, would stand side by side; neither is the one it meant.
	type at struct {
		Slot
		subject pdid.Id
		from    int64
	}
	seen := map[at]bool{}
	for _, st := range sts {
		for _, r := range st.rows {
			k := at{r.Slot, st.subject, st.from.UnixMicro()}
			if seen[k] {
				return status.Errorf(codes.InvalidArgument, "field %d of %s is said twice from %s in one operation",
					r.Field, st.subject, st.from.Format(time.RFC3339Nano))
			}
			seen[k] = true
		}
	}

	if err := o.supersedeAt(ctx, sts); err != nil {
		return err
	}

	rows := []FactRow{}
	for _, st := range sts {
		for _, r := range st.rows {
			rows = append(rows, FactRow{
				Tenant:   o.tenant,
				Subject:  st.subject,
				Domain:   st.subject.Domain(),
				Field:    r.Field,
				Key:      r.Key,
				Value:    r.value,
				From:     st.from,
				Event:    st.event,
				Recorded: o.now,
			})
		}
	}

	return o.s.AddFacts(ctx, rows)
}

// supersedeAt supersedes the facts standing for the slots `sts` write, of
// their subjects, from exactly their moments -- each by the event of the
// statement that replaces it.
func (o *Op) supersedeAt(ctx context.Context, sts []statement) error {
	by := map[pdid.Id][]statement{}
	subjects := []pdid.Id{}
	froms := []time.Time{}
	for _, st := range sts {
		if _, ok := by[st.subject]; !ok {
			subjects = append(subjects, st.subject)
		}
		by[st.subject] = append(by[st.subject], st)
		froms = append(froms, st.from)
	}
	froms = times(froms)

	where := []func(*sql.Selector){is(colTenant, o.tenant), standing()}
	if len(froms) <= page {
		// By the moments too, which the index on a subject's field reads
		// last; past a page of them, by the subjects alone, and the moments
		// are asked of the rows below.
		where = append(where, sql.FieldIn(colFrom, froms...))
	}

	replaced := map[pdid.Id][]FactRow{}
	for _, ids := range pages(subjects) {
		rows, err := o.s.Facts(ctx, Select{Where: append(slices.Clone(where), in(colSubject, ids))})
		if err != nil {
			return err
		}

		for _, r := range rows {
			for _, st := range by[r.Subject] {
				if !st.from.Equal(moment(r.From)) {
					continue
				}
				if slices.ContainsFunc(st.rows, func(v said) bool { return v.Slot == r.Slot() }) {
					replaced[st.event] = append(replaced[st.event], r)
				}
			}
		}
	}

	for _, ev := range slices.SortedFunc(maps.Keys(replaced), func(a, b pdid.Id) int { return a.Uuid().Compare(b.Uuid()) }) {
		if err := o.s.Supersede(ctx, replaced[ev], o.now, ev); err != nil {
			return err
		}
	}

	return nil
}

// Correct says the fact `fact` was true from `from` and not from when it
// says: it is superseded, and its value written again from `from`, as this
// operation's -- which is what the correction is, and why. What the system
// said before stays readable as it was known then.
//
// It corrects when a value became true. A value that was wrong is a fact of
// the right one from the same moment ([Op.Set]), which supersedes it.
func (o *Op) Correct(ctx context.Context, fact pdid.Id, from time.Time) error {
	r, err := o.standingFact(ctx, fact)
	if err != nil {
		return err
	}
	if err := o.s.Supersede(ctx, []FactRow{r}, o.now, o.event); err != nil {
		return err
	}

	return o.write(ctx, []statement{{
		event:   o.event,
		subject: r.Subject,
		from:    moment(from),
		rows:    []said{{r.Slot(), r.Value}},
	}})
}

// Retract says the fact `fact` never held: it is superseded by this
// operation, and nothing is written in its place. What it said before stays
// readable as it was known then.
func (o *Op) Retract(ctx context.Context, fact pdid.Id) error {
	r, err := o.standingFact(ctx, fact)
	if err != nil {
		return err
	}

	return o.s.Supersede(ctx, []FactRow{r}, o.now, o.event)
}

// CorrectEvent says the operation `event` happened at `from` -- "that move
// was on 9/28" -- for everything it wrote that still stands: each of its
// facts is superseded and written again from `from`, as this operation's, a
// statement for each subject. A fact of it another correction already
// replaced is that correction's, and stays.
//
// The event itself is not changed, as no event is: this operation's says
// what was corrected, and when to.
func (o *Op) CorrectEvent(ctx context.Context, event pdid.Id, from time.Time) error {
	rows, err := o.factsOf(ctx, event)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	from = moment(from)
	var sts []statement
	for _, r := range rows {
		i := slices.IndexFunc(sts, func(st statement) bool { return st.subject == r.Subject })
		if i < 0 {
			sts = append(sts, statement{event: o.event, subject: r.Subject, from: from})
			i = len(sts) - 1
		}
		if slices.ContainsFunc(sts[i].rows, func(v said) bool { return v.Slot == r.Slot() }) {
			// It said this field twice, from two moments; from one, it
			// would say neither.
			return status.Errorf(codes.FailedPrecondition,
				"%s said field %d of %s from more than one moment, which one moment cannot say", event, r.Field, r.Subject)
		}
		sts[i].rows = append(sts[i].rows, said{r.Slot(), r.Value})
	}

	if err := o.s.Supersede(ctx, rows, o.now, o.event); err != nil {
		return err
	}

	return o.write(ctx, sts)
}

// RetractEvent says the operation `event` never happened, for everything it
// wrote that still stands: each of its facts is superseded by this one, and
// nothing is written in their place.
func (o *Op) RetractEvent(ctx context.Context, event pdid.Id) error {
	rows, err := o.factsOf(ctx, event)
	if err != nil || len(rows) == 0 {
		return err
	}

	return o.s.Supersede(ctx, rows, o.now, o.event)
}

// standingFact is the fact `id` of the operation's tenant, which no
// correction or later fact replaced.
func (o *Op) standingFact(ctx context.Context, id pdid.Id) (FactRow, error) {
	rows, err := o.s.Facts(ctx, Select{Where: []func(*sql.Selector){
		is(colTenant, o.tenant),
		is(colId, id),
		standing(),
	}, Limit: 1})
	if err != nil {
		return FactRow{}, err
	}
	if len(rows) == 0 {
		return FactRow{}, status.Errorf(codes.NotFound, "no fact %s stands", id)
	}

	return rows[0], nil
}

// factsOf is the facts the event `event` of the operation's tenant wrote that
// still stand, and NotFound for an event that is not there.
func (o *Op) factsOf(ctx context.Context, event pdid.Id) ([]FactRow, error) {
	evs, err := o.s.Events(ctx, Select{Where: []func(*sql.Selector){
		is(colTenant, o.tenant),
		is(colId, event),
	}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 {
		return nil, status.Errorf(codes.NotFound, "event %s not found", event)
	}

	return o.s.Facts(ctx, Select{
		Where: []func(*sql.Selector){is(colTenant, o.tenant), is(colEvent, event), standing()},
		Order: order(colSubject, colField, colKey, colFrom, colId),
	})
}
