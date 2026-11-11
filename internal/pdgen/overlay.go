package pdgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

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
	entitlements := []string{}
	for own := range owned {
		switch {
		case s.Has(own):
		case features[own] == "":
			gone = append(gone, own.String())
		case features[own] == schema.DirPast && s.Past != nil:
			past = append(past, own.String())
		case features[own] == schema.DirNotify && s.Notify != nil:
			notifications = append(notifications, own.String())
		case features[own] == schema.DirEntitle && s.Entitlements != nil:
			entitlements = append(entitlements, own.String())
		}
	}
	sort.Strings(notifications)
	if len(gone) == 0 && len(notifications) > 0 {
		return fmt.Errorf("%s: %s declares notifications, and nothing in this schema carries the marker; run `pd gen` to restore the notification records",
			strings.Join(notifications, ", "), s.Notify.At)
	}
	if len(gone) == 0 && len(entitlements) > 0 {
		// A copy nothing reads is a tenant entitled to nothing, which is the
		// quiet answer: every feature refused, every limit none.
		return fmt.Errorf("%s: %s declares entitlements, and nothing in this schema carries the marker; run `pd gen` to restore the copy",
			strings.Join(entitlements, ", "), s.Entitlements.At)
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
	gone = append(gone, entitlements...)
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
// generated, by number: the name it was given, the kind it holds and whether
// it is a list, for a message field what it points at, and everything said
// beside it -- its JSON name and every option. The merge takes an overlay's
// field whole, so an option payday's says is gone when the overlay's does not
// say it: without `(orm.field) = {disabled: true}` a field payday keeps out of
// the table is a column, and without `(payday.field) = {personal: true}` what
// a holder is called goes onto the trail as a value. A field redeclared as
// payday declares it, option for option, changes nothing and passes. An app
// is free to do anything at a number payday never used.
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
// at any number the template leaves empty -- 17 of an Event, 11 of a Fact --
// since their 8..12 are payday's, and an Event's 6 is its `desc`, which is in
// no column.
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
				errs = append(errs, redeclared(v.FullName(), n, w, g))
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
		v := schema.Field{Name: string(f.Name()), Kind: f.Kind(), Cardinality: f.Cardinality(), JSONName: f.JSONName()}
		if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
			v.Message = schema.Relative(string(f.Message().FullName()), pkg)
		}
		// The descriptors a plugin is handed hold options it links, and a
		// round trip of those cannot fail; one that did would be a field that
		// says something nobody can read, which is what it is compared as.
		v.Options, _ = schema.OptionsOf(f)
		vs[int32(f.Number())] = v
	}

	return vs
}

func describe(f schema.Field) string {
	v := f.Kind.String()
	if f.Message != "" {
		v = f.Message
	}
	if f.Cardinality == protoreflect.Repeated {
		v = "repeated " + v
	}

	return v
}

// redeclared says how one of payday's fields was declared again: as another
// field, where its name or what it holds moved, and otherwise by the options
// that say something else, each named -- `(payday.field).personal`,
// `(orm.field).disabled`, `json_name` -- with what payday's say and what these
// do.
func redeclared(entity protoreflect.FullName, n int32, w, g schema.Field) string {
	if g.Name != w.Name || g.Kind != w.Kind || g.Message != w.Message || g.Cardinality != w.Cardinality {
		return fmt.Sprintf("%s: %d is payday's %q (%s) and was redeclared as %q (%s)",
			entity, n, w.Name, describe(w), g.Name, describe(g))
	}

	paths := []string{}
	json := g.JSONName != w.JSONName
	if json {
		paths = append(paths, "json_name")
	}
	paths = append(paths, optionsDiffer(w.Options, g.Options)...)

	return fmt.Sprintf("%s: %d is payday's %q (%s) and was redeclared with other options at %s: payday's says %s, and this %s",
		entity, n, w.Name, describe(w), strings.Join(paths, ", "), optionsText(w, json), optionsText(g, json))
}

// readOptions is what [schema.OptionsOf] wrote, as the options it was written
// from; none for what reads as nothing.
func readOptions(v string) protoreflect.Message {
	o := &descriptorpb.FieldOptions{}
	if err := proto.Unmarshal([]byte(v), o); err != nil {
		o.Reset()
	}

	return o.ProtoReflect()
}

// optionsDiffer is the path of each option the two say differently, either
// way round: the merge takes the overlay's field whole, so one payday's says
// and the overlay's leaves out is as gone as one the overlay changed.
func optionsDiffer(want, got string) []string {
	return pathsDiffer(readOptions(want), readOptions(got), "")
}

func pathsDiffer(a, b protoreflect.Message, at string) []string {
	set := map[protoreflect.FullName]protoreflect.FieldDescriptor{}
	collect := func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		set[fd.FullName()] = fd
		return true
	}
	a.Range(collect)
	b.Range(collect)

	fds := make([]protoreflect.FieldDescriptor, 0, len(set))
	for _, fd := range set {
		fds = append(fds, fd)
	}
	sort.Slice(fds, func(i, j int) bool { return optionName(fds[i]) < optionName(fds[j]) })

	out := []string{}
	for _, fd := range fds {
		p := at + optionName(fd)
		switch ha, hb := a.Has(fd), b.Has(fd); {
		case ha != hb:
			out = append(out, p)
		case fd.Message() != nil && !fd.IsList() && !fd.IsMap():
			out = append(out, pathsDiffer(a.Get(fd).Message(), b.Get(fd).Message(), p+".")...)
		case !a.Get(fd).Equal(b.Get(fd)):
			out = append(out, p)
		}
	}
	if string(a.GetUnknown()) != string(b.GetUnknown()) {
		out = append(out, at+"(an option this payday does not link)")
	}

	return out
}

// optionName is an option as it is written: `(payday.field)` for an
// extension, and its own name for one of protobuf's.
func optionName(fd protoreflect.FieldDescriptor) string {
	if fd.IsExtension() {
		return "(" + string(fd.FullName()) + ")"
	}

	return string(fd.Name())
}

// optionsText is a field's options as a person reads them beside it:
// `[(orm.field) = {disabled: true}]`, and `[]` for none -- with its JSON name
// where that is what differs, since every field has one.
func optionsText(f schema.Field, json bool) string {
	vs := []string{}
	if json {
		vs = append(vs, fmt.Sprintf("json_name = %q", f.JSONName))
	}
	readOptions(f.Options).Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Message() != nil && !fd.IsList() && !fd.IsMap() {
			text := strings.Join(strings.Fields(prototext.MarshalOptions{}.Format(v.Message().Interface())), " ")
			vs = append(vs, optionName(fd)+" = {"+text+"}")
			return true
		}
		vs = append(vs, fmt.Sprintf("%s = %v", optionName(fd), v))
		return true
	})
	sort.Strings(vs)

	return "[" + strings.Join(vs, ", ") + "]"
}
