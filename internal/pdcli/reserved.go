package pdcli

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
)

// keepReserved puts back what an overlay reserves in a message or an enum
// both sides declare, which `protobuf-merge` leaves out.
//
// # Why
//
// The merge keeps the base's word on everything in a message that is not a
// field, a oneof, a nested type or an option, and on everything in an enum
// that is not a value or an option -- `reserved` among them -- and emits a
// message or an enum only the overlay declares whole. So `reserved 8;` in an
// overlay of payday's Holder, a number the app took out of the entity, was
// merged into the copy without it, and nothing in the descriptors stopped the
// number being spent again; the same line in a request only a service overlay
// declares did reach the contract. A number an app has given up is the one
// thing about it that has to outlive the field or the value, so it is carried
// here, for every merge, entity and contract alike.
//
// # What it writes
//
// For each message and enum the overlay declares, at the same path in the
// merged file, what its `reserved` say and the merged one does not reserve
// already: a number, range or name both sides reserve is said once, since
// protoc refuses a range that overlaps another and a name reserved twice. A
// statement whose every number or name is new is copied as the overlay wrote
// it, comments and all; one that is partly reserved already is written again
// with only what is new. Both go at the end of the body, after what the merge
// put there.
//
// Nothing here is `protobuf-merge`'s, whose project takes no code, so it is a
// pass over its output rather than a change to it.
func keepReserved(merged []byte, overlay []byte, overlayName string) ([]byte, error) {
	b, err := parseProto(overlayName, overlay)
	if err != nil {
		return nil, err
	}

	// What the overlay reserves, by the path of the message or enum that
	// says it.
	want := map[string][]*ast.ReservedNode{}
	for _, v := range bodiesOf(b) {
		if len(v.reserved) > 0 {
			want[v.path] = v.reserved
		}
	}
	if len(want) == 0 {
		return merged, nil
	}

	a, err := parseProto("merged.proto", merged)
	if err != nil {
		return nil, err
	}
	editions := a.Edition != nil

	type insert struct {
		at   int
		text string
	}
	var inserts []insert
	for _, v := range bodiesOf(a) {
		rs, ok := want[v.path]
		if !ok {
			continue
		}
		has := v.reservedSet()
		indent := strings.Repeat("  ", v.depth+1)

		var text strings.Builder
		for _, r := range rs {
			s, ok := newReserved(b, r, has, editions)
			if !ok {
				continue
			}
			text.WriteString("\n")
			info := b.NodeInfo(r)
			for i := range info.LeadingComments().Len() {
				text.WriteString("\n" + indent + info.LeadingComments().Index(i).RawText())
			}
			text.WriteString("\n" + indent + s)
		}
		if text.Len() > 0 {
			// Before the line the closing brace is on, so that the brace
			// keeps its own indentation.
			at := a.NodeInfo(v.closeBrace).Start().Offset
			at = bytes.LastIndexByte(merged[:at], '\n')
			inserts = append(inserts, insert{at: at, text: text.String()})
		}
	}
	if len(inserts) == 0 {
		return merged, nil
	}

	slices.SortFunc(inserts, func(x, y insert) int { return y.at - x.at })
	out := slices.Clone(merged)
	for _, v := range inserts {
		out = slices.Insert(out, v.at, []byte(v.text)...)
	}
	return out, nil
}

// body is a message or an enum of a file, where a `reserved` can be said.
type body struct {
	// path is its name from the top of the file, with what it is: a message
	// and an enum are kept apart, though protoc refuses one of each by a name.
	path       string
	depth      int
	enum       bool
	reserved   []*ast.ReservedNode
	closeBrace *ast.RuneNode
}

// bodiesOf is every message and enum of f, nested ones too.
func bodiesOf(f *ast.FileNode) []body {
	var vs []body
	var message func(prefix string, m *ast.MessageNode, depth int)
	enum := func(prefix string, e *ast.EnumNode, depth int) {
		v := body{path: prefix + "enum " + e.Name.Val, depth: depth, enum: true, closeBrace: e.CloseBrace}
		for _, d := range e.Decls {
			if r, ok := d.(*ast.ReservedNode); ok {
				v.reserved = append(v.reserved, r)
			}
		}
		vs = append(vs, v)
	}
	message = func(prefix string, m *ast.MessageNode, depth int) {
		path := prefix + "message " + m.Name.Val
		v := body{path: path, depth: depth, closeBrace: m.CloseBrace}
		for _, d := range m.Decls {
			if r, ok := d.(*ast.ReservedNode); ok {
				v.reserved = append(v.reserved, r)
			}
		}
		vs = append(vs, v)
		for _, d := range m.Decls {
			switch n := d.(type) {
			case *ast.MessageNode:
				message(path+".", n, depth+1)
			case *ast.EnumNode:
				enum(path+".", n, depth+1)
			}
		}
	}
	for _, d := range f.Decls {
		switch n := d.(type) {
		case *ast.MessageNode:
			message("", n, 0)
		case *ast.EnumNode:
			enum("", n, 0)
		}
	}
	return vs
}

func parseProto(name string, b []byte) (*ast.FileNode, error) {
	f, err := parser.Parse(name, bytes.NewReader(b), reporter.NewHandler(nil))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return f, nil
}

// reserved is what a message or an enum reserves: numbers as inclusive
// ranges, and names, and the bounds its numbers are in.
type reserved struct {
	nums     [][2]int64
	names    map[string]bool
	min, max int32
}

// A message's numbers run from 1 to 2^29-1, `max` in its reserved range; an
// enum's are any int32.
const (
	maxField = 536870911
	minEnum  = math.MinInt32
	maxEnum  = math.MaxInt32
)

func (v body) reservedSet() *reserved {
	has := &reserved{names: map[string]bool{}, min: 1, max: maxField}
	if v.enum {
		has.min, has.max = minEnum, maxEnum
	}
	for _, r := range v.reserved {
		for _, n := range r.Ranges {
			has.nums = append(has.nums, has.rangeOf(n))
		}
		for _, n := range r.Names {
			has.names[n.AsString()] = true
		}
		for _, n := range r.Identifiers {
			has.names[n.Val] = true
		}
	}
	return has
}

func (has *reserved) rangeOf(r *ast.RangeNode) [2]int64 {
	lo, _ := r.StartValueAsInt32(has.min, has.max)
	hi, _ := r.EndValueAsInt32(has.min, has.max)
	return [2]int64{int64(lo), int64(hi)}
}

// less is v without what has reserves already.
func (has *reserved) less(v [2]int64) [][2]int64 {
	rest := [][2]int64{v}
	for _, h := range has.nums {
		var next [][2]int64
		for _, r := range rest {
			if h[1] < r[0] || r[1] < h[0] {
				next = append(next, r)
				continue
			}
			if r[0] < h[0] {
				next = append(next, [2]int64{r[0], h[0] - 1})
			}
			if h[1] < r[1] {
				next = append(next, [2]int64{h[1] + 1, r[1]})
			}
		}
		rest = next
	}
	return rest
}

// newReserved is r as the merged message is to say it, given what it reserves
// already, and false when it reserves all of it. What it answers with is
// counted as reserved, so that two statements of the overlay that overlap are
// not said twice either.
func newReserved(f *ast.FileNode, r *ast.ReservedNode, has *reserved, editions bool) (string, bool) {
	whole := true
	var parts []string
	if len(r.Ranges) > 0 {
		var add [][2]int64
		for _, v := range r.Ranges {
			want := has.rangeOf(v)
			rest := has.less(want)
			if len(rest) != 1 || rest[0] != want {
				whole = false
			}
			add = append(add, rest...)
		}
		for _, v := range add {
			switch {
			case v[0] == v[1]:
				parts = append(parts, strconv.FormatInt(v[0], 10))
			case v[1] == int64(has.max):
				parts = append(parts, strconv.FormatInt(v[0], 10)+" to max")
			default:
				parts = append(parts, strconv.FormatInt(v[0], 10)+" to "+strconv.FormatInt(v[1], 10))
			}
		}
		has.nums = append(has.nums, add...)
	} else {
		var names []string
		for _, v := range r.Names {
			names = append(names, v.AsString())
		}
		for _, v := range r.Identifiers {
			names = append(names, v.Val)
		}
		for _, v := range names {
			if has.names[v] {
				whole = false
				continue
			}
			has.names[v] = true
			if editions {
				parts = append(parts, v)
			} else {
				parts = append(parts, strconv.Quote(v))
			}
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	if whole {
		return f.NodeInfo(r).RawText(), true
	}
	return "reserved " + strings.Join(parts, ", ") + ";", true
}

// keepOverlayReserved is [keepReserved] of the overlay at path.
func keepOverlayReserved(merged []byte, path string) ([]byte, error) {
	overlay, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return keepReserved(merged, overlay, path)
}
