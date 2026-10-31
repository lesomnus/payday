package pdgen

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/meter"
	"github.com/lesomnus/payday/pdpb"
)

const pkgMeter = protogen.GoImportPath("github.com/lesomnus/payday/meter")

// Meter is one meter the schema declares (payday#186), resolved against the
// entity it measures: what `pd.Meters` writes the query of.
type Meter struct {
	Key  string
	Unit string

	// Of is the entity whose rows are measured.
	Of *Entity

	// Sum is the field summed, by name, and empty for a count.
	Sum string

	// Where is what a row must hold to be measured, beside belonging to the
	// tenant and not being erased.
	Where []Where

	// At is the file that declared it, as a person would type it.
	At string
}

// Where is one condition of a [Meter], resolved: the predicate of ent's it is
// written as, and the values as Go literals.
type Where struct {
	// Prop is the field or the edge.
	Prop graph.Prop

	// Column is what ent's predicates of it are called before their suffix:
	// `Mode` of `ModeIn`, and `RobotId` of an edge's `RobotIdIsNil`.
	Column string

	// In and NotIn are the values, as Go literals of the predicate's type.
	In, NotIn []string

	// Set is whether the value is to be there at all, said when HasSet is.
	Set, HasSet bool

	// Bool is a field of `bool`, whose predicates are EQ and NEQ and not In.
	Bool bool
}

// declared is the meters one file says, before the entities they name are
// read.
type declared struct {
	meters []*pdpb.Meter
	pkg    protoreflect.FullName
	at     string
}

// readMeters is what the app's files say of its meters: one of them, which is
// the file that says the package is the app's. Each is held here to what does
// not need the entities -- a key, a unit, one measure -- and to them in
// [Schema.checkMeters].
func readMeters(files []*protogen.File) (*declared, error) {
	var v *declared
	for _, f := range files {
		if !f.Generate {
			continue
		}

		a, _ := proto.GetExtension(f.Desc.Options(), pdpb.E_App).(*pdpb.App)
		if len(a.GetMeters()) == 0 {
			continue
		}
		if v != nil {
			return nil, fmt.Errorf(
				"%s and %s both declare meters; they are said once, in the file that says the package is the app's",
				v.at, f.Desc.Path())
		}

		seen := map[string]bool{}
		for _, m := range a.GetMeters() {
			if err := meter.CheckKey(m.GetKey()); err != nil {
				return nil, fmt.Errorf("%s: meters: %w", f.Desc.Path(), err)
			}

			key := m.GetKey()
			switch {
			case seen[key]:
				return nil, fmt.Errorf("%s: meters: two are called %q, and a reading of one would be taken for the other's", f.Desc.Path(), key)
			case !m.HasCount() && !m.HasSum():
				return nil, fmt.Errorf("%s: meters: %s says neither `count:` nor `sum:`, so nothing is measured", f.Desc.Path(), key)
			}
			if err := meter.CheckUnit(m.GetUnit()); err != nil {
				return nil, fmt.Errorf("%s: meters: %s: %w", f.Desc.Path(), key, err)
			}
			seen[key] = true
		}

		v = &declared{meters: a.GetMeters(), pkg: f.Desc.Package(), at: f.Desc.Path()}
	}

	return v, nil
}

// checkMeters resolves each meter the schema declares against the entity it
// names, and refuses one that names a field or a value the entity does not
// have, or one the generated query could not say safely.
//
// Everything refused is something a bill would otherwise get wrong without a
// word: a meter on a field that was renamed away counts another column, one on
// a value an enum no longer has counts nothing, and one on a row no tenant
// holds is somebody's bill made of everybody's rows.
func (s *Schema) checkMeters(d *declared) error {
	if d == nil {
		return nil
	}

	byName := map[protoreflect.FullName]*Entity{}
	for _, v := range s.Entities {
		byName[v.FullName()] = v
	}

	for _, m := range d.meters {
		v, err := s.meterOf(byName, d.pkg, m)
		if err != nil {
			return fmt.Errorf("%s: meters: %s: %w", d.at, m.GetKey(), err)
		}

		v.At = d.at
		s.Meters = append(s.Meters, v)
	}

	return nil
}

// meterOf is one declaration resolved.
func (s *Schema) meterOf(byName map[protoreflect.FullName]*Entity, pkg protoreflect.FullName, m *pdpb.Meter) (*Meter, error) {
	of, where := m.GetCount().GetOf(), m.GetCount().GetWhere()
	if m.HasSum() {
		of, where = m.GetSum().GetOf(), m.GetSum().GetWhere()
	}

	e, err := meterEntity(byName, pkg, of)
	if err != nil {
		return nil, err
	}

	v := &Meter{Key: m.GetKey(), Unit: m.GetUnit(), Of: e}
	if m.HasSum() {
		f, err := summed(e, m.GetSum().GetField())
		if err != nil {
			return nil, err
		}
		v.Sum = f.Name()
	}

	seen := map[string]bool{}
	for _, w := range where {
		c, err := condition(e, w)
		if err != nil {
			return nil, err
		}
		if seen[c.Prop.Name()] {
			return nil, fmt.Errorf("where: says two things of %q; say them as one, since a row holds one value", c.Prop.Name())
		}
		seen[c.Prop.Name()] = true

		v.Where = append(v.Where, c)
	}

	return v, nil
}

// meterEntity is the entity a meter is `of`: by its name in the app's package,
// or by its full name, and one whose rows a tenant holds.
func meterEntity(byName map[protoreflect.FullName]*Entity, pkg protoreflect.FullName, of string) (*Entity, error) {
	if of == "" {
		return nil, fmt.Errorf("says no `of:`, and a meter measures the rows of one entity")
	}

	e, ok := byName[pkg.Append(protoreflect.Name(of))]
	if !ok {
		e, ok = byName[protoreflect.FullName(of)]
	}
	switch {
	case !ok:
		return nil, fmt.Errorf("of: %q is no entity of this schema", of)

	case e.IsGlobal:
		return nil, fmt.Errorf(
			"of: %s is declared `global`, so no row of it is any tenant's -- and a meter is what one tenant holds",
			e.FullName())

	case len(e.Columns) > 1:
		// The trail's shape, and it is not one a tenant holds: a row is
		// readable by every tenant it names, so counting it for each would
		// count it more than once across the deployment.
		return nil, fmt.Errorf(
			"of: a row of %s names a tenant in each of %s, and is held by none of them more than another; "+
				"a meter counts what one tenant holds",
			e.FullName(), strings.Join(e.Columns, ", "))
	}

	return e, nil
}

// referred is the prop a meter's `{name, number}` names: both, since a meter
// is money, and the name has to be what the number is called.
//
// The rest of a schema may say either one alone ([named]). A meter may not,
// and the reason is the quiet half of a rename: a field renamed away and a new
// one given its old name is a declaration by name that follows the name, onto
// the new field, and a bill that counts another column with nothing failing.
func referred(e *Entity, ref *ormpb.Ref, what string) (graph.Prop, error) {
	name, number := ref.GetName(), ref.GetNumber()
	if name == "" || number == 0 {
		return nil, fmt.Errorf(
			"%s: says %s, and a meter names a field by both its name and its number -- "+
				"`{name: \"size_bytes\", number: 10}` -- so that a field renamed is still found, "+
				"and one that took the old name is not counted instead",
			what, refText(ref))
	}

	for p := range e.Props() {
		if int32(p.Number()) != number {
			continue
		}
		if p.Name() != name {
			return nil, fmt.Errorf(
				"%s: %d of %s is %q and this says %q: one of them followed a rename and the other did not, "+
					"and a meter on either would count a column nobody meant",
				what, number, e.FullName(), p.Name(), name)
		}

		return p, nil
	}

	return nil, fmt.Errorf("%s: %s has nothing at %d", what, e.FullName(), number)
}

func refText(ref *ormpb.Ref) string {
	switch {
	case ref.GetName() == "" && ref.GetNumber() == 0:
		return "nothing"
	case ref.GetNumber() == 0:
		return fmt.Sprintf("only the name %q", ref.GetName())
	default:
		return fmt.Sprintf("only the number %d", ref.GetNumber())
	}
}

// summed is the field a sum is of: an integer, of a row's own.
func summed(e *Entity, ref *ormpb.Ref) (graph.Field, error) {
	p, err := referred(e, ref, "sum: field")
	if err != nil {
		return nil, err
	}

	f, ok := p.(graph.Field)
	switch {
	case !ok:
		return nil, fmt.Errorf("sum: field: %q is an edge, and what is summed is a number", p.Name())
	case graph.IsCollection(f):
		return nil, fmt.Errorf("sum: field: %q holds many values, and a sum is of one to a row", p.Name())
	case !isInteger(f.Type()):
		return nil, fmt.Errorf(
			"sum: field: %q is %s, and a bill is whole numbers: a float summed is not exact, and two "+
				"databases sum one differently. Keep the amount in its smallest unit, in an integer",
			p.Name(), strings.ToLower(strings.TrimPrefix(f.Type().String(), "TYPE_")))
	}

	return f, nil
}

func isInteger(t ormpb.Type) bool {
	switch t {
	case ormpb.Type_TYPE_INT32, ormpb.Type_TYPE_INT64, ormpb.Type_TYPE_SINT32, ormpb.Type_TYPE_SINT64,
		ormpb.Type_TYPE_SFIXED32, ormpb.Type_TYPE_SFIXED64,
		ormpb.Type_TYPE_UINT32, ormpb.Type_TYPE_UINT64, ormpb.Type_TYPE_FIXED32, ormpb.Type_TYPE_FIXED64:
		return true
	}

	return false
}

// condition is one `where` resolved, and refused when the generated query
// could not say it safely. See `payday.Meter.Where`.
func condition(e *Entity, w *pdpb.Meter_Where) (Where, error) {
	p, err := referred(e, w.GetField(), "where: field")
	if err != nil {
		return Where{}, err
	}

	name := p.Name()
	said := 0
	for _, has := range []bool{len(w.GetIn()) > 0, len(w.GetNotIn()) > 0, w.HasSet()} {
		if has {
			said++
		}
	}
	switch {
	case said == 0:
		return Where{}, fmt.Errorf("where: %q says none of `in`, `not_in` and `set`, and a condition says one", name)
	case said > 1:
		return Where{}, fmt.Errorf("where: %q says more than one of `in`, `not_in` and `set`; a condition says one", name)
	}

	if f, ok := p.(graph.Field); ok && f.IsErased() {
		return Where{}, fmt.Errorf(
			"where: %q is what says a row is erased, and an erased row is never measured -- a meter is what a tenant holds",
			name)
	}
	if lead := tenantLead(e); lead != "" && name == lead {
		return Where{}, fmt.Errorf("where: %q is what says whose a row is, and a meter is read one tenant at a time by it already", name)
	}

	v := Where{Prop: p, Column: pascal(name)}
	edge, isEdge := p.(graph.Edge)
	if isEdge {
		v.Column = pascal(name) + "Id"
	}

	if w.HasSet() {
		switch {
		case isEdge && (edge.IsList() || edge.Inverse() != nil):
			return Where{}, fmt.Errorf("where: %q is held by the rows it points at and not by this one, which is a join", name)
		case !p.IsNullable():
			return Where{}, fmt.Errorf("where: %q is never null, so whether it is set says nothing of a row", name)
		}
		v.Set, v.HasSet = w.GetSet(), true

		return v, nil
	}

	if isEdge {
		return Where{}, fmt.Errorf(
			"where: %q is an edge, and the rows it points at are one tenant's identifiers and not a schema's; "+
				"say `set:` of it, or count what it points at", name)
	}
	f := p.(graph.Field)

	values := w.GetIn()
	if len(values) == 0 {
		values = w.GetNotIn()
	}
	lits, err := literals(f, values)
	if err != nil {
		return Where{}, fmt.Errorf("where: %q: %w", name, err)
	}

	v.Bool = f.Type() == ormpb.Type_TYPE_BOOL
	if v.Bool && len(lits) > 1 {
		return Where{}, fmt.Errorf("where: %q is a bool, and says both of its values: say one, or nothing of it", name)
	}
	if len(w.GetIn()) > 0 {
		v.In = lits
	} else {
		v.NotIn = lits
	}

	return v, nil
}

// literals is `values` as Go literals of the field's predicates, and the
// refusal of one the field cannot hold.
func literals(f graph.Field, values []string) ([]string, error) {
	switch {
	case graph.IsCollection(f):
		return nil, fmt.Errorf("holds many values, and a condition compares one")
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, s := range values {
		if seen[s] {
			return nil, fmt.Errorf("says %q twice", s)
		}
		seen[s] = true

		v, err := literal(f, s)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}

	return out, nil
}

// literal is one value as a Go literal: text format's way of writing the
// field's kind, held to what the field may hold.
func literal(f graph.Field, s string) (string, error) {
	switch t := f.Type(); t {
	case ormpb.Type_TYPE_STRING:
		return strconv.Quote(s), nil

	case ormpb.Type_TYPE_BOOL:
		switch s {
		case "true", "false":
			return s, nil
		}
		return "", fmt.Errorf("%q is not a bool: `true` or `false`", s)

	case ormpb.Type_TYPE_ENUM:
		d := f.Descriptor().Enum()
		ev := d.Values().ByName(protoreflect.Name(s))
		if ev == nil {
			names := []string{}
			for i := range d.Values().Len() {
				names = append(names, string(d.Values().Get(i).Name()))
			}
			return "", fmt.Errorf("%s has no value %q: it has %s", d.FullName(), s, strings.Join(names, ", "))
		}
		// The number, which is what the column holds, with the name beside
		// it: the name was held to the enum here, and is again by every
		// generation after this one.
		return fmt.Sprintf("int32(%d) /* %s */", ev.Number(), ev.Name()), nil

	case ormpb.Type_TYPE_INT32, ormpb.Type_TYPE_SINT32, ormpb.Type_TYPE_SFIXED32:
		return signed(s, 32)
	case ormpb.Type_TYPE_INT64, ormpb.Type_TYPE_SINT64, ormpb.Type_TYPE_SFIXED64:
		return signed(s, 64)
	case ormpb.Type_TYPE_UINT32, ormpb.Type_TYPE_FIXED32:
		return unsigned(s, 32)
	case ormpb.Type_TYPE_UINT64, ormpb.Type_TYPE_FIXED64:
		return unsigned(s, 64)

	case ormpb.Type_TYPE_FLOAT, ormpb.Type_TYPE_DOUBLE:
		return "", fmt.Errorf("is a float, and a float compared for equality is a value that is never quite there")

	default:
		return "", fmt.Errorf(
			"is %s, and a meter's condition compares a string, an enum, a bool or an integer -- or says `set:` of a field that may be null",
			strings.ToLower(strings.TrimPrefix(t.String(), "TYPE_")))
	}
}

func signed(s string, bits int) (string, error) {
	v, err := strconv.ParseInt(s, 10, bits)
	if err != nil {
		return "", fmt.Errorf("%q is not an int%d in decimal", s, bits)
	}

	return strconv.FormatInt(v, 10), nil
}

func unsigned(s string, bits int) (string, error) {
	v, err := strconv.ParseUint(s, 10, bits)
	if err != nil {
		return "", fmt.Errorf("%q is not a uint%d in decimal", s, bits)
	}

	return strconv.FormatUint(v, 10), nil
}

// tenantLead is the prop a read of one tenant's rows of `e` narrows by first:
// the edge to the tenant, the stamp of it, or the column naming it -- and
// empty for the tenant itself, which is narrowed by its key, and for a `via`
// of more than one step, which is narrowed by a walk.
func tenantLead(e *Entity) string {
	switch {
	case e.Stamp != "":
		return e.Stamp
	case len(e.Columns) == 1:
		return e.Columns[0]
	case len(e.Via) == 1:
		return e.Via[0]
	}

	return ""
}

// checkMeterIndex warns about a meter of an entity no index serves by tenant.
//
// A pass reads one tenant's rows at a time, so without an index that begins
// with what says whose a row is, every tenant's reading is a scan of the whole
// table -- and a pass over a thousand tenants reads it a thousand times, on the
// table a meter is pointed at because it is the one that grows. A warning and
// not a refusal, for the reason [checkListIndex] is one.
func checkMeterIndex(s *Schema) []string {
	vs := []string{}
	warned := map[*Entity]bool{}
	for _, m := range s.Meters {
		e := m.Of
		lead := tenantLead(e)
		if lead == "" || warned[e] || covered(e, []string{lead}) {
			continue
		}
		warned[e] = true

		vs = append(vs, fmt.Sprintf(
			"%s: meters: %s: no index of %s begins with %s, so measuring each tenant scans the table.\n"+
				"  a pass measures one tenant at a time, and reads the whole table for every one of them.\n"+
				"  declare it beside the entity:\n\n"+
				"    indexes: [{name: %q, refs: [%s]}]",
			m.At, m.Key, e.FullName(), lead, lead, refsOf(e, []string{lead})))
	}

	return vs
}

// EmitMeters writes the meters the schema declares, each a `meter.Meter` whose
// query is the declaration's, and the tenants there are to measure (payday#186).
// Nothing at all for an app that declares none.
func EmitMeters(g *protogen.GeneratedFile, s *Schema, p Paths) {
	if len(s.Meters) == 0 || s.Tenant == nil {
		return
	}

	ctx, id := pkgCtx.Ident("Context"), pkgPdid.Ident("Id")

	g.P("// Meters are the meters this app's schema declares (payday#186), each")
	g.P("// measured on `db`: what `meter.Reader.Meters` is, with the app's own meters")
	g.P("// beside them. Built on a client of a replica, they are measured there.")
	g.P("//")
	g.P("// Each is the rows of one entity that one tenant holds -- erased rows are")
	g.P("// not held -- narrowed by what its declaration says, counted or summed in")
	g.P("// one statement.")
	g.P("func Meters(db *", p.Ent.Ident("Client"), ") []", pkgMeter.Ident("Meter"), " {")
	g.P("	return []", pkgMeter.Ident("Meter"), "{")
	for _, m := range s.Meters {
		e := m.Of
		at := p.EntPkg(e)

		g.P("		{")
		g.P("			Key:  ", strconv.Quote(m.Key), ",")
		g.P("			Unit: ", strconv.Quote(m.Unit), ",")
		g.P("			Measure: func(ctx ", ctx, ", tenant ", id, ") (int64, error) {")
		g.P("				vs := []", pkgUuid.Ident("UUID"), "{tenant.Uuid()}")
		g.P("				q := db.", e.GoName(), ".Query().Where(")
		g.P("					", predicateOf(g, e, s, p), ",")
		for f := range e.Fields() {
			if f.IsErased() {
				g.P("					", at.Ident(pascal(f.Name())+"IsNil"), "(),")
			}
		}
		for _, w := range m.Where {
			g.P("					", wherePredicate(g, at, w), ",")
		}
		g.P("				)")
		if m.Sum == "" {
			g.P("				n, err := q.Count(ctx)")
		} else {
			g.P("				n, err := q.Aggregate(meterSum(", at.Ident("Field"+pascal(m.Sum)), ")).Int(ctx)")
		}
		g.P("				return int64(n), err")
		g.P("			},")
		g.P("		},")
	}
	g.P("	}")
	g.P("}")
	g.P("")

	if slices.ContainsFunc(s.Meters, func(m *Meter) bool { return m.Sum != "" }) {
		g.P("// meterSum is the sum of a column over the rows a query reads, as a whole")
		g.P("// number: zero for no rows, which SUM says is NULL, and BIGINT on both")
		g.P("// databases -- PostgreSQL's SUM of a bigint is a numeric, and a sum past")
		g.P("// what a bigint holds is refused by the cast rather than rounded.")
		g.P("func meterSum(column string) ", p.Ent.Ident("AggregateFunc"), " {")
		g.P("	return func(s *", pkgEntsql.Ident("Selector"), ") string {")
		g.P("		return \"CAST(COALESCE(\" + ", pkgEntsql.Ident("Sum"), "(s.C(column)) + \", 0) AS BIGINT)\"")
		g.P("	}")
		g.P("}")
		g.P("")
	}

	t := s.Tenant
	tp := p.EntPkg(t)
	g.P("// MeterTenants is every tenant there is, a page of at most `limit` after")
	g.P("// `after` in the order of their identifiers: what `meter.Reader.Tenants` is.")
	g.P("//")
	g.P("// Every one, closed or not: a tenant holds what it holds until it is")
	g.P("// destroyed, and is billed for it.")
	g.P("func MeterTenants(db *", p.Ent.Ident("Client"), ") func(ctx ", ctx, ", after ", id, ", limit int) ([]", id, ", error) {")
	g.P("	return func(ctx ", ctx, ", after ", id, ", limit int) ([]", id, ", error) {")
	g.P("		vs, err := db.", t.GoName(), ".Query().")
	g.P("			Where(", tp.Ident("IdGT"), "(after.Uuid())).")
	g.P("			Order(", tp.Ident("ById"), "()).")
	g.P("			Limit(limit).")
	g.P("			Ids(ctx)")
	g.P("		if err != nil {")
	g.P("			return nil, err")
	g.P("		}")
	g.P("")
	g.P("		out := make([]", id, ", len(vs))")
	g.P("		for i, v := range vs {")
	g.P("			out[i] = ", id, "(v)")
	g.P("		}")
	g.P("")
	g.P("		return out, nil")
	g.P("	}")
	g.P("}")
	g.P("")
}

// wherePredicate is one condition as ent's predicate of it.
func wherePredicate(g *protogen.GeneratedFile, at protogen.GoImportPath, w Where) string {
	q := func(suffix string) string { return g.QualifiedGoIdent(at.Ident(w.Column + suffix)) }

	var notIn string
	switch {
	case w.HasSet && w.Set:
		return q("NotNil") + "()"
	case w.HasSet:
		return q("IsNil") + "()"

	case w.Bool && len(w.In) > 0:
		return q("EQ") + "(" + w.In[0] + ")"
	case len(w.In) > 0:
		return q("In") + "(" + strings.Join(w.In, ", ") + ")"

	case w.Bool:
		notIn = q("NEQ") + "(" + w.NotIn[0] + ")"
	default:
		notIn = q("NotIn") + "(" + strings.Join(w.NotIn, ", ") + ")"
	}

	// A value that is null is none of these, and SQL says a null compared
	// with anything is null -- which a WHERE reads as false, leaving the row
	// out. So of a field that may be null it is said out loud.
	if !w.Prop.IsNullable() {
		return notIn
	}

	return g.QualifiedGoIdent(at.Ident("Or")) + "(" + q("IsNil") + "(), " + notIn + ")"
}
