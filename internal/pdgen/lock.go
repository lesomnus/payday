package pdgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Lock is the record of every kind of event an app has declared, of the
// message the rows of each hold, and of what a row of each message holds
// (payday#151): `proto/<pkg>/payday/past.lock`.
//
// # Why a lock
//
// A row of payday's Event keeps its value as the canonical protobuf JSON of
// the message its kind names, and reads it back by name, for as long as the
// tenant keeps its history -- which is longer than any version of the code
// that wrote it. So a kind is never dropped and never moved to another
// message, a message its rows hold is never deleted, a field of one is never
// removed and keeps its JSON name, its kind and whether it is a list or a
// map, and a value of an enum one holds is never renamed: each of those is
// every row written before it no longer reading, or reading as something
// else.
//
// `buf breaking` says some of that, and forgets it: it compares one change
// with the one before, so a deletion that lands is the new baseline, and the
// next change is compared with a schema that never had it. Rows do not forget.
// The lock is what does not either: `pd gen` only adds to it, and refuses a
// schema that lost or changed anything in it.
//
// # What it records
//
// Exactly what decides whether an old row still reads, and no more. A kind is
// recorded with the full name of its message, and a message once, however
// many kinds hold it: what a row of a kind is read back with is its message,
// so a message may take more kinds and its rows of the others read as they
// did. A field is recorded by number with its JSON name and not its name, so
// renaming one that keeps its `json_name` is not a change here, as it is not
// to a row. Its kind is protobuf's, and its type the full name of the message
// or enum it holds. Every message an event message holds is recorded as it
// is, and every enum one of them holds, by number, with the name a row is
// written with -- protobuf's own types excepted, which protobuf keeps.
type Lock struct {
	// Kinds is every kind of event, with the full name of the event message
	// its rows hold.
	Kinds map[string]string `json:"kinds,omitempty"`

	// Messages is every message an event message holds, the event messages
	// themselves among them, by full name.
	Messages map[string]LockMessage `json:"messages,omitempty"`

	// Enums is every enum one of those messages holds, by full name.
	Enums map[string]LockEnum `json:"enums,omitempty"`
}

// LockMessage is a message's fields, by number.
type LockMessage map[int32]LockField

// LockField is what a row of the message holds at one number.
type LockField struct {
	// JSON is the name the field is written under in a row.
	JSON string `json:"json"`

	// Kind is protobuf's kind of what it holds -- of a map, of its values --
	// as `protoreflect.Kind` says it: `string`, `int64`, `message`, `enum`.
	Kind string `json:"kind"`

	// Type is the full name of the message or enum it holds, and empty for a
	// scalar.
	Type string `json:"type,omitempty"`

	// List says it is repeated. Map is the kind of a map's key, and empty for
	// a field that is not one.
	List bool   `json:"list,omitempty"`
	Map  string `json:"map,omitempty"`
}

// LockEnum is an enum's values: by number, the name a row is written with.
type LockEnum map[int32]string

// EventsLock is the lock of the event messages a schema declares: what the
// committed one is held to, and what is added to it.
//
// Pure, and of the schema [Read] accepted -- never of one it refused, so a
// lock never records an event message that holds an Any or a person.
func EventsLock(s *Schema) *Lock {
	l := &Lock{
		Kinds:    map[string]string{},
		Messages: map[string]LockMessage{},
		Enums:    map[string]LockEnum{},
	}
	for _, v := range s.Events {
		for _, kind := range v.Kinds {
			l.Kinds[kind] = string(v.FullName())
		}
		l.message(v.Message.Desc)
	}

	return l
}

func (l *Lock) message(m protoreflect.MessageDescriptor) {
	name := string(m.FullName())
	if _, ok := l.Messages[name]; ok {
		return
	}

	vs := LockMessage{}
	l.Messages[name] = vs

	fs := m.Fields()
	for i := range fs.Len() {
		f := fs.Get(i)

		v := LockField{JSON: f.JSONName()}
		held := f
		switch {
		case f.IsMap():
			v.Map = f.MapKey().Kind().String()
			held = f.MapValue()
		case f.IsList():
			v.List = true
		}

		v.Kind = held.Kind().String()
		switch held.Kind() {
		case protoreflect.MessageKind, protoreflect.GroupKind:
			v.Type = string(held.Message().FullName())
			if !wellKnown(held.Message()) {
				l.message(held.Message())
			}
		case protoreflect.EnumKind:
			v.Type = string(held.Enum().FullName())
			if !wellKnown(held.Enum()) {
				l.enum(held.Enum())
			}
		}

		vs[int32(f.Number())] = v
	}
}

func (l *Lock) enum(e protoreflect.EnumDescriptor) {
	name := string(e.FullName())
	if _, ok := l.Enums[name]; ok {
		return
	}

	vs := LockEnum{}
	l.Enums[name] = vs

	// The first name of a number, which is the one protobuf writes a row with
	// when several share it.
	es := e.Values()
	for i := range es.Len() {
		v := es.Get(i)
		if _, ok := vs[int32(v.Number())]; !ok {
			vs[int32(v.Number())] = string(v.Name())
		}
	}
}

// ErrLock is a schema that lost or changed what its lock holds.
type ErrLock struct {
	// Lost is each thing, as a sentence, in the order they sort in.
	Lost []string
}

func (e *ErrLock) Error() string {
	b := &strings.Builder{}
	b.WriteString("holds what this schema no longer declares as it was. A row of an event keeps its " +
		"value as JSON and reads it back by name, for as long as the tenant keeps its history, " +
		"so every row written before this would stop reading or read as something else:\n\n")
	for _, v := range e.Lost {
		b.WriteString("  ")
		b.WriteString(v)
		b.WriteString("\n")
	}
	b.WriteString("\nPut back what went. A field may be renamed if its `json_name` stays what it was, " +
		"and anything may be added -- a field, a message, a kind of a message; nothing in the lock is " +
		"otherwise taken back, and a kind is never moved to another message. A type or a kind no " +
		"deployment ever wrote a row of is taken out of the lock by hand, in a change a reviewer " +
		"sees: https://github.com/lesomnus/payday/blob/main/docs/guide/past.md#4-the-lock")

	return b.String()
}

// Admit is the lock with `next` -- the lock of what the schema declares now --
// added to it, and refuses a schema that lost or changed anything in it.
//
// Lost is a kind no message declares now -- its message gone, no longer an
// event message, or no longer holding it -- or one another message declares
// now; a message one of them holds gone; a field removed or changed in its
// JSON name, its kind, its type or whether it is a list or a map; and an enum
// value removed or renamed. Everything else is added: a kind, a message, a
// field, a value.
//
// The answer is a union and never a replacement, so that whatever the lock
// holds stays in it even where nothing here refuses its absence.
func (l *Lock) Admit(next *Lock) (*Lock, error) {
	var lost []string

	// The event messages, whose going their kinds' lines say.
	events := map[string]bool{}
	for kind, name := range l.Kinds {
		events[name] = true

		now, ok := next.Kinds[kind]
		switch {
		case !ok:
			lost = append(lost, fmt.Sprintf("kind %q: its rows hold %s, and no message declares it now", kind, name))
		case now != name:
			lost = append(lost, fmt.Sprintf("kind %q: its rows hold %s, and %s declares it now", kind, name, now))
		}
	}

	for name, was := range l.Messages {
		now, ok := next.Messages[name]
		if !ok {
			if !events[name] {
				lost = append(lost, fmt.Sprintf("%s: was held by an event message, and none holds it now", name))
			}
			continue
		}

		for n, f := range was {
			at := fmt.Sprintf("%s (%d)", name, n)
			g, ok := now[n]
			switch {
			case !ok:
				lost = append(lost, fmt.Sprintf("%s: was %q, and is gone", at, f.JSON))
				continue
			case g.JSON != f.JSON:
				lost = append(lost, fmt.Sprintf("%s: was written as %q, and is %q now", at, f.JSON, g.JSON))
			}
			if g.Kind != f.Kind || g.Type != f.Type {
				lost = append(lost, fmt.Sprintf("%s: %q held %s, and holds %s now", at, f.JSON, f.holds(), g.holds()))
			}
			if g.List != f.List || g.Map != f.Map {
				lost = append(lost, fmt.Sprintf("%s: %q was %s, and is %s now", at, f.JSON, f.shape(), g.shape()))
			}
		}
	}

	for name, was := range l.Enums {
		now, ok := next.Enums[name]
		if !ok {
			lost = append(lost, fmt.Sprintf("%s: was held by an event message, and none holds it now", name))
			continue
		}

		for n, v := range was {
			w, ok := now[n]
			switch {
			case !ok:
				lost = append(lost, fmt.Sprintf("%s: %d was %s, and is gone", name, n, v))
			case w != v:
				lost = append(lost, fmt.Sprintf("%s: %d was %s, and is %s now", name, n, v, w))
			}
		}
	}

	if len(lost) > 0 {
		sort.Strings(lost)
		return nil, &ErrLock{Lost: lost}
	}

	return l.union(next), nil
}

func (f LockField) holds() string {
	if f.Type != "" {
		return f.Kind + " " + f.Type
	}

	return f.Kind
}

func (f LockField) shape() string {
	switch {
	case f.Map != "":
		return "a map keyed by " + f.Map
	case f.List:
		return "a list"
	}

	return "one value"
}

func (l *Lock) union(next *Lock) *Lock {
	v := &Lock{
		Kinds:    maps.Clone(l.Kinds),
		Messages: map[string]LockMessage{},
		Enums:    map[string]LockEnum{},
	}
	if v.Kinds == nil {
		v.Kinds = map[string]string{}
	}
	for name, m := range l.Messages {
		v.Messages[name] = maps.Clone(m)
	}
	for name, e := range l.Enums {
		v.Enums[name] = maps.Clone(e)
	}

	maps.Copy(v.Kinds, next.Kinds)
	for name, m := range next.Messages {
		if v.Messages[name] == nil {
			v.Messages[name] = LockMessage{}
		}
		maps.Copy(v.Messages[name], m)
	}
	for name, e := range next.Enums {
		if v.Enums[name] == nil {
			v.Enums[name] = LockEnum{}
		}
		maps.Copy(v.Enums[name], e)
	}

	return v
}

// ErrLockOfEvents is a lock a payday from before a message could hold several
// kinds (payday#171) wrote: it records each event message's one kind, under
// `events`, where a lock now records each kind's message, under `kinds`. The
// same record the other way round, which is a hand edit to make once.
var ErrLockOfEvents = errors.New(`records each event message's kind under "events", as a payday from ` +
	`before payday#171 wrote it, and a lock now records each kind's message under "kinds": write each ` +
	`"app.Message": "kind" of "events" as "kind": "app.Message" under "kinds", and run pd gen again. A ` +
	`lock no deployment ever wrote a row under may be deleted instead, for pd gen to write from the schema`)

// ReadLock is a lock as it is committed, and an empty one for none at all.
//
// A key it does not know is refused rather than dropped. The lock is written
// back with what is new added to it, so a key dropped on the way in -- one a
// newer payday writes, or an older one did -- is a record of what rows were
// written with gone from the lock without a word, which is the one thing it
// is there to never do.
func ReadLock(b []byte) (*Lock, error) {
	v := &Lock{}
	if len(bytes.TrimSpace(b)) == 0 {
		return v, nil
	}

	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var old struct {
			Events json.RawMessage `json:"events"`
		}
		if json.Unmarshal(b, &old) == nil && old.Events != nil {
			return nil, ErrLockOfEvents
		}

		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("holds more than one JSON value")
	}

	return v, nil
}

// Marshal is the lock as it is committed: indented JSON with every key in
// order -- a number in the order of numbers -- so that one schema is one file
// byte for byte, and a change to it is a diff a reviewer reads.
func (l *Lock) Marshal() []byte {
	b := &bytes.Buffer{}
	enc := json.NewEncoder(b)
	enc.SetIndent("", "  ")

	// A kind is the app's word, and `<` in one is not a thing to escape in a
	// file nobody serves as HTML.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(l); err != nil {
		// Strings, numbers and booleans, which always marshal.
		panic(err)
	}

	return b.Bytes()
}

// MarshalJSON writes the fields in the order of their numbers, which is the
// order the message declares them in more often than not, rather than in the
// order of their numbers as text: 1, 10, 2.
func (m LockMessage) MarshalJSON() ([]byte, error) {
	return marshalByNumber(m)
}

// MarshalJSON writes the values in the order of their numbers; see
// [LockMessage.MarshalJSON].
func (e LockEnum) MarshalJSON() ([]byte, error) {
	return marshalByNumber(e)
}

func marshalByNumber[V any](vs map[int32]V) ([]byte, error) {
	b := &bytes.Buffer{}
	b.WriteByte('{')
	for i, n := range slices.Sorted(maps.Keys(vs)) {
		if i > 0 {
			b.WriteByte(',')
		}

		v, err := json.Marshal(vs[n])
		if err != nil {
			return nil, err
		}

		b.WriteString(strconv.Quote(strconv.Itoa(int(n))))
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')

	return b.Bytes(), nil
}
