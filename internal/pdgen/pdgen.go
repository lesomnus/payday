// Package pdgen reads what a schema declared to payday and writes out what
// follows from it.
//
// Three files come out of one declaration:
//
//   - the domain of each entity, as a constant and as a registration, so that
//     `pdid` can say what an identifier names and a slug can be read back;
//   - a [Minter] that stamps every new row with the domain of the entity it
//     belongs to and refuses one that was handed the wrong kind;
//   - a [Scope] -- the wall -- that narrows every read of a tenanted entity to
//     the tenants the caller may see.
//
// The wall is the reason this exists. Written by hand it is a method per
// entity, and an entity added later gets no method: the app goes on compiling
// and the new rows are outside the wall, which is a leak that nothing reports.
// Declared, it is a line in the schema and a generator that **refuses to
// generate** what does not say it. The decision is still the app's; what
// changes is that forgetting to make it stops being possible.
package pdgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdpb"
)

// Entity is one entity of the schema, with what the schema declared to payday
// beside what it declared to `orm`.
type Entity struct {
	graph.Entity

	// Opts is the `payday.entity` option, and is never nil by the time
	// anything reads it: [Read] refuses an entity that has none.
	Opts *pdpb.Entity

	// Domain is what identifiers of this entity carry.
	Domain uint8

	// Name is what a person writes this domain as.
	Name string

	// Via is the path of edges that reaches the tenant, empty for an entity
	// that is the tenant, is not behind the wall, or names its tenant with a
	// column instead.
	Via []string

	// Field is the column holding the tenant's identifier, for a row that
	// names one without an edge to it. Empty when [Entity.Via] says how.
	Field string

	// IsTenant says this entity is the one a wall is made of.
	IsTenant bool

	// IsGlobal says rows of this entity are not behind the wall, which is a
	// thing the schema had to say out loud.
	IsGlobal bool
}

// Schema is every entity of one generation, in a stable order.
type Schema struct {
	Entities []*Entity

	// Tenant is the entity a wall is made of, and nil for a schema that has
	// none. A schema with a tenanted entity and no tenant does not get this
	// far; see [Read].
	Tenant *Entity
}

// Read gathers what the schema declared and refuses what it did not.
//
// Everything it refuses is something that fails quietly when it is left to be
// noticed later: an entity with no domain would hand out identifiers that say
// nothing, two entities sharing one would make an identifier lie about what it
// names, and an entity that says nothing about tenancy would sit outside the
// wall while everything went on compiling.
//
// So they are refused here, which is the earliest and cheapest place any of
// them can be: before a line of code exists to be wrong.
func Read(g *graph.Graph, files []*protogen.File) (*Schema, error) {
	s := &Schema{}

	// By file and then by declaration, so that the output does not move about
	// between runs for reasons nobody asked for.
	for _, f := range files {
		if !f.Generate {
			continue
		}
		for _, m := range f.Messages {
			e, ok := g.Entities[m.Desc.FullName()]
			if !ok {
				continue
			}

			v, err := read(e, m)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", m.Desc.FullName(), err)
			}

			s.Entities = append(s.Entities, v)
		}
	}
	if len(s.Entities) == 0 {
		return s, nil
	}

	if err := s.check(); err != nil {
		return nil, err
	}
	if err := CheckOverlay(s); err != nil {
		return nil, err
	}

	return s, nil
}

func read(e graph.Entity, m *protogen.Message) (*Entity, error) {
	opts, _ := proto.GetExtension(m.Desc.Options(), pdpb.E_Entity).(*pdpb.Entity)
	if opts == nil {
		return nil, fmt.Errorf(
			"no (payday.entity) option: every entity has to say what its identifiers "+
				"name and whether its rows are behind the tenant wall\n\n"+
				"    option (payday.entity) = {domain: %d, tenanted: {via: \"tenant\"}};",
			suggestDomain(e))
	}

	d := opts.GetDomain()
	switch {
	case d == 0:
		return nil, fmt.Errorf("domain: 0 is what an identifier nothing registered reads as, so nothing may hold it")
	case d > 255:
		return nil, fmt.Errorf("domain: %d does not fit in the one byte an identifier carries; it must be 1..255", d)
	}

	v := &Entity{
		Entity: e,
		Opts:   opts,
		Domain: uint8(d),
		Name:   opts.GetName(),
	}
	if v.Name == "" {
		v.Name = kebab(string(e.FullName().Name()))
	}

	switch {
	case opts.HasTenant():
		v.IsTenant = true

	case opts.HasGlobal():
		v.IsGlobal = true

	case opts.HasTenanted():
		t := opts.GetTenanted()
		switch {
		case t.GetVia() != "" && t.GetField() != "":
			return nil, fmt.Errorf(
				"tenanted: says both `via: %q` and `field: %q`, and they are not two ways of "+
					"writing one thing: an edge says the tenant is still there, a column says "+
					"only what its identifier was",
				t.GetVia(), t.GetField())

		case t.GetField() != "":
			v.Field = t.GetField()

		case t.GetVia() != "":
			v.Via = strings.Split(t.GetVia(), ".")

		default:
			v.Via = []string{"tenant"}
		}

	default:
		return nil, fmt.Errorf(
			"no tenancy: an entity is behind the tenant wall or it is not, and which one " +
				"cannot be guessed -- guessing wrong in one direction hides every row and in " +
				"the other shows every row to everybody, and only the first of those is noticed\n\n" +
				"    tenanted: {via: \"tenant\"}   rows belong to a tenant, reached by that edge\n" +
				"    tenant: {}                  this entity is the tenant\n" +
				"    global: {}                  not behind the wall, and said so on purpose")
	}

	return v, nil
}

// check is what one entity cannot know about itself.
func (s *Schema) check() error {
	byDomain := map[uint8]*Entity{}
	byName := map[string]*Entity{}

	for _, v := range s.Entities {
		if u, ok := byDomain[v.Domain]; ok {
			return fmt.Errorf(
				"%s and %s both declare domain %d; a domain outlives the row it named, "+
					"so two entities sharing one makes an old identifier lie about what it was",
				u.FullName(), v.FullName(), v.Domain)
		}
		if u, ok := byName[v.Name]; ok {
			return fmt.Errorf("%s and %s are both written %q", u.FullName(), v.FullName(), v.Name)
		}

		byDomain[v.Domain] = v
		byName[v.Name] = v

		if v.IsTenant {
			if s.Tenant != nil {
				return fmt.Errorf(
					"%s and %s both say they are the tenant; a wall is made of one thing",
					s.Tenant.FullName(), v.FullName())
			}
			s.Tenant = v
		}
	}

	for _, v := range s.Entities {
		if len(v.Via) == 0 && v.Field == "" {
			continue
		}
		if s.Tenant == nil {
			return fmt.Errorf(
				"%s is tenanted but nothing in this schema says it is the tenant; "+
					"one entity has to declare `tenant: {}`", v.FullName())
		}
		if v.Field != "" {
			if err := s.checkField(v); err != nil {
				return fmt.Errorf("%s: field: %w", v.FullName(), err)
			}
			continue
		}
		if err := s.checkVia(v); err != nil {
			return fmt.Errorf("%s: via: %w", v.FullName(), err)
		}
	}

	return nil
}

// checkVia walks the declared path and refuses one that does not arrive at the
// tenant, which is a wall with a hole in it written as a typo.
func (s *Schema) checkVia(v *Entity) error {
	at := v.Entity
	for i, step := range v.Via {
		e, ok := edge(at, step)
		if !ok {
			return fmt.Errorf("%s has no edge %q", at.FullName(), step)
		}
		at = e.Target()

		if i == len(v.Via)-1 && at.FullName() != s.Tenant.FullName() {
			return fmt.Errorf(
				"%q arrives at %s, and the tenant is %s",
				strings.Join(v.Via, "."), at.FullName(), s.Tenant.FullName())
		}
	}

	return nil
}

// checkField refuses a column that is not there, or one that could not hold an
// identifier if it were.
func (s *Schema) checkField(v *Entity) error {
	for p := range v.Props() {
		if p.Name() != v.Field {
			continue
		}

		f, ok := p.(graph.Field)
		if !ok {
			return fmt.Errorf("%q is an edge; say `via` for those", v.Field)
		}
		if f.Type() != ormpb.Type_TYPE_UUID {
			return fmt.Errorf("%q is %s, and a tenant is named by a uuid", v.Field, f.Type())
		}

		return nil
	}

	return fmt.Errorf("%s has no field %q", v.FullName(), v.Field)
}

func edge(e graph.Entity, name string) (graph.Edge, bool) {
	for v := range e.Edges() {
		if v.Name() == name {
			return v, true
		}
	}

	return nil, false
}

// suggestDomain answers with a number nothing is likely to be using, for the
// message that tells somebody what to write. It is a hint and not an
// allocation: two entities that take the hint at once are caught by [check].
func suggestDomain(e graph.Entity) uint32 {
	var h uint32 = 2166136261
	for _, c := range []byte(e.FullName()) {
		h ^= uint32(c)
		h *= 16777619
	}

	return h%255 + 1
}

// kebab is what a message name is written as when the schema did not say.
func kebab(v string) string {
	var b strings.Builder
	for i, r := range v {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}

	return b.String()
}

// Sorted answers with the entities by full name, for output that does not
// depend on which file something happened to be declared in.
func (s *Schema) Sorted() []*Entity {
	vs := make([]*Entity, len(s.Entities))
	copy(vs, s.Entities)
	sort.Slice(vs, func(i, j int) bool { return vs[i].FullName() < vs[j].FullName() })

	return vs
}

// GoName is the identifier this entity is known by in Go.
func (e *Entity) GoName() string { return string(e.FullName().Name()) }

// EntPkg is the name ent gives the package of predicates for this entity,
// which is its type name folded down.
func (e *Entity) EntPkg() string { return strings.ToLower(e.GoName()) }

var _ protoreflect.FullName = protoreflect.FullName("")
