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

	// SecretNumbers are the same fields by number, which is what a patch
	// document names them by -- see the recorder's `hidden`. Kept beside the
	// names rather than looked up again, so the two cannot come to disagree
	// about which fields they are.
	SecretNumbers []uint32

	// Secrets are the fields declared `(payday.field).secret`: written, and
	// never answered with. Empty for nearly every entity.
	Secrets []string

	// Domain is what identifiers of this entity carry.
	Domain uint8

	// Name is what a person writes this domain as.
	Name string

	// Via is the path of edges that reaches the tenant, empty for an entity
	// that is the tenant, is not behind the wall, or names its tenant with a
	// column instead.
	Via []string

	// Fields are the columns holding a tenant's identifier, for a row that
	// names one without an edge to it. Empty when [Entity.Via] says how.
	//
	// More than one is OR: the row is behind the wall of **any** tenant it
	// names. That is the trail and nothing else -- one write has a tenant whose
	// row changed and a tenant whose actor made it, and both are parties to it.
	Columns []string

	// Stamp is a column on this row holding what [Entity.Via] reaches, written
	// by the server and refused to a caller. Empty for nearly every entity.
	//
	// What it buys is the wall: a `Via` of more than one step is a subquery on
	// every read, and a stamp makes it the same indexed comparison a direct
	// edge gets. It is safe to keep because it cannot go stale -- every edge on
	// `Via` is immutable, which [Schema.checkStamp] refuses a path without.
	Stamp string

	// Agrees are other paths from this row to a tenant that must reach the same
	// one, checked when the row is written. Each is split the way [Entity.Via]
	// is.
	//
	// Opt-in, and deliberately: payday cannot know which disagreements are
	// mistakes -- its own trail holds two tenants on purpose -- so a path
	// nobody named here is an ordinary edge and nothing is said about it.
	Agrees [][]string

	// Set is the edge at field 3 -- the set inside a tenant this row belongs
	// to -- and is empty for an entity that declared none.
	//
	// Field 3 is the app's, and the number is the declaration: there is no
	// option to write. See [Schema.Set] and `docs/SCHEMA.md` for why the slot
	// is a number rather than a name.
	Set string

	// Own says this is one of payday's own entities, and [pdpb.Own_OWN_UNSPECIFIED]
	// for everything an app writes.
	//
	// It is read instead of the full name, which is what used to identify them.
	// The name made the proto package payday's rather than the app's, and it did
	// so quietly: renaming it did not fail, it made the layers built out of
	// these entities **not be generated**. See [CheckOwn].
	Own pdpb.Own

	// IsSet says this entity is what the field 3s point at.
	IsSet bool

	// IsTenant says this entity is the one a wall is made of.
	IsTenant bool

	// IsGlobal says rows of this entity are not behind the wall, which is a
	// thing the schema had to say out loud.
	IsGlobal bool

	// IsHard says `Erase` loses the row, which is the other thing the schema
	// has to say out loud. Saying nothing is soft, and an entity that carries
	// no field to be softly erased by is refused; see [Schema.checkErase].
	IsHard bool

	// List is how this entity is read a page at a time, and nil for one that
	// answers no List.
	List *List

	// Watch says this entity is read as it changes, which is the List it
	// declared over and over.
	Watch bool

	// assumed says nothing in the schema declared tenancy, so it was taken to
	// be behind the wall by the edge called `tenant`. It is carried only so
	// that a refusal can say the path was assumed rather than written.
	assumed bool

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

	// Set is the second axis, and nil for an app that declared none -- which
	// is most of them.
	//
	// It is whatever every field 3 in the schema points at. payday does not
	// name it: the same slot is a Site, a Workspace, a Project or a Cell
	// depending on the app, and those are the same idea in different words.
	// What payday fixes is the **shape** -- an edge, at 3, to one entity, all
	// the way through -- because a slot whose shape is not fixed is a slot a
	// generic reader can never be taught to ask about, and then it may as well
	// have been 8.
	Set *Entity
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
	if err := s.checkHeader(); err != nil {
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

	// The fields this entity says are written and never answered with. See
	// `EmitSecret`, and `payday.Field`.
	var secrets []string
	var secretNumbers []uint32
	for prop := range e.Props() {
		f, _ := proto.GetExtension(prop.Descriptor().Options(), pdpb.E_Field).(*pdpb.Field)
		if f.GetSecret() {
			secrets = append(secrets, prop.Name())
			secretNumbers = append(secretNumbers, uint32(prop.Descriptor().Number()))
		}
	}

	v := &Entity{
		Entity:        e,
		Opts:          opts,
		Secrets:       secrets,
		SecretNumbers: secretNumbers,
		Domain:        uint8(d),
		Name:          opts.GetName(),
		Own:           opts.GetOwn(),
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
		case t.GetVia() != "" && len(t.GetField()) > 0:
			return nil, fmt.Errorf(
				"tenanted: says both `via: %q` and `field: %q`, and they are not two ways of "+
					"writing one thing: an edge says the tenant is still there, a column says "+
					"only what its identifier was",
				t.GetVia(), t.GetField()[0])

		case len(t.GetField()) > 0:
			v.Columns = t.GetField()

		case t.GetVia() != "":
			v.Via = strings.Split(t.GetVia(), ".")

		default:
			v.Via = []string{"tenant"}
		}

		// A stamp is what `via` reaches, kept on this row. There is nothing to
		// derive it from without one, and the column form already **is** a
		// column -- saying both would be asking for two of the same thing.
		if s := t.GetStamp(); s != "" {
			if len(v.Columns) > 0 {
				return nil, fmt.Errorf(
					"tenanted: says both `field: %q` and `stamp: %q`, and a stamp is a column "+
						"payday fills from an edge -- there is no edge here to fill it from",
					v.Columns[0], s)
			}

			v.Stamp = s
		}

		if len(t.GetAgrees()) > 0 && len(v.Columns) > 0 {
			// The column form is the trail, which holds two tenants **because**
			// they differ. A rule that they agree is the opposite of what it is
			// for.
			return nil, fmt.Errorf(
				"tenanted: says both `field:` and `agrees:`, and the column form is the one " +
					"that names more than one tenant on purpose -- there is nothing here for " +
					"them to agree with")
		}

		for _, a := range t.GetAgrees() {
			v.Agrees = append(v.Agrees, strings.Split(a, "."))
		}

	default:
		// Nothing said, which means behind the wall by the edge called `tenant`.
		//
		// # Why there is a default at all, and why it is this one
		//
		// Getting tenancy wrong fails in two directions and they are not alike:
		// assuming a wall hides every row, and assuming none shows every row to
		// everybody. The first is noticed within minutes -- the screen is empty
		// -- and the second is noticed by whoever it happens to.
		//
		// So the safe thing to assume is the loud one. And it cannot even be
		// wrong quietly: an entity with a `tenant` edge to the tenant gets the
		// right wall, one whose `tenant` edge goes somewhere else is refused by
		// [Schema.checkVia], and one with no such edge is refused below. The
		// default either produces the correct predicate or a refusal, never a
		// wall that narrows to the wrong rows.
		//
		// What that buys is the shape a rule should have: **the dangerous case
		// is the one written down.** An entity that is not behind the wall says
		// `global: {}` and a reader can find every one of them by searching for
		// it; the ordinary case says nothing, because saying it on six entities
		// out of eight is boilerplate rather than a decision.
		v.Via = []string{"tenant"}
		v.assumed = true
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
					"    list: {by: [{name: \"ref\"}%s]}\n\n"+
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
	byOwn := map[pdpb.Own]*Entity{}

	for _, v := range s.Entities {
		// Before anything about tenancy, because it is about the fields
		// themselves and applies to an entity whether or not it is walled.
		if err := s.checkPresence(v); err != nil {
			return err
		}

		if v.Own != pdpb.Own_OWN_UNSPECIFIED {
			// Two of them would make [Schema.Own] answer with whichever came
			// first, which is the schema deciding a trust boundary by its own
			// declaration order.
			if u, ok := byOwn[v.Own]; ok {
				return fmt.Errorf(
					"%s and %s both say `own: %s`, and payday ships one of each; "+
						"an app writes no `own:` at all -- it is copied in with the entity",
					u.FullName(), v.FullName(), v.Own)
			}
			byOwn[v.Own] = v
		}
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

	if err := s.readSets(); err != nil {
		return err
	}
	if err := s.checkErase(); err != nil {
		return err
	}

	for _, v := range s.Entities {
		if len(v.Via) == 0 && len(v.Columns) == 0 {
			continue
		}
		if s.Tenant == nil {
			return fmt.Errorf(
				"%s is tenanted but nothing in this schema says it is the tenant; "+
					"one entity has to declare `tenant: {}`", v.FullName())
		}
		if len(v.Columns) > 0 {
			if err := s.checkField(v); err != nil {
				return fmt.Errorf("%s: field: %w", v.FullName(), err)
			}
			continue
		}
		if err := s.checkVia(v); err != nil {
			return fmt.Errorf("%s: via: %w", v.FullName(), err)
		}
		if err := s.checkStamp(v); err != nil {
			return fmt.Errorf("%s: %w", v.FullName(), err)
		}
		if err := s.checkAgrees(v); err != nil {
			return fmt.Errorf("%s: %w", v.FullName(), err)
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

		name := v.Field
		if v.Edge != "" {
			name = v.Edge
		}

		fmt.Fprintf(b, ", {name: %q}", name)
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
	// A refusal about a path nobody wrote reads as nonsense unless it says so.
	assumed := ""
	if v.assumed {
		assumed = "\n\nNothing here declared tenancy, so it was taken to be behind the wall " +
			"by the edge called \"tenant\" -- which is the assumption that fails loudly rather " +
			"than the one that leaks. Say `global: {}` if these rows are not behind it, or " +
			"`tenanted: {via: \"...\"}` if they reach a tenant some other way."
	}

	at := v.Entity
	for i, step := range v.Via {
		e, ok := edge(at, step)
		if !ok {
			return fmt.Errorf("%s has no edge %q%s", at.FullName(), step, assumed)
		}
		at = e.Target()

		if i == len(v.Via)-1 && at.FullName() != s.Tenant.FullName() {
			return fmt.Errorf(
				"%q arrives at %s, and the tenant is %s%s",
				strings.Join(v.Via, "."), at.FullName(), s.Tenant.FullName(), assumed)
		}
	}

	return nil
}

// checkStamp refuses a stamp that cannot be kept true.
//
// Two rules, and the second is the one worth having.
//
// The column has to be there and has to be able to hold an identifier, which is
// [Schema.checkOneField]'s job and the same test a `field` gets.
//
// And **every edge on the path has to be immutable**. A stamp is decided once,
// when the row is written; if any step of the path could be repointed
// afterwards the column would go on saying where the row used to belong, and
// the wall reads the column. Refusing the schema is cheaper than a check on
// every write, and it costs nothing today: every tenancy path in payday, in
// this repository's own test app and in the apps built on it already runs
// through immutable edges. What it prevents is the next one that does not.
func (s *Schema) checkStamp(v *Entity) error {
	if v.Stamp == "" {
		return nil
	}
	if len(v.Via) == 0 {
		return fmt.Errorf("tenanted: `stamp: %q` with no `via`, and nothing to fill it from", v.Stamp)
	}

	if len(v.Via) == 1 {
		// One hop is already a column. `collapse` reads the foreign key rather
		// than walking it, so the wall on a direct edge is the comparison a
		// stamp would add -- and the stamp would be a second copy of the same
		// identifier, kept in step with nothing to keep it in step with.
		return fmt.Errorf(
			"tenanted: `stamp: %q` over `via: %q`, which is one hop and already a column: "+
				"the wall reads the foreign key rather than walking it, so there is nothing "+
				"here for a stamp to save",
			v.Stamp, v.Via[0])
	}

	if err := s.checkOneField(v, v.Stamp); err != nil {
		return fmt.Errorf("tenanted: stamp: %w", err)
	}

	// And an index it leads, or the wall is a scan.
	//
	// This is the rule that makes the feature honest rather than merely
	// compiling. What a stamp replaces is `HasHolderWith(holder.TenantIDIn(...))`
	// -- a semi-join over two indexed columns -- and `tenant_id IN (...)` on an
	// unindexed column is not faster than that, it is a sequential scan of the
	// table the wall is read from most.
	//
	// **Leads**, not merely appears in. A composite index answers a predicate
	// on its first column and not on its third, so an index of
	// (provider, subject, tenant_id) would satisfy a rule about membership and
	// none about speed.
	led := false
	for x := range v.Entity.Indexes() {
		for p := range x.Props() {
			led = p.Name() == v.Stamp
			break
		}
		if led {
			break
		}
	}
	if !led {
		return fmt.Errorf(
			"tenanted: `stamp: %q` is not the first column of any index, so the wall it is "+
				"read by scans -- which is not faster than the join it replaced. Declare one "+
				"under `(orm.message).indexes` that begins with %q; a unique index it already "+
				"leads counts",
			v.Stamp, v.Stamp)
	}

	at := v.Entity
	for _, step := range v.Via {
		e, ok := edge(at, step)
		if !ok {
			// checkVia says this better; it runs first.
			return nil
		}
		if e.IsNullable() {
			// A row with no edge has no tenant to stamp, and an empty stamp is
			// a row the wall hides from everybody. Today that entity is
			// invisible anyway -- `HasRobotWith(...)` matches nothing when
			// there is no robot -- so this refuses a declaration rather than
			// changing what a row does.
			return fmt.Errorf(
				"tenanted: `stamp: %q` reaches the tenant through %s.%s, which is nullable -- "+
					"a row with no edge has no tenant to stamp, and an empty stamp is a row "+
					"nobody can read",
				v.Stamp, at.FullName(), step)
		}
		if !e.IsImmutable() {
			return fmt.Errorf(
				"tenanted: `stamp: %q` reaches the tenant through %s.%s, which is not immutable -- "+
					"a stamp is written once and the wall reads it, so a step that can be repointed "+
					"leaves the column saying where the row used to belong. Mark it "+
					"`[(orm.edge) = {immutable: true}]` or drop the stamp",
				v.Stamp, at.FullName(), step)
		}

		at = e.Target()
	}

	return nil
}

// checkAgrees refuses a path that does not arrive at the tenant.
//
// The same walk `via` gets, for the same reason: a path that arrives somewhere
// else is not a second opinion about the tenant, it is a mistake that would be
// compared against one.
func (s *Schema) checkAgrees(v *Entity) error {
	for _, path := range v.Agrees {
		at := v.Entity
		for i, step := range path {
			e, ok := edge(at, step)
			if !ok {
				return fmt.Errorf("tenanted: agrees: %s has no edge %q", at.FullName(), step)
			}

			// Immutable for the same reason a `stamp`'s path is, arrived at
			// from the other side: the comparison happens when the row is
			// written, and a step that can be repointed afterwards makes it a
			// statement about a moment rather than an invariant. Checking on
			// every write instead would be a read per path per write, to hold
			// something the schema can simply not allow to move.
			if !e.IsImmutable() {
				return fmt.Errorf(
					"tenanted: agrees: %q goes through %s.%s, which is not immutable -- the "+
						"paths are compared when the row is written, so a step that can be "+
						"repointed makes the agreement true only until somebody moves it. Mark "+
						"it `[(orm.edge) = {immutable: true}]`",
					strings.Join(path, "."), at.FullName(), step)
			}

			at = e.Target()

			if i == len(path)-1 && at.FullName() != s.Tenant.FullName() {
				return fmt.Errorf(
					"tenanted: agrees: %q arrives at %s, and the tenant is %s",
					strings.Join(path, "."), at.FullName(), s.Tenant.FullName())
			}
		}
	}

	return nil
}

// checkField refuses a column that is not there, or one that could not hold an
// identifier if it were.
func (s *Schema) checkField(v *Entity) error {
	for _, name := range v.Columns {
		if err := s.checkOneField(v, name); err != nil {
			return err
		}
	}

	return nil
}

func (s *Schema) checkOneField(v *Entity, name string) error {
	for p := range v.Props() {
		if p.Name() != name {
			continue
		}

		f, ok := p.(graph.Field)
		if !ok {
			return fmt.Errorf("%q is an edge; say `via` for those", name)
		}
		if f.Type() != ormpb.Type_TYPE_UUID {
			return fmt.Errorf("%q is %s, and a tenant is named by a uuid", name, f.Type())
		}

		return nil
	}

	return fmt.Errorf("%s has no field %q", v.FullName(), name)
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

	// Field is the column compared for equality, empty when Ref or Edge.
	Field string

	// Type is what that column holds, so that the generated code knows how to
	// read it out of the request.
	Type ormpb.Type

	// Edge is the edge compared, empty otherwise, and Target is the entity it
	// points at, by its **full** name.
	//
	// It is a column comparison like any other: an edge is a foreign key, so
	// "in this tenant" is `WHERE tenant_id = ?` against an index and not a
	// join. What the filter carries is the target's **ref** rather than its
	// identifier, so a caller may name it the way they name it everywhere else
	// -- by id, or by the alias they typed.
	//
	// Full rather than short because an app may hold entities in more than one
	// package -- see `payday.App` -- and a filter naming `RobotRef` when the
	// robot is in another one is a file that does not compile. Where to shorten
	// it is the writer's to decide, since only the writer knows which file it
	// is writing into.
	Edge   string
	Target string
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

	for _, w := range opts.GetWith() {
		name, err := named(e.Entity, w, "with")
		if err != nil {
			return err
		}

		v.With = append(v.With, name)
	}

	key := e.Key().Name()
	for _, o := range opts.GetOrder() {
		name, err := named(e.Entity, o.GetField(), "order")
		if err != nil {
			return err
		}
		if _, ok := field(e.Entity, name); !ok {
			return fmt.Errorf("list: order: %s has no field %q", e.FullName(), name)
		}

		v.Order = append(v.Order, Order{Field: name, Desc: o.GetDesc()})
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

	for _, w := range opts.GetBy() {
		if w.GetName() == "ref" && w.GetNumber() == 0 {
			v.By = append(v.By, By{Ref: true})
			continue
		}

		name, err := named(e.Entity, w, "by")
		if err != nil {
			return err
		}

		if d, ok := edge(e.Entity, name); ok {
			// An edge, which is a foreign key and therefore a column. A filter
			// on one costs an indexed comparison, which is what makes "everyone
			// in this tenant" a declaration rather than a hand-written RPC.
			if d.IsList() {
				return fmt.Errorf(
					"list: by: %q is a one-to-many edge, and there is no column on this row "+
						"to compare -- which is a join, and a join wants an RPC somebody wrote",
					name)
			}

			v.By = append(v.By, By{
				Edge:   name,
				Target: string(d.Target().FullName()),
			})

			continue
		}

		f, ok := field(e.Entity, name)
		if !ok {
			return fmt.Errorf(
				"list: by: %s has no field or edge %q", e.FullName(), name)
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
// on the table a List is pointed at -- which is the one that grows. An earlier
// system declared the index its trail is read by and wrote the reason in a
// comment beside it; a comment is not something the next person reads.
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

// header is the shape a reflective reader expects of the fields every entity
// can have; see `payday/header`.
//
// It is matched by **name**. A number is what an overlay guard compares and
// what a wire format is made of; what a generic reader needs is only that two
// schemas calling something `name` mean the same thing by it.
var header = []struct {
	Name string
	Type ormpb.Type
	// At is where payday's own entities put it, and where every entity is held
	// to put it -- see [Schema.checkHeader] for why the number is part of the
	// rule and not a convention.
	At int
}{
	{"alias", ormpb.Type_TYPE_STRING, 4},
	{"name", ormpb.Type_TYPE_STRING, 5},
	{"desc", ormpb.Type_TYPE_STRING, 6},
}

// checkHeader refuses a header field that is not the kind a reader will look
// for.
//
// # Why the type is refused
//
// A reflective read matches on the name and then on the kind, so a `name` that
// is an int is a field that **silently reads as absent** -- a page falls back
// to the alias, or to the identifier, and nothing anywhere says why. That is
// the class payday refuses.
//
// # Why the number is refused too
//
// The read does not use numbers, so a `name` at 9 costs nothing today. What
// they buy is tomorrow: 4..7 are reserved so that payday can add a header field
// later without every app having spent that number on something else, and a
// reservation nothing enforces reserves nothing.
//
// # Why presence is not required
//
// payday's own `Audit` and `Outbox` have none of these, and their early fields
// are structural rather than descriptive -- a trail row's 4..7 are the trace,
// the action, the object and the patch. A rule that made every entity carry a
// name would be a rule payday exempts itself from twice on the first day, and
// that is the shape of a rule that is wrong.
func (s *Schema) checkHeader() error {
	for _, v := range s.Entities {
		for f := range v.Fields() {
			for _, want := range header {
				if string(f.Name()) != want.Name {
					continue
				}
				if f.Type() != want.Type {
					return fmt.Errorf(
						"%s: %s: is a %s, and everything that reads a row it has no type for "+
							"expects a %s.\n\n"+
							"    %s %s = %d;\n\n"+
							"It is refused rather than ignored because ignoring it is invisible: "+
							"a page looking for a name would find none and fall back, and nothing "+
							"would say why. Call it something else if it is not that",
						v.FullName(), want.Name, f.Type(), want.Type,
						protoTypeOf(want.Type), want.Name, want.At)
				}
				if int(f.Number()) != want.At {
					return fmt.Errorf(
						"%s: %s: is %d, and the header is at %d in every entity payday ships.\n\n"+
							"    %s %s = %d;\n\n"+
							"The numbers are the rule rather than the convention: 1 is the key, 2 is "+
							"the tenant, 4..7 are the alias, the name, the description and the labels. "+
							"An entity that does not want one of them leaves the number empty, which "+
							"is what keeps payday able to add a header field later without every app "+
							"having spent it on something else",
						v.FullName(), want.Name, f.Number(), want.At,
						protoTypeOf(want.Type), want.Name, want.At)
				}
			}
		}
	}

	return nil
}

// setAt is the field number the second axis lives at; see [Schema.Set].
//
// A number rather than an option, because the number is the scarce thing. The
// header is what anything can read off a row it has no type for, and 3 is the
// one place a reader could ever be taught to ask "what set is this row in"
// without knowing what the set is called. An option would let two apps answer
// that question at two different numbers, and then nothing generic can ask it.
const setAt = 3

// readSets works out whether this app declared a second axis, and refuses the
// shapes that would make it mean two things.
//
// It is the only place field 3 is read, and everything it refuses is something
// that compiles: a scalar at 3 says nothing a predicate can be built from, two
// entities pointing at two sets is two axes wearing one number, and a set that
// is itself inside a set is the hierarchy the slot exists to avoid.
func (s *Schema) readSets() error {
	for _, v := range s.Entities {
		var at graph.Prop
		for f := range v.Fields() {
			if f.Number() == setAt {
				at = f
			}
		}
		for e := range v.Edges() {
			if e.Number() == setAt {
				at = e
			}
		}
		if at == nil {
			continue
		}

		e, ok := at.(graph.Edge)
		if !ok {
			return fmt.Errorf(
				"%s: field %d is a %s, and field %d is the set this row is in -- an edge to it\n\n"+
					"    Set set = %d [(orm.edge) = {}];\n\n"+
					"payday leaves that number for an app to put a set smaller than a tenant in, "+
					"and reads it as one; anything else there is a field that will be read as a set "+
					"the day something asks",
				v.FullName(), setAt, at.Type(), setAt, setAt)
		}
		if e.IsList() {
			return fmt.Errorf(
				"%s: %s is a list, and the set a row is in is one set",
				v.FullName(), e.Name())
		}

		v.Set = string(e.Name())

		to, ok := s.of(e.Target().FullName())
		if !ok {
			return fmt.Errorf(
				"%s: %s names %s, which is not an entity of this app",
				v.FullName(), e.Name(), e.Target().FullName())
		}
		if s.Set == nil {
			s.Set = to
			to.IsSet = true

			continue
		}
		if s.Set != to {
			return fmt.Errorf(
				"%s says its set is %s and %s says it is %s; field %d is one axis, "+
					"so every entity that declares it has to mean the same set",
				v.FullName(), to.FullName(), setName(s, s.Set), s.Set.FullName(), setAt)
		}
	}

	if s.Set == nil {
		return nil
	}
	if s.Set.Set != "" {
		return fmt.Errorf(
			"%s is the set and declares one of its own at field %d; a set inside a set is a "+
				"hierarchy, and a hierarchy is what labels are for -- put the grouping on the "+
				"set as a label rather than as another level",
			s.Set.FullName(), setAt)
	}
	if s.Set.IsGlobal {
		return fmt.Errorf(
			"%s is the set and is `global: {}`; a set outside the wall can be named by two "+
				"tenants, and then narrowing to it crosses the wall without anything failing",
			s.Set.FullName())
	}

	return nil
}

// of is the entity of this schema with that name.
func (s *Schema) of(name protoreflect.FullName) (*Entity, bool) {
	for _, v := range s.Entities {
		if v.FullName() == name {
			return v, true
		}
	}

	return nil, false
}

// setName is the first entity found declaring the set, for a message that has
// to name the disagreement from both ends.
func setName(s *Schema, to *Entity) protoreflect.FullName {
	for _, v := range s.Entities {
		if v.Set != "" && v != to {
			if e, ok := edge(v.Entity, v.Set); ok && e.Target().FullName() == to.FullName() {
				return v.FullName()
			}
		}
	}

	return to.FullName()
}

// checkErase refuses an entity that does not say what `Erase` does to a row.
//
// Two ways of saying it, and one of them is the row itself: a field marked
// `erased:` makes the ORM stamp instead of delete, so an entity that has one is
// soft and needs nothing else. An entity with none loses the row for good, and
// that has to be written down.
//
// The two failures are not alike, which is the whole of why this is a refusal
// rather than a default. Assuming soft wrongly leaves rows somebody meant to be
// gone, and that is noticed by looking at them. Assuming hard wrongly destroys
// them, and that is noticed by somebody asking for one back.
func (s *Schema) checkErase() error {
	for _, v := range s.Entities {
		var at graph.Field
		for f := range v.Fields() {
			if f.IsErased() {
				at = f
			}
		}

		hard := v.Opts.GetErase().HasHard()
		switch {
		case at != nil && hard:
			return fmt.Errorf(
				"%s: erase: says `hard: {}` and carries %q, and they are two different "+
					"answers: the field is what makes an Erase a stamp rather than a delete, "+
					"so the row would be softly erased while the schema says it is destroyed",
				v.FullName(), at.Name())

		case at == nil && !hard:
			return fmt.Errorf(
				"%s: erase: nothing says what `Erase` does to a row, and with no field marked "+
					"`erased:` it destroys one\n\n"+
					"    google.protobuf.Timestamp date_erased = %d [(orm.field) = {erased: {}}];\n\n"+
					"or, if losing the row is what this entity is for:\n\n"+
					"    option (payday.entity) = {... erase: {hard: {}}};",
				v.FullName(), suggestErased(v.Entity))
		}

		v.IsHard = hard
	}

	return nil
}

// suggestErased is a field number that is free, for the message that tells an
// entity to declare one. 14 is where payday's own Holder puts it, and where an
// entity that has not spent it should put it too, so that the marker is in the
// same place in every schema payday reads.
//
// Past 14 it counts up from 16 and not from the first hole anywhere, because
// 13..15 are payday's on every entity it ships -- the version, this marker, the
// creation stamp -- and a number suggested out of that range is a collision
// scheduled for whenever the app grows into it.
//
// It is the first free number rather than a list of candidates, because a list
// has to end somewhere and the entity that has spent every number on it is
// answered with one of them anyway. The suggestion is a line to paste, and a
// number the message names while the schema already uses it is worse than no
// message at all: it reads as an answer and does not compile.
func suggestErased(e graph.Entity) int {
	taken := map[int]bool{}
	for f := range e.Fields() {
		taken[int(f.Number())] = true
	}
	if !taken[14] {
		return 14
	}
	for f := 16; ; f++ {
		if !taken[f] {
			return f
		}
	}
}

// checkPresence refuses a field that has presence in the API and nowhere to
// keep it.
//
// # What goes wrong
//
// A message field -- a `google.protobuf.Timestamp`, a nested message -- with no
// `nullable`, no `default` and no marker generates a **NOT NULL** column. The
// API generated beside it still has `HasXxx()`, because a message field has
// presence in proto whatever the column does.
//
// So the two disagree, and the caller is the one told the lie: they ask whether
// a value is set, are told yes, and read a zero somebody wrote because the
// column would not take null. It is not a failure anywhere -- it is a row that
// says a thing happened at the beginning of the epoch.
//
// # Why it is refused rather than fixed
//
// Because both fixes are the schema's to choose and mean different things.
// `nullable: true` says the value may be absent; a default says it is always
// there and here is what it starts as. A generator picking one would be
// deciding what an app meant.
//
// # The three that are exempt, and why the boundary is where it is
//
// `date_created` has a default, `date_updated` is the version and `date_erased`
// is the erased marker -- and each is stamped by the **server** rather than
// given by a caller. Their presence is not a claim about what somebody sent.
//
// The boundary is stated as those three declarations rather than those three
// names, so an app that puts its version on a differently named field is not
// caught by a rule about spelling.
func (s *Schema) checkPresence(v *Entity) error {
	// Fields and not props, which is what leaves edges out. An edge is a
	// message field too, and its presence is the foreign key being there rather
	// than a claim about what somebody sent.
	for prop := range v.Fields() {
		fd := prop.Descriptor()
		if fd.Kind() != protoreflect.MessageKind {
			// Scalars under IMPLICIT presence have no `Has`, so there is
			// nothing to disagree with.
			continue
		}
		if fd.IsMap() || fd.IsList() {
			// Neither has presence either, and a map's descriptor says
			// `MessageKind` because its entries are a synthetic message -- which
			// is what this check caught the first time it was run. Empty and
			// absent are one state for both, so there is no `Has` to be wrong.
			continue
		}
		if prop.IsNullable() || prop.HasDefault() {
			continue
		}

		opts, _ := proto.GetExtension(fd.Options(), ormpb.E_Field).(*ormpb.FieldOptions)
		if opts.GetVersion() != nil || opts.GetErased() != nil {
			// Stamped by the server. Its presence says nothing about what a
			// caller sent, so there is nothing to be wrong about.
			continue
		}

		return fmt.Errorf(
			"%s.%s: this field has presence in the API -- `Has%s()` -- and a NOT NULL "+
				"column to keep it in, so a caller who never set it is told it is set\n\n"+
				"    say which one you meant:\n"+
				"      [(orm.field) = {nullable: true}]  the value may be absent\n"+
				"      [(orm.field) = {default: \"\"}]     it is always there, and starts here",
			v.FullName(), prop.Name(), camel(prop.Name()))
	}

	return nil
}

// named is what a `{name, number}` points at, and the refusal when the two
// disagree.
//
// # Why both
//
// A proto field is renamed without the wire changing, so the **number** is the
// identity and the name is a label. A declaration carrying only the label
// follows a rename onto whatever took the old name -- which is not the loud
// failure of "there is no such field" but the quiet one of "that is a different
// field now", and a paging order or a filter that quietly moved to another
// column is exactly the kind of thing this generator exists to refuse.
//
// It is also how the rest of a schema already points at a field: `orm`'s
// indexes carry `{name, number}`, and a schema with two conventions for the
// same thing is one somebody has to remember which is which for.
//
// # What is allowed
//
// Either alone works, so an existing schema is not rewritten to be read and a
// small one is not made verbose -- `pd new` itself writes name-only. Both
// together is what is checked: resolved by the number, refused when the name
// no longer agrees, which is the only form the quiet rename above is caught
// in.
func named(e graph.Entity, ref *ormpb.Ref, what string) (string, error) {
	name, number := ref.GetName(), ref.GetNumber()
	switch {
	case name == "" && number == 0:
		return "", fmt.Errorf("list: %s: names nothing; write a name, a number, or both", what)

	case number == 0:
		return name, nil
	}

	for p := range e.Props() {
		if int32(p.Number()) != number {
			continue
		}
		if name != "" && p.Name() != name {
			return "", fmt.Errorf(
				"list: %s: %d is %q and this says %q.\n"+
					"a number is what a field **is** and a name is what it is called, so the two "+
					"disagreeing means one of them followed a rename and the other did not -- and "+
					"whichever way round that is, this would read a column nobody meant",
				what, number, p.Name(), name)
		}

		return p.Name(), nil
	}

	return "", fmt.Errorf("list: %s: %s has nothing at %d", what, e.FullName(), number)
}
