// Package logcheck_test holds payday to the way it logs an error, in its own
// packages, in the code it generates and in the app `pd new` writes: as the
// error, and one to a record (docs/guide/server.md, "Logging an error").
//
// A test and not a sentence, because neither way of getting it wrong shows.
// The OpenTelemetry bridge every app logs through makes an error-valued
// attribute the record's error, from which the logs SDK writes
// `exception.type` and `exception.message`. An error passed as its text is a
// string under whatever key it was given -- no type, and nothing a backend
// knows for an exception. And the bridge keeps one error to a record, the
// last: a second is dropped without a word.
package logcheck_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"golang.org/x/tools/go/packages"

	"github.com/lesomnus/payday/internal/pdcli"
)

// records are the log/slog functions and methods that write a record, and
// attrs those that make an attribute of a value.
var (
	records = map[string]bool{
		"Debug": true, "DebugContext": true,
		"Info": true, "InfoContext": true,
		"Warn": true, "WarnContext": true,
		"Error": true, "ErrorContext": true,
		"Log": true, "LogAttrs": true,
	}
	attrs = map[string]bool{"Any": true, "String": true, "With": true}
)

// TestTheBridgeMakesTheErrorAnException is what the rest rests on, held to the
// bridge and the SDK this payday links: an error value is the record's error,
// and the SDK writes it as `exception.type` and `exception.message`; its text
// is a string under its key and no exception; and of two errors, one is kept.
// A bridge that changes any of it changes the convention, and this says so in
// the pull request that moves it.
func TestTheBridgeMakesTheErrorAnException(t *testing.T) {
	x := require.New(t)

	var got []sdklog.Record
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(processor(func(r *sdklog.Record) { got = append(got, r.Clone()) })))
	l := slog.New(otelslog.NewHandler("logcheck", otelslog.WithLoggerProvider(lp)))

	first, second := errors.New("the first"), errors.New("the second")
	l.Error("as the error", slog.Any("err", first))
	l.Error("as its text", slog.String("err", first.Error()))
	l.Error("two of them", slog.Any("err", first), slog.Any("cause", second))
	x.Len(got, 3)

	attrs := func(r sdklog.Record) map[string]string {
		vs := map[string]string{}
		r.WalkAttributes(func(kv attribute.KeyValue) bool {
			vs[string(kv.Key)] = kv.Value.Emit()
			return true
		})
		return vs
	}
	message, kind := string(semconv.ExceptionMessageKey), string(semconv.ExceptionTypeKey)

	x.Equal(map[string]string{message: "the first", kind: "*errors.errorString"}, attrs(got[0]), "the error, as an exception")
	x.Equal(map[string]string{"err": "the first"}, attrs(got[1]), "its text, as a string nothing knows is an error")
	x.Equal(map[string]string{message: "the second", kind: "*errors.errorString"}, attrs(got[2]), "two, and the first is gone")
}

// processor hands every record emitted to f.
type processor func(*sdklog.Record)

func (processor) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }
func (f processor) OnEmit(_ context.Context, r *sdklog.Record) error     { f(r); return nil }
func (processor) Shutdown(context.Context) error                         { return nil }
func (processor) ForceFlush(context.Context) error                       { return nil }

// TestAnErrorIsLoggedAsOneError: no record payday writes, or the code it
// generates writes, carries an error's text, or two errors.
func TestAnErrorIsLoggedAsOneError(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	// Each module from its own directory, so that this does not rest on
	// go.work. apptest is where the generated code is, compiled with the
	// rest of an app.
	var found []string
	for _, dir := range []string{root, filepath.Join(root, "internal", "apptest")} {
		vs, n := typed(t, dir)
		require.NotZero(t, n, "%s writes no record at all, so this read nothing", dir)
		found = append(found, vs...)
	}
	slices.Sort(found)
	found = slices.Compact(found)
	require.Empty(t, found, "log an error as the error, and one to a record:\n%s", strings.Join(found, "\n"))
}

// TestWhatPdNewWritesLogsAnErrorAsOne: the same of the app `pd new` writes,
// which nothing here compiles, so it is read and not typed -- an `Error()` of
// nothing is an error's text.
func TestWhatPdNewWritesLogsAnErrorAsOne(t *testing.T) {
	x := require.New(t)

	dir := filepath.Join(t.TempDir(), "app")
	x.NoError((pdcli.New{Dir: dir, Module: "github.com/acme/thing"}).Write())

	var found []string
	x.NoError(filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (!records[sel.Sel.Name] && !attrs[sel.Sel.Name]) {
				return true
			}
			for _, a := range call.Args {
				if isErrorCall(a) {
					rel, _ := filepath.Rel(dir, p)
					found = append(found, rel+":"+strconv.Itoa(fset.Position(a.Pos()).Line)+": an error's text")
				}
			}
			return true
		})
		return nil
	}))
	x.Empty(found, "log an error as the error, and one to a record:\n%s", strings.Join(found, "\n"))
}

// typed reads every package of the module in dir, with its types, and answers
// with what it found and how many records it read.
func typed(t *testing.T, dir string) ([]string, int) {
	t.Helper()

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  dir,
	}
	pkgs, err := packages.Load(cfg, "./...")
	require.NoError(t, err)

	errorType := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
	isError := func(info *types.Info, e ast.Expr) bool {
		v := info.TypeOf(e)
		return v != nil && types.Implements(v, errorType) && !types.Identical(v, types.Typ[types.UntypedNil])
	}
	fromSlog := func(info *types.Info, sel *ast.SelectorExpr) bool {
		fn, ok := info.Uses[sel.Sel].(*types.Func)
		return ok && fn.Pkg() != nil && fn.Pkg().Path() == "log/slog"
	}

	var found []string
	calls := 0
	for _, p := range pkgs {
		for _, e := range p.Errors {
			t.Errorf("%s: %s", p.PkgPath, e)
		}
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !fromSlog(p.TypesInfo, sel) {
					return true
				}
				at := func(e ast.Expr) string {
					pos := p.Fset.Position(e.Pos())
					rel, err := filepath.Rel(dir, pos.Filename)
					if err != nil {
						rel = pos.Filename
					}
					return rel + ":" + strconv.Itoa(pos.Line)
				}

				switch {
				case attrs[sel.Sel.Name]:
					for _, a := range call.Args {
						if isErrorCall(a) && isError(p.TypesInfo, a.(*ast.CallExpr).Fun.(*ast.SelectorExpr).X) {
							found = append(found, at(a)+": an error's text")
						}
					}

				case records[sel.Sel.Name]:
					calls++
					errs := 0
					for _, a := range call.Args {
						if isErrorCall(a) && isError(p.TypesInfo, a.(*ast.CallExpr).Fun.(*ast.SelectorExpr).X) {
							found = append(found, at(a)+": an error's text")
						}
						// The error itself, as a value of a key, or in an
						// attribute made of it.
						if isError(p.TypesInfo, a) {
							errs++
						}
						if c, ok := a.(*ast.CallExpr); ok && len(c.Args) == 2 {
							if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "Any" && fromSlog(p.TypesInfo, s) && isError(p.TypesInfo, c.Args[1]) {
								errs++
							}
						}
					}
					if errs > 1 {
						found = append(found, at(call)+": two errors, and the bridge keeps one")
					}
				}
				return true
			})
		}
	}

	return found, calls
}

// isErrorCall says e is `x.Error()`, which is what an error's text is.
func isErrorCall(e ast.Expr) bool {
	c, ok := e.(*ast.CallExpr)
	if !ok || len(c.Args) != 0 {
		return false
	}
	s, ok := c.Fun.(*ast.SelectorExpr)
	return ok && s.Sel.Name == "Error"
}
