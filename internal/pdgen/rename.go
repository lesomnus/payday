package pdgen

import (
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
)

const pkgRename = protogen.GoImportPath("github.com/lesomnus/payday/rename")

// EmitIndexRenames writes `pd.IndexRenames`, the indexes an earlier build
// stored under another name, for `rename.Indexes` to move before a migration.
//
// It holds none. Every index a schema names is stored under the name it
// declares, `<table>_<name>`, and payday moves no index an earlier payday
// stored otherwise: until the first app launches, it keeps nothing for an
// earlier payday (CLAUDE.md). It is written for every app all the same, as
// [EmitColumnRenames] is, so that an app's `Migrate` hands it over whatever the
// app declares, and an index payday renames after that launch is moved with no
// change to any app.
func EmitIndexRenames(g *protogen.GeneratedFile) {
	g.P("// IndexRenames are the indexes of this schema that an earlier build stored")
	g.P("// under another name. Hand them to `rename.Indexes` before a migration, which")
	g.P("// makes again whatever it does not find by name.")
	g.P("var IndexRenames = []", pkgRename.Ident("Index"), "{}")
	g.P("")
}

// columnRenames are the columns of payday's own entities that an earlier build
// stored under another name, by the entity's marker: what [ColumnRenames]
// answers for an app that has the entity.
//
// None: until the first app launches, payday keeps nothing for an earlier
// payday (CLAUDE.md), and a column it renames before then is just renamed.
// From that launch on, a column payday renames goes here, and the list is only
// ever added to: a deployment may migrate from any build before the one that
// renamed it.
var columnRenames = []struct {
	own      pdpb.Own
	from, to string
}{}

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
