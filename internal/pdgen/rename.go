package pdgen

import (
	"strconv"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"google.golang.org/protobuf/compiler/protogen"
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
