package pdcli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// accessPkg is the access record's runtime.
const accessPkg = "github.com/lesomnus/payday/access"

// doctorAccess says when the app does not wire the access record (#111).
//
// It is the quietest thing asked about here. An app that never wires it fails
// nothing, and records nothing of who read whose personal data -- which is
// found the day somebody asks for that record, and is then a record nobody can
// make.
//
// Three parts, each found by what the app's own code names: the interceptor,
// built from an `access.Config`, without which nothing is recorded at all; the
// `access.Writer` it hands a read's row to; and the generated `AccessRecorder`,
// without which a write's row is not in the write's transaction. A test does
// not count: what it wires is not what the app serves.
func doctorAccess(l Layout) []Finding {
	var config, writer, recorder bool

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

		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}

		name := importedAs(f, accessPkg)
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.SelectorExpr:
				// `pd.AccessRecorder`, whatever the generated package is
				// called where it is used.
				if v.Sel.Name == "AccessRecorder" {
					recorder = true
				}

			case *ast.CompositeLit:
				sel, ok := v.Type.(*ast.SelectorExpr)
				if !ok || name == "" {
					break
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == name {
					switch sel.Sel.Name {
					case "Config":
						config = true
					case "Writer":
						writer = true
					}
				}
			}

			return true
		})

		return nil
	})

	var missing []string
	if !config {
		missing = append(missing, "no interceptor from an `access.Config`, which is what records anything at all")
	}
	if !writer {
		missing = append(missing, "no `access.Writer` for a read's row to wait in")
	}
	if !recorder {
		missing = append(missing, "no `AccessRecorder` among the servers' recorders, which puts a write's row in its transaction")
	}
	if len(missing) == 0 {
		return nil
	}

	return []Finding{{
		What: "the access record is not wired, so nothing records who read or wrote whose personal data: " +
			strings.Join(missing, "; "),
		Fix: `rec := bare.Recorders{pd.Recorder(content), ..., pd.AccessRecorder()}

w := &access.Writer{Sink: pd.AccessSink(client)}
s.Spin = append(s.Spin, w)

chain := grpcx.Serving(ctx, ...).
	... // who is calling, then how often they may
	With(access.Config{Subjects: pd.Subjects, Refs: pd.SubjectRefs, Address: access.Peer, Writer: w}.Interceptor()).
	With(gate.Interceptor(s.Policy))

pd new's cmd/serve.go has it whole; docs/migrating.md says what each part is.`,
	}}
}

// importedAs is what `f` calls the package at `path`, and empty when it does
// not import it.
func importedAs(f *ast.File, path string) string {
	for _, v := range f.Imports {
		p, err := strconv.Unquote(v.Path.Value)
		if err != nil || p != path {
			continue
		}
		if v.Name != nil {
			return v.Name.Name
		}

		return filepath.Base(p)
	}

	return ""
}
