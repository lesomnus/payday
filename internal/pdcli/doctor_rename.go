package pdcli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/lesomnus/payday/internal/pdgen"
)

// renamePkg moves what an earlier build made under another name.
const renamePkg = "github.com/lesomnus/payday/rename"

// A column of payday's own that an earlier build stored under another name,
// and nothing that moves it before a migration.
//
// It is the one migration step whose absence nothing else reports. A migration
// finds a column by its name and drops none, so on a store an earlier build
// made it adds the new column, empty, beside the old: every row written before
// reads as never having had a value, the schema check a server makes before
// serving passes, and nothing fails. The values are still there, in a column
// nothing reads; and on SQLite, in a table whose index names the column, not
// even that, since the table is made again without it.
//
// Asked only of a column payday has renamed, and there is none until the first
// app launches ([pdgen.ColumnRenames]). Read off the app's own Go, as the
// access record's wiring is: a call of `rename.Columns` anywhere outside a
// test is taken for the step, whatever it is handed -- an app that wrote its
// own list is an app that decided. A test does not count, and neither does
// generated Go.
func doctorRenames(l Layout, vs []pdgen.ColumnRename) []Finding {
	if len(vs) == 0 || callsRename(l, "Columns") {
		return nil
	}

	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = fmt.Sprintf("%s.%s, which an earlier build called %s", v.Table, v.To, v.From)
	}

	return []Finding{{
		What: fmt.Sprintf("nothing hands pd.ColumnRenames to rename.Columns, so a migration of a store an earlier build "+
			"made adds each renamed column empty beside the old one, and what the old one holds is no longer read -- "+
			"on SQLite, a table whose index names one is made again without the old column, and what it held is lost: %s",
			strings.Join(names, "; ")),
		Fix: `// in the app's Migrate, after rename.Tables and before the migration
if err := rename.Columns(ctx, s.Db, s.Dialect, pd.ColumnRenames); err != nil {
	return err
}

docs/migrating.md has the rest.`,
	}}
}

// A table of an entity the schema renamed (`(payday.entity).formerly`), and
// nothing that moves it before a migration.
//
// It fails as a column does, and worse: a migration finds a table by its name
// and drops none, so on a store an earlier build made it makes the renamed
// entity's table again, empty, and every row it had stays in the old one --
// the schema check a server makes before serving passes, and the app serves
// an empty table. Read off the app's own Go, as the column's step is.
func doctorTableRenames(l Layout, vs []pdgen.TableRename) []Finding {
	if len(vs) == 0 || callsRename(l, "Tables") {
		return nil
	}

	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = fmt.Sprintf("%s, which an earlier build called %s", v.To, v.From)
	}

	return []Finding{{
		What: fmt.Sprintf("nothing hands pd.TableRenames to rename.Tables, so a migration of a store an earlier build "+
			"made makes each renamed entity's table again, empty, and its rows stay in the old one, read by nothing: %s",
			strings.Join(names, "; ")),
		Fix: `// first in the app's Migrate: before rename.Columns, rename.Indexes and the migration
if err := rename.Tables(ctx, s.Db, s.Dialect, pd.TableRenames); err != nil {
	return err
}

docs/migrating.md has the rest.`,
	}}
}

// callsRename is whether the app's own Go, outside its tests and what is
// generated, calls `rename.<fn>`.
func callsRename(l Layout, fn string) bool {
	found := false
	_ = filepath.WalkDir(l.Work, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
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

		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil || ast.IsGenerated(f) {
			return nil
		}
		rename := importedAs(f, renamePkg)
		if rename == "" {
			return nil
		}

		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return !found
			}
			sel, ok := c.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != fn {
				return !found
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == rename {
				found = true
			}

			return !found
		})

		return nil
	})

	return found
}
