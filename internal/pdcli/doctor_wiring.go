package pdcli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// loopWiring is what an app's own Go says of one kind of loop payday ships --
// a `meter.Reader`, an `entitle.Follower` -- read file by file, outside its
// tests: each literal of it, which of the fields asked about it says, what it
// is bound to, and whether it is handed to an `append` or put in a slice,
// which is how a spin list is made; and whether one function of the
// generated package is called anywhere.
//
// Read off the syntax and not the types, as the access record's wiring is: a
// doctor that had to build the app first could not say anything about one
// that does not build. A test does not count, since what it wires is not what
// the app serves.
type loopWiring struct {
	l    Layout
	pd   string
	fset *token.FileSet

	// pkg and typ are the loop's package and type, fields what is asked of a
	// literal of it, and call the generated function looked for.
	pkg, typ string
	fields   []string
	call     string

	// called is whether `pd.<call>` is called anywhere.
	called bool

	loops []loopLit

	// spun are the names handed to an `append` or put in a slice: an
	// identifier, a field, or a function called there. set are the names
	// each asked-about field is assigned through, by field.
	spun map[string]bool
	set  map[string]map[string]bool
}

// loopLit is one literal of the loop: where it is, which fields it says, the
// names it is bound to, and whether it is spun where it is written.
type loopLit struct {
	at     string
	said   map[string]bool
	names  []string
	inline bool

	// shown is the first thing it is bound to as it is written -- `s.Meters`
	// -- for the line a fix gives.
	shown string
}

// readWiring is what `l`'s Go says of `pkg`'s `typ`, asking each literal of it
// about `fields`, and whether `pd.<call>` is called.
func readWiring(l Layout, pkg, typ, call string, fields ...string) *loopWiring {
	w := &loopWiring{
		l: l, pd: l.Module + "/" + DirPd_, fset: token.NewFileSet(),
		pkg: pkg, typ: typ, call: call, fields: fields,
		spun: map[string]bool{}, set: map[string]map[string]bool{},
	}
	for _, f := range fields {
		w.set[f] = map[string]bool{}
	}

	_ = filepath.WalkDir(l.Work, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "testdata", "vendor", ".git", "ent":
				return fs.SkipDir
			}

			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}

		f, err := parser.ParseFile(w.fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}
		w.read(p, f)

		return nil
	})

	return w
}

// says is whether `r` says `field`, or a name it is bound to is given it
// later.
func (w *loopWiring) says(r loopLit, field string) bool {
	return r.said[field] || slices.ContainsFunc(r.names, func(n string) bool { return w.set[field][n] })
}

// spins is whether `r` is handed to a spin list: as it is written, or by a
// name it is bound to.
func (w *loopWiring) spins(r loopLit) bool {
	return r.inline || slices.ContainsFunc(r.names, func(n string) bool { return w.spun[n] })
}

// shownAs is what a fix calls `r`: what it is bound to as it is written, or
// `name`.
func (r loopLit) shownAs(name string) string {
	if r.shown != "" {
		return r.shown
	}

	return name
}

func (w *loopWiring) read(p string, f *ast.File) {
	pd, rt := importedAs(f, w.pd), importedAs(f, w.pkg)

	// What each literal is bound to, found from where it is bound: an
	// assignment, a declaration, a field of a literal, a return.
	bound := map[*ast.CompositeLit][]string{}
	shown := map[*ast.CompositeLit]string{}
	inline := map[*ast.CompositeLit]bool{}
	bind := func(e ast.Expr, name string, as string) {
		c := w.lit(e, rt)
		if c == nil || name == "" {
			return
		}
		bound[c] = append(bound[c], name)
		if shown[c] == "" {
			shown[c] = as
		}
	}

	var fn string
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncDecl:
			fn = v.Name.Name

		case *ast.CallExpr:
			if s, ok := v.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == w.call {
				if x, ok := s.X.(*ast.Ident); ok && pd != "" && x.Name == pd {
					w.called = true
				}
			}
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "append" {
				for _, a := range v.Args[min(1, len(v.Args)):] {
					w.spin(a, rt, inline)
				}
			}

		case *ast.CompositeLit:
			if _, ok := v.Type.(*ast.ArrayType); ok {
				for _, e := range v.Elts {
					w.spin(e, rt, inline)
				}
			}
			for _, e := range v.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok {
						bind(kv.Value, k.Name, "")
					}
				}
			}

		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				if s, ok := lhs.(*ast.SelectorExpr); ok {
					if set, ok := w.set[s.Sel.Name]; ok {
						set[nameOf(s.X)] = true
					}
				}
				if i < len(v.Rhs) {
					bind(v.Rhs[i], nameOf(lhs), written(lhs))
				}
			}

		case *ast.ValueSpec:
			for i, n := range v.Names {
				if i < len(v.Values) {
					bind(v.Values[i], n.Name, n.Name)
				}
			}

		case *ast.ReturnStmt:
			for _, r := range v.Results {
				if fn != "" {
					bind(r, fn, fn+"(...)")
				}
			}
		}

		return true
	})

	ast.Inspect(f, func(n ast.Node) bool {
		c := w.lit(n, rt)
		if c == nil || n != ast.Node(c) {
			return true
		}

		r := loopLit{
			at:    w.l.rel(p) + fmt.Sprintf(":%d", w.fset.Position(c.Pos()).Line),
			said:  map[string]bool{},
			names: bound[c], inline: inline[c], shown: shown[c],
		}
		for _, e := range c.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, ok := kv.Key.(*ast.Ident)
			if !ok || !slices.Contains(w.fields, k.Name) {
				continue
			}
			if id, ok := kv.Value.(*ast.Ident); !ok || id.Name != "nil" {
				r.said[k.Name] = true
			}
		}
		w.loops = append(w.loops, r)

		return true
	})
}

// spin is one value handed to an `append` or put in a slice.
func (w *loopWiring) spin(e ast.Expr, rt string, inline map[*ast.CompositeLit]bool) {
	if c := w.lit(e, rt); c != nil {
		inline[c] = true
		return
	}
	if c, ok := e.(*ast.CallExpr); ok {
		e = c.Fun
	}
	if n := nameOf(e); n != "" {
		w.spun[n] = true
	}
}

// lit is the `<rt>.<typ>{...}` literal `n` is, or takes the address of, and
// nil for anything else.
func (w *loopWiring) lit(n ast.Node, rt string) *ast.CompositeLit {
	if rt == "" {
		return nil
	}
	if u, ok := n.(*ast.UnaryExpr); ok && u.Op == token.AND {
		n = u.X
	}

	c, ok := n.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	s, ok := c.Type.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != w.typ {
		return nil
	}
	if x, ok := s.X.(*ast.Ident); !ok || x.Name != rt {
		return nil
	}

	return c
}

// written is a name or a selector as it is written, and empty for anything
// else.
func written(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if x := written(v.X); x != "" {
			return x + "." + v.Sel.Name
		}
	}

	return ""
}

// nameOf is what a value is called where it is used: an identifier's name,
// or a selector's last one -- `s.Meters` is `Meters`, as a field of anything.
func nameOf(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.StarExpr:
		return nameOf(v.X)
	case *ast.ParenExpr:
		return nameOf(v.X)
	}

	return ""
}
