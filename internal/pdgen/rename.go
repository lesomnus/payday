package pdgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
)

const pkgRename = protogen.GoImportPath("github.com/lesomnus/payday/rename")

// EmitIndexRenames writes what each index the schema names was stored as
// before protoc-gen-orm-ent stored it under that name, for `rename.Indexes`
// to move before a migration (protobuf-orm/protoc-gen-orm-ent#15).
//
// Before, an index was stored under the name ent makes of its columns,
// `<table>_<column>_<column>`; now under the one it declares, `<table>_<name>`.
// A migration finds an index by name, so one an earlier build made is not
// found under the new one: it is made again, from every row, and the old one
// is left beside it, since a migration drops no index. Renamed first, it is
// found and nothing is made.
//
// Both names are protoc-gen-orm-ent's, written out here a second time: the
// table is the entity's name in lower case, and an edge's column is its name
// and `_id`. They are ent's for an index of no name, and one whose name is
// the one ent would make moves nowhere; neither is listed.
//
// The old name is of the columns the index has now. One whose columns changed
// in the same release was not stored under it either -- an app that reordered
// an index before this keeps its own list of those (lesomnus/rove#122).
func EmitIndexRenames(g *protogen.GeneratedFile, s *Schema) {
	g.P("// IndexRenames are the indexes this schema names, by what an earlier build")
	g.P("// stored each under: the name made of its columns, where protoc-gen-orm-ent")
	g.P("// now stores it under the one it declares. Hand them to `rename.Indexes`")
	g.P("// before a migration, which makes again whatever it does not find by name.")
	g.P("var IndexRenames = []", pkgRename.Ident("Index"), "{")
	for _, v := range s.Sorted() {
		table := strings.ToLower(v.Entity.Name())
		for idx := range v.Indexes() {
			if idx.Name() == "" {
				continue
			}

			columns := []string{}
			for p := range idx.Props() {
				switch p := p.(type) {
				case graph.Field:
					columns = append(columns, p.Name())
				case graph.Edge:
					columns = append(columns, p.Name()+"_id")
				}
			}

			from, to := table+"_"+strings.Join(columns, "_"), table+"_"+idx.Name()
			if from == to {
				continue
			}

			g.P("	{Table: ", strconv.Quote(table), ", From: ", strconv.Quote(from), ", To: ", strconv.Quote(to), "},")
		}
	}
	g.P("}")
	g.P("")
}

// columnRenames are the columns of payday's own entities that an earlier build
// stored under another name, by the entity's marker: what [ColumnRenames]
// answers for an app that has the entity. Only ever added to: a deployment may
// migrate from any build before the one that renamed it.
var columnRenames = []struct {
	own      pdpb.Own
	from, to string
}{
	// A receipt's first read was `data_read`, a typo of the `date_*` every
	// other time payday keeps is named by (payday#196).
	{pdpb.Own_OWN_NOTIFICATION_RECEIPT, "data_read", "date_read"},
}

// ColumnRename is one column of payday's own entities that an earlier build
// stored under another name: its table, as protoc-gen-orm-ent names it, and
// both names.
type ColumnRename struct {
	Table, From, To string
}

// ColumnRenames are the columns of payday's own entities that `s` has and an
// earlier build stored under another name, in the order they were renamed.
func ColumnRenames(s *Schema) []ColumnRename {
	var out []ColumnRename
	for _, v := range columnRenames {
		e := s.Own(v.own)
		if e == nil {
			continue
		}

		found := false
		for f := range e.Fields() {
			found = found || f.Name() == v.to
		}
		if !found {
			// The copy is payday's and holds the field, and an overlay may not
			// take one away: a copy without it is a payday that forgot this
			// list.
			panic(fmt.Sprintf("%s has no %s to rename %s to", e.FullName(), v.to, v.from))
		}

		out = append(out, ColumnRename{Table: strings.ToLower(e.Entity.Name()), From: v.from, To: v.to})
	}

	return out
}

// EmitColumnRenames writes [ColumnRenames], for `rename.Columns` to move
// before a migration.
//
// A migration finds a column by its name and drops none, so for one renamed it
// adds the new one, empty, beside the old: every row written before reads as
// never having had a value, and nothing fails. Moved first, it is found with
// what it holds.
//
// Written for every app, empty for one that has none of them, so that an
// app's `Migrate` names it whatever the app declares.
func EmitColumnRenames(g *protogen.GeneratedFile, s *Schema) {
	g.P("// ColumnRenames are the columns of payday's own entities in this schema that")
	g.P("// an earlier build stored under another name. Hand them to `rename.Columns`")
	g.P("// before a migration, which otherwise adds each beside the old one, empty.")
	g.P("var ColumnRenames = []", pkgRename.Ident("Column"), "{")
	for _, v := range ColumnRenames(s) {
		g.P("	{Table: ", strconv.Quote(v.Table), ", From: ", strconv.Quote(v.From), ", To: ", strconv.Quote(v.To), "},")
	}
	g.P("}")
	g.P("")
}
