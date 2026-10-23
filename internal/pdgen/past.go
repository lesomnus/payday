package pdgen

import (
	"fmt"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/schema"
)

// Past is what an app declared of its past (payday#151): that payday's `Event`
// and `Fact` are among its entities, and the domain each of them takes.
//
//	option (payday.app) = {past: {event: 17, fact: 16}};
//
// It is nil for an app that declared none, which is every app until it does.
type Past struct {
	Event uint8
	Fact  uint8

	// At is the file that declared it, as the schema names it, for a refusal to
	// point at.
	At string
}

// Domain is the number the declaration gives the entity of that marker, and
// false for one that is not the past's.
func (p *Past) Domain(own pdpb.Own) (uint8, bool) {
	switch {
	case p == nil:
		return 0, false
	case own == pdpb.Own_OWN_EVENT:
		return p.Event, true
	case own == pdpb.Own_OWN_FACT:
		return p.Fact, true
	}

	return 0, false
}

// The domains an app's own entities hold. payday numbers what every app has up
// from 1, and what it added once apps were numbering theirs up from 7 down from
// 255 -- so an app's are the ones between, and the two ends are kept for
// payday.
const (
	AppDomainMin = 7
	AppDomainMax = 247
)

// CheckPastDomains refuses two numbers a past cannot be declared with.
//
// It is the one rule, and two readers ask it: `pd gen`, before anything is
// compiled -- the copies it writes then carry the numbers, so a wrong one has to
// be refused before it is written into a file -- and [Read], of the schema as
// compiled, which is what a generation that did not go through `pd gen` has.
//
// Whether another entity of the app's holds one of them is not here: only the
// whole schema can say, and [Read] says it.
func CheckPastDomains(event, fact uint32) error {
	for _, v := range []struct {
		name string
		n    uint32
	}{{"event", event}, {"fact", fact}} {
		switch n := v.n; {
		case n == 0:
			return fmt.Errorf(
				"past: says no `%s:`, and an entity's domain is what every identifier of it carries; "+
					"say both, each in %d..%d -- `past: {event: 17, fact: 16}`",
				v.name, AppDomainMin, AppDomainMax)

		case n > 255:
			return fmt.Errorf("past: %s: %d does not fit in the one byte an identifier carries; it must be %d..%d",
				v.name, n, AppDomainMin, AppDomainMax)

		case n < AppDomainMin || n > AppDomainMax:
			return fmt.Errorf(
				"past: %s: %d is payday's: it numbers what every app has from 1 up and from 255 down, "+
					"and keeps 1..%d and %d..255 for that. An app's past is numbered like the app's "+
					"own entities, %d..%d",
				v.name, n, AppDomainMin-1, AppDomainMax+1, AppDomainMin, AppDomainMax)
		}
	}
	if event == fact {
		return fmt.Errorf(
			"past: says %d for both `event:` and `fact:`, and a domain is one entity's: an identifier "+
				"that could be either is one that says nothing", event)
	}

	return nil
}

// readPast is the past the schema's own files declare, and nil when none does.
//
// Read off the compiled option rather than taken from `pd gen`, so that what a
// generation is held to is the schema it is handed: a generation buf ran by
// itself, or the copies left behind by a declaration that has since been taken
// out, are what [Schema.checkPast] is for.
func readPast(files []*protogen.File) (*Past, error) {
	var v *Past
	for _, f := range files {
		if !f.Generate {
			continue
		}

		a, _ := proto.GetExtension(f.Desc.Options(), pdpb.E_App).(*pdpb.App)
		if !a.HasPast() {
			continue
		}
		if v != nil {
			// One answer, in one place. Two that agree today are two lines to
			// keep agreeing, and the copies can only be numbered by one of them.
			return nil, fmt.Errorf(
				"%s and %s both declare a past; it is said once, in the file that says the package is the app's",
				v.At, f.Desc.Path())
		}

		p := a.GetPast()
		if err := CheckPastDomains(p.GetEvent(), p.GetFact()); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Desc.Path(), err)
		}

		v = &Past{Event: uint8(p.GetEvent()), Fact: uint8(p.GetFact()), At: f.Desc.Path()}
	}

	return v, nil
}

// checkPast refuses a past that is not what the schema declared: a copy of
// payday's Event or Fact with no declaration behind it, one at a number other
// than the one declared, and a declared number another entity holds.
//
// `pd gen` writes the copies from the declaration and takes them out when it
// goes, so none of these comes out of a generation it ran. They come out of the
// others -- buf run by hand over a tree whose declaration moved, a copy edited
// -- and each is a table numbered by nothing the schema says: identifiers whose
// domain the declaration does not stand behind, or two kinds of row under one
// number, which is an old identifier starting to lie about what it named.
//
// It runs before the rest of the checks, so that a number the past and another
// entity both hold is said as the declaration's rather than as two entities'.
func (s *Schema) checkPast() error {
	opted, err := schema.Opted()
	if err != nil {
		return err
	}

	for _, v := range s.Entities {
		if !opted[v.Own] {
			continue
		}

		want, ok := s.Past.Domain(v.Own)
		switch {
		case s.Past == nil:
			return fmt.Errorf(
				"%s is payday's %s, which is copied into an app that declares a past, and this one "+
					"declares none. `pd gen` takes the copy out when the declaration goes; declare it "+
					"again if it is still wanted:\n\n"+
					"    option (payday.app) = {past: {event: %d, fact: %d}};",
				v.FullName(), v.Own, s.copied(pdpb.Own_OWN_EVENT), s.copied(pdpb.Own_OWN_FACT))

		case !ok:
			// An entity payday added to the past and this reader did not learn.
			return fmt.Errorf("%s is payday's %s, and a past declares no number for it", v.FullName(), v.Own)

		case v.Domain != want:
			return fmt.Errorf(
				"%s says domain %d, and %s declares %d for it. The copy is numbered from the "+
					"declaration by `pd gen`, so one that says otherwise was not written by it: "+
					"run `pd gen`, which writes it again",
				v.FullName(), v.Domain, s.Past.At, want)
		}
	}
	if s.Past == nil {
		return nil
	}

	for _, v := range s.Entities {
		if opted[v.Own] {
			continue
		}

		for _, w := range []struct {
			name string
			n    uint8
		}{{"Event", s.Past.Event}, {"Fact", s.Past.Fact}} {
			if v.Domain != w.n {
				continue
			}

			return fmt.Errorf(
				"%s declares domain %d, and %s gives it to the past's %s; a domain is one "+
					"entity's, so an old identifier would start lying about what it named. "+
					"Give the past a number nothing holds",
				v.FullName(), w.n, s.Past.At, w.name)
		}
	}

	return nil
}

// checkPointer refuses `(payday.field) = {event: true}` on a column that cannot
// name an event: anything but a `bytes` column of `TYPE_UUID` holding one
// identifier, and the key.
//
// What reads it is the past's expiry, which asks of each event it would take
// whether a row that stays names it: `NOT EXISTS (... WHERE col = event.id)`.
// A list holds its identifiers as a document no such comparison finds, an edge
// names a row of the entity it is to, and the key names the row itself -- so on
// any of them the declaration would keep nothing, and say it did.
func checkPointer(e graph.Entity, prop graph.Prop) error {
	if _, ok := prop.(graph.Edge); ok {
		return fmt.Errorf(
			"%s: event: true on an edge, which names a row of the entity it is to; an event is named "+
				"by its identifier, in a column of its own:\n\n"+
				"    bytes event_id = %d [(orm.field) = {type: TYPE_UUID}, (payday.field) = {event: true}];",
			prop.FullName(), prop.Number())
	}

	switch {
	case prop.Number() == e.Key().Number():
		return fmt.Errorf("%s: event: true on the key, which names this row and no event", prop.FullName())

	case prop.Descriptor().Kind() != protoreflect.BytesKind || prop.Type() != ormpb.Type_TYPE_UUID:
		return fmt.Errorf(
			"%s: event: true on a column of %s; an event is named by its identifier, a `bytes` column of "+
				"`TYPE_UUID`", prop.FullName(), prop.Type())

	case prop.IsList():
		return fmt.Errorf(
			"%s: event: true on a list, whose identifiers are kept as one document that no comparison "+
				"of a column finds; one row naming many events is a row of each, in an entity of its own",
			prop.FullName())
	}

	return nil
}

// checkPointers refuses an event pointer the past cannot follow: one in an app
// that declares no past, and one on payday's Event other than its cause.
//
// The second is a rule about the expiry. An event an event names stays while
// that one does, which is a chain: a child keeps its operation's event, and
// that one its own cause. The expiry follows the chain by `cause_id`, which it
// reads off every event it takes, and would not see a link made by another
// column -- an event the past kept one pass and took the next, with nothing
// having changed.
func (s *Schema) checkPointers() error {
	for _, v := range s.Entities {
		for _, p := range v.Pointers {
			switch {
			case s.Past == nil:
				return fmt.Errorf(
					"%s.%s: event: true names an event of the app's past, and this app declares none; "+
						"declare one, or say nothing:\n\n"+
						"    option (payday.app) = {past: {event: %d, fact: %d}};",
					v.FullName(), p, s.copied(pdpb.Own_OWN_EVENT), s.copied(pdpb.Own_OWN_FACT))

			case v.Own == pdpb.Own_OWN_EVENT && p != "cause_id":
				return fmt.Errorf(
					"%s.%s: event: true on payday's Event, whose one is its cause: an event it names stays "+
						"while it does, and the past follows that chain by `cause_id` alone",
					v.FullName(), p)
			}
		}
	}

	return nil
}

// copied is the domain the copy of the past's entity of that marker holds, for
// a refusal that writes the declaration back out: what it said is the number
// its rows were minted under, and the one to say again. The first number from
// the app's that nothing holds, for one that is not there.
func (s *Schema) copied(own pdpb.Own) int {
	for _, v := range s.Entities {
		if v.Own == own {
			return int(v.Domain)
		}
	}

	return s.free()
}
