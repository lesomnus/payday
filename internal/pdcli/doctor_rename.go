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
// reads as never having had a value -- a receipt as read at no time at all
// (payday#196), an event as having happened at none -- the schema check a
// server makes before serving passes, and nothing fails. The values are still
// there, in a column nothing reads; and on SQLite, in a table whose index
// names the column, not even that, since the table is made again without it.
//
// Read off the app's own Go, as the access record's wiring is: a call of
// `rename.Columns` anywhere outside a test is taken for the step, whatever it
// is handed -- an app that wrote its own list is an app that decided. A test
// does not count, and neither does generated Go.
func doctorRenames(l Layout, vs []pdgen.ColumnRename) []Finding {
	if len(vs) == 0 || callsRenameColumns(l) {
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
		Fix: `// in the app's Migrate, before the migration
if err := rename.Columns(ctx, s.Db, s.Dialect, pd.ColumnRenames); err != nil {
	return err
}

docs/migrating.md has the rest.`,
	}}
}

// callsRenameColumns is whether the app's own Go, outside its tests and what
// is generated, calls `rename.Columns`.
func callsRenameColumns(l Layout) bool {
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
			if !ok || sel.Sel.Name != "Columns" {
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
