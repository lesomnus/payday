package pdgen

import (
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
)

var (
	pkgSql    = protogen.GoImportPath("database/sql")
	pkgErrors = protogen.GoImportPath("errors")
	pkgMaps   = protogen.GoImportPath("maps")
)

// emitDigests writes `Digests`, the check of the live rows against the trail
// (payday#102, #108), and `Record`, the one way onto the trail that is not a
// write through the servers: every row of every kind on the trail -- written
// through the servers, or by the app and recorded -- read a page at a time the
// way a Get reads it, beside the newest row of the trail about it, in the
// database and in the archive for what has left.
//
// Comparing is the audit package's (`audit.CheckKind`), and reading the archive
// the trail package's (`trail.Newest`). What only generation knows is the
// kinds, their tables, which of their rows are live, and the trail's own table
// -- which is all this writes.
func emitDigests(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	a := s.Own(pdpb.Own_OWN_AUDIT)
	if a == nil {
		return
	}
	trailPkg := p.Ent + "/" + protogen.GoImportPath(a.EntPkg())
	pred := p.Ent + "/predicate"
	archive := s.Has(pdpb.Own_OWN_ARCHIVED)

	// Every kind but the ones kept off the trail, and payday's own tables of
	// the trail, the outbox and the archive, which are written beneath it: a
	// kind a server writes, which the recorder sees, and a kind the app writes
	// and records (`trail: {recorded: {}}`). See [Entity.Checked].
	var kinds []*Entity
	for _, v := range s.Sorted() {
		if v.Checked() {
			kinds = append(kinds, v)
		}
	}

	g.P("// Digests checks every live row of every kind on the trail -- every kind but")
	g.P("// the ones kept off it -- against the trail (payday#102, #108): each digested")
	g.P("// again, as a Get reads it, and compared with the digest the newest row of the trail about it")
	g.P("// keeps -- in the database, or in the archive `p` names once it has left. A")
	g.P("// row somebody changed past the servers is one that differs, and a row")
	g.P("// somebody added past them is one nothing on the trail is about.")
	g.P("//")
	g.P("// It changes nothing. It reads a page of a kind at a time, each page with the")
	g.P("// trail about it in one read-only transaction, so `db` is the database and")
	g.P("// not a transaction. With no archive, what is not in the database is not on")
	g.P("// the trail.")
	g.P("func Digests(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", d *", pkgAudit.Ident("Digest"), ", p ", pkgTrail.Ident("Policy"), ") (", pkgAudit.Ident("Rows"), ", error) {")
	g.P("	var archived ", pkgAudit.Ident("Archived"))
	g.P("	if p.Archive != nil {")
	if archive {
		g.P("		n, err := p.Newest(ctx, TrailStore(db))")
		g.P("		if err != nil {")
		g.P("			return ", pkgAudit.Ident("Rows"), "{}, ", pkgFmt.Ident("Errorf"), "(\"the archive: %w\", err)")
		g.P("		}")
		g.P("		archived = archivedDigests(n)")
	} else {
		g.P("		return ", pkgAudit.Ident("Rows"), "{}, ", pkgErrors.Ident("New"), "(\"this app keeps no manifest of an archive to read it by\")")
	}
	g.P("	}")
	g.P("")
	g.P("	out := ", pkgAudit.Ident("Rows"), "{}")
	for _, v := range kinds {
		g.P("	if err := ", pkgAudit.Ident("CheckKind"), "(ctx, d, checked", v.GoName(), "(db), archived, &out); err != nil {")
		g.P("		return out, ", pkgFmt.Ident("Errorf"), "(\"", v.Name, ": %w\", err)")
		g.P("	}")
	}
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	for _, v := range kinds {
		e := v.GoName()
		entPkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())

		g.P("// checked", e, " is what a check of the rows reads of the ", v.Name, " kind.")
		g.P("func checked", e, "(db *", p.Ent.Ident("Client"), ") ", pkgAudit.Ident("Kind"), " {")
		g.P("	read := func(ctx ", pkgCtx.Ident("Context"), ", ps ...", pred.Ident(e), ") (", pkgAudit.Ident("Page"), ", error) {")
		g.P("		var out ", pkgAudit.Ident("Page"))
		g.P("		err := snapshot(ctx, db, func(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ") error {")
		g.P("			rows, err := live", e, "(ctx, db, ps...)")
		g.P("			if err != nil {")
		g.P("				return err")
		g.P("			}")
		g.P("")
		g.P("			recorded, err := newestDigests(ctx, db, rows)")
		g.P("			out = ", pkgAudit.Ident("Page"), "{Rows: rows, Recorded: recorded}")
		g.P("			return err")
		g.P("		})")
		g.P("")
		g.P("		return out, err")
		g.P("	}")
		g.P("")
		g.P("	return ", pkgAudit.Ident("Kind"), "{")
		g.P("		Domain: ", e, "Domain,")
		g.P("		After: func(ctx ", pkgCtx.Ident("Context"), ", after ", pkgPdid.Ident("Id"), ") (", pkgAudit.Ident("Page"), ", error) {")
		g.P("			return read(ctx, ", entPkg.Ident("IdGT"), "(", pkgUuid.Ident("UUID"), "(after)))")
		g.P("		},")
		g.P("		Of: func(ctx ", pkgCtx.Ident("Context"), ", ids []", pkgPdid.Ident("Id"), ") (", pkgAudit.Ident("Page"), ", error) {")
		g.P("			return read(ctx, ", entPkg.Ident("IdIn"), "(uuids(ids)...))")
		g.P("		},")
		g.P("	}")
		g.P("}")
		g.P("")

		g.P("// live", e, " is the live rows `ps` says of the ", v.Name, " kind, at most a page of")
		g.P("// them in identifier order, each as a Get reads it.")
		g.P("func live", e, "(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", ps ...", pred.Ident(e), ") ([]", pkgAudit.Ident("Live"), ", error) {")
		if v.HasErasedField() {
			// Live, which is what the newest row of the trail about it is a
			// write's and not an Erase's.
			g.P("	q := db.", e, ".Query().Where(append(ps, ", entPkg.Ident(pascal(v.GetErasedField().Name())+"IsNil"), "())...)")
		} else {
			g.P("	q := db.", e, ".Query().Where(ps...)")
		}
		if v.Rpcs().HasGet() {
			// As its Get reads it, which is what the recorder digests: the
			// row a write handed over is that Get's.
			g.P("	", p.Bare.Ident(e+"SelectInit"), "(q, nil)")
		}
		g.P("")
		g.P("	vs, err := q.Order(", entPkg.Ident("ById"), "()).Limit(", pkgAudit.Ident("RowsPage"), ").All(ctx)")
		g.P("	if err != nil {")
		g.P("		return nil, err")
		g.P("	}")
		g.P("")
		g.P("	out := make([]", pkgAudit.Ident("Live"), ", len(vs))")
		g.P("	for i, v := range vs {")
		g.P("		out[i] = ", pkgAudit.Ident("Live"), "{Object: ", pkgPdid.Ident("Id"), "(v.Id), Row: v.Proto()}")
		g.P("	}")
		g.P("")
		g.P("	return out, nil")
		g.P("}")
		g.P("")
	}

	// One moment of the database for a page and the trail about it. Without
	// it a write between the two reads is a row that differs from its own
	// trail -- found by nobody's fault, and paged about all the same.
	g.P("// snapshot runs `fn` on one read-only transaction, so that a page of rows and")
	g.P("// the trail about them are read at one moment of the database: REPEATABLE")
	g.P("// READ on PostgreSQL, and what every SQLite transaction already is.")
	g.P("func snapshot(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", fn func(", pkgCtx.Ident("Context"), ", *", p.Ent.Ident("Client"), ") error) error {")
	g.P("	opts := &", pkgSql.Ident("TxOptions"), "{ReadOnly: true}")
	g.P("	if db.Dialect() == ", pkgDialect.Ident("Postgres"), " {")
	g.P("		opts.Isolation = ", pkgSql.Ident("LevelRepeatableRead"))
	g.P("	}")
	g.P("")
	g.P("	tx, err := db.BeginTx(ctx, opts)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("	defer tx.Rollback()")
	g.P("")
	g.P("	return fn(ctx, tx.Client())")
	g.P("}")
	g.P("")

	g.P("// newestDigests is what the newest row of the trail in the database about")
	g.P("// each of `rows` keeps of its digest: the rows about them read newest first,")
	g.P("// and the first of each thing's kept.")
	g.P("func newestDigests(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", rows []", pkgAudit.Ident("Live"), ") (map[", pkgPdid.Ident("Id"), "]", pkgAudit.Ident("Recorded"), ", error) {")
	g.P("	out := map[", pkgPdid.Ident("Id"), "]", pkgAudit.Ident("Recorded"), "{}")
	g.P("	if len(rows) == 0 {")
	g.P("		return out, nil")
	g.P("	}")
	g.P("")
	g.P("	ks := make([]", pkgUuid.Ident("UUID"), ", len(rows))")
	g.P("	for i, v := range rows {")
	g.P("		ks[i] = ", pkgUuid.Ident("UUID"), "(v.Object)")
	g.P("	}")
	g.P("")
	g.P("	vs, err := db.", a.GoName(), ".Query().")
	g.P("		Where(", trailPkg.Ident("ObjectIdIn"), "(ks...)).")
	g.P("		Order(", trailPkg.Ident("ByDateCreated"), "(", pkgEntSql.Ident("OrderDesc"), "()), ", trailPkg.Ident("ById"), "(", pkgEntSql.Ident("OrderDesc"), "())).")
	g.P("		Select(", trailPkg.Ident("FieldObjectId"), ", ", trailPkg.Ident("FieldDigest"), ", ", trailPkg.Ident("FieldDigestFields"), ").")
	g.P("		All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	for _, v := range vs {")
	g.P("		k := ", pkgPdid.Ident("Id"), "(v.ObjectId)")
	g.P("		if _, ok := out[k]; !ok {")
	g.P("			out[k] = ", pkgAudit.Ident("Recorded"), "{Digest: v.Digest, Fields: v.DigestFields}")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	if archive {
		g.P("// archivedDigests is what the newest row of the archive about each thing")
		g.P("// keeps, as a check of the rows asks for it; see [trail.Newest].")
		g.P("func archivedDigests(n *", pkgTrail.Ident("Newest"), ") ", pkgAudit.Ident("Archived"), " {")
		g.P("	return func(ctx ", pkgCtx.Ident("Context"), ", kind ", pkgPdid.Ident("Domain"), ", ids []", pkgPdid.Ident("Id"), ") (map[", pkgPdid.Ident("Id"), "]", pkgAudit.Ident("Recorded"), ", error) {")
		g.P("		vs, err := n.Of(ctx, kind, ids)")
		g.P("		if err != nil {")
		g.P("			return nil, err")
		g.P("		}")
		g.P("")
		g.P("		out := make(map[", pkgPdid.Ident("Id"), "]", pkgAudit.Ident("Recorded"), ", len(vs))")
		g.P("		for k, v := range vs {")
		g.P("			out[k] = ", pkgAudit.Ident("Recorded"), "{Digest: v.Digest, Fields: v.DigestFields}")
		g.P("		}")
		g.P("")
		g.P("		return out, nil")
		g.P("	}")
		g.P("}")
		g.P("")
	}

	g.P("// uuids is identifiers as ent's predicates take them.")
	g.P("func uuids(ids []", pkgPdid.Ident("Id"), ") []", pkgUuid.Ident("UUID"), " {")
	g.P("	out := make([]", pkgUuid.Ident("UUID"), ", len(ids))")
	g.P("	for i, v := range ids {")
	g.P("		out[i] = ", pkgUuid.Ident("UUID"), "(v)")
	g.P("	}")
	g.P("")
	g.P("	return out")
	g.P("}")
	g.P("")

	emitRecord(g, s, p, root, kinds)
}

// emitRecord writes `Record`: rows an app wrote beneath the servers on purpose,
// put on the trail as the act that wrote them.
//
// # Why there is a way past the recorder at all
//
// Because there are writes no server makes, and they are not mistakes. A
// workspace put back from its export is written as it was, under its own
// identifiers, and the trail it came with is another deployment's history,
// which this one's checkpoints do not vouch for. A migration rewrites a column
// in every row. Unrecorded, each is a row nothing on the trail is about -- the
// same answer a check gives for a row somebody **added** past the servers --
// and a check that has to be told which of those is fine is one nobody reads.
//
// So the act is recorded instead: one row of the trail for each row it wrote,
// saying who put it there, when, and by what, and keeping the digest of the row
// as it is now. Not what it holds: the export holds that, or the migration,
// and a copy of every row of a workspace in the one table nothing rewrites is
// the copy #92 took out of the trail.
//
// # Read as a check reads it
//
// The digest is of the row read back as a Get reads it, by the same query the
// check reads it with, rather than of whatever the caller wrote -- a timestamp
// the database rounds, an edge it holds as a key, is then the same in both.
func emitRecord(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath, kinds []*Entity) {
	g.P("// Record puts on the trail rows that were written beneath the servers on")
	g.P("// purpose -- a workspace put back from its export, rows a migration rewrote")
	g.P("// and somebody looked at -- as the act `action` names (payday#108): one row of")
	g.P("// the trail for each, by whoever `ctx` is framed as and filed under the row's")
	g.P("// own tenant, keeping the digest of the row as a Get reads it now and nothing")
	g.P("// of what it holds. A check of the rows then finds each as it is now, and the")
	g.P("// trail says who put it there, when, and by what.")
	g.P("//")
	g.P("// It is the one way onto the trail that is not a write through the servers,")
	g.P("// and it writes nothing but the trail. `db` is the transaction that wrote the")
	g.P("// rows, or the database. A row of a kind kept off the trail (`trail: {off:")
	g.P("// ...}}`) is passed over; one that is no live row is an error. It answers")
	g.P("// how many it recorded.")
	g.P("func Record(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", content ", pkgAudit.Ident("Contents"), ", action string, ids []", pkgPdid.Ident("Id"), ") (int, error) {")
	g.P("	if action == \"\" {")
	g.P("		return 0, ", pkgErrors.Ident("New"), "(\"an act on the trail is named\")")
	g.P("	}")
	g.P("")
	g.P("	s, err := ", p.Bare.Ident("NewServer"), "(db)")
	g.P("	if err != nil {")
	g.P("		return 0, err")
	g.P("	}")
	g.P("")
	g.P("	ids = ", pkgSlices.Ident("Clone"), "(ids)")
	g.P("	", pkgSlices.Ident("SortFunc"), "(ids, func(a, b ", pkgPdid.Ident("Id"), ") int { return ", pkgUuid.Ident("UUID"), "(a).Compare(", pkgUuid.Ident("UUID"), "(b)) })")
	g.P("	ids = ", pkgSlices.Ident("Compact"), "(ids)")
	g.P("")
	g.P("	byKind := map[", pkgPdid.Ident("Domain"), "][]", pkgPdid.Ident("Id"), "{}")
	g.P("	for _, v := range ids {")
	g.P("		byKind[v.Domain()] = append(byKind[v.Domain()], v)")
	g.P("	}")
	g.P("")
	g.P("	n := 0")
	g.P("	for _, d := range ", pkgSlices.Ident("Sorted"), "(", pkgMaps.Ident("Keys"), "(byKind)) {")
	g.P("		for page := range ", pkgSlices.Ident("Chunk"), "(byKind[d], ", pkgAudit.Ident("RowsPage"), ") {")
	g.P("			var rows []", pkgAudit.Ident("Live"))
	g.P("			switch d {")
	for _, v := range kinds {
		e := v.GoName()
		entPkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())
		g.P("			case ", e, "Domain:")
		g.P("				rows, err = live", e, "(ctx, db, ", entPkg.Ident("IdIn"), "(uuids(page)...))")
	}
	g.P("			default:")
	g.P("				// Kept off the trail, so no act on it is recorded.")
	g.P("				continue")
	g.P("			}")
	g.P("			if err != nil {")
	g.P("				return n, err")
	g.P("			}")
	g.P("			if len(rows) != len(page) {")
	g.P("				return n, ", pkgFmt.Ident("Errorf"), "(\"%s is no live row\", unread(page, rows))")
	g.P("			}")
	g.P("")
	g.P("			if err := recordRows(ctx, db, s, action, content, rows); err != nil {")
	g.P("				return n, err")
	g.P("			}")
	g.P("			n += len(rows)")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return n, nil")
	g.P("}")
	g.P("")

	g.P("// unread is the first of `ids` that `rows` does not have.")
	g.P("func unread(ids []", pkgPdid.Ident("Id"), ", rows []", pkgAudit.Ident("Live"), ") ", pkgPdid.Ident("Id"), " {")
	g.P("	for _, id := range ids {")
	g.P("		if !", pkgSlices.Ident("ContainsFunc"), "(rows, func(v ", pkgAudit.Ident("Live"), ") bool { return v.Object == id }) {")
	g.P("			return id")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return ", pkgPdid.Ident("Nil"))
	g.P("}")
	g.P("")

	a := s.Own(pdpb.Own_OWN_AUDIT)
	g.P("// recordRows writes the row of the trail [Record] says for each of `rows`.")
	g.P("func recordRows(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", s ", p.Bare.Ident("Server"), ", action string, content ", pkgAudit.Ident("Contents"), ", rows []", pkgAudit.Ident("Live"), ") error {")
	if keepsTrail(s) {
		g.P("	qs := make([]*", p.Ent.Ident(a.GoName()+"Create"), ", 0, len(rows))")
	}
	g.P("	for _, v := range rows {")
	g.P("		md, secret, personal := described(v.Object)")
	g.P("		digest, covered := []byte{}, []uint32{}")
	g.P("		if d := content.Digest; d != nil && md != nil {")
	g.P("			covered = ", pkgAudit.Ident("Covered"), "(md, ", pkgSlices.Ident("Concat"), "(secret, personal))")
	g.P("")
	g.P("			var err error")
	g.P("			if digest, err = d.Of(v.Object, v.Row, covered); err != nil {")
	g.P("				return ", pkgFmt.Ident("Errorf"), "(\"the trail of %s: %w\", action, err)")
	g.P("			}")
	g.P("		}")
	g.P("")
	g.P("		w, err := ", pkgAudit.Ident("Of"), "(ctx, action, v.Object, nil)")
	g.P("		if err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("")
	g.P("		// Filed where the recorder files a write of it: under its tenant, or")
	g.P("		// the actor's when it is behind no wall.")
	g.P("		tenant, _, err := subject(ctx, s, v.Object, v.Row, false)")
	g.P("		if err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("		if tenant == ", pkgUuid.Ident("Nil"), "() {")
	g.P("			tenant = ", pkgUuid.Ident("UUID"), "(w.Tenant)")
	g.P("		}")
	g.P("		domain := uint32(v.Object.Domain())")
	g.P("")
	if keepsTrail(s) {
		g.P("		q := db.", a.GoName(), ".Create().")
		g.P("			SetId(", pkgUuid.Ident("New"), "()).")
		g.P("			SetTenantId(tenant).")
		g.P("			SetActorTenantId(", pkgUuid.Ident("UUID"), "(w.Tenant)).")
		g.P("			SetActorId(", pkgUuid.Ident("UUID"), "(w.Actor)).")
		g.P("			SetTraceId(notNull(w.Trace)).")
		g.P("			SetAction(action).")
		g.P("			SetObjectId(", pkgUuid.Ident("UUID"), "(v.Object)).")
		g.P("			SetDomain(domain).")
		g.P("			SetPatch([]byte{}).")
		g.P("			SetValue([]byte{}).")
		g.P("			SetFields([]uint32{}).")
		g.P("			SetDigest(digest).")
		g.P("			SetDigestFields(covered).")
		g.P("			SetDateCreated(", pkgTime.Ident("Now"), "().UTC())")
		g.P("		if !w.Counterpart.IsZero() {")
		g.P("			q.SetCounterpartTenantId(", pkgUuid.Ident("UUID"), "(w.Counterpart))")
		g.P("		}")
		g.P("		qs = append(qs, q)")
		g.P("	}")
		g.P("")
		g.P("	for vs := range ", pkgSlices.Ident("Chunk"), "(qs, keptTrailRows) {")
		g.P("		if err := db.", a.GoName(), ".CreateBulk(vs...).Exec(ctx); err != nil {")
		g.P("			return ", pkgFmt.Ident("Errorf"), "(\"write the trail: %w\", err)")
		g.P("		}")
		g.P("	}")
		g.P("")
		g.P("	return nil")
		g.P("}")
		g.P("")
		return
	}

	// An app that added columns of its own to the trail writes it through its
	// server, as the recorder does, so that what the server makes of a row is
	// what is written.
	g.P("		_, err = s.", a.GoName(), "().Add(ctx, ", root.Ident(a.GoName()+"AddRequest_builder"), "{")
	g.P("			TenantId:            tenant[:],")
	g.P("			ActorTenantId:       w.Tenant.Bytes(),")
	g.P("			ActorId:             w.Actor.Bytes(),")
	g.P("			TraceId:             notNull(w.Trace),")
	g.P("			Action:              action,")
	g.P("			ObjectId:            v.Object.Bytes(),")
	g.P("			Domain:              &domain,")
	g.P("			Patch:               []byte{},")
	g.P("			Value:               []byte{},")
	g.P("			Fields:              []uint32{},")
	g.P("			Digest:              digest,")
	g.P("			DigestFields:        covered,")
	g.P("			CounterpartTenantId: w.CounterpartBytes(),")
	g.P("		}.Build())")
	g.P("		if err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")
}
