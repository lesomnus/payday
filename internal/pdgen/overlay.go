package pdgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/schema"
)

// CheckOwn refuses a schema that is missing an entity payday ships.
//
// # Why it is a refusal and not a skip
//
// Three emitters are built out of payday's own entities -- [EmitGate] from the
// tenant and the holder, [EmitAudit] from the trail, [EmitOutbox] from the
// queue -- and each of them returned quietly when it did not find one.
//
// Quietly is the problem. What EmitGate writes is the whole of the `Add` tenant
// check: the wall is a predicate and an insert has no query, so with the layer
// gone **reads stay walled and writes stop being**. Nothing else changes. It
// compiles, it links, it serves, and the first signal is a row planted in
// somebody else's tenant.
//
// They cannot go missing by accident, since `pd gen` copies these four files
// into the app whole every time. So an absence means something took one out,
// and stopping is the only answer that does not hand back a smaller app which
// looks whole.
//
// # It is the marker that is looked for, not the name
//
// These used to be found by full name -- `payday.Tenant` and the rest -- which
// made the proto package payday's rather than the app's, and made renaming it
// silently destructive in exactly the way above. Each entity now carries
// `own:`, so an app may put them in its own package and this goes on finding
// them. See [Schema.Own].
//
// # Why the plugin calls it rather than [Read]
//
// Because `pdgen` is a library and its own tests build partial schemas on
// purpose -- a tenant called `test.Tenant` is a perfectly good schema to ask
// questions of, and is not an app. What makes the invariant true is `pd gen`
// having just copied the files in, so it is asserted where that is known.
//
// # And the past, when there is one
//
// payday's Event and Fact are copied only into an app that declares a past
// ([Schema.Past]), so what is required is what payday ships less those two,
// and the two as well when the schema declares them. An app that does not
// declare one has neither, and that is not something gone.
func CheckOwn(s *Schema) error {
	owned, err := schema.Owned()
	if err != nil {
		return err
	}
	features, err := schema.Features()
	if err != nil {
		return err
	}

	gone := []string{}
	past := []string{}
	notifications := []string{}
	for own := range owned {
		switch {
		case s.Has(own):
		case features[own] == "":
			gone = append(gone, own.String())
		case features[own] == schema.DirPast && s.Past != nil:
			past = append(past, own.String())
		case features[own] == schema.DirNotify && s.Notify != nil:
			notifications = append(notifications, own.String())
		}
	}
	sort.Strings(notifications)
	if len(gone) == 0 && len(notifications) > 0 {
		return fmt.Errorf("%s: %s declares notifications, and nothing in this schema carries the marker; run `pd gen` to restore the notification records",
			strings.Join(notifications, ", "), s.Notify.At)
	}

	sort.Strings(past)

	if len(gone) == 0 && len(past) > 0 {
		// Said apart, because what took them out is not what takes the others:
		// the declaration is what puts them in.
		return fmt.Errorf(
			"%s: %s declares a past, and nothing in this schema carries the marker. `pd gen` "+
				"writes payday's Event and Fact into the app on every run while the declaration "+
				"is there, so something removed a copy or its marker, or this was generated "+
				"without `pd gen`",
			strings.Join(past, ", "), s.Past.At)
	}
	if len(gone) == 0 {
		return nil
	}
	gone = append(gone, past...)
	gone = append(gone, notifications...)
	sort.Strings(gone)

	return fmt.Errorf(
		"%s: nothing in this schema declares it, and payday copies its own entities in "+
			"whole on every `pd gen`\n\n"+
			"What is built out of them is the Gate layer -- which is the whole of the `Add` "+
			"tenant check -- the audit trail's layer, the outbox drain and the access record. Missing, those are "+
			"not generated and nothing else fails: reads stay walled and writes stop being.\n\n"+
			"They are found by the `own:` each carries and not by name, so renaming the proto "+
			"package is not what did this. Something removed the entity or the marker.",
		strings.Join(gone, ", "))
}

// CheckOverlay refuses an entity of payday's that an app changed rather than
// added to.
//
// Merging an overlay takes the overlay's word for a number that is already
// there, and says nothing about it. So `string alias = 4` becomes `int64 alias
// = 4` and the app still compiles: the wall goes on reading a tenant and `auth`
// goes on looking a holder up, both against a column that is no longer what
// they were written for. It is the shape of mistake this design is arranged to
// catch, and until now it was a sentence in a document.
//
// What is compared is what payday shipped against what is about to be
// generated, by number: the name it was given, the kind it holds, and for a
// message field what it points at. An app is free to do anything at a number
// payday never used.
//
// # And the options, which are the declaration
//
// The merge keeps payday's value for an option both sides set, and fills in
// what only the overlay set. So the one way left to change payday's entity is
// to set what payday left unset -- `global: {}` on a Holder that says nothing
// about tenancy because saying nothing **is** its declaration -- and that is
// refused the same way a number is. What an overlay may add is an index: a key
// over the fields it added is what those fields are often for, and an index
// narrows what may be written without changing what anything reads.
//
// # The past's two
//
// payday's Event and Fact are held to their templates the same way, with one
// difference: what payday ships says `domain: 0` and the copy says the app's
// number, which `pd gen` wrote there. So the template is compared as though it
// said the copy's number, and whether that is the number the app declared is
// [Schema.checkPast]'s to say. And what an overlay may add to them is a field
// at any number the template leaves empty -- 6 and 17 of an Event, 11 of a Fact
// -- since their 8..12 are payday's.
//
// # And the two an app may number
//
// payday's Archived and Access are compared the same way, as though what ships
// said the copy's number: `pd gen` writes the app's there when it declares one
// (payday#163), and whether the copy's is the declared one, or payday's where
// nothing is declared, is [Schema.checkDomains]'s to say. Only the domain:
// every other entity of payday's is held to the number it ships with here, and
// anything else in these two's declaration is payday's as it is in every
// other's.
func CheckOverlay(s *Schema) error {
	owned, err := schema.Owned()
	if err != nil {
		return err
	}
	declared, err := schema.OwnedOptions()
	if err != nil {
		return err
	}
	opted, err := schema.Opted()
	if err != nil {
		return err
	}

	errs := []string{}
	for _, v := range s.Entities {
		want, ok := owned[v.Own]
		if !ok {
			// Not one of payday's, so all of it is the app's. Read off the
			// marker rather than the name: keyed on the name, a renamed proto
			// package matched nothing here and this check quietly stopped
			// running -- so `string alias = 4` could become `int64 alias = 4`
			// and nothing said so.
			continue
		}

		got := fieldsOf(v.Descriptor())
		for n, w := range want {
			if n == 2 && s.Notify != nil && s.Notify.Global && (v.Own == pdpb.Own_OWN_NOTIFICATION || v.Own == pdpb.Own_OWN_NOTIFICATION_RECEIPT) {
				continue
			}
			g, ok := got[n]
			switch {
			case !ok && opted[v.Own]:
				errs = append(errs, fmt.Sprintf(
					"%s: %d is payday's %q and is gone; the optional feature is written and read by it",
					v.FullName(), n, w.Name))
			case !ok:
				errs = append(errs, fmt.Sprintf(
					"%s: %d is payday's %q and is gone; the wall and `auth` read these",
					v.FullName(), n, w.Name))
			case g != w:
				errs = append(errs, fmt.Sprintf(
					"%s: %d is payday's %q (%s) and was redeclared as %q (%s)",
					v.FullName(), n, w.Name, describe(w), g.Name, describe(g)))
			}
		}

		d := declared[v.Own]
		e, _ := proto.GetExtension(v.Written, pdpb.E_Entity).(*pdpb.Entity)
		shipped := d.Entity
		if opted[v.Own] || numbered(v.Own) {
			shipped = proto.Clone(shipped).(*pdpb.Entity)
			shipped.SetDomain(e.GetDomain())
		}
		if s.Notify != nil && s.Notify.Global && (v.Own == pdpb.Own_OWN_NOTIFICATION || v.Own == pdpb.Own_OWN_NOTIFICATION_RECEIPT) {
			shipped = proto.Clone(shipped).(*pdpb.Entity)
			shipped.SetGlobal(&pdpb.Entity_Global{})
		}
		for _, p := range added(e.ProtoReflect(), shipped.ProtoReflect(), "") {
			errs = append(errs, fmt.Sprintf(
				"%s: (payday.entity) %s is not payday's; an overlay may not add to payday's declaration",
				v.FullName(), p))
		}

		m, _ := proto.GetExtension(v.Written, ormpb.E_Message).(*ormpb.MessageOptions)
		for _, p := range added(withoutIndexes(m), withoutIndexes(d.Message), "") {
			errs = append(errs, fmt.Sprintf(
				"%s: (orm.message) %s is not payday's; an overlay may add an index there and nothing else",
				v.FullName(), p))
		}
	}
	if len(errs) == 0 {
		return nil
	}

	sort.Strings(errs)
	return fmt.Errorf(
		"an overlay may add to one of payday's entities and may not change it.\n"+
			"payday keeps 1, 2, 4..7 and 13..15; 3 is the app's set edge, and an app's own go in 8..12 and from 16.\n"+
			"Of its optional records, payday keeps every number they declare, and an app's own go at any other.\n"+
			"Of its options, an overlay may add an index to (orm.message), and nothing else.\n\n  %s",
		strings.Join(errs, "\n  "))
}

// added answers with what `got` sets that `want` does not, by path: a field
// `want` leaves unset, a value it sets to something else, and an entry of a
// list or a map it does not hold.
//
// One direction only, and on purpose. The merge cannot take anything out of
// payday's options, so what `want` sets and `got` does not is a schema written
// by hand -- a test's, nearly always -- and not an overlay.
func added(got, want protoreflect.Message, at string) []string {
	out := []string{}
	got.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		p := at + string(f.Name())
		if !want.Has(f) {
			out = append(out, p)
			return true
		}

		w := want.Get(f)
		switch {
		case f.IsList():
			for i := range v.List().Len() {
				if !holds(f, w.List(), v.List().Get(i)) {
					out = append(out, fmt.Sprintf("%s[%d]", p, i))
				}
			}
		case f.IsMap():
			v.Map().Range(func(k protoreflect.MapKey, e protoreflect.Value) bool {
				if !w.Map().Has(k) || !same(f.MapValue(), w.Map().Get(k), e) {
					out = append(out, fmt.Sprintf("%s[%v]", p, k.Interface()))
				}
				return true
			})
		case f.Message() != nil:
			out = append(out, added(v.Message(), w.Message(), p+".")...)
		case !v.Equal(w):
			out = append(out, p)
		}
		return true
	})

	return out
}

// holds reports whether list `l` of field `f` has an entry equal to `v`.
func holds(f protoreflect.FieldDescriptor, l protoreflect.List, v protoreflect.Value) bool {
	for i := range l.Len() {
		if same(f, l.Get(i), v) {
			return true
		}
	}

	return false
}

func same(f protoreflect.FieldDescriptor, a, b protoreflect.Value) bool {
	if f.Message() != nil {
		return proto.Equal(a.Message().Interface(), b.Message().Interface())
	}

	return a.Equal(b)
}

// withoutIndexes is `(orm.message)` with what an overlay may add to it taken
// out, so that what is left is compared whole.
func withoutIndexes(v *ormpb.MessageOptions) protoreflect.Message {
	if v == nil {
		return (*ormpb.MessageOptions)(nil).ProtoReflect()
	}

	v = proto.Clone(v).(*ormpb.MessageOptions)
	v.SetIndexes(nil)

	return v.ProtoReflect()
}

// fieldsOf is what an entity declares, in the shape [schema.Owned] answers in.
//
// Message-typed fields are named relative to the package this entity is in, for
// the reason [schema.Relative] holds: an app may put payday's entities in its
// own proto package, and then every one of these points at a message whose full
// name is different and whose identity is not.
func fieldsOf(d protoreflect.MessageDescriptor) map[int32]schema.Field {
	pkg := string(d.ParentFile().Package())

	vs := map[int32]schema.Field{}
	for i := range d.Fields().Len() {
		f := d.Fields().Get(i)
		v := schema.Field{Name: string(f.Name()), Kind: f.Kind()}
		if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
			v.Message = schema.Relative(string(f.Message().FullName()), pkg)
		}
		vs[int32(f.Number())] = v
	}

	return vs
}

func describe(f schema.Field) string {
	if f.Message != "" {
		return f.Message
	}

	return f.Kind.String()
}
