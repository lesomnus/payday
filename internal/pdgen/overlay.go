package pdgen

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/schema"
)

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
