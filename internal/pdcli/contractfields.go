package pdcli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bufbuild/protocompile/ast"
)

// checkOverlayFields refuses a service overlay that says again a field of a
// message the contract already has: a number it has, or a name it has under
// another number.
//
// # Why nothing else does
//
// An overlay may declare a message the contract declares too, and the two are
// merged: that is how an app adds a field to a generated request, at a number
// the generator left empty. Where the overlay says a field the generator
// wrote, `protobuf-merge` matches it by number or by name and takes the
// overlay's whole -- its type, its presence, its options, and under a name its
// number -- and keeps nothing of the generated one. So `int64 hours = 9
// [deprecated = true];` loses the `features.field_presence = IMPLICIT` the
// generator gave `hours`, which the overlay never mentioned, and `int32 model
// = 8;` makes the request an `int32` where the entity is a `string`. The merge
// exits 0 and prints nothing, and so did `pd gen` (payday#316).
//
// It is the rule an entity overlay is held to already, by number, and for the
// same reason: adding is allowed, and saying again what was generated is not,
// because the merge takes the overlay's word and says nothing. Merging such a
// field's options instead -- the overlay's word for what it says and the
// generator's for the rest -- was the other way, and would let an overlay put
// an option on a generated field. The owner chose this one (2026-11-27): a
// request that is to be different is a request of the app's own, under an RPC
// of its own.
//
// # What it compares against
//
// The contract **as merged so far**, as [checkOverlayRpcs] does and for its
// reason: payday's own half (`*_svc.pd.proto`, the List and the Watch) is
// held to it against what the service generator wrote, and the app's overlay
// against both, so a `FooListRequest` is the generator's to an app as much as
// a `FooAddRequest` is. Messages are matched by their path from the top of
// the file, so a message nested in a generated one is held to the same rule
// under its own name, and a field in a `oneof` is its message's field.
//
// # How the files are read
//
// As syntax, with protocompile's parser, which [keepReserved] and
// [overlaysWord] read them with too. Not as descriptors: an overlay names
// messages that exist only after the merge and is never compiled alone, and a
// contract imports the app's entities, so neither links by itself; the
// descriptors [pdgen.CheckOverlay] reads are of the merged files, where the
// generator's field is already gone. And not by a pattern: a field's options
// run over lines in a generated contract, and a pattern that reads a line
// reads half a field, and so would print half of it as the contract's.
func checkOverlayFields(b []byte, overlay string) error {
	v, err := os.ReadFile(overlay)
	if err != nil {
		return err
	}

	ov, err := parseProto(filepath.Base(overlay), v)
	if err != nil {
		return err
	}
	contract, err := parseProto("contract.proto", b)
	if err != nil {
		return err
	}

	has := map[string]*messageFields{}
	for _, m := range messageFieldsOf(contract, b) {
		has[m.path] = m
	}

	// In the overlay's order, so that of two fields said again the refusal
	// names the same one every time.
	for _, m := range messageFieldsOf(ov, v) {
		c, ok := has[m.path]
		if !ok {
			// A message of the app's own; everything in it is the app's.
			continue
		}
		for _, f := range m.fields {
			if g, ok := c.byNumber[f.number]; ok {
				return fieldSaidAgain(overlay, m.path,
					fmt.Sprintf("says field %d again, which the contract's message already has", f.number),
					g.text, f.text)
			}
			if g, ok := c.byName[f.name]; ok {
				return fieldSaidAgain(overlay, m.path,
					fmt.Sprintf("says field `%s` again at %d, which the contract's message already has at %d", f.name, f.number, g.number),
					g.text, f.text)
			}
		}
	}

	return nil
}

func fieldSaidAgain(overlay string, message string, what string, contract string, said string) error {
	return fmt.Errorf(
		"%s: message %s %s.\n\n"+
			"    the contract's: %s\n"+
			"    the overlay's:  %s\n\n"+
			"The merge would take the overlay's field whole -- its type, its presence and its "+
			"options -- and keep nothing the generator said of it: what the generator wrote, "+
			"and every layer and client written against that, would be reading another field. "+
			"Nothing would say so.\n\n"+
			"An overlay **adds**. Add what is wanted at a number %s leaves empty, under a name "+
			"it does not use; or, if a different request is wanted, give it an RPC of its own "+
			"under its own name",
		filepath.Base(overlay), message, what, contract, said, message)
}

// messageFields is what a message of a file says of its fields, a oneof's
// among them.
type messageFields struct {
	path     string
	fields   []field
	byNumber map[uint64]field
	byName   map[string]field
}

// field is one field of a message, as written: its source with the spacing
// taken out, so that one whose options run over lines reads as one line.
type field struct {
	name   string
	number uint64
	text   string
}

// messageFieldsOf is every message of f, nested ones too, by its path from the
// top of the file, in the order they are written.
func messageFieldsOf(f *ast.FileNode, src []byte) []*messageFields {
	var out []*messageFields
	var message func(prefix string, m *ast.MessageNode)
	message = func(prefix string, m *ast.MessageNode) {
		v := &messageFields{path: prefix + m.Name.Val, byNumber: map[uint64]field{}, byName: map[string]field{}}
		out = append(out, v)

		add := func(name string, number uint64, n ast.Node) {
			from, to := span(f, n)
			text := canonicalText(string(src[from:to]))
			// `[ (orm.field) = {...} ]` is how options over lines read on one.
			text = strings.ReplaceAll(strings.ReplaceAll(text, "[ ", "["), " ]", "]")
			x := field{name: name, number: number, text: text}
			v.fields = append(v.fields, x)
			if _, ok := v.byNumber[number]; !ok {
				v.byNumber[number] = x
			}
			if _, ok := v.byName[name]; !ok {
				v.byName[name] = x
			}
		}
		for _, d := range m.Decls {
			switch n := d.(type) {
			case *ast.FieldNode:
				add(n.Name.Val, n.Tag.Val, n)
			case *ast.MapFieldNode:
				add(n.Name.Val, n.Tag.Val, n)
			case *ast.OneofNode:
				for _, e := range n.Decls {
					if n, ok := e.(*ast.FieldNode); ok {
						add(n.Name.Val, n.Tag.Val, n)
					}
				}
			case *ast.MessageNode:
				message(v.path+".", n)
			}
		}
	}
	for _, d := range f.Decls {
		if m, ok := d.(*ast.MessageNode); ok {
			message("", m)
		}
	}
	return out
}
