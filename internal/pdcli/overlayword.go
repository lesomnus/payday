package pdcli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bufbuild/protocompile/ast"
)

// overlaysWord is payday's entity file `base` with each value an option of
// its messages says replaced by what `overlay` says there instead, so that the
// merge after it keeps the overlay's word.
//
// # Why
//
// `protobuf-merge` merges an option both sides put on a message, and where
// both set one scalar inside it differently it keeps the base's and says so
// only under `-strict`. So `option (payday.entity) = {list: {max: 500}}` in an
// overlay of payday's Holder was merged into a copy that still said `max:
// 100`: `pd gen` printed nothing, the server capped a page at a hundred, and
// the app's author had no way to know that what they wrote did nothing. It
// was the same for every scalar of payday's declaration -- `domain: 9` on a
// Holder, `own:` -- and for an option written as a value rather than a
// message literal.
//
// Here the overlay's word is put where payday's was, before the merge, so the
// copy says what the overlay said and [pdgen.CheckOverlay] reads it. That is
// where what an overlay may say of payday's entities is decided, against what
// payday ships: the page of Holder and Tenant is the overlay's to say, and
// anything else is refused there by its path. Nothing an overlay says of an
// option is dropped on the way to it.
//
// `-strict` was the other way, and it refuses every conflict the merge knows
// of with the merge's words: a field of payday's redeclared as well, which
// CheckOverlay says better, and the page, which is allowed.
//
// # What it replaces
//
// Within the options both say, by name, of a message both declare (nested
// ones too): a value both write once, recursively through message literals,
// where the two differ. A list, or a field written more than once, is left to
// the merge, which appends the overlay's entries to the base's, and what was
// appended is what CheckOverlay compares. What only one side says is the
// merge's as before.
//
// Nothing here is `protobuf-merge`'s, whose project takes no code, so it is a
// pass before it rather than a change to it -- as [keepReserved] is a pass
// after.
func overlaysWord(base []byte, baseName string, overlay []byte, overlayName string) ([]byte, error) {
	b, err := parseProto(overlayName, overlay)
	if err != nil {
		return nil, err
	}
	want, twice := messageOptionsOf(b)
	if len(twice) > 0 {
		// The merge takes one of them by name, and the other is gone without a
		// word. protoc refuses a message that says an option twice; an overlay
		// is never compiled by itself, so this is where it is said.
		return nil, fmt.Errorf("%s: %s; say each once, with everything it says in one literal",
			overlayName, strings.Join(twice, "; "))
	}
	if len(want) == 0 {
		return base, nil
	}

	a, err := parseProto(baseName, base)
	if err != nil {
		return nil, err
	}

	r := replacer{a: a, aText: base, b: b, bText: overlay}
	paydays, _ := messageOptionsOf(a)
	for path, got := range paydays {
		ws, ok := want[path]
		if !ok {
			continue
		}
		for name, o := range got {
			if w, ok := ws[name]; ok {
				r.value(o.Val, w.Val)
			}
		}
	}
	if len(r.edits) == 0 {
		return base, nil
	}

	slices.SortFunc(r.edits, func(x, y edit) int { return y.from - x.from })
	out := slices.Clone(base)
	for _, v := range r.edits {
		out = slices.Replace(out, v.from, v.to, []byte(v.text)...)
	}
	return out, nil
}

type edit struct {
	from, to int
	text     string
}

type replacer struct {
	a, b         *ast.FileNode
	aText, bText []byte
	edits        []edit
}

func (r *replacer) text(f *ast.FileNode, src []byte, n ast.Node) string {
	from, to := span(f, n)
	return string(src[from:to])
}

// span is where n is in its source, as offsets from and to. Not
// `NodeInfo.End().Offset`: that is the offset of the last character, and only
// its column is moved past it.
func span(f *ast.FileNode, n ast.Node) (int, int) {
	last := f.TokenInfo(n.End())
	return f.NodeInfo(n).Start().Offset, last.Start().Offset + len(last.RawText())
}

// value puts b's `w` where a's `v` is, or goes into both when both are
// message literals.
func (r *replacer) value(v, w ast.ValueNode) {
	vl, v_ok := v.(*ast.MessageLiteralNode)
	wl, w_ok := w.(*ast.MessageLiteralNode)
	if v_ok && w_ok {
		vs := literalOf(vl)
		ws := literalOf(wl)
		for k, ve := range vs {
			we, ok := ws[k]
			if !ok || len(ve) != 1 || len(we) != 1 {
				continue
			}
			_, v_list := ve[0].Val.(*ast.ArrayLiteralNode)
			_, w_list := we[0].Val.(*ast.ArrayLiteralNode)
			if v_list || w_list {
				continue
			}
			r.value(ve[0].Val, we[0].Val)
		}
		return
	}

	if canonicalText(r.text(r.a, r.aText, v)) == canonicalText(r.text(r.b, r.bText, w)) {
		return
	}
	from, to := span(r.a, v)
	r.edits = append(r.edits, edit{from: from, to: to, text: r.text(r.b, r.bText, w)})
}

// literalOf is a message literal's fields by the name they are written as.
func literalOf(l *ast.MessageLiteralNode) map[string][]*ast.MessageFieldNode {
	out := map[string][]*ast.MessageFieldNode{}
	for _, el := range l.Elements {
		out[el.Name.Value()] = append(out[el.Name.Value()], el)
	}
	return out
}

// messageOptionsOf is the options each message of f declares, by the path of
// the message and the option's name as written, and each option a message
// says more than once.
func messageOptionsOf(f *ast.FileNode) (map[string]map[string]*ast.OptionNode, []string) {
	out := map[string]map[string]*ast.OptionNode{}
	var twice []string
	var message func(prefix string, m *ast.MessageNode)
	message = func(prefix string, m *ast.MessageNode) {
		path := prefix + m.Name.Val
		for _, d := range m.Decls {
			switch n := d.(type) {
			case *ast.OptionNode:
				if out[path] == nil {
					out[path] = map[string]*ast.OptionNode{}
				}
				name := optionNameText(n.Name)
				if _, ok := out[path][name]; ok {
					twice = append(twice, fmt.Sprintf("message %s says option %s twice, and the merge keeps one of them", path, name))
				}
				out[path][name] = n
			case *ast.MessageNode:
				message(path+".", n)
			}
		}
	}
	for _, d := range f.Decls {
		if m, ok := d.(*ast.MessageNode); ok {
			message("", m)
		}
	}
	return out, twice
}

// optionNameText is an option's name as it is written, `(payday.entity)`.
func optionNameText(n *ast.OptionNameNode) string {
	var s strings.Builder
	for i, p := range n.Parts {
		if i > 0 {
			s.WriteString(".")
		}
		s.WriteString(p.Value())
	}
	return s.String()
}

// canonicalText is source text with its spacing taken out, so that two values
// written differently compare equal when they say the same.
func canonicalText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
