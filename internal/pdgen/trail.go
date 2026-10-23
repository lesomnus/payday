package pdgen

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/trail"
)

var (
	pkgTrail     = protogen.GoImportPath("github.com/lesomnus/payday/trail")
	pkgProtojson = protogen.GoImportPath("google.golang.org/protobuf/encoding/protojson")
)

// checkSealed refuses a field of the trail's two entities -- `Audit`, and the
// access record beside it -- that protojson would write as
// [trail.SealedMember], or that it reads under that name.
//
// That member is where a row of the archive keeps what it said once it is
// sealed, in place of `value` and `patch`, and it is no field of payday's: so
// protobuf's own refusal of two fields that write one JSON name does not see
// it. A field of an app's written under it would make a row read as sealed
// already, which is then sealed by nothing and opened by nothing. An overlay
// is the one way such a field gets there, and this is where an overlay is held
// to what payday's documents need -- rather than the member moving to a name no
// field could take, which would be a second format for an archive that already
// holds the first.
func (s *Schema) checkSealed() error {
	errs := []string{}
	for _, own := range []pdpb.Own{pdpb.Own_OWN_AUDIT, pdpb.Own_OWN_ACCESS} {
		v := s.Own(own)
		if v == nil {
			continue
		}

		fs := v.Descriptor().Fields()
		for i := range fs.Len() {
			f := fs.Get(i)
			if string(f.Name()) == trail.SealedMember || f.JSONName() == trail.SealedMember {
				errs = append(errs, fmt.Sprintf("%s: %s = %d is written as %q", v.FullName(), f.Name(), f.Number(), f.JSONName()))
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}

	return fmt.Errorf(
		"a field of the trail's entities may not be named, or JSON-named, %q: it is where the archive keeps "+
			"what a sealed row said (`trail.Keys`), and a row with a field written as it reads as sealed "+
			"and is sealed by nothing. Name it something else\n\n  %s",
		trail.SealedMember, strings.Join(errs, "\n  "))
}

// EmitTrail writes the app's half of the retention policy: the audit table, as
// much of it as `trail` needs to move rows out of the database.
//
// It is generated rather than written in the runtime for the reason the outbox
// drain is -- `ent.Client` and its predicates are the app's types and payday
// cannot name them, and payday has no Go type for its own `Audit` either, since
// the schema is **copied** into each app and generated there.
//
// What is not generated is any judgement. There is none here beyond *read a
// batch of these rows older than this, say which tenants rows are filed under,
// and forget or blank the ones I name*: the two clocks, whose clock a row is
// on, what a hold keeps, the refusal to destroy what was never written, the
// archive's layout and what may be destroyed are all in the runtime, where they
// can be argued with once instead of per app. Even the rule for which tenants a
// row names is the runtime's -- `trail.TenantsOf` -- and this hands it the
// three columns.
//
// Which is why erasing one subject is not generated either, though it was:
// `pd.ForgetInTrail` blanked two columns for a set the caller chose, and once a
// legal hold could be on some of those rows, which of them was a judgement.
// It is `trail.Policy.Forget`, and this is the `Blank` it calls.
//
// The document that travels between the two halves is protojson, which is the
// archive's format anyway -- *readable by anything that can read an `Audit`*.
func EmitTrail(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	// Refused by [CheckOwn] before this, for a real app; see there.
	if !s.Has(pdpb.Own_OWN_AUDIT) || !s.Has(pdpb.Own_OWN_ARCHIVED) {
		return
	}

	v := s.Own(pdpb.Own_OWN_AUDIT)
	e := v.GoName()
	entPkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())
	pred := p.Ent + "/predicate"

	// The access record's rows are rows of this store too (payday#111), so
	// that the passes that keep the trail keep them: archived, sealed,
	// checkpointed and purged as a kind of their own.
	acc := s.Own(pdpb.Own_OWN_ACCESS)
	var accPkg protogen.GoImportPath
	if acc != nil {
		accPkg = p.Ent + "/" + protogen.GoImportPath(acc.EntPkg())
	}

	g.P("// TrailStore is the audit table as [trail.Store] sees it.")
	g.P("//")
	g.P("// Handed to `trail.Sweep` beside a policy, and to whatever command an app")
	g.P("// gives an operator:")
	g.P("//")
	g.P("//	s.Spin = append(s.Spin, trail.Sweep(pd.TrailStore(db), policy))")
	g.P("func TrailStore(db *", p.Ent.Ident("Client"), ") ", pkgTrail.Ident("Store"), " {")
	g.P("	return trailStore{db}")
	g.P("}")
	g.P("")
	g.P("type trailStore struct{ db *", p.Ent.Ident("Client"), " }")
	g.P("")
	g.P("var _ ", pkgTrail.Ident("Store"), " = trailStore{}")
	g.P("")

	g.P("// of narrows to the kinds a read is about, and to whose rows they are.")
	g.P("//")
	g.P("// By the `domain` column and not by the domain byte inside `object_id`,")
	g.P("// which carries the same fact and cannot be indexed: *what kind was this")
	g.P("// row about* is answered by reading a row, and *which rows were about")
	g.P("// robots* is a query over a set. The second is what a retention policy is")
	g.P("// made of.")
	g.P("//")
	g.P("// And by the three tenant columns, which are what the wall on the trail")
	g.P("// reads: a row is readable by every tenant it names, and so it is on each")
	g.P("// of their windows.")
	g.P("func (s trailStore) of(k ", pkgTrail.Ident("Scope"), ") *", p.Ent.Ident(e+"Query"), " {")
	g.P("	q := s.db.", e, ".Query()")
	g.P("	if len(k.Only) > 0 {")
	g.P("		q = q.Where(", entPkg.Ident("DomainIn"), "(numbers(k.Only)...))")
	g.P("	} else if len(k.Except) > 0 {")
	g.P("		q = q.Where(", entPkg.Ident("DomainNotIn"), "(numbers(k.Except)...))")
	g.P("	}")
	g.P("")
	g.P("	if len(k.Objects) > 0 {")
	g.P("		ids := make([]", pkgUuid.Ident("UUID"), ", len(k.Objects))")
	g.P("		for i, v := range k.Objects {")
	g.P("			ids[i] = v.Uuid()")
	g.P("		}")
	g.P("")
	g.P("		q = q.Where(", entPkg.Ident("ObjectIdIn"), "(ids...))")
	g.P("	}")
	g.P("")
	g.P("	t := k.Tenant.Uuid()")
	g.P("	switch k.Whose {")
	g.P("	case ", pkgTrail.Ident("FiledUnder"), ":")
	g.P("		q = q.Where(", entPkg.Ident("TenantIdEQ"), "(t))")
	g.P("	case ", pkgTrail.Ident("Alone"), ":")
	g.P("		q = q.Where(trailAlone(t))")
	g.P("	case ", pkgTrail.Ident("Together"), ":")
	g.P("		q = q.Where(")
	g.P("			", entPkg.Ident("Or"), "(", entPkg.Ident("TenantIdEQ"), "(t), ",
		entPkg.Ident("ActorTenantIdEQ"), "(t), ", entPkg.Ident("CounterpartTenantIdEQ"), "(t)),")
	g.P("			", entPkg.Ident("Not"), "(trailAlone(t)),")
	g.P("		)")
	g.P("	case ", pkgTrail.Ident("Sharing"), ":")
	g.P("		q = q.Where(", entPkg.Ident("TenantIdEQ"), "(t), ", entPkg.Ident("Not"), "(trailAlone(t)))")
	g.P("	}")
	g.P("")
	g.P("	return q")
	g.P("}")
	g.P("")

	g.P("// trailAlone is a row filed under `t` that names no other tenant: the actor")
	g.P("// is of `t` or is nobody, and so is the other party if there is one. Nobody")
	g.P("// is the zero identifier -- the deployment, writing to itself.")
	g.P("func trailAlone(t ", pkgUuid.Ident("UUID"), ") ", pred.Ident(e), " {")
	g.P("	none := ", pkgUuid.Ident("Nil"), "()")
	g.P("")
	g.P("	return ", entPkg.Ident("And"), "(")
	g.P("		", entPkg.Ident("TenantIdEQ"), "(t),")
	g.P("		", entPkg.Ident("ActorTenantIdIn"), "(t, none),")
	g.P("		", entPkg.Ident("Or"), "(", entPkg.Ident("CounterpartTenantIdIsNil"), "(), ",
		entPkg.Ident("CounterpartTenantIdIn"), "(t, none)),")
	g.P("	)")
	g.P("}")
	g.P("")

	g.P("// Tenants is one seek per tenant: the next `tenant_id` after the last, which")
	g.P("// the index on `(tenant_id, date_created)` answers without reading the rows")
	g.P("// in between. A `DISTINCT` would read all of them.")
	g.P("func (s trailStore) Tenants(ctx ", pkgCtx.Ident("Context"), ", after ", pkgPdid.Ident("Id"),
		", limit int) ([]", pkgPdid.Ident("Id"), ", error) {")
	g.P("\tout := []", pkgPdid.Ident("Id"), "{}")
	g.P("\tlast := after.Uuid()")
	g.P("\tfor len(out) < limit {")
	g.P("\t\tnext, found := ", pkgUuid.Ident("Nil"), "(), false")
	g.P("\t\tv, err := s.db.", e, ".Query().")
	g.P("\t\t\tWhere(", entPkg.Ident("TenantIdGT"), "(last)).")
	g.P("\t\t\tOrder(", p.Ent.Ident("Asc"), "(", entPkg.Ident("FieldTenantId"), ")).")
	g.P("\t\t\tFirst(ctx)")
	g.P("\t\tswitch {")
	g.P("\t\tcase err == nil:")
	g.P("\t\t\tnext, found = v.TenantId, true")
	g.P("\t\tcase !", p.Ent.Ident("IsNotFound"), "(err):")
	g.P("\t\t\treturn out, err")
	g.P("\t\t}")
	if acc != nil {
		// The access record's tenants as well: the next of either table.
		g.P("\t\tw, err := s.db.", acc.GoName(), ".Query().")
		g.P("\t\t\tWhere(", accPkg.Ident("TenantIdGT"), "(last)).")
		g.P("\t\t\tOrder(", p.Ent.Ident("Asc"), "(", accPkg.Ident("FieldTenantId"), ")).")
		g.P("\t\t\tFirst(ctx)")
		g.P("\t\tswitch {")
		g.P("\t\tcase err == nil && (!found || w.TenantId.Compare(next) < 0):")
		g.P("\t\t\tnext, found = w.TenantId, true")
		g.P("\t\tcase err != nil && !", p.Ent.Ident("IsNotFound"), "(err):")
		g.P("\t\t\treturn out, err")
		g.P("\t\t}")
	}
	g.P("\t\tif !found {")
	g.P("\t\t\tbreak")
	g.P("\t\t}")
	g.P("")
	g.P("\t\tout = append(out, ", pkgPdid.Ident("Id"), "(next))")
	g.P("\t\tlast = next")
	g.P("\t}")
	g.P("")
	g.P("\treturn out, nil")
	g.P("}")
	g.P("")

	g.P("// older is a scope's rows written before `at`, past `after`, oldest first.")
	g.P("func (s trailStore) older(k ", pkgTrail.Ident("Scope"), ", at ", pkgTime.Ident("Time"),
		", after ", pkgTrail.Ident("Cursor"), ", limit int) (*", p.Ent.Ident(e+"Query"), ", error) {")
	g.P("	q := s.of(k).Where(", entPkg.Ident("DateCreatedLT"), "(at))")
	g.P("	if after.Key != nil {")
	g.P("		id, err := cursorOf(after.Key)")
	g.P("		if err != nil {")
	g.P("			return nil, err")
	g.P("		}")
	g.P("")
	emitAfter(g, "		", entPkg, "after.Created", "id")
	g.P("	}")
	g.P("")
	g.P("	return q.")
	g.P("		Order(", p.Ent.Ident("Asc"), "(", entPkg.Ident("FieldDateCreated"),
		", ", entPkg.Ident("FieldId"), ")).")
	g.P("		Limit(limit), nil")
	g.P("}")
	g.P("")
	g.P("func (s trailStore) Older(ctx ", pkgCtx.Ident("Context"), ", k ", pkgTrail.Ident("Scope"),
		", at ", pkgTime.Ident("Time"), ", after ", pkgTrail.Ident("Cursor"), ", limit int) (",
		pkgTrail.Ident("Rows"), ", error) {")
	g.P("	q, err := s.older(k, at, after, limit)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	vs, err := q.All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	out := make(", pkgTrail.Ident("Rows"), ", 0, len(vs))")
	g.P("	for _, v := range vs {")
	// `Proto()` rather than a conversion written here, so that a column an app
	// added to the trail is in the archive too. See the note on 3 and 19+ in
	// payday's audit.proto.
	g.P("		b, err := ", pkgProtojson.Ident("Marshal"), "(v.Proto())")
	g.P("		if err != nil {")
	g.P("			return nil, err")
	g.P("		}")
	g.P("")
	g.P("		out = append(out, ", pkgTrail.Ident("Row"), "{")
	g.P("			Doc:     b,")
	g.P("			Key:     v.Id,")
	g.P("			Domain:  ", pkgPdid.Ident("Domain"), "(v.Domain),")
	g.P("			Created: v.DateCreated,")
	g.P("			Tenants: ", pkgTrail.Ident("TenantsOf"), "(v.TenantId, v.ActorTenantId, v.CounterpartTenantId),")
	g.P("		})")
	g.P("	}")
	g.P("")
	if acc != nil {
		g.P("	b, err := s.accessRows(ctx, k, at, after, limit, true)")
		g.P("	if err != nil {")
		g.P("		return nil, err")
		g.P("	}")
		g.P("")
		g.P("	return mergeRows(out, b, limit), nil")
	} else {
		g.P("	return out, nil")
	}
	g.P("}")
	g.P("")

	g.P("// Heads is the same rows with only what a decision about them reads: no")
	g.P("// `value`, no `patch`, and nothing marshalled.")
	g.P("func (s trailStore) Heads(ctx ", pkgCtx.Ident("Context"), ", k ", pkgTrail.Ident("Scope"),
		", at ", pkgTime.Ident("Time"), ", after ", pkgTrail.Ident("Cursor"), ", limit int) (",
		pkgTrail.Ident("Rows"), ", error) {")
	g.P("	q, err := s.older(k, at, after, limit)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	vs, err := q.Select(")
	g.P("		", entPkg.Ident("FieldId"), ",")
	g.P("		", entPkg.Ident("FieldDateCreated"), ",")
	g.P("		", entPkg.Ident("FieldDomain"), ",")
	g.P("		", entPkg.Ident("FieldTenantId"), ",")
	g.P("		", entPkg.Ident("FieldActorTenantId"), ",")
	g.P("		", entPkg.Ident("FieldCounterpartTenantId"), ",")
	g.P("	).All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	out := make(", pkgTrail.Ident("Rows"), ", 0, len(vs))")
	g.P("	for _, v := range vs {")
	g.P("		out = append(out, ", pkgTrail.Ident("Row"), "{")
	g.P("			Key:     v.Id,")
	g.P("			Domain:  ", pkgPdid.Ident("Domain"), "(v.Domain),")
	g.P("			Created: v.DateCreated,")
	g.P("			Tenants: ", pkgTrail.Ident("TenantsOf"), "(v.TenantId, v.ActorTenantId, v.CounterpartTenantId),")
	g.P("		})")
	g.P("	}")
	g.P("")
	if acc != nil {
		g.P("	b, err := s.accessRows(ctx, k, at, after, limit, false)")
		g.P("	if err != nil {")
		g.P("		return nil, err")
		g.P("	}")
		g.P("")
		g.P("	return mergeRows(out, b, limit), nil")
	} else {
		g.P("	return out, nil")
	}
	g.P("}")
	g.P("")
	g.P("func (s trailStore) Count(ctx ", pkgCtx.Ident("Context"), ", k ", pkgTrail.Ident("Scope"),
		", at ", pkgTime.Ident("Time"), ") (int, error) {")
	if acc != nil {
		g.P("	n, err := s.of(k).Where(", entPkg.Ident("DateCreatedLT"), "(at)).Count(ctx)")
		g.P("	if err != nil {")
		g.P("		return n, err")
		g.P("	}")
		g.P("	if q := s.accessOf(k); q != nil {")
		g.P("		m, err := q.Where(", accPkg.Ident("DateCreatedLT"), "(at)).Count(ctx)")
		g.P("		return n + m, err")
		g.P("	}")
		g.P("")
		g.P("	return n, nil")
	} else {
		g.P("	return s.of(k).Where(", entPkg.Ident("DateCreatedLT"), "(at)).Count(ctx)")
	}
	g.P("}")
	g.P("")

	g.P("// Forget removes exactly the rows these keys name.")
	g.P("//")
	g.P("// By identifier and not by the cutoff again, which is the whole of what")
	g.P("// makes the archive trustworthy: a second query matches whatever is true")
	g.P("// when it runs, and a row backdated by a clock that stepped is one it")
	g.P("// removes and the archive does not have.")
	g.P("func (s trailStore) Forget(ctx ", pkgCtx.Ident("Context"), ", keys []any) (int, error) {")
	g.P("	ids, accessed, err := trailKeys(keys)")
	g.P("	if err != nil {")
	g.P("		return 0, err")
	g.P("	}")
	g.P("")
	g.P("	n, err := s.db.", e, ".Delete().Where(", entPkg.Ident("IdIn"), "(ids...)).Exec(ctx)")
	if acc != nil {
		g.P("	if err != nil || len(accessed) == 0 {")
		g.P("		return n, err")
		g.P("	}")
		g.P("")
		g.P("	m, err := s.db.", acc.GoName(), ".Delete().Where(", accPkg.Ident("IdIn"), "(accessed...)).Exec(ctx)")
		g.P("	return n + m, err")
	} else {
		g.P("	_ = accessed")
		g.P("	return n, err")
	}
	g.P("}")
	g.P("")
	g.P("// Blank empties the two columns that hold contents, for exactly these rows.")
	g.P("//")
	g.P("// Empty and not nil: nil is SQL NULL, which a NOT NULL column refuses on")
	g.P("// PostgreSQL and accepts on SQLite.")
	g.P("func (s trailStore) Blank(ctx ", pkgCtx.Ident("Context"), ", keys []any) (int, error) {")
	g.P("	ids, accessed, err := trailKeys(keys)")
	g.P("	if err != nil {")
	g.P("		return 0, err")
	g.P("	}")
	g.P("")
	g.P("	n, err := s.db.", e, ".Update().")
	g.P("		Where(", entPkg.Ident("IdIn"), "(ids...)).")
	g.P("		SetValue([]byte{}).")
	g.P("		SetPatch([]byte{}).")
	g.P("		Save(ctx)")
	if acc != nil {
		// An access row says what happened in its columns, and what it
		// carried in `value`; it is the second that goes.
		g.P("	if err != nil || len(accessed) == 0 {")
		g.P("		return n, err")
		g.P("	}")
		g.P("")
		g.P("	m, err := s.db.", acc.GoName(), ".Update().Where(", accPkg.Ident("IdIn"), "(accessed...)).SetValue([]byte{}).Save(ctx)")
		g.P("	return n + m, err")
	} else {
		g.P("	_ = accessed")
		g.P("	return n, err")
	}
	g.P("}")
	g.P("")
	g.P("// trailKeys is the keys this store gave out, back as what each table deletes")
	g.P("// by: the trail's, and the access record's.")
	g.P("func trailKeys(keys []any) (trailed, accessed []", pkgUuid.Ident("UUID"), ", err error) {")
	g.P("\tfor _, k := range keys {")
	g.P("\t\tswitch v := k.(type) {")
	g.P("\t\tcase ", pkgUuid.Ident("UUID"), ":")
	g.P("\t\t\ttrailed = append(trailed, v)")
	if acc != nil {
		g.P("\t\tcase accessKey:")
		g.P("\t\t\taccessed = append(accessed, ", pkgUuid.Ident("UUID"), "(v))")
	}
	g.P("\t\tdefault:")
	g.P("\t\t\treturn nil, nil, ", pkgFmt.Ident("Errorf"), "(\"trail: %T is not a key this store gave out\", k)")
	g.P("\t\t}")
	g.P("\t}")
	g.P("")
	g.P("\treturn trailed, accessed, nil")
	g.P("}")
	g.P("")
	g.P("// cursorOf is the identifier a key this store gave out holds, which is what")
	g.P("// a read carries on from in either table: both order by when a row was")
	g.P("// written, then by its identifier.")
	g.P("func cursorOf(k any) (", pkgUuid.Ident("UUID"), ", error) {")
	g.P("\tswitch v := k.(type) {")
	g.P("\tcase ", pkgUuid.Ident("UUID"), ":")
	g.P("\t\treturn v, nil")
	if acc != nil {
		g.P("\tcase accessKey:")
		g.P("\t\treturn ", pkgUuid.Ident("UUID"), "(v), nil")
	}
	g.P("\t}")
	g.P("")
	g.P("\treturn ", pkgUuid.Ident("Nil"), "(), ", pkgFmt.Ident("Errorf"), "(\"trail: %T is not a key this store gave out\", k)")
	g.P("}")
	g.P("")
	if acc != nil {
		emitAccessRows(g, p, acc, accPkg)
	}

	g.P("// numbers is what the column holds, which is a domain widened to what")
	g.P("// protobuf has: there is no `uint8`.")
	g.P("func numbers(ds []", pkgPdid.Ident("Domain"), ") []uint32 {")
	g.P("	out := make([]uint32, len(ds))")
	g.P("	for i, d := range ds {")
	g.P("		out[i] = uint32(d)")
	g.P("	}")
	g.P("")
	g.P("	return out")
	g.P("}")
	g.P("")

	emitManifest(g, s, p)
	emitTrailSeals(g, s, p)
	emitOwed(g, s, p)

	g.P("// TrailOf is a reader of the archive's documents as the messages they hold:")
	g.P("// what `policy.Read` and `policy.ReadTenant` are handed, and `trail.Read`,")
	g.P("// `trail.ReadTenant` and `trail.ReadFiles`, which check nothing.")
	g.P("//")
	g.P("//	err := policy.ReadTenant(ctx, store, tenant, pd.TrailOf(func(v *app.", e, ") error {")
	g.P("//		...")
	g.P("//	}))")
	g.P("//")
	g.P("// The runtime hands over documents, because it has no `", e, "` type to")
	g.P("// unmarshal into. This is the half that does.")
	g.P("//")
	g.P("// It opens no database, deliberately: what an archive is for is outliving")
	g.P("// the deployment that wrote it, and a reader that needed the deployment")
	g.P("// would be answering the question at the one moment nobody can.")
	g.P("func TrailOf(fn func(*", root.Ident(e), ") error) func(doc []byte) error {")
	g.P("	return func(doc []byte) error {")
	if acc != nil {
		// The access record's rows are in the same archive, and are
		// [AccessOf]'s to read.
		g.P("		if accessDoc(doc) {")
		g.P("			return nil")
		g.P("		}")
		g.P("")
	}
	g.P("		v := &", root.Ident(e), "{}")
	g.P("		if err := ", pkgProtojson.Ident("Unmarshal"), "(doc, v); err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("")
	g.P("		return fn(v)")
	g.P("	}")
	g.P("}")
	g.P("")
}

// emitAfter writes `q` narrowed to the rows after the one a cursor names, in
// the order every read that carries on from one goes in -- `date_created`,
// then the key -- where `created` and `key` are what the generated code holds
// the cursor's two in. Those reads are the trail store's, of the trail, the
// access record and the erasures owed, and a check of the rows reading the
// trail the other way round.
//
// # One comparison of the pair
//
// It was `date_created > a OR (date_created = a AND id > b)`, which is the same
// rows, and which no database starts an index's range at: an OR is a filter,
// so each page was read from the first entry the rest of the query allows --
// the kind's first line, or the tenant's -- up to the cursor, one page more
// than the page before. A read of the whole was quadratic. A check of the rows
// over a deployment's million lines of the trail took three minutes, with
// PostgreSQL a core busy throughout and the app waiting on it. The page from
// the middle of a kind of 647,926 lines read 323,964 of them only to throw
// them away, in 143 ms; it now reads one more than it answers, in 0.18 ms.
//
// The row value is the order said once, and PostgreSQL and SQLite both start
// the range at it. Every index a read of the trail or the access record by
// kind or by tenant uses ends in `date_created`, so there it starts at
// `date_created >= a` and filters what was written at that instant; `owed`'s
// ends in the key as well, and there it is the whole bound -- which is where
// it counts, because one `Owe` stamps every object it owes with one instant.
//
// Rejected: the OR kept, with `date_created >= a` in front of it. It is the
// first half of that and not the second -- the rows owed by one call read
// again from the first of them, page after page -- and it says the order three
// times.
func emitAfter(g *protogen.GeneratedFile, indent string, pkg protogen.GoImportPath, created, key string) {
	g.P(indent, "// After the cursor, as one comparison of the pair: an index starts the read")
	g.P(indent, "// at it, where the OR it means is a filter and every page is read from the")
	g.P(indent, "// first row.")
	g.P(indent, "q = q.Where(func(s *", pkgEntSql.Ident("Selector"), ") {")
	g.P(indent, "	s.Where(", pkgEntSql.Ident("CompositeGT"), "([]string{s.C(", pkg.Ident("FieldDateCreated"), "), s.C(", pkg.Ident("FieldId"), ")}, ",
		created, ", ", key, "))")
	g.P(indent, "})")
}

// emitManifest writes the database's half of the archive: [trail.Manifest] on
// payday's `Archived`, which is what tells the archive's blobs that should be
// there from the ones that should not.
//
// The judgement is in the runtime, as for the rest of the trail. What is here
// is rows: one per blob, moved from state to state a row at a time, each
// written whole beside the signature the runtime answers for it; and the one
// counter whose row lock numbers checkpoints in order.
func emitManifest(g *protogen.GeneratedFile, s *Schema, p Paths) {
	v := s.Own(pdpb.Own_OWN_ARCHIVED)
	e := v.GoName()
	pkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())
	field := func(name string) protogen.GoIdent { return pkg.Ident(name) }

	g.P("// archivedCounter is the manifest's one row that is not a blob: the number")
	g.P("// of the latest checkpoint, whose row lock is what keeps two checkpoints")
	g.P("// from taking one number.")
	g.P("var archivedCounter = ", pkgUuid.Ident("Nil"), "()")
	g.P("")
	g.P("func (s trailStore) Adding(ctx ", pkgCtx.Ident("Context"), ", ns, intent string, labels map[string]string, sign ", pkgTrail.Ident("Sign"), ") (any, error) {")
	g.P("	id := ", pkgPdid.Ident("New"), "(", e, "Domain).Uuid()")
	g.P("	sig, err := sign(", pkgTrail.Ident("Archived"), "{}, ", pkgTrail.Ident("Archived"), "{Key: id, Namespace: ns, Intent: intent, Labels: labels, State: ", pkgTrail.Ident("Adding"), "})")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	v, err := s.db.", e, ".Create().")
	g.P("		SetId(id).")
	g.P("		SetNamespace(ns).")
	g.P("		SetDigest(\"\").")
	g.P("		SetIntent(intent).")
	g.P("		SetLabels(labels).")
	g.P("		SetState(", pkgTrail.Ident("Adding"), ").")
	g.P("		SetSince(0).")
	g.P("		SetGone(0).")
	g.P("		SetSignature(sig.Sig).")
	g.P("		SetSignedBy(sig.Key).")
	g.P("		SetDateUpdated(", pkgTime.Ident("Now"), "()).")
	g.P("		Save(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return v.Id, nil")
	g.P("}")
	g.P("")
	g.P("func (s trailStore) Added(ctx ", pkgCtx.Ident("Context"), ", key any, digest string, labels map[string]string, sign ", pkgTrail.Ident("Sign"), ") error {")
	g.P("	id, ok := key.(", pkgUuid.Ident("UUID"), ")")
	g.P("	if !ok {")
	g.P("		return ", pkgFmt.Ident("Errorf"), "(\"trail: %T is not a key this store gave out\", key)")
	g.P("	}")
	g.P("")
	g.P("	v, err := s.db.", e, ".Get(ctx, id)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("")
	g.P("	was := archivedRow(v)")
	g.P("	is := was")
	g.P("	is.Digest, is.Labels, is.State = digest, labels, ", pkgTrail.Ident("Present"))
	g.P("")
	g.P("	return s.archivedWrite(ctx, id, was, is, sign)")
	g.P("}")
	g.P("")
	g.P("func (s trailStore) Drop(ctx ", pkgCtx.Ident("Context"), ", key any) error {")
	g.P("	id, ok := key.(", pkgUuid.Ident("UUID"), ")")
	g.P("	if !ok {")
	g.P("		return ", pkgFmt.Ident("Errorf"), "(\"trail: %T is not a key this store gave out\", key)")
	g.P("	}")
	g.P("")
	g.P("	_, err := s.db.", e, ".Delete().Where(", field("IdEQ"), "(id), ", field("StateEQ"), "(", pkgTrail.Ident("Adding"), ")).Exec(ctx)")
	g.P("	return err")
	g.P("}")
	g.P("")
	g.P("// Mark is a row at a time, each signed as it is about to be.")
	g.P("func (s trailStore) Mark(ctx ", pkgCtx.Ident("Context"), ", ns, digest, from, to string, sign ", pkgTrail.Ident("Sign"), ") error {")
	g.P("	vs, err := s.db.", e, ".Query().")
	g.P("		Where(", field("NamespaceEQ"), "(ns), ", field("DigestEQ"), "(digest), ", field("StateEQ"), "(from)).")
	g.P("		All(ctx)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("")
	g.P("	for _, v := range vs {")
	g.P("		was := archivedRow(v)")
	g.P("		is := was")
	g.P("		is.State = to")
	g.P("		if err := s.archivedWrite(ctx, v.Id, was, is, sign); err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")
	g.P("func (s trailStore) Label(ctx ", pkgCtx.Ident("Context"), ", ns, digest string, labels map[string]string, sign ", pkgTrail.Ident("Sign"), ") error {")
	g.P("	vs, err := s.db.", e, ".Query().")
	g.P("		Where(", field("NamespaceEQ"), "(ns), ", field("DigestEQ"), "(digest), ", field("StateNEQ"), "(", pkgTrail.Ident("Erased"), ")).")
	g.P("		All(ctx)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("")
	g.P("	for _, v := range vs {")
	g.P("		was := archivedRow(v)")
	g.P("		is := was")
	g.P("		is.Labels = labels")
	g.P("		if err := s.archivedWrite(ctx, v.Id, was, is, sign); err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")
	g.P("// archivedWrite writes one row whole -- every field its signature is over,")
	g.P("// and the signature -- so that what a row says is what was signed, whichever")
	g.P("// of two writers is last. Only while the row is still in the state it was")
	g.P("// read in: one another act moved meanwhile is that act's.")
	g.P("func (s trailStore) archivedWrite(ctx ", pkgCtx.Ident("Context"), ", id ", pkgUuid.Ident("UUID"), ", was, is ", pkgTrail.Ident("Archived"), ", sign ", pkgTrail.Ident("Sign"), ") error {")
	g.P("	sig, err := sign(was, is)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("")
	g.P("	_, err = s.db.", e, ".Update().")
	g.P("		Where(", field("IdEQ"), "(id), ", field("StateEQ"), "(was.State)).")
	g.P("		SetDigest(is.Digest).")
	g.P("		SetLabels(is.Labels).")
	g.P("		SetState(is.State).")
	g.P("		SetSignature(sig.Sig).")
	g.P("		SetSignedBy(sig.Key).")
	g.P("		SetDateUpdated(", pkgTime.Ident("Now"), "()).")
	g.P("		Save(ctx)")
	g.P("	return err")
	g.P("}")
	g.P("")
	g.P("func (s trailStore) Rows(ctx ", pkgCtx.Ident("Context"), ") ([]", pkgTrail.Ident("Archived"), ", error) {")
	g.P("	vs, err := s.db.", e, ".Query().")
	g.P("		Where(", field("NamespaceNEQ"), "(\"\"), ", field("StateNEQ"), "(", pkgTrail.Ident("Erased"), ")).")
	g.P("		All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return archivedRows(vs), nil")
	g.P("}")
	g.P("")
	g.P("// Checkpoint is one transaction, and the counter's row is the first thing it")
	g.P("// writes: a second checkpoint waits on that row until the first has")
	g.P("// committed, and then takes the next number.")
	g.P("func (s trailStore) Checkpoint(ctx ", pkgCtx.Ident("Context"), ", after int) (int, []", pkgTrail.Ident("Archived"), ", error) {")
	g.P("	tx, err := s.db.Tx(ctx)")
	g.P("	if err != nil {")
	g.P("		return 0, nil, err")
	g.P("	}")
	g.P("	defer tx.Rollback()")
	g.P("")
	g.P("	n, err := tx.", e, ".Update().Where(", field("IdEQ"), "(archivedCounter)).AddSince(1).SetDateUpdated(", pkgTime.Ident("Now"), "()).Save(ctx)")
	g.P("	if err != nil {")
	g.P("		return 0, nil, err")
	g.P("	}")
	g.P("	if n == 0 {")
	g.P("		// The first. Two at once both try, and one is refused for the key.")
	g.P("		if err := tx.", e, ".Create().")
	g.P("			SetId(archivedCounter).")
	g.P("			SetNamespace(\"\").")
	g.P("			SetDigest(\"\").")
	g.P("			SetIntent(\"\").")
	g.P("			SetLabels(map[string]string{}).")
	g.P("			SetState(\"counter\").")
	g.P("			SetSince(1).")
	g.P("			SetGone(0).")
	g.P("			SetDateUpdated(", pkgTime.Ident("Now"), "()).")
	g.P("			Exec(ctx); err != nil {")
	g.P("			return 0, nil, err")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	c, err := tx.", e, ".Get(ctx, archivedCounter)")
	g.P("	if err != nil {")
	g.P("		return 0, nil, err")
	g.P("	}")
	g.P("	k := c.Since")
	g.P("")
	g.P("	// Past `after` as well: an acceptance of a database restored from before")
	g.P("	// checkpoints the store keeps.")
	g.P("	if int64(after) >= int64(k) {")
	g.P("		if int64(after) >= 1<<32-1 {")
	g.P("			return 0, nil, ", pkgFmt.Ident("Errorf"), "(\"checkpoint %d: past what the manifest numbers\", after)")
	g.P("		}")
	g.P("		k = uint32(after) + 1")
	g.P("		if _, err := tx.", e, ".Update().Where(", field("IdEQ"), "(archivedCounter)).SetSince(k).Save(ctx); err != nil {")
	g.P("			return 0, nil, err")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	// In it from now on: what was added since the last one.")
	g.P("	if _, err := tx.", e, ".Update().")
	g.P("		Where(", field("NamespaceNEQ"), "(\"\"), ", field("StateIn"), "(", pkgTrail.Ident("Present"), ", ", pkgTrail.Ident("Erasing"), "), ", field("SinceEQ"), "(0)).")
	g.P("		SetSince(k).")
	g.P("		Save(ctx); err != nil {")
	g.P("		return 0, nil, err")
	g.P("	}")
	g.P("")
	g.P("	// Out of it from now on: what was erased since.")
	g.P("	if _, err := tx.", e, ".Update().")
	g.P("		Where(", field("NamespaceNEQ"), "(\"\"), ", field("StateEQ"), "(", pkgTrail.Ident("Erased"), "), ", field("GoneEQ"), "(0), ", field("SinceGT"), "(0)).")
	g.P("		SetGone(k).")
	g.P("		Save(ctx); err != nil {")
	g.P("		return 0, nil, err")
	g.P("	}")
	g.P("")
	g.P("	// And what no checkpoint read back any more was in: erased before one")
	g.P("	// saw it, or gone before the one before this.")
	g.P("	if _, err := tx.", e, ".Delete().")
	g.P("		Where(")
	g.P("			", field("NamespaceNEQ"), "(\"\"),")
	g.P("			", field("StateEQ"), "(", pkgTrail.Ident("Erased"), "),")
	g.P("			", field("Or"), "(", field("SinceEQ"), "(0), ", field("And"), "(", field("GoneGT"), "(0), ", field("GoneLT"), "(k))),")
	g.P("		).")
	g.P("		Exec(ctx); err != nil {")
	g.P("		return 0, nil, err")
	g.P("	}")
	g.P("")
	g.P("	vs, err := tx.", e, ".Query().Where(archivedAt(k)).All(ctx)")
	g.P("	if err != nil {")
	g.P("		return 0, nil, err")
	g.P("	}")
	g.P("")
	g.P("	return int(k), archivedRows(vs), tx.Commit()")
	g.P("}")
	g.P("")
	g.P("func (s trailStore) At(ctx ", pkgCtx.Ident("Context"), ", k int) ([]", pkgTrail.Ident("Archived"), ", error) {")
	g.P("	vs, err := s.db.", e, ".Query().Where(archivedAt(uint32(k))).All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return archivedRows(vs), nil")
	g.P("}")
	g.P("")
	g.P("func (s trailStore) Latest(ctx ", pkgCtx.Ident("Context"), ") (int, ", pkgTime.Ident("Time"), ", error) {")
	g.P("	c, err := s.db.", e, ".Get(ctx, archivedCounter)")
	g.P("	if ", p.Ent.Ident("IsNotFound"), "(err) {")
	g.P("		return 0, ", pkgTime.Ident("Time"), "{}, nil")
	g.P("	}")
	g.P("	if err != nil {")
	g.P("		return 0, ", pkgTime.Ident("Time"), "{}, err")
	g.P("	}")
	g.P("")
	g.P("	return int(c.Since), c.DateUpdated, nil")
	g.P("}")
	g.P("")
	g.P("// archivedAt is the rows a checkpoint was taken over: in it by then, and")
	g.P("// not gone by then.")
	g.P("func archivedAt(k uint32) ", (p.Ent + "/predicate").Ident(e), " {")
	g.P("	return ", field("And"), "(")
	g.P("		", field("NamespaceNEQ"), "(\"\"),")
	g.P("		", field("SinceGT"), "(0),")
	g.P("		", field("SinceLTE"), "(k),")
	g.P("		", field("Or"), "(", field("GoneEQ"), "(0), ", field("GoneGT"), "(k)),")
	g.P("	)")
	g.P("}")
	g.P("")
	g.P("func archivedRow(v *", p.Ent.Ident(e), ") ", pkgTrail.Ident("Archived"), " {")
	g.P("	return ", pkgTrail.Ident("Archived"), "{")
	g.P("		Key:       v.Id,")
	g.P("		Namespace: v.Namespace,")
	g.P("		Digest:    v.Digest,")
	g.P("		Intent:    v.Intent,")
	g.P("		Labels:    v.Labels,")
	g.P("		State:     v.State,")
	g.P("		Since:     int(v.Since),")
	g.P("		Gone:      int(v.Gone),")
	g.P("		Created:   v.DateCreated,")
	g.P("		Updated:   v.DateUpdated,")
	g.P("		Signature: ", pkgTrail.Ident("Signature"), "{Key: v.SignedBy, Sig: v.Signature},")
	g.P("	}")
	g.P("}")
	g.P("")
	g.P("func archivedRows(vs []*", p.Ent.Ident(e), ") []", pkgTrail.Ident("Archived"), " {")
	g.P("	out := make([]", pkgTrail.Ident("Archived"), ", 0, len(vs))")
	g.P("	for _, v := range vs {")
	g.P("		out = append(out, archivedRow(v))")
	g.P("	}")
	g.P("")
	g.P("	return out")
	g.P("}")
	g.P("")
}
