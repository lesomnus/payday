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

	"github.com/lesomnus/payday/internal/pdgen"
)

// meterPkg is the meters' runtime.
const meterPkg = "github.com/lesomnus/payday/meter"

// doctorMeters says when what each tenant holds is declared and nothing reads
// it, and when a reader is built and never run (payday#186).
//
// Both are as quiet as anything asked about here. A meter nobody reads fails
// nothing: the app serves, and the billing service is handed no readings -- or
// the ones of the meters the app wrote itself and not the schema's -- which is
// found the day somebody looks at a bill. A reader that is built and not in
// the spin list reads whenever the app asks one tenant to be read, before it
// destroys it, and on no schedule at all.
//
// Each is found by what the app's own code names, as the access record's
// parts are: `pd.Meters`, which is the schema's meters; a `meter.Reader`
// literal, and its `Sink`; and that reader -- or what it was put in, or the
// function that builds it -- handed to an `append` or in a slice, which is how
// a spin list is made. A test does not count: what it wires is not what the
// app serves.
func doctorMeters(l Layout, s *pdgen.Schema) []Finding {
	w := meterWiring{l: l, pd: l.Module + "/" + DirPd_, fset: token.NewFileSet(), spun: map[string]bool{}, sunk: map[string]bool{}}
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

	var vs []Finding
	if n := len(s.Meters); n > 0 && !w.called {
		keys := []string{}
		for _, m := range s.Meters {
			keys = append(keys, m.Key)
		}
		vs = append(vs, Finding{
			What: fmt.Sprintf("the schema declares %d meters -- %s -- and nothing reads them: "+
				"`pd.Meters` is called nowhere, so no reading of them reaches a sink", n, strings.Join(keys, ", ")),
			Fix: `s.Meters = &meter.Reader{
	Tenants: pd.MeterTenants(client),
	Meters:  pd.Meters(client), // and the app's own, if it writes any
	Sink:    sink,              // the billing service's client
}
s.Spin = append(s.Spin, s.Meters)

docs/guide/meters.md has the rest: a replica, and reading a tenant before it is destroyed.`,
		})
	}

	for _, r := range w.readers {
		if !r.sink && !slices.ContainsFunc(r.names, func(n string) bool { return w.sunk[n] }) {
			vs = append(vs, Finding{
				What: fmt.Sprintf("%s builds a meter.Reader with no Sink, and a reader with none gives up as it starts: "+
					"nothing is measured and the process ends", r.at),
				Fix: "Sink: sink, // the billing service's client: anything with Send(ctx, []meter.Reading) error",
			})
		}

		if r.inline || slices.ContainsFunc(r.names, func(n string) bool { return w.spun[n] }) {
			continue
		}
		name := "s.Meters"
		if r.shown != "" {
			name = r.shown
		}
		vs = append(vs, Finding{
			What: fmt.Sprintf("%s builds a meter.Reader that is never handed to spin.Run, so no tenant is measured "+
				"on a schedule -- only when the app asks for one", r.at),
			Fix: fmt.Sprintf("s.Spin = append(s.Spin, %s)", name),
		})
	}

	return vs
}

// meterWiring is what the app's Go says of its meters, gathered file by file.
type meterWiring struct {
	l    Layout
	pd   string
	fset *token.FileSet

	// called is whether `pd.Meters` is called anywhere.
	called bool

	readers []meterReader

	// spun are the names handed to an `append` or put in a slice: an
	// identifier, a field, or a function called there. sunk are the names a
	// `.Sink` is assigned through.
	spun, sunk map[string]bool
}

// meterReader is one `meter.Reader` literal: where it is, whether it says its
// sink, the names it is bound to, and whether it is spun where it is written.
type meterReader struct {
	at     string
	sink   bool
	names  []string
	inline bool

	// shown is the first thing it is bound to as it is written -- `s.Meters`
	// -- for the line a fix gives.
	shown string
}

func (w *meterWiring) read(p string, f *ast.File) {
	pd, meter := importedAs(f, w.pd), importedAs(f, meterPkg)

	// What each literal is bound to, found from where it is bound: an
	// assignment, a declaration, a field of a literal, a return.
	bound := map[*ast.CompositeLit][]string{}
	shown := map[*ast.CompositeLit]string{}
	inline := map[*ast.CompositeLit]bool{}
	bind := func(e ast.Expr, name string, as string) {
		c := readerLit(e, meter)
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
			if s, ok := v.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "Meters" {
				if x, ok := s.X.(*ast.Ident); ok && pd != "" && x.Name == pd {
					w.called = true
				}
			}
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "append" {
				for _, a := range v.Args[min(1, len(v.Args)):] {
					w.spin(a, meter, inline)
				}
			}

		case *ast.CompositeLit:
			if _, ok := v.Type.(*ast.ArrayType); ok {
				for _, e := range v.Elts {
					w.spin(e, meter, inline)
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
				if s, ok := lhs.(*ast.SelectorExpr); ok && s.Sel.Name == "Sink" {
					w.sunk[nameOf(s.X)] = true
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
		c := readerLit(n, meter)
		if c == nil || n != ast.Node(c) {
			return true
		}

		r := meterReader{at: w.l.rel(p) + fmt.Sprintf(":%d", w.fset.Position(c.Pos()).Line), names: bound[c], inline: inline[c], shown: shown[c]}
		for _, e := range c.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Sink" {
				if id, ok := kv.Value.(*ast.Ident); !ok || id.Name != "nil" {
					r.sink = true
				}
			}
		}
		w.readers = append(w.readers, r)

		return true
	})
}

// spin is one value handed to an `append` or put in a slice.
func (w *meterWiring) spin(e ast.Expr, meter string, inline map[*ast.CompositeLit]bool) {
	if c := readerLit(e, meter); c != nil {
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

// readerLit is the `meter.Reader{...}` literal `n` is, or takes the address
// of, and nil for anything else.
func readerLit(n ast.Node, meter string) *ast.CompositeLit {
	if meter == "" {
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
	if !ok || s.Sel.Name != "Reader" {
		return nil
	}
	if x, ok := s.X.(*ast.Ident); !ok || x.Name != meter {
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
