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
// Three emitters look for payday's own entities **by full name** -- [EmitGate]
// wants `payday.Tenant` and `payday.Holder`, [EmitAudit] wants `payday.Audit`,
// [EmitOutbox] wants `payday.Outbox` -- and each of them returned quietly when
// it did not find one.
//
// Quietly is the problem. What EmitGate writes is the whole of the `Add` tenant
// check: the wall is a predicate and an insert has no query, so with the layer
// gone **reads stay walled and writes stop being**. Nothing else changes. It
// compiles, it links, it serves, and the first signal is a row planted in
// somebody else's tenant.
//
// The names cannot be missing by accident: `pd gen` copies these four files
// into the app whole, every time. So an absence means something took one out --
// and the likeliest something is **renaming the proto package**, which is a
// reasonable thing to want and is not supported yet. Either way, stopping is
// the only answer that does not hand back a smaller app that looks whole.
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
	for name := range owned {
		if !s.Has(name) {
			gone = append(gone, name)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	sort.Strings(gone)

	return fmt.Errorf(
		"%s: not in this schema, and payday copies these in whole on every `pd gen`\n\n"+
			"Generation finds payday's own entities by full name, and what is built from them "+
			"is the Gate layer -- which is the whole of the `Add` tenant check -- the audit "+
			"trail's layer, and the outbox drain. Missing, those are not generated and nothing "+
			"else fails: reads stay walled and writes stop being.\n\n"+
			"If the proto package was renamed, that is why. payday has no way to be told a "+
			"different name for its own entities yet.",
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
		want, ok := owned[string(v.FullName())]
		if !ok {
			// Not one of payday's, so all of it is the app's.
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

func fieldsOf(d protoreflect.MessageDescriptor) map[int32]schema.Field {
	vs := map[int32]schema.Field{}
	for i := range d.Fields().Len() {
		f := d.Fields().Get(i)
		v := schema.Field{Name: string(f.Name()), Kind: f.Kind()}
		if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
			v.Message = string(f.Message().FullName())
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
