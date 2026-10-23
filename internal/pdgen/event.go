package pdgen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdpb"
)

// Event is one message the app declared to be what an event of one kind holds
// -- `option (payday.event)` -- and the kind its rows are written under
// (payday#151).
//
// It is not an entity. It has no table and no identifier: a row of payday's
// Event holds one in its `value`, as the message's canonical protobuf JSON, for
// as long as the event is kept. That is what every rule [readEvents] refuses
// with is about, and what the lock -- [EventsLock] -- keeps true afterwards.
type Event struct {
	// Message is the message, as the plugin was handed it: what the Go half
	// names it by, and where the TypeScript half finds it.
	Message *protogen.Message

	// Kind is the declaration's `kind`, and the message's full name when it
	// said none.
	Kind string
}

// FullName is the event message's.
func (e *Event) FullName() protoreflect.FullName { return e.Message.Desc.FullName() }

// readEvents gathers the event messages of the app's own files, nested ones
// included, and refuses one that could not be read back for as long as an
// event is kept, or would keep what history must not.
//
// It runs before [checkFieldOptions]. An event message is not an entity, so
// that check refuses a `(payday.field)` in one too -- and tells whoever wrote
// it to say it on the entity's column instead, which for an event message is
// the wrong advice: what is wrong is that a person is in it at all.
//
// Whether the app declared a past for them to be kept in is asked afterwards,
// once the entities are read; see [Schema.checkEvents].
func readEvents(g *graph.Graph, files []*protogen.File) ([]*Event, error) {
	var vs []*Event
	byKind := map[string]*Event{}

	var walk func(ms []*protogen.Message) error
	walk = func(ms []*protogen.Message) error {
		for _, m := range ms {
			if err := walk(m.Messages); err != nil {
				return err
			}

			opts := m.Desc.Options()
			if opts == nil || !proto.HasExtension(opts, pdpb.E_Event) {
				continue
			}

			if _, ok := g.Entities[m.Desc.FullName()]; ok {
				return fmt.Errorf(
					"%s: says it is an event message, and is an entity. An event holds what happened "+
						"to a row, kept in the event's own row; an entity is a table of rows, with an "+
						"identifier other rows name it by and the people its fields hold. Declare a "+
						"message for what happened, naming the row by its identifier -- `bytes %s = 1`",
					m.Desc.FullName(), snake(string(m.Desc.Name())))
			}

			o, _ := proto.GetExtension(opts, pdpb.E_Event).(*pdpb.EventMessage)
			v := &Event{Message: m, Kind: o.GetKind()}
			if v.Kind == "" {
				v.Kind = string(m.Desc.FullName())
			}

			if u, ok := byKind[v.Kind]; ok {
				return fmt.Errorf(
					"%s and %s are both of kind %q; a row says which message its value is by its "+
						"kind, so a kind is one message's",
					u.FullName(), v.FullName(), v.Kind)
			}
			byKind[v.Kind] = v

			if err := checkEventFields(g, v, m.Desc, map[protoreflect.FullName]bool{}); err != nil {
				return err
			}

			vs = append(vs, v)
		}

		return nil
	}

	for _, f := range files {
		if !f.Generate {
			continue
		}
		if err := walk(f.Messages); err != nil {
			return nil, err
		}
	}

	sort.Slice(vs, func(i, j int) bool { return vs[i].FullName() < vs[j].FullName() })

	return vs, nil
}

// checkEventFields refuses, in `m` and in every message it holds, what an
// event message cannot hold.
//
// Transitively, because a row keeps all of it: a message one of its fields
// holds is in the event's JSON as much as the event message's own fields are,
// and is kept exactly as long.
func checkEventFields(g *graph.Graph, ev *Event, m protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) error {
	if seen[m.FullName()] {
		return nil
	}
	seen[m.FullName()] = true

	in := ""
	if m.FullName() != ev.FullName() {
		in = fmt.Sprintf(", which event message %s holds", ev.FullName())
	}

	fs := m.Fields()
	for i := range fs.Len() {
		f := fs.Get(i)

		if opts := f.Options(); opts != nil && proto.HasExtension(opts, pdpb.E_Field) {
			return fmt.Errorf(
				"%s: (payday.field) in an event message%s. An event names people by identifier only "+
					"and holds nothing of theirs: it is kept as long as the tenant keeps its history, "+
					"which is longer than what is kept of a person, and neither the trail nor the access "+
					"record nor an erasure looks inside one. So there is nothing about a person in it "+
					"to declare -- name them by identifier, and take the option off",
				f.FullName(), in)
		}

		// What a map holds is its value; its key is a scalar.
		held := f
		if f.IsMap() {
			held = f.MapValue()
		}
		if held.Kind() != protoreflect.MessageKind && held.Kind() != protoreflect.GroupKind {
			continue
		}

		t := held.Message()
		switch {
		case t.FullName() == "google.protobuf.Any":
			return fmt.Errorf(
				"%s: holds a google.protobuf.Any, in an event message%s. The event's value is the one "+
					"Any of its row already, and an Any in it holds a message nothing declared: no rule "+
					"here can be held to that message, the lock cannot keep it readable, and a page "+
					"reads it only with its type registered. Declare the message it holds",
				f.FullName(), in)

		case g.Entities[t.FullName()] != nil:
			return fmt.Errorf(
				"%s: holds %s, an entity, in an event message%s. An event names a row by its "+
					"identifier -- `bytes %s = %d` -- and one holding the row would keep every field "+
					"of it, the personal ones included, for as long as the event is kept",
				f.FullName(), t.FullName(), in, f.Name(), f.Number())

		case strings.HasSuffix(t.ParentFile().Path(), "_svc.g.proto"):
			return fmt.Errorf(
				"%s: holds %s, a message of a generated contract (%s), in an event message%s. A "+
					"contract's messages are the API's and move with the entity: a Ref carries a slug, "+
					"which is somebody's name for the row, and a request carries the entity's fields. "+
					"An event names a row by its identifier -- `bytes %s = %d`",
				f.FullName(), t.FullName(), t.ParentFile().Path(), in, f.Name(), f.Number())

		case wellKnown(t):
			// Its own JSON shape, and protobuf's to keep.
			continue
		}

		if err := checkEventFields(g, ev, t, seen); err != nil {
			return err
		}
	}

	return nil
}

// wellKnown says a type is one of protobuf's own -- a Timestamp, a Duration --
// which protobuf keeps the JSON shape of, and which neither the rules about an
// event message nor its lock look inside.
func wellKnown(d protoreflect.Descriptor) bool {
	return strings.HasPrefix(d.ParentFile().Path(), "google/protobuf/")
}

// checkEvents refuses an event message in an app that declares no past to keep
// its events in.
//
// After the entities are read, so that the declaration it writes out names two
// numbers nothing holds.
func (s *Schema) checkEvents() error {
	if len(s.Events) == 0 || s.Past != nil {
		return nil
	}

	taken := map[int]bool{}
	for _, v := range s.Entities {
		taken[int(v.Domain)] = true
	}
	free := []int{}
	for n := AppDomainMin; n <= AppDomainMax && len(free) < 2; n++ {
		if !taken[n] {
			free = append(free, n)
		}
	}
	for len(free) < 2 {
		free = append(free, 0)
	}

	return fmt.Errorf(
		"%s: says it is an event message, and this app declares no past to keep its events in. An "+
			"event is a row of payday's Event, which an app has once it says which domains its past "+
			"takes, on the file that says the package is its own:\n\n"+
			"    option (payday.app) = {past: {event: %d, fact: %d}};",
		s.Events[0].FullName(), free[0], free[1])
}

// snake is a message name as a field would be named after it: `RobotMoved` is
// `robot_moved`.
func snake(v string) string {
	b := &strings.Builder{}
	for i, c := range v {
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			c += 'a' - 'A'
		}
		b.WriteRune(c)
	}

	return b.String()
}

// EventKinds is the event messages by kind, in the order the kinds sort in,
// which is what every registry written from them is written in.
func (s *Schema) EventKinds() []*Event {
	vs := make([]*Event, len(s.Events))
	copy(vs, s.Events)
	sort.Slice(vs, func(i, j int) bool { return vs[i].Kind < vs[j].Kind })

	return vs
}

// EmitEventKinds writes the Go registry of the app's event messages, by kind:
// what a row's `value` is read back into, its `kind` naming the message
// (payday#151).
//
// Only into an app that declares a past. One that does not has no Event to
// hold them, and an empty map beside nothing would be a name to import for no
// reason.
func EmitEventKinds(g *protogen.GeneratedFile, s *Schema) {
	if s.Past == nil {
		return
	}

	t := g.QualifiedGoIdent(pkgReflect.Ident("MessageType"))

	g.P("// EventKinds is every event message this app declares, by the kind its rows")
	g.P("// are written under: the message's `option (payday.event) = {kind: ...}`, or")
	g.P("// its full name when it says none.")
	g.P("//")
	g.P("// A row of payday's Event says which message its `value` holds by its kind,")
	g.P("// so this is how a kind is read back into a message -- and the lock beside")
	g.P("// the schema, `past.lock`, is what keeps every kind in it for as long as a row")
	g.P("// of it may be.")
	g.P("var EventKinds = map[string]", t, "{")
	for _, v := range s.EventKinds() {
		g.P(fmt.Sprintf("%q", v.Kind), ": (*", g.QualifiedGoIdent(v.Message.GoIdent), ")(nil).ProtoReflect().Type(),")
	}
	g.P("}")
	g.P("")
}

// TsEvents is `events.ts`: the app's event messages as a registry, and by
// kind (payday#151).
//
// The registry is what a page hands its transport: Connect's JSON names the
// message an Any holds by its type, and protobuf-es reads one only with that
// type registered -- without it, a whole List of events fails on the first row
// that holds a value. It is written for every app, empty for one with no past,
// so that a page passes it from the day it is made rather than from the day a
// List of events first fails.
//
// `at` is where a message's TypeScript landed, which only the plugin knows.
func TsEvents(s *Schema, at func(*Event) string) string {
	b := &strings.Builder{}

	b.WriteString(`// Code generated by protoc-gen-pd. DO NOT EDIT.

/**
 * The messages this app's events hold: a registry to read them with, and each
 * by the kind its rows are written under.
 *
 * A row of payday's Event keeps its value as an Any, and Connect's JSON names
 * the message an Any holds by its type -- which protobuf-es reads only with
 * that type registered. So the page's transport is given ` + "`events`" + `:
 *
 *     createConnectTransport({ baseUrl, jsonOptions: { registry: events } })
 *
 * and a value is read with ` + "`anyUnpack(ev.value, events)`" + `, or as one kind's
 * message with ` + "`anyUnpack(ev.value, kinds['robot.move'])`" + `.
 *
 * @module
 */

import { createRegistry, type DescMessage } from '@bufbuild/protobuf'
`)

	vs := s.EventKinds()

	// One import per file, in the order the files are first named, with the
	// schemas each holds -- several event messages to a file is the usual case.
	seen := map[string][]string{}
	order := []string{}
	names := map[*Event]string{}
	for _, v := range vs {
		names[v] = tsSchemaName(v.Message.Desc)

		p := at(v)
		if _, ok := seen[p]; !ok {
			order = append(order, p)
		}
		seen[p] = append(seen[p], names[v])
	}
	if len(order) > 0 {
		b.WriteString("\n")
	}
	for _, p := range order {
		ns := seen[p]
		sort.Strings(ns)
		fmt.Fprintf(b, "import { %s } from '%s'\n", strings.Join(ns, ", "), p)
	}

	b.WriteString("\n/** Each event message, by the kind its rows are written under. */\n")
	if len(vs) == 0 {
		b.WriteString("export const kinds = {} as const satisfies Readonly<Record<string, DescMessage>>\n")
	} else {
		b.WriteString("export const kinds = {\n")
		for _, v := range vs {
			fmt.Fprintf(b, "\t%s: %s,\n", tsString(v.Kind), names[v])
		}
		b.WriteString("} as const satisfies Readonly<Record<string, DescMessage>>\n")
	}

	b.WriteString("\n/** Every event message, for a transport's `jsonOptions` and for `anyUnpack`. */\n")
	b.WriteString("export const events = createRegistry(")
	for i, v := range vs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(names[v])
	}
	b.WriteString(")\n")

	return b.String()
}

// tsSchemaName is what protobuf-es calls a message's schema: its name inside
// its package, the messages it is nested in joined to it by `_`, and `Schema`
// after it -- `Robot.Moved` is `Robot_MovedSchema`.
//
// Mirrored rather than asked, as [TsImport] is. protobuf-es adds a `$` when two
// names would come out alike, which this does not, and then the import names
// something that is not there: a page that does not compile, which is the loud
// way for it to be wrong.
func tsSchemaName(m protoreflect.MessageDescriptor) string {
	name := string(m.FullName())
	if pkg := string(m.ParentFile().Package()); pkg != "" {
		name = strings.TrimPrefix(name, pkg+".")
	}

	return strings.ReplaceAll(name, ".", "_") + "Schema"
}

// tsString is a string as a TypeScript literal. JSON's form, which is one
// ECMAScript reads the same, where Go's `%q` writes `\U` for what JavaScript
// has no such escape for.
func tsString(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		// A Go string always marshals.
		panic(err)
	}

	return string(b)
}

// TsEventSource is where an event message's TypeScript landed, for [TsEvents]:
// protoc-gen-es keeps the directory its file was in and renames the file.
func TsEventSource(v *Event) string { return TsImport(v.Message.Desc.ParentFile().Path()) }
