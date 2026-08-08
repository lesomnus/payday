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
	"strconv"
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

	// List is how this entity is read a page at a time, and nil for one that
	// answers no List.
	List *List

	// Watch says this entity is read as it changes, which is the List it
	// declared over and over.
	Watch bool

	// Alias says this entity has the string field a person writes it as, so
	// every write of one goes through [slug.ParseAlias] on the way in.
	//
	// It is found rather than declared. A field called `alias` is what a slug
	// resolves against and what `@acme/arm-01` is made of -- there is no second
	// meaning for the name in a payday schema, so asking the schema to say
	// again what it already said would be a thing to forget.
	Alias bool
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
	if err := s.checkAlias(); err != nil {
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

	for f := range e.Fields() {
		if f.Name() == "alias" && f.Type() == ormpb.Type_TYPE_STRING {
			v.Alias = true
		}
	}

	if err := readList(v, opts.GetList()); err != nil {
		return nil, err
	}
	if opts.HasWatch() {
		if v.List == nil {
			return nil, fmt.Errorf(
				"watch: needs a `list`. A watch is that list over and over, so without one " +
					"there is no order to read in, nothing for a filter to be about, and no cap " +
					"on the first message")
		}
		if !hasRef(v.List) {
			// A watch names the rows it is about, and a filter that cannot name
			// one by reference gives it nothing to name them with. Without this
			// the generated stream refers to a field the filter message does
			// not have, which is a **compile** error in the app -- late, and in
			// generated code somebody then has to read.
			//
			// It is refused rather than added silently, because `by:` is the
			// list of things a caller may filter on and quietly putting one
			// more in it is the generator deciding what an API offers.
			return nil, fmt.Errorf(
				"watch: needs `ref` among the list's `by:`, and this one has %s.\n\n"+
					"    list: {by: [\"ref\"%s]}\n\n"+
					"A watch says which rows it is about, and a reference is how one is named. "+
					"A list can be filtered by other things and a watch cannot be filtered by "+
					"nothing",
				byNames(v.List), moreBy(v.List))
		}
		if !e.HasVersionField() {
			// The one refusal that is about what happens on the **client**.
			//
			// A watch sends state rather than deltas, so a subscriber keeps
			// what it was last told about a row and replaces it. Two answers
			// about one row can arrive out of order -- a snapshot racing an
			// event, a reconnection replaying, an outbox draining late -- and
			// without something to compare, the replacement is unconditional:
			// a stale answer overwrites a fresh one and the screen is wrong
			// with nothing having failed.
			//
			// It cannot be worked around by the client either. Nothing outside
			// the row says which of two copies of it is newer.
			return nil, fmt.Errorf(
				"watch: needs a version field, and this entity has none.\n\n"+
					"    google.protobuf.Timestamp date_updated = %d [(orm.field) = {version: {}}];\n\n"+
					"A watch sends state, so a client replaces what it holds -- and two answers "+
					"about one row can arrive out of order. Without something to compare, a "+
					"stale one overwrites a fresh one and nothing anywhere fails",
				suggestVersion(e))
		}

		v.Watch = true
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

// hasRef reports whether a list can be filtered by a reference.
func hasRef(l *List) bool {
	if l == nil {
		return false
	}
	for _, v := range l.By {
		if v.Ref {
			return true
		}
	}

	return false
}

// byNames is what a list can be filtered by, for the message that says it is
// not enough.
func byNames(l *List) string {
	if l == nil || len(l.By) == 0 {
		return "none"
	}

	vs := make([]string, len(l.By))
	for i, v := range l.By {
		if v.Ref {
			vs[i] = "ref"
			continue
		}

		vs[i] = strconv.Quote(v.Field)
	}

	return strings.Join(vs, ", ")
}

// moreBy is the rest of the `by:` a schema already had, so that the suggestion
// is the whole line rather than a line that throws the others away.
func moreBy(l *List) string {
	if l == nil || len(l.By) == 0 {
		return ""
	}

	b := &strings.Builder{}
	for _, v := range l.By {
		if v.Ref {
			continue
		}

		fmt.Fprintf(b, ", %q", v.Field)
	}

	return b.String()
}

// suggestVersion is a field number that is free, for the message that tells an
// entity to declare a version.
//
// It is a suggestion and not a rule: 13 is where payday's own entities put it,
// which makes an app's schema read like payday's, and anything free will do.
func suggestVersion(e graph.Entity) int {
	taken := map[int]bool{}
	for f := range e.Fields() {
		taken[int(f.Number())] = true
	}
	for n := 13; n < 536870911; n++ {
		if !taken[n] {
			return n
		}
	}

	return 13
}

// checkAlias refuses a field called `alias` that is not text.
//
// It is refused rather than skipped, and that is the only reason this is a
// check at all: a non-string `alias` gets no folding and no rule, and the way
// that is found out is `@acme/arm-01` naming nothing months later. There is no
// second meaning for the name in a payday schema, so a field that holds
// something else under it is a mistake and not a choice.
//
// It runs after the overlay guard so that an app that redeclared payday's own
// `alias` is told which rule it broke -- that the number is payday's -- rather
// than the consequence of having broken it.
func (s *Schema) checkAlias() error {
	for _, v := range s.Entities {
		for f := range v.Fields() {
			if f.Name() != "alias" || f.Type() == ormpb.Type_TYPE_STRING {
				continue
			}

			return fmt.Errorf("%s: alias: is a %s, and an alias is the text a person writes a row as",
				v.FullName(), f.Type())
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

// List is what a schema said about reading this entity a page at a time, and
// is nil for an entity that answers no List.
type List struct {
	Order   []Order
	With    []string
	By      []By
	Size    int
	Max     int
	Filters int
}

// Order is one column of a list's order.
type Order struct {
	Field string
	Desc  bool
}

// By is one thing a filter may be about.
type By struct {
	// Ref says this is the reference that names a row, which is what the
	// generated servers already know how to turn into a predicate.
	Ref bool

	// Field is the column compared for equality, empty when Ref.
	Field string

	// Type is what that column holds, so that the generated code knows how to
	// read it out of the request.
	Type ormpb.Type
}

// readList reads the list declaration and refuses what would be wrong.
//
// Two of these are the whole reason a List is worth generating rather than
// copying. An order that does not end in the key is a cursor that cannot tell
// two rows apart, and the page after them repeats one or skips one -- which
// shows up as a row a caller never saw, weeks later, under load. A page with no
// cap is an answer with no cap.
func readList(e *Entity, opts *pdpb.Entity_List) error {
	if opts == nil {
		return nil
	}

	v := &List{
		With:    opts.GetWith(),
		Size:    int(opts.GetSize()),
		Max:     int(opts.GetMax()),
		Filters: int(opts.GetFilters()),
	}
	if v.Size == 0 {
		v.Size = 50
	}
	if v.Filters == 0 {
		v.Filters = 32
	}
	if v.Max == 0 {
		return fmt.Errorf("list: `max` is required; a page with no cap is an answer with no cap, " +
			"and the request that finds that out is the one that reads the whole table")
	}
	if v.Max < v.Size {
		return fmt.Errorf("list: `max` is %d and `size` is %d, so what a request gets by asking for "+
			"nothing is more than it may have", v.Max, v.Size)
	}

	key := e.Key().Name()
	for _, o := range opts.GetOrder() {
		if _, ok := field(e.Entity, o.GetField()); !ok {
			return fmt.Errorf("list: order: %s has no field %q", e.FullName(), o.GetField())
		}
		v.Order = append(v.Order, Order{Field: o.GetField(), Desc: o.GetDesc()})
	}
	switch {
	case len(v.Order) == 0:
		// The key alone, which is always correct and rarely what somebody
		// wanted to read.
		v.Order = []Order{{Field: key}}
	case v.Order[len(v.Order)-1].Field != key:
		return fmt.Errorf(
			"list: order ends in %q and has to end in the key, %q.\n"+
				"a cursor cannot tell apart two rows equal in every column of the order, so the page "+
				"after the first of them repeats the second or skips it -- and rows written by one "+
				"request are stamped a moment apart at best",
			v.Order[len(v.Order)-1].Field, key)
	}

	for _, name := range opts.GetBy() {
		if name == "ref" {
			v.By = append(v.By, By{Ref: true})
			continue
		}

		f, ok := field(e.Entity, name)
		if !ok {
			return fmt.Errorf("list: by: %s has no field %q", e.FullName(), name)
		}
		if protoTypeOf(f.Type()) == "" {
			return fmt.Errorf(
				"list: by: %q is %s, and a filter compares for equality -- which is not "+
					"something this generator writes for that. An RPC somebody wrote can",
				name, f.Type())
		}
		v.By = append(v.By, By{Field: name, Type: f.Type()})
	}

	for _, name := range v.With {
		if _, ok := edge(e.Entity, name); !ok {
			return fmt.Errorf("list: with: %s has no edge %q", e.FullName(), name)
		}
	}

	e.List = v
	return nil
}

func field(e graph.Entity, name string) (graph.Field, bool) {
	for p := range e.Props() {
		f, ok := p.(graph.Field)
		if ok && f.Name() == name {
			return f, true
		}
	}

	return nil, false
}

// checkListIndex warns about an order no index covers.
//
// It is the most valuable check here and the only one that is a warning rather
// than a refusal. An order the database cannot walk is a full scan and a sort,
// on the table a List is pointed at -- which is the one that grows. go-app
// declared the index its trail is read by and wrote the reason in a comment
// beside it; a comment is not something the next person reads.
//
// A warning and not a refusal because a small table is a real thing. A hundred
// rows of configuration do not need an index and refusing to generate for them
// would be a framework insisting on a cost nobody is paying.
//
// The order is covered when some index begins with it: an index on (a, b, c)
// serves an order of (a) and (a, b), and not one of (b) or (b, a).
func checkListIndex(e *Entity) string {
	if e.List == nil || len(e.List.Order) == 0 {
		return ""
	}
	// The key alone is the primary index, which every table has.
	if len(e.List.Order) == 1 && e.List.Order[0].Field == e.Key().Name() {
		return ""
	}

	want := make([]string, len(e.List.Order))
	for i, o := range e.List.Order {
		want[i] = o.Field
	}

	for idx := range e.Indexes() {
		var has []string
		for p := range idx.Props() {
			has = append(has, p.Name())
		}
		if len(has) < len(want) {
			continue
		}

		covers := true
		for i := range want {
			if has[i] != want[i] {
				covers = false
				break
			}
		}
		if covers {
			return ""
		}
	}

	return fmt.Sprintf(
		"%s: list: no index begins with (%s), so reading a page scans the table and sorts what it finds.\n"+
			"  that cost is on the table a List is pointed at, which is the one that grows.\n"+
			"  declare it beside the entity, or say out loud that this table stays small:\n\n"+
			"    indexes: [{name: %q, refs: [%s]}]",
		e.FullName(), strings.Join(want, ", "), strings.Join(want, "_"), refsOf(e, want))
}

// refsOf writes the index the check is asking for, so that the answer to it is
// something to paste rather than something to look up.
func refsOf(e *Entity, names []string) string {
	vs := make([]string, 0, len(names))
	for _, name := range names {
		for p := range e.Props() {
			if p.Name() == name {
				vs = append(vs, fmt.Sprintf("{name: %q, number: %d}", name, p.Number()))
				break
			}
		}
	}

	return strings.Join(vs, ", ")
}

// Warnings is what a schema does not fail for and should still hear about.
func Warnings(s *Schema) []string {
	vs := []string{}
	for _, v := range s.Sorted() {
		if w := checkListIndex(v); w != "" {
			vs = append(vs, w)
		}
	}

	return vs
}
