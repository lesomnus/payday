package pdgen

import (
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
)

// emitOwed writes the store's half of an erasure owed (payday#115): whether
// its client is in a transaction, and [trail.Store.Owe], `Owed`, `Paid` and
// `Kept` on payday's `Owed`, a queue as the outbox is.
//
// The judgement is in the runtime: what is owed when, what pays it, what a hold
// keeps of it. What is here is rows -- one per object owed, written in the
// store's transaction, read oldest first, and removed by key once paid.
func emitOwed(g *protogen.GeneratedFile, s *Schema, p Paths) {
	g.P("// InTx is whether the client is bound to a transaction -- one it began, or")
	g.P("// one begun with `dialect.BeginTx` that it shares. The archive takes no")
	g.P("// part in it: an erasure is owed there rather than written, and every other")
	g.P("// act is refused (`trail.ErrInTx`).")
	g.P("func (s trailStore) InTx() bool { return s.db.InTx() }")
	g.P("")

	v := s.Own(pdpb.Own_OWN_OWED)
	if v == nil {
		// Refused by [CheckOwn] before this, for a real app: an erasure owed
		// with nowhere to write it is no erasure, and says so.
		g.P("func (s trailStore) Owe(", pkgCtx.Ident("Context"), ", []", pkgPdid.Ident("Id"), ") error {")
		g.P("	return ", pkgErrors.Ident("New"), "(\"no table of erasures owed: payday's `Owed` is not in the schema\")")
		g.P("}")
		g.P("")
		g.P("func (s trailStore) Owed(", pkgCtx.Ident("Context"), ", ", pkgTrail.Ident("Cursor"), ", int) ([]", pkgTrail.Ident("Owed"), ", error) {")
		g.P("	return nil, nil")
		g.P("}")
		g.P("")
		g.P("func (s trailStore) Paid(", pkgCtx.Ident("Context"), ", []any) (int, error) {")
		g.P("	return 0, nil")
		g.P("}")
		g.P("")
		g.P("func (s trailStore) Kept(", pkgCtx.Ident("Context"), ", any, []", pkgPdid.Ident("Id"), ") error {")
		g.P("	return nil")
		g.P("}")
		g.P("")
		return
	}

	e := v.GoName()
	pkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())

	g.P("func (s trailStore) Owe(ctx ", pkgCtx.Ident("Context"), ", objects []", pkgPdid.Ident("Id"), ") error {")
	g.P("	now := ", pkgTime.Ident("Now"), "()")
	g.P("	qs := make([]*", p.Ent.Ident(e+"Create"), ", 0, len(objects))")
	g.P("	for _, v := range objects {")
	g.P("		qs = append(qs, s.db.", e, ".Create().")
	g.P("			SetId(", pkgPdid.Ident("New"), "(", e, "Domain).Uuid()).")
	g.P("			SetObject(v.Uuid()).")
	g.P("			SetHeldBy([][]byte{}).")
	g.P("			SetDateCreated(now))")
	g.P("	}")
	g.P("	for vs := range ", pkgSlices.Ident("Chunk"), "(qs, keptTrailRows) {")
	g.P("		if err := s.db.", e, ".CreateBulk(vs...).Exec(ctx); err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")

	g.P("func (s trailStore) Owed(ctx ", pkgCtx.Ident("Context"), ", after ", pkgTrail.Ident("Cursor"), ", limit int) ([]", pkgTrail.Ident("Owed"), ", error) {")
	g.P("	q := s.db.", e, ".Query()")
	g.P("	if after.Key != nil {")
	g.P("		id, err := cursorOf(after.Key)")
	g.P("		if err != nil {")
	g.P("			return nil, err")
	g.P("		}")
	g.P("")
	g.P("		q = q.Where(", pkg.Ident("Or"), "(")
	g.P("			", pkg.Ident("DateCreatedGT"), "(after.Created),")
	g.P("			", pkg.Ident("And"), "(", pkg.Ident("DateCreatedEQ"), "(after.Created), ", pkg.Ident("IdGT"), "(id)),")
	g.P("		))")
	g.P("	}")
	g.P("")
	g.P("	vs, err := q.Order(", p.Ent.Ident("Asc"), "(", pkg.Ident("FieldDateCreated"), ", ", pkg.Ident("FieldId"), ")).Limit(limit).All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	out := make([]", pkgTrail.Ident("Owed"), ", 0, len(vs))")
	g.P("	for _, v := range vs {")
	g.P("		by := make([]", pkgPdid.Ident("Id"), ", 0, len(v.HeldBy))")
	g.P("		for _, b := range v.HeldBy {")
	g.P("			if id, err := ", pkgPdid.Ident("From"), "(b); err == nil {")
	g.P("				by = append(by, id)")
	g.P("			}")
	g.P("		}")
	g.P("		out = append(out, ", pkgTrail.Ident("Owed"), "{Key: v.Id, Object: ", pkgPdid.Ident("Id"), "(v.Object), Created: v.DateCreated, HeldBy: by})")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	g.P("func (s trailStore) Paid(ctx ", pkgCtx.Ident("Context"), ", keys []any) (int, error) {")
	g.P("	ids := make([]", pkgUuid.Ident("UUID"), ", 0, len(keys))")
	g.P("	for _, k := range keys {")
	g.P("		id, ok := k.(", pkgUuid.Ident("UUID"), ")")
	g.P("		if !ok {")
	g.P("			return 0, ", pkgFmt.Ident("Errorf"), "(\"trail: %T is not a key this store gave out\", k)")
	g.P("		}")
	g.P("		ids = append(ids, id)")
	g.P("	}")
	g.P("	if len(ids) == 0 {")
	g.P("		return 0, nil")
	g.P("	}")
	g.P("")
	g.P("	return s.db.", e, ".Delete().Where(", pkg.Ident("IdIn"), "(ids...)).Exec(ctx)")
	g.P("}")
	g.P("")

	g.P("func (s trailStore) Kept(ctx ", pkgCtx.Ident("Context"), ", key any, by []", pkgPdid.Ident("Id"), ") error {")
	g.P("	id, ok := key.(", pkgUuid.Ident("UUID"), ")")
	g.P("	if !ok {")
	g.P("		return ", pkgFmt.Ident("Errorf"), "(\"trail: %T is not a key this store gave out\", key)")
	g.P("	}")
	g.P("	bs := make([][]byte, len(by))")
	g.P("	for i, v := range by {")
	g.P("		bs[i] = v.Bytes()")
	g.P("	}")
	g.P("")
	g.P("	return s.db.", e, ".UpdateOneId(id).SetHeldBy(bs).Exec(ctx)")
	g.P("}")
	g.P("")
}
