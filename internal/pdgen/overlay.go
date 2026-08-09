package pdgen

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

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
func CheckOwn(s *Schema) error {
	owned, err := schema.Owned()
	if err != nil {
		return err
	}

	// From what payday ships rather than a list written here, so an entity
	// added to payday's schema is covered by having been added.
	gone := []string{}
	for own := range owned {
		if !s.Has(own) {
			gone = append(gone, own.String())
		}
	}
	if len(gone) == 0 {
		return nil
	}
	sort.Strings(gone)

	return fmt.Errorf(
		"%s: nothing in this schema declares it, and payday copies its own entities in "+
			"whole on every `pd gen`\n\n"+
			"What is built out of them is the Gate layer -- which is the whole of the `Add` "+
			"tenant check -- the audit trail's layer, and the outbox drain. Missing, those are "+
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
func CheckOverlay(s *Schema) error {
	owned, err := schema.Owned()
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
			g, ok := got[n]
			switch {
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
	}
	if len(errs) == 0 {
		return nil
	}

	sort.Strings(errs)
	return fmt.Errorf(
		"an overlay may add to one of payday's entities and may not change it.\n"+
			"payday keeps 1..7 and 13..15; an app's own go in 8..12 and from 16.\n\n  %s",
		strings.Join(errs, "\n  "))
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
