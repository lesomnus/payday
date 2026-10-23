package pdcli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
)

// What an app that keeps a past (payday#151) can get wrong and still generate.
//
// `pd gen` refuses what it can tell is wrong: a pointer on a column that cannot
// name an event, a person declared in an event message, a lock the schema lost
// something of. What is left is what it cannot tell, because it is a question
// of what a thing means rather than of what it is -- whether an identifier
// names an event, whether a string is somebody's email -- and the two checks
// here ask it, by a name that says so, as personal.go does of an entity.
//
// And the lock, which `pd gen` holds the schema to as it writes it: asked here
// without writing, so that a lock the schema has moved away from is said before
// a generation refuses it, and one behind the schema before CI says so.
func doctorPast(l Layout, s *pdgen.Schema) []Finding {
	if s.Past == nil {
		return nil
	}

	vs := doctorPointers(s)
	vs = append(vs, doctorEventPeople(s)...)
	vs = append(vs, doctorLock(l, s)...)

	return vs
}

// A column named for an event that does not say it names one.
//
// The past's expiry takes an event once no row that stays names it, and the
// columns it asks are the ones that say `(payday.field) = {event: true}`. One
// that does not say it is not asked, whatever it is called: the event goes with
// its window while the row goes on naming it -- a time row that says which
// operation wrote it, pointing at nothing, and nothing failing to say so.
//
// Whether a column names an event is the schema's to say, and the declaration
// exists because nothing else can say it: an identifier is an identifier, and
// an expiry that guessed from a name would take an event a column it did not
// guess still names. Asking is not guessing. What is asked about is a column
// the declaration would be taken on -- `bytes`, `TYPE_UUID`, one to a row, not
// the key -- named `event_id` or `…_event_id`, and not named for an entity of
// the app's own: `calendar_event_id` beside a `CalendarEvent` names a row of
// that.
//
// Not of payday's Event, whose one pointer is its cause: `pd gen` refuses the
// declaration on any other column of it, and advice to write what is refused
// is no advice.
//
// Advice and not a refusal, as `personal` is: generation does what it always
// did, and what a column names stays the schema's to say.
func doctorPointers(s *pdgen.Schema) []Finding {
	// The app's entities by the name a column named for one of them carries.
	named := map[string]bool{}
	for _, e := range s.Entities {
		if e.Own != pdpb.Own_OWN_EVENT {
			named[snake(e.GoName())] = true
		}
	}

	var vs []Finding
	for _, e := range s.Sorted() {
		if e.Own == pdpb.Own_OWN_EVENT {
			continue
		}

		var asked []graph.Prop
		for p := range e.Props() {
			if slices.Contains(e.Pointers, p.Name()) || !namedForAnEvent(p.Name(), named) {
				continue
			}
			if _, ok := p.(graph.Edge); ok {
				continue
			}
			if p.Number() == e.Key().Number() || p.IsList() ||
				p.Descriptor().Kind() != protoreflect.BytesKind || p.Type() != ormpb.Type_TYPE_UUID {
				continue
			}

			asked = append(asked, p)
		}
		if len(asked) == 0 {
			continue
		}

		names := make([]string, len(asked))
		for i, p := range asked {
			names[i] = p.Name()
		}

		fix := &strings.Builder{}
		fix.WriteString("A column that names an event says so, and the past's expiry keeps the event while a row that " +
			"stays names it. Say it beside what the column says already:\n\n")
		fix.WriteString("    [(orm.field) = {...}, (payday.field) = {event: true}]\n\n")
		fix.WriteString("And give it an index that begins with it, which the expiry asks of every event it would take. " +
			"A column that names something else is better called after what it names.\n")
		for _, p := range asked {
			fmt.Fprintf(fix, "\n%s  %s", where(p), p.Name())
		}

		vs = append(vs, Finding{
			What: fmt.Sprintf("%s says nothing of %s, which %s named for an event: the past's expiry asks only a column "+
				"that says it names one, and takes the event while the row goes on naming it",
				e.FullName(), strings.Join(names, ", "), are(len(names))),
			Fix: fix.String(),
		})
	}

	return vs
}

// namedForAnEvent is whether a column's name says it holds an event's
// identifier, and not that of a row of the app's own entities, `named`, by the
// name a column named for one would carry.
func namedForAnEvent(name string, named map[string]bool) bool {
	if name != "event_id" && !strings.HasSuffix(name, "_event_id") {
		return false
	}

	return !named[strings.TrimSuffix(name, "_id")]
}

// A field of an event message named for something about a person.
//
// An event names people by identifier and holds nothing of theirs: it is kept
// for as long as the tenant keeps its history, and neither the trail, the
// access record nor an erasure looks inside one (payday#151). `pd gen` refuses
// an event message that declares a field `personal`, and that is the case that
// is said; what it cannot refuse is one that says nothing -- a field called
// `email` is somebody's email whether or not it is declared, and nothing in an
// event message can be.
//
// So a field is asked about by its name, with personal.go's words: of the
// event message, and of every message it holds, since a row keeps all of it.
// There is no `personal: false` to answer with, because no `(payday.field)` is
// taken in an event message. A field about no one -- a machine's network
// address -- is called something that does not say otherwise; one whose rows
// have been written keeps the JSON name they were written with.
func doctorEventPeople(s *pdgen.Schema) []Finding {
	// A message two event messages hold is asked about once, under the first.
	seen := map[protoreflect.FullName]bool{}

	var vs []Finding
	for _, ev := range s.Events {
		var asked []protoreflect.FieldDescriptor
		var walk func(m protoreflect.MessageDescriptor)
		walk = func(m protoreflect.MessageDescriptor) {
			if seen[m.FullName()] {
				return
			}
			seen[m.FullName()] = true

			fs := m.Fields()
			for i := range fs.Len() {
				f := fs.Get(i)
				if namedForSomebody(string(f.Name())) {
					asked = append(asked, f)
				}

				held := f
				if f.IsMap() {
					held = f.MapValue()
				}
				if held.Kind() == protoreflect.MessageKind || held.Kind() == protoreflect.GroupKind {
					// Protobuf's own types are protobuf's, and have no field of
					// anybody's in them.
					if t := held.Message(); !strings.HasPrefix(t.ParentFile().Path(), "google/protobuf/") {
						walk(t)
					}
				}
			}
		}
		walk(ev.Message.Desc)
		if len(asked) == 0 {
			continue
		}

		names := make([]string, len(asked))
		for i, f := range asked {
			names[i] = heldName(ev.FullName(), f)
		}

		fix := &strings.Builder{}
		fix.WriteString("An event is kept as long as the tenant keeps its history, and nothing that keeps a person's data " +
			"in step -- the trail, the access record, an erasure -- looks inside one. Name whoever it is about by " +
			"identifier, and a sentence names them as they are called when it is read:\n\n")
		fix.WriteString("    bytes holder = 2;   // their identifier, and nothing of theirs\n\n")
		fix.WriteString("A field about no one is called something that does not say otherwise. One whose rows were written " +
			"keeps the JSON name they were written with -- `[json_name = \"...\"]` -- which is what the lock holds.\n")
		for _, f := range asked {
			fmt.Fprintf(fix, "\n%s  %s", whereField(f), heldName(ev.FullName(), f))
		}

		vs = append(vs, Finding{
			What: fmt.Sprintf("event message %s holds %s, which %s named for something about a person, and an event "+
				"names people by identifier only", ev.FullName(), strings.Join(names, ", "), are(len(names))),
			Fix: fix.String(),
		})
	}

	return vs
}

// heldName is a field as a reader of the event message finds it: by its name
// when it is the event message's own, and by its full name when it is a field
// of a message it holds.
func heldName(ev protoreflect.FullName, f protoreflect.FieldDescriptor) string {
	if f.ContainingMessage().FullName() == ev {
		return string(f.Name())
	}

	return string(f.FullName())
}

// The lock, held to the schema without writing it.
//
// `pd gen` holds the event messages to `past.lock` and writes it back with
// what is new added ([run.lock]). Asked here, the same comparison says two
// things before a generation does, and changes nothing:
//
//   - **A schema the lock refuses.** It lost or changed what rows of an event
//     were written with, and `pd gen` stops on it -- in the words it uses.
//   - **A lock behind the schema**, missing what it declares now: a lock not
//     yet written, or one a change to the schema was committed without. Nothing
//     refuses a change to what it lacks until `pd gen` adds it, and `pd gen
//     --check` fails on it until it is committed.
//
// A missing lock is the second when the schema declares event messages, and
// nothing when it declares none: there is nothing yet for it to hold.
func doctorLock(l Layout, s *pdgen.Schema) []Finding {
	at := l.Path(l.DirPd(), LockFile)
	name := l.rel(at)

	gen := "go tool pd gen"
	if r := l.Rel(); r != "." {
		gen += " " + r
	}

	b, err := os.ReadFile(at)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if len(s.Events) == 0 {
			return nil
		}

		return []Finding{{
			What: fmt.Sprintf("%s is not there, and this schema declares %d event %s: nothing holds %s to what "+
				"%s rows are written with, and a field removed or renamed is not refused, until `pd gen` writes it "+
				"and it is committed", name, len(s.Events), plural(len(s.Events), "message", "messages"),
				plural(len(s.Events), "it", "them"), plural(len(s.Events), "its", "their")),
			Fix: gen,
		}}

	case err != nil:
		return []Finding{{What: fmt.Sprintf("cannot read %s: %s", name, err)}}
	}

	was, err := pdgen.ReadLock(b)
	if err != nil {
		return []Finding{{
			What:  fmt.Sprintf("pd gen would refuse this schema: %s: %s; it is JSON `pd gen` writes, and a hand edit of it has to stay that", name, err),
			Fatal: true,
		}}
	}

	now := pdgen.EventsLock(s)
	admitted, err := was.Admit(now)
	if err != nil {
		// The same words `pd gen` uses, because it is the same comparison.
		return []Finding{{
			What:  fmt.Sprintf("pd gen would refuse this schema: %s %s", name, err),
			Fatal: true,
		}}
	}
	if bytes.Equal(admitted.Marshal(), b) {
		return nil
	}

	lacks := lacking(was, now)
	if len(lacks) == 0 {
		// Everything in it, and written another way: a hand edit, which `pd
		// gen` writes back as it writes it.
		return []Finding{{
			What: fmt.Sprintf("%s is not written as `pd gen` writes it, so `pd gen --check` fails on it until "+
				"it is written again and committed", name),
			Fix: gen,
		}}
	}

	return []Finding{{
		What: fmt.Sprintf("%s does not hold all this schema declares: nothing refuses a change to what it lacks "+
			"that leaves rows unreadable, and `pd gen --check` fails on it, until `pd gen` adds it and it is "+
			"committed", name),
		Fix: gen + "\n\nwhich adds\n\n" + strings.Join(lacks, "\n"),
	}}
}

// lacking is what `now` declares and `was` does not hold, each as a line: an
// event message, a message one holds, a field of a message the lock holds, an
// enum, a value of an enum the lock holds. A message or an enum the lock does
// not hold is said once, and not field by field.
func lacking(was, now *pdgen.Lock) []string {
	var vs []string
	for _, name := range slices.Sorted(maps.Keys(now.Events)) {
		if _, ok := was.Events[name]; !ok {
			vs = append(vs, fmt.Sprintf("  %s: an event message of kind %q", name, now.Events[name]))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(now.Messages)) {
		held, ok := was.Messages[name]
		if !ok {
			if _, event := now.Events[name]; !event {
				vs = append(vs, fmt.Sprintf("  %s: a message an event message holds", name))
			}
			continue
		}

		m := now.Messages[name]
		for _, n := range slices.Sorted(maps.Keys(m)) {
			if _, ok := held[n]; !ok {
				vs = append(vs, fmt.Sprintf("  %s (%d): %q", name, n, m[n].JSON))
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(now.Enums)) {
		held, ok := was.Enums[name]
		if !ok {
			vs = append(vs, fmt.Sprintf("  %s: an enum an event message holds", name))
			continue
		}

		e := now.Enums[name]
		for _, n := range slices.Sorted(maps.Keys(e)) {
			if _, ok := held[n]; !ok {
				vs = append(vs, fmt.Sprintf("  %s: %d, %s", name, n, e[n]))
			}
		}
	}

	return vs
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}

	return many
}
