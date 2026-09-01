package pdgen

import (
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
)

var (
	pkgTrail     = protogen.GoImportPath("github.com/lesomnus/payday/trail")
	pkgProtojson = protogen.GoImportPath("google.golang.org/protobuf/encoding/protojson")
)

// EmitTrail writes the app's half of the retention policy: the audit table, as
// much of it as `trail` needs to move rows out of the database.
//
// It is generated rather than written in the runtime for the reason the outbox
// drain is -- `ent.Client` and its predicates are the app's types and payday
// cannot name them, and payday has no Go type for its own `Audit` either, since
// the schema is **copied** into each app and generated there.
//
// What is not generated is any judgement. There is none here beyond *read a
// batch of these kinds older than this, and forget the ones I name*: the two
// clocks, the refusal to destroy what was never written, the archive's layout
// and what may be purged are all in the runtime, where they can be argued with
// once instead of per app.
//
// The document that travels between the two halves is protojson, which is the
// archive's format anyway -- *readable by anything that can read an `Audit`*.
func EmitTrail(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	// Refused by [CheckOwn] before this, for a real app; see there.
	if !s.Has(pdpb.Own_OWN_AUDIT) {
		return
	}

	v := s.Own(pdpb.Own_OWN_AUDIT)
	e := v.GoName()
	entPkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())

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

	g.P("// of narrows to the kinds a pass is about.")
	g.P("//")
	g.P("// By the `domain` column and not by the domain byte inside `object_id`,")
	g.P("// which carries the same fact and cannot be indexed: *what kind was this")
	g.P("// row about* is answered by reading a row, and *which rows were about")
	g.P("// robots* is a query over a set. The second is what a retention policy is")
	g.P("// made of.")
	g.P("func (s trailStore) of(k ", pkgTrail.Ident("Kinds"), ") *", p.Ent.Ident(e+"Query"), " {")
	g.P("	q := s.db.", e, ".Query()")
	g.P("	if len(k.Only) > 0 {")
	g.P("		return q.Where(", entPkg.Ident("DomainIn"), "(numbers(k.Only)...))")
	g.P("	}")
	g.P("	if len(k.Except) > 0 {")
	g.P("		return q.Where(", entPkg.Ident("DomainNotIn"), "(numbers(k.Except)...))")
	g.P("	}")
	g.P("")
	g.P("	return q")
	g.P("}")
	g.P("")

	g.P("func (s trailStore) Older(ctx ", pkgCtx.Ident("Context"), ", k ", pkgTrail.Ident("Kinds"),
		", at ", pkgTime.Ident("Time"), ", limit int) (", pkgTrail.Ident("Rows"), ", error) {")
	g.P("	vs, err := s.of(k).")
	g.P("		Where(", entPkg.Ident("DateCreatedLT"), "(at)).")
	g.P("		Order(", p.Ent.Ident("Asc"), "(", entPkg.Ident("FieldDateCreated"),
		", ", entPkg.Ident("FieldId"), ")).")
	g.P("		Limit(limit).")
	g.P("		All(ctx)")
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
	g.P("		})")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	g.P("func (s trailStore) Count(ctx ", pkgCtx.Ident("Context"), ", k ", pkgTrail.Ident("Kinds"),
		", at ", pkgTime.Ident("Time"), ") (int, error) {")
	g.P("	return s.of(k).Where(", entPkg.Ident("DateCreatedLT"), "(at)).Count(ctx)")
	g.P("}")
	g.P("")

	g.P("// Forget removes exactly the rows these keys name.")
	g.P("//")
	g.P("// By identifier and not by the cutoff again, which is the whole of what")
	g.P("// makes the archive trustworthy: a second query matches whatever is true")
	g.P("// when it runs, and a row backdated by a clock that stepped is one it")
	g.P("// removes and the file does not have.")
	g.P("func (s trailStore) Forget(ctx ", pkgCtx.Ident("Context"), ", keys []any) (int, error) {")
	g.P("	ids := make([]", pkgUuid.Ident("UUID"), ", 0, len(keys))")
	g.P("	for _, k := range keys {")
	g.P("		v, ok := k.(", pkgUuid.Ident("UUID"), ")")
	g.P("		if !ok {")
	g.P("			return 0, ", pkgFmt.Ident("Errorf"), "(\"trail: %T is not a key this store gave out\", k)")
	g.P("		}")
	g.P("")
	g.P("		ids = append(ids, v)")
	g.P("	}")
	g.P("")
	g.P("	return s.db.", e, ".Delete().Where(", entPkg.Ident("IdIn"), "(ids...)).Exec(ctx)")
	g.P("}")
	g.P("")

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

	g.P("// ForgetInTrail blanks the contents of every trail row about one of these")
	g.P("// objects, and answers how many it changed.")
	g.P("//")
	g.P("// # What it takes out")
	g.P("//")
	g.P("// `value` and `patch`, which are the two columns that hold contents.")
	g.P("// Everything else -- who acted, what they did, which object, when -- stays,")
	g.P("// and stays on purpose: that is the record a trail exists to be, and it is")
	g.P("// what a legal-obligation exemption is an exemption *for*. What is destroyed")
	g.P("// is what the row said about somebody; what survives is that it happened.")
	g.P("//")
	g.P("// The actor is not touched. It is an identifier, and it is personal data only")
	g.P("// because it **resolves** -- a property of the row it points at rather than of")
	g.P("// this one. A caller that has destroyed the person's own record has already")
	g.P("// made it a pseudonym reaching nothing, and blanking it here would destroy")
	g.P("// *who did this*, which is the whole of what a trail is for.")
	g.P("//")
	g.P("// # And why payday offers it at all")
	g.P("//")
	g.P("// **Which** rows, and **when**, is the app's -- what it owes a person and")
	g.P("// under what regime is not a thing a framework can know, which is why")
	g.P("// `docs/runtime.md` lists erasing a subject among the things payday does not")
	g.P("// do. This is the other half: two columns of payday's own table, blanked for")
	g.P("// a set the caller chose. There is no judgement in it, which is the same line")
	g.P("// `internal/pdgen/outbox.go` draws about the drain.")
	g.P("//")
	g.P("// The archive is `trail.Forget`, and a caller that keeps one has to call both:")
	g.P("// a mechanism that stopped at the database would destroy the copy an operator")
	g.P("// can see and leave the copy on the disk beside it.")
	g.P("func ForgetInTrail(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"),
		", objects []", pkgPdid.Ident("Id"), ") (int, error) {")
	g.P("	if len(objects) == 0 {")
	g.P("		return 0, nil")
	g.P("	}")
	g.P("")
	g.P("	ids := make([]", pkgUuid.Ident("UUID"), ", len(objects))")
	g.P("	for i, v := range objects {")
	g.P("		ids[i] = v.Uuid()")
	g.P("	}")
	g.P("")
	// Empty and not nil, for the reason the recorder's `notNull` gives: nil is
	// Sql NULL, which a NOT NULL column refuses on Postgres and accepts on
	// SQLite.
	g.P("	return db.", e, ".Update().")
	g.P("		Where(", entPkg.Ident("ObjectIdIn"), "(ids...)).")
	g.P("		SetValue([]byte{}).")
	g.P("		SetPatch([]byte{}).")
	g.P("		Save(ctx)")
	g.P("}")
	g.P("")

	g.P("// ReadTrail is an archive read back as the messages it holds.")
	g.P("//")
	g.P("// The runtime hands over documents, because it has no `", e, "` type to")
	g.P("// unmarshal into. This is the half that does.")
	g.P("//")
	g.P("// It opens no database, deliberately: what an archive is for is outliving")
	g.P("// the deployment that wrote it, and a reader that needed the deployment")
	g.P("// would be answering the question at the one moment nobody can.")
	g.P("func ReadTrail(paths []string, fn func(*", root.Ident(e), ") error) error {")
	g.P("	return ", pkgTrail.Ident("Read"), "(paths, func(doc []byte) error {")
	g.P("		v := &", root.Ident(e), "{}")
	g.P("		if err := ", pkgProtojson.Ident("Unmarshal"), "(doc, v); err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("")
	g.P("		return fn(v)")
	g.P("	})")
	g.P("}")
	g.P("")
}
