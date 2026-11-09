package pdgen

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// Former is a name an entity had, as what an earlier build wrote down under it
// (`(payday.entity).formerly`).
type Former struct {
	// FullName is its message's full name then, in the entity's package --
	// what the identifiers' registry knew it by, and what a trail's patch
	// names.
	FullName protoreflect.FullName

	// Name is the word a person wrote it as then: a slug's, a configuration
	// file's, a chunk of the trail's archive's label.
	Name string

	// Table is the table its rows were in, as protoc-gen-orm-ent named it: the
	// message's name in lower case, which [TableOf] says of an entity now.
	Table string
}

// TableOf is the table an entity's rows are in, as protoc-gen-orm-ent names
// it: its message's name in lower case. Written out a second time here: the
// name is protoc-gen-orm-ent's, and what is moved has to agree with it.
func TableOf(e graph.Entity) string { return strings.ToLower(e.Name()) }

// identifier is a message name protobuf would take.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// readFormerly is what `opts` says `e` was called, refused where it says
// something no earlier build could have written down.
func readFormerly(e graph.Entity, opts *pdpb.Entity) ([]Former, error) {
	vs := opts.GetFormerly()
	if len(vs) == 0 {
		return nil, nil
	}

	pkg := e.FullName().Parent()
	out := make([]Former, 0, len(vs))
	seen := map[string]bool{}
	for _, v := range vs {
		m := v.GetMessage()
		switch {
		case m == "":
			return nil, fmt.Errorf("formerly: a name it had says no `message`: it is the message's name then, such as %q", "Old"+e.Name())
		case !identifier.MatchString(m):
			return nil, fmt.Errorf("formerly: %q is no message's name; it is the name alone, in this package, such as %q", m, "Old"+e.Name())
		case m == e.Name():
			return nil, fmt.Errorf("formerly: %q is what it is called now", m)
		case seen[m]:
			return nil, fmt.Errorf("formerly: %q is said twice", m)
		}
		seen[m] = true

		out = append(out, Former{
			FullName: pkg.Append(protoreflect.Name(m)),
			Name:     pdid.Name(v.GetName(), m),
			Table:    strings.ToLower(m),
		})
	}

	return out, nil
}

// checkFormerly refuses a name somebody had that somebody else has, or had.
//
// A former name is read back as the domain of the entity that had it, and what
// it is read back from was written by a build that meant that entity: a chunk
// of the archive labelled with its word, a table of its rows. So a word, a
// full name or a table two entities share would be one of them read as the
// other -- a table moved onto another entity's, an archive's rows checked as
// another kind's. It is the domain number's rule: chosen once, and never given
// to something else after the entity that had it is gone. What an entity has
// now and had before is its own, which is the one case a name may be both:
// an entity whose word stayed what it was.
func (s *Schema) checkFormerly() error {
	type owner struct {
		e   *Entity
		now bool
	}

	names := map[string]owner{}
	full := map[protoreflect.FullName]owner{}
	tables := map[string]owner{}
	for _, v := range s.Entities {
		names[v.Name] = owner{v, true}
		full[v.FullName()] = owner{v, true}
		tables[TableOf(v.Entity)] = owner{v, true}
	}

	say := func(o owner) string {
		if o.now {
			return "is"
		}
		return "was"
	}
	claim := func(m map[string]owner, k, what string, v *Entity) error {
		if o, ok := m[k]; ok && o.e != v {
			return fmt.Errorf("%s: formerly: %s %q, which %s %s too; a name is never given to another entity, as a domain number is not",
				v.FullName(), what, k, o.e.FullName(), say(o))
		}
		if _, ok := m[k]; !ok {
			m[k] = owner{v, false}
		}

		return nil
	}

	for _, v := range s.Entities {
		for _, f := range v.Formerly {
			if o, ok := full[f.FullName]; ok && o.e != v {
				return fmt.Errorf("%s: formerly: %s, which %s %s too; a name is never given to another entity, as a domain number is not",
					v.FullName(), f.FullName, o.e.FullName(), say(o))
			}
			if _, ok := full[f.FullName]; !ok {
				full[f.FullName] = owner{v, false}
			}
			if err := claim(names, f.Name, "written", v); err != nil {
				return err
			}
			if err := claim(tables, f.Table, "in the table", v); err != nil {
				return err
			}
		}
	}

	return nil
}

// TableRename is one table an earlier build made under the name its entity
// had then: its name then, and its name now.
type TableRename struct {
	From, To string
}

// TableRenames are the tables of the entities of `s` that an earlier build
// made under another name, in the order the entities are generated and each
// entity's names in the order it says them. A name whose table is the one it
// has now -- the same letters in another case -- moved nothing.
func TableRenames(s *Schema) []TableRename {
	var out []TableRename
	for _, v := range s.Sorted() {
		to := TableOf(v.Entity)
		for _, f := range v.Formerly {
			if f.Table == to {
				continue
			}
			out = append(out, TableRename{From: f.Table, To: to})
		}
	}

	return out
}

// EmitTableRenames writes [TableRenames], for `rename.Tables` to move before a
// migration -- and before `rename.Columns` and `rename.Indexes`, which look for
// a column and an index on its table by the name the table has now.
//
// A migration finds a table by its name and drops none, so for an entity that
// was renamed it makes the new table, empty, and the rows stay in the old one,
// read by nothing. Moved first, the table is found with its rows.
//
// Written for every app, empty for one that renamed nothing, so that an app's
// `Migrate` names it whatever the app declares, as [EmitColumnRenames] is.
func EmitTableRenames(g *protogen.GeneratedFile, s *Schema) {
	g.P("// TableRenames are the tables of this schema's entities that an earlier")
	g.P("// build made under the name the entity had then (`(payday.entity).formerly`).")
	g.P("// Hand them to `rename.Tables` first, before `rename.Columns`, `rename.Indexes`")
	g.P("// and the migration, which otherwise makes each again, empty, beside the one")
	g.P("// with the rows.")
	g.P("var TableRenames = []", pkgRename.Ident("Table"), "{")
	for _, v := range TableRenames(s) {
		g.P("	{From: ", strconv.Quote(v.From), ", To: ", strconv.Quote(v.To), "},")
	}
	g.P("}")
	g.P("")
}
