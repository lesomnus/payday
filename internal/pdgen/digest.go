package pdgen

import (
	"strconv"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
)

var (
	pkgSql         = protogen.GoImportPath("database/sql")
	pkgErrors      = protogen.GoImportPath("errors")
	pkgMaps        = protogen.GoImportPath("maps")
	pkgTimestamppb = protogen.GoImportPath("google.golang.org/protobuf/types/known/timestamppb")
)

// emitDigests writes `Digests`, the check of the rows against the trail
// (payday#102, #108, #115), and `Record` and `RecordErased`, the ways onto the
// trail that are not a write through the servers: every row of every kind on
// the trail -- written through the servers, or by the app and recorded -- read
// a page at a time the way a Get reads it, beside the newest line of the trail
// about it, in the database and in the archive for what has left; and every
// thing the trail is about that is no row, beside the same.
//
// Comparing is the audit package's (`audit.CheckKind`), and reading the archive
// the trail package's (`trail.Newest`). What only generation knows is the
// kinds, their tables, which of their rows are there, and the trail's own table
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
	var kinds, off []*Entity
	for _, v := range s.Sorted() {
		switch {
		case v.Checked():
			kinds = append(kinds, v)
		case !v.Records():
			off = append(off, v)
		}
	}

	g.P("// Digests checks every row of every kind on the trail -- every kind but the")
	g.P("// ones kept off it -- against the trail (payday#102, #108, #115): each")
	g.P("// digested again, as a Get reads it, and compared with the digest the newest")
	g.P("// line of the trail about it keeps -- in the database, or in the archive `p`")
	g.P("// names once it has left. A row somebody changed past the servers is one that")
	g.P("// differs, a row somebody added past them is one nothing on the trail is")
	g.P("// about, and a row somebody removed past them is a thing the trail says is a")
	g.P("// row and is not. The kinds it does not read are in what it answers, with")
	g.P("// why, so that a check that found nothing says what it did not look at.")
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
	g.P("	out := ", pkgAudit.Ident("Rows"), "{Skipped: []", pkgAudit.Ident("Skipped"), "{")
	for _, v := range off {
		why := v.Opts.GetTrail().GetOff().GetWhy()
		g.P("		{Kind: ", strconv.Quote(v.Name), ", Why: ", strconv.Quote(why), "},")
	}
	g.P("	}}")
	for _, v := range kinds {
		g.P("	if err := ", pkgAudit.Ident("CheckKind"), "(ctx, d, checked", v.GoName(), "(db), archived, &out); err != nil {")
		g.P("		return out, ", pkgFmt.Ident("Errorf"), "(\"", v.Name, ": %w\", err)")
		g.P("	}")
	}
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	sealing := s.Has(pdpb.Own_OWN_ROW_SEAL)
	for _, v := range kinds {
		e := v.GoName()
		entPkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())
		sealed := sealing && v.Sealed()

		g.P("// checked", e, " is what a check of the rows reads of the ", v.Name, " kind.")
		g.P("func checked", e, "(db *", p.Ent.Ident("Client"), ") ", pkgAudit.Ident("Kind"), " {")
		g.P("	read := func(ctx ", pkgCtx.Ident("Context"), ", ps ...", pred.Ident(e), ") (", pkgAudit.Ident("Page"), ", error) {")
		g.P("		var out ", pkgAudit.Ident("Page"))
		g.P("		err := snapshot(ctx, db, func(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ") error {")
		g.P("			rows, err := rows", e, "(ctx, db, ps...)")
		g.P("			if err != nil {")
		g.P("				return err")
		g.P("			}")
		g.P("")
		g.P("			recorded, err := newestDigests(ctx, db, objects(rows))")
		if sealed {
			// And the seals beside them, at the same moment: a write between the
			// reads would be a seal of another line than the one read.
			g.P("			if err != nil {")
			g.P("				return err")
			g.P("			}")
			g.P("")
			g.P("			seals, err := sealsOf(ctx, db, objects(rows))")
			g.P("			out = ", pkgAudit.Ident("Page"), "{Rows: rows, Recorded: recorded, Seals: seals}")
		} else {
			g.P("			out = ", pkgAudit.Ident("Page"), "{Rows: rows, Recorded: recorded}")
		}
		g.P("			return err")
		g.P("		})")
		g.P("")
		g.P("		return out, err")
		g.P("	}")
		g.P("")
		g.P("	return ", pkgAudit.Ident("Kind"), "{")
		g.P("		Domain: ", e, "Domain,")
		if sealed {
			g.P("		Sealed: true,")
		}
		g.P("		After: func(ctx ", pkgCtx.Ident("Context"), ", after ", pkgPdid.Ident("Id"), ") (", pkgAudit.Ident("Page"), ", error) {")
		g.P("			return read(ctx, ", entPkg.Ident("IdGT"), "(", pkgUuid.Ident("UUID"), "(after)))")
		g.P("		},")
		g.P("		Of: func(ctx ", pkgCtx.Ident("Context"), ", ids []", pkgPdid.Ident("Id"), ") (", pkgAudit.Ident("Page"), ", error) {")
		g.P("			return read(ctx, ", entPkg.Ident("IdIn"), "(uuids(ids)...))")
		g.P("		},")
		g.P("		Trail: func(ctx ", pkgCtx.Ident("Context"), ", yield func([]", pkgPdid.Ident("Id"), ") error) error {")
		g.P("			return trailObjects(ctx, db, ", e, "Domain, yield)")
		g.P("		},")
		g.P("		Gone: func(ctx ", pkgCtx.Ident("Context"), ", ids []", pkgPdid.Ident("Id"), ") (map[", pkgPdid.Ident("Id"), "]*", pkgAudit.Ident("Recorded"), ", error) {")
		g.P("			var out map[", pkgPdid.Ident("Id"), "]*", pkgAudit.Ident("Recorded"))
		g.P("			err := snapshot(ctx, db, func(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ") error {")
		g.P("				rows, err := rows", e, "(ctx, db, ", entPkg.Ident("IdIn"), "(uuids(ids)...))")
		g.P("				if err != nil {")
		g.P("					return err")
		g.P("				}")
		g.P("")
		g.P("				out, err = goneOf(ctx, db, ids, rows)")
		g.P("				return err")
		g.P("			})")
		g.P("")
		g.P("			return out, err")
		g.P("		},")
		g.P("	}")
		g.P("}")
		g.P("")
	}

	for _, v := range s.Sorted() {
		if v.Records() {
			continue
		}

		e := v.GoName()
		entPkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())

		g.P("// rows", e, " is the rows `ps` says of the ", v.Name, " kind -- every row there is,")
		g.P("// live or erased softly -- at most a page of them in identifier order, each as")
		g.P("// a Get reads it.")
		g.P("func rows", e, "(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", ps ...", pred.Ident(e), ") ([]", pkgAudit.Ident("Live"), ", error) {")
		g.P("	q := db.", e, ".Query().Where(ps...)")
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

	g.P("// objects is what `rows` are about.")
	g.P("func objects(rows []", pkgAudit.Ident("Live"), ") []", pkgPdid.Ident("Id"), " {")
	g.P("	out := make([]", pkgPdid.Ident("Id"), ", len(rows))")
	g.P("	for i, v := range rows {")
	g.P("		out[i] = v.Object")
	g.P("	}")
	g.P("")
	g.P("	return out")
	g.P("}")
	g.P("")

	g.P("// newestDigests is what the newest line of the trail in the database about")
	g.P("// each of `ids` keeps of its digest, and the line itself: the lines about")
	g.P("// them read newest first, and the first of each thing's kept.")
	g.P("func newestDigests(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", ids []", pkgPdid.Ident("Id"), ") (map[", pkgPdid.Ident("Id"), "]", pkgAudit.Ident("Recorded"), ", error) {")
	g.P("	out := map[", pkgPdid.Ident("Id"), "]", pkgAudit.Ident("Recorded"), "{}")
	g.P("	if len(ids) == 0 {")
	g.P("		return out, nil")
	g.P("	}")
	g.P("")
	g.P("	vs, err := db.", a.GoName(), ".Query().")
	g.P("		Where(", trailPkg.Ident("ObjectIdIn"), "(uuids(ids)...)).")
	g.P("		Order(", trailPkg.Ident("ByDateCreated"), "(", pkgEntSql.Ident("OrderDesc"), "()), ", trailPkg.Ident("ById"), "(", pkgEntSql.Ident("OrderDesc"), "())).")
	g.P("		Select(", trailPkg.Ident("FieldId"), ", ", trailPkg.Ident("FieldDateCreated"), ", ", trailPkg.Ident("FieldAction"), ", ",
		trailPkg.Ident("FieldObjectId"), ", ", trailPkg.Ident("FieldDigest"), ", ", trailPkg.Ident("FieldDigestFields"), ").")
	g.P("		All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	for _, v := range vs {")
	g.P("		k := ", pkgPdid.Ident("Id"), "(v.ObjectId)")
	g.P("		if _, ok := out[k]; !ok {")
	g.P("			out[k] = ", pkgAudit.Ident("Recorded"), "{")
	g.P("				Digest: v.Digest,")
	g.P("				Fields: v.DigestFields,")
	g.P("				Line:   ", pkgAudit.Ident("Line"), "{Id: v.Id, Created: v.DateCreated, Action: v.Action},")
	g.P("			}")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	// The other way round from a page of rows: the things the trail names,
	// and of them the ones that are no row.
	g.P("// goneOf is, of `ids`, the ones `rows` -- the ones of them that are rows --")
	g.P("// does not have, each with the newest line of the trail in the database")
	g.P("// about it, and nil for one the database's trail says nothing about.")
	g.P("func goneOf(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", ids []", pkgPdid.Ident("Id"), ", rows []", pkgAudit.Ident("Live"), ") (map[", pkgPdid.Ident("Id"), "]*", pkgAudit.Ident("Recorded"), ", error) {")
	g.P("	there := map[", pkgPdid.Ident("Id"), "]bool{}")
	g.P("	for _, v := range rows {")
	g.P("		there[v.Object] = true")
	g.P("	}")
	g.P("")
	g.P("	absent := []", pkgPdid.Ident("Id"), "{}")
	g.P("	for _, id := range ids {")
	g.P("		if !there[id] {")
	g.P("			absent = append(absent, id)")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	lines, err := newestDigests(ctx, db, absent)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	out := make(map[", pkgPdid.Ident("Id"), "]*", pkgAudit.Ident("Recorded"), ", len(absent))")
	g.P("	for _, id := range absent {")
	g.P("		if r, ok := lines[id]; ok {")
	g.P("			out[id] = &r")
	g.P("		} else {")
	g.P("			out[id] = nil")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	g.P("// trailObjects hands `yield` the things of kind `d` the database's trail is")
	g.P("// about, a page of its lines at a time, oldest first -- by the index on")
	g.P("// `(domain, date_created)`, which a DISTINCT over the table would not use.")
	g.P("// A thing with more than one line is handed more than once.")
	g.P("func trailObjects(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", d ", pkgPdid.Ident("Domain"), ", yield func([]", pkgPdid.Ident("Id"), ") error) error {")
	g.P("	var after *", p.Ent.Ident(a.GoName()))
	g.P("	for {")
	g.P("		q := db.", a.GoName(), ".Query().Where(", trailPkg.Ident("DomainEQ"), "(uint32(d)))")
	g.P("		if after != nil {")
	g.P("			q = q.Where(", trailPkg.Ident("Or"), "(")
	g.P("				", trailPkg.Ident("DateCreatedGT"), "(after.DateCreated),")
	g.P("				", trailPkg.Ident("And"), "(", trailPkg.Ident("DateCreatedEQ"), "(after.DateCreated), ", trailPkg.Ident("IdGT"), "(after.Id)),")
	g.P("			))")
	g.P("		}")
	g.P("")
	g.P("		vs, err := q.")
	g.P("			Order(", trailPkg.Ident("ByDateCreated"), "(), ", trailPkg.Ident("ById"), "()).")
	g.P("			Select(", trailPkg.Ident("FieldId"), ", ", trailPkg.Ident("FieldDateCreated"), ", ", trailPkg.Ident("FieldObjectId"), ").")
	g.P("			Limit(", pkgAudit.Ident("RowsPage"), ").")
	g.P("			All(ctx)")
	g.P("		if err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("		if len(vs) == 0 {")
	g.P("			return nil")
	g.P("		}")
	g.P("")
	g.P("		ids := make([]", pkgPdid.Ident("Id"), ", len(vs))")
	g.P("		for i, v := range vs {")
	g.P("			ids[i] = ", pkgPdid.Ident("Id"), "(v.ObjectId)")
	g.P("		}")
	g.P("		if err := yield(ids); err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("		if len(vs) < ", pkgAudit.Ident("RowsPage"), " {")
	g.P("			return nil")
	g.P("		}")
	g.P("")
	g.P("		after = vs[len(vs)-1]")
	g.P("	}")
	g.P("}")
	g.P("")

	if archive {
		g.P("// archivedDigests is what the newest line of the archive about each thing")
		g.P("// of a kind keeps, as a check of the rows asks for it; see [trail.Newest].")
		g.P("func archivedDigests(n *", pkgTrail.Ident("Newest"), ") ", pkgAudit.Ident("Archived"), " {")
		g.P("	return func(ctx ", pkgCtx.Ident("Context"), ", kind ", pkgPdid.Ident("Domain"), ") (map[", pkgPdid.Ident("Id"), "]", pkgAudit.Ident("Recorded"), ", error) {")
		g.P("		vs, err := n.All(ctx, kind)")
		g.P("		if err != nil {")
		g.P("			return nil, err")
		g.P("		}")
		g.P("")
		g.P("		out := make(map[", pkgPdid.Ident("Id"), "]", pkgAudit.Ident("Recorded"), ", len(vs))")
		g.P("		for k, v := range vs {")
		g.P("			out[k] = ", pkgAudit.Ident("Recorded"), "{Digest: v.Digest, Fields: v.DigestFields, Line: v.Line()}")
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

// emitRecord writes `Record` and `RecordErased`: rows an app wrote or removed
// beneath the servers on purpose, put on the trail as the act that did it.
//
// # Why there is a way past the recorder at all
//
// Because there are writes no server makes, and they are not mistakes. A
// workspace put back from its export is written as it was, under its own
// identifiers, and the trail it came with is another deployment's history,
// which this one's checkpoints do not vouch for. A migration rewrites a column
// in every row. A person forgotten takes the rows that were their ways in with
// them. Unrecorded, each is a row nothing on the trail is about, or a thing the
// trail says is a row that is not -- the same answers a check gives for a row
// somebody **added** or **removed** past the servers -- and a check that has to
// be told which of those is fine is one nobody reads.
//
// So the act is recorded instead: one line of the trail for each row it
// wrote or removed, saying who did it, when, and by what, and keeping the
// digest of the row as it is now, or that there is none. Not what it holds:
// the export holds that, or the migration, and a copy of every row of a
// workspace in the one table nothing rewrites is the copy #92 took out of the
// trail.
//
// # Read as a check reads it
//
// The digest is of the row read back as a Get reads it, by the same query the
// check reads it with, rather than of whatever the caller wrote -- a timestamp
// the database rounds, an edge it holds as a key, is then the same in both.
func emitRecord(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath, kinds []*Entity) {
	g.P("// Record puts on the trail rows that were written beneath the servers on")
	g.P("// purpose -- a workspace put back from its export, rows a migration rewrote")
	g.P("// and somebody looked at -- as the act `action` names (payday#108): one line")
	g.P("// of the trail for each, by whoever `ctx` is framed as and filed under the")
	g.P("// row's own tenant, keeping the digest of the row as a Get reads it now and")
	g.P("// nothing of what it holds. A check of the rows then finds each as it is")
	g.P("// now, and the trail says who put it there, when, and by what.")
	g.P("//")
	g.P("// It writes nothing but the trail. `db` is the transaction that wrote the")
	g.P("// rows, or the database. A row of a kind kept off the trail (`trail: {off:")
	g.P("// ...}}`) is passed over; one that is no row at all is an error, since that")
	g.P("// is [RecordErased]'s. A row erased softly is a row. It answers how many it")
	g.P("// recorded.")
	g.P("func Record(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", content ", pkgAudit.Ident("Contents"), ", action string, ids []", pkgPdid.Ident("Id"), ") (int, error) {")
	g.P("	return record(ctx, db, content, action, ids, true)")
	g.P("}")
	g.P("")
	g.P("// RecordErased puts on the trail rows that were removed beneath the servers")
	g.P("// on purpose -- what a person forgotten took with them, a workspace")
	g.P("// destroyed -- as the act `action` names (payday#115): one line of the trail")
	g.P("// for each, by whoever `ctx` is framed as, keeping a digest that says the row")
	g.P("// went. Without it a check of the rows finds each a thing the trail says is a")
	g.P("// row and is not, which is what a row somebody removed past the servers is.")
	g.P("//")
	g.P("// There is no row to file it under, so it is filed under the tenant `ctx`")
	g.P("// is framed as, as the recorder files an erase that took its row. One that")
	g.P("// is still a row is an error -- that is [Record]'s -- and one of a kind kept")
	g.P("// off the trail is passed over. It answers how many it recorded.")
	g.P("func RecordErased(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", content ", pkgAudit.Ident("Contents"), ", action string, ids []", pkgPdid.Ident("Id"), ") (int, error) {")
	g.P("	return record(ctx, db, content, action, ids, false)")
	g.P("}")
	g.P("")

	g.P("// record is [Record] when `there`, and [RecordErased] when not.")
	g.P("func record(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", content ", pkgAudit.Ident("Contents"), ", action string, ids []", pkgPdid.Ident("Id"), ", there bool) (int, error) {")
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
		g.P("				rows, err = rows", e, "(ctx, db, ", entPkg.Ident("IdIn"), "(uuids(page)...))")
	}
	g.P("			default:")
	g.P("				// Kept off the trail, so no act on it is recorded.")
	g.P("				continue")
	g.P("			}")
	g.P("			if err != nil {")
	g.P("				return n, err")
	g.P("			}")
	g.P("")
	g.P("			if there && len(rows) != len(page) {")
	g.P("				return n, ", pkgFmt.Ident("Errorf"), "(\"%s is no row\", unread(page, rows))")
	g.P("			}")
	g.P("			if !there && len(rows) > 0 {")
	g.P("				return n, ", pkgFmt.Ident("Errorf"), "(\"%s is still a row\", rows[0].Object)")
	g.P("			}")
	g.P("			if !there {")
	g.P("				rows = make([]", pkgAudit.Ident("Live"), ", len(page))")
	g.P("				for i, id := range page {")
	g.P("					rows[i] = ", pkgAudit.Ident("Live"), "{Object: id}")
	g.P("				}")
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
	g.P("// recordRows writes the line [Record] says for each of `rows`, and the one")
	g.P("// [RecordErased] says for each of them that has no row.")
	g.P("func recordRows(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", s ", p.Bare.Ident("Server"), ", action string, content ", pkgAudit.Ident("Contents"), ", rows []", pkgAudit.Ident("Live"), ") error {")
	if keepsTrail(s) {
		g.P("	qs := make([]*", p.Ent.Ident(a.GoName()+"Create"), ", 0, len(rows))")
	}
	g.P("	// And the seal of each row of a sealed kind, as its write would leave it:")
	g.P("	// written over the one there is, or removed with the row.")
	g.P("	seals := make([]*rowSeal, 0, len(rows))")
	g.P("	for _, v := range rows {")
	g.P("		w, err := ", pkgAudit.Ident("Of"), "(ctx, action, v.Object, nil)")
	g.P("		if err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("")
	g.P("		at := line(s, action)")
	g.P("		digest, covered, sealing, err := digested(content.Digest, v.Object, v.Row, at)")
	g.P("		if err != nil {")
	g.P("			return ", pkgFmt.Ident("Errorf"), "(\"the trail of %s: %w\", action, err)")
	g.P("		}")
	g.P("")
	g.P("		// Filed where the recorder files a write of it: under its tenant, or")
	g.P("		// the actor's when it is behind no wall or is no row.")
	g.P("		tenant, _, err := subject(ctx, s, v.Object, v.Row, false)")
	g.P("		if err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("		if tenant == ", pkgUuid.Ident("Nil"), "() {")
	g.P("			tenant = ", pkgUuid.Ident("UUID"), "(w.Tenant)")
	g.P("		}")
	g.P("		seals = append(seals, sealing.in(tenant))")
	g.P("		domain := uint32(v.Object.Domain())")
	g.P("")
	if keepsTrail(s) {
		g.P("		q := db.", a.GoName(), ".Create().")
		g.P("			SetId(", pkgUuid.Ident("UUID"), "(at.Id)).")
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
		g.P("			SetDateCreated(at.Created)")
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
		g.P("	return keepSeals(ctx, db, seals...)")
		g.P("}")
		g.P("")
	} else {
		// An app that added columns of its own to the trail writes it through
		// its server, as the recorder does, so that what the server makes of a
		// row is what is written.
		g.P("		_, err = s.", a.GoName(), "().Add(ctx, ", root.Ident(a.GoName()+"AddRequest_builder"), "{")
		g.P("			Id:                  at.Id[:],")
		g.P("			DateCreated:         ", pkgTimestamppb.Ident("New"), "(at.Created),")
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
		g.P("	return keepSeals(ctx, db, seals...)")
		g.P("}")
		g.P("")
	}

	emitCarry(g, s, p, root, kinds)
}

// emitCarry writes the trail store's [trail.Store.Carry]: the lines a pass
// carries forward before it destroys the newest line about a row that is still
// there (payday#115), written as [Record] writes its own -- a line minted
// here, keeping what the destroyed line said of the row bound to the new one.
//
// What it decides is only what the database knows and the archive does not:
// which of the things are still rows, and which have a line in the database's
// trail already, newer than anything the archive holds.
func emitCarry(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath, kinds []*Entity) {
	if !s.Has(pdpb.Own_OWN_ARCHIVED) {
		return
	}

	a := s.Own(pdpb.Own_OWN_AUDIT)
	trailPkg := p.Ent + "/" + protogen.GoImportPath(a.EntPkg())
	sealing := s.Has(pdpb.Own_OWN_ROW_SEAL)

	g.P("func (s trailStore) Carry(ctx ", pkgCtx.Ident("Context"), ", digest *", pkgAudit.Ident("Digest"), ", d ", pkgPdid.Ident("Domain"),
		", vs []", pkgTrail.Ident("Carried"), ") (int, error) {")
	g.P("	n := 0")
	g.P("	for page := range ", pkgSlices.Ident("Chunk"), "(vs, ", pkgAudit.Ident("RowsPage"), ") {")
	g.P("		ids := make([]", pkgPdid.Ident("Id"), ", len(page))")
	g.P("		for i, v := range page {")
	g.P("			ids[i] = v.Object")
	g.P("		}")
	g.P("")
	g.P("		var rows []", pkgAudit.Ident("Live"))
	g.P("		var err error")
	g.P("		switch d {")
	for _, v := range kinds {
		entPkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())
		g.P("		case ", v.GoName(), "Domain:")
		g.P("			rows, err = rows", v.GoName(), "(ctx, s.db, ", entPkg.Ident("IdIn"), "(uuids(ids)...))")
	}
	g.P("		default:")
	g.P("			// A kind no check reads, so there is nothing to carry forward to.")
	g.P("			return n, nil")
	g.P("		}")
	g.P("		if err != nil {")
	g.P("			return n, err")
	g.P("		}")
	g.P("")
	g.P("		there := map[", pkgPdid.Ident("Id"), "]bool{}")
	g.P("		for _, v := range rows {")
	g.P("			there[v.Object] = true")
	g.P("		}")
	g.P("")
	g.P("		about, err := s.db.", a.GoName(), ".Query().")
	g.P("			Where(", trailPkg.Ident("ObjectIdIn"), "(uuids(ids)...)).")
	g.P("			Select(", trailPkg.Ident("FieldObjectId"), ").")
	g.P("			All(ctx)")
	g.P("		if err != nil {")
	g.P("			return n, err")
	g.P("		}")
	g.P("		for _, v := range about {")
	g.P("			delete(there, ", pkgPdid.Ident("Id"), "(v.ObjectId))")
	g.P("		}")
	g.P("")
	g.P("		srv, err := ", p.Bare.Ident("NewServer"), "(s.db)")
	g.P("		if err != nil {")
	g.P("			return n, err")
	g.P("		}")
	if keepsTrail(s) {
		g.P("		qs := []*", p.Ent.Ident(a.GoName()+"Create"), "{}")
	}
	if sealing {
		g.P("		// The seals of the rows carried for, which go with their lines.")
		g.P("		rebound := map[", pkgPdid.Ident("Id"), "]", pkgAudit.Ident("Line"), "{}")
		g.P("		tenants := map[", pkgPdid.Ident("Id"), "]", pkgUuid.Ident("UUID"), "{}")
	}
	g.P("		for _, v := range page {")
	g.P("			if !there[v.Object] {")
	g.P("				continue")
	g.P("			}")
	g.P("")
	g.P("			at := line(srv, ", pkgTrail.Ident("CarriedAction"), ")")
	if sealing {
		g.P("			if isSealed(v.Object) {")
		g.P("				rebound[v.Object] = at")
		g.P("				tenants[v.Object] = v.Tenant.Uuid()")
		g.P("			}")
	}
	g.P("			sum, err := digest.Carry(v.Sum, v.Object, at)")
	g.P("			if err != nil {")
	g.P("				return n, ", pkgFmt.Ident("Errorf"), "(\"carry %s forward: %w\", v.Object, err)")
	g.P("			}")
	g.P("")
	g.P("			domain := uint32(d)")
	if keepsTrail(s) {
		g.P("			qs = append(qs, s.db.", a.GoName(), ".Create().")
		g.P("				SetId(", pkgUuid.Ident("UUID"), "(at.Id)).")
		g.P("				SetTenantId(v.Tenant.Uuid()).")
		g.P("				SetActorTenantId(", pkgUuid.Ident("Nil"), "()).")
		g.P("				SetActorId(", pkgUuid.Ident("Nil"), "()).")
		g.P("				SetTraceId([]byte{}).")
		g.P("				SetAction(at.Action).")
		g.P("				SetObjectId(v.Object.Uuid()).")
		g.P("				SetDomain(domain).")
		g.P("				SetPatch([]byte{}).")
		g.P("				SetValue([]byte{}).")
		g.P("				SetFields([]uint32{}).")
		g.P("				SetDigest(sum).")
		g.P("				SetDigestFields(v.DigestFields).")
		g.P("				SetDateCreated(at.Created))")
		g.P("		}")
		g.P("		for vs := range ", pkgSlices.Ident("Chunk"), "(qs, keptTrailRows) {")
		g.P("			if err := s.db.", a.GoName(), ".CreateBulk(vs...).Exec(ctx); err != nil {")
		g.P("				return n, ", pkgFmt.Ident("Errorf"), "(\"write the trail: %w\", err)")
		g.P("			}")
		g.P("			n += len(vs)")
		g.P("		}")
		if sealing {
			g.P("		if err := carrySeals(ctx, s.db, digest, rebound, tenants); err != nil {")
			g.P("			return n, err")
			g.P("		}")
		}
	} else {
		g.P("			if _, err := srv.", a.GoName(), "().Add(ctx, ", root.Ident(a.GoName()+"AddRequest_builder"), "{")
		g.P("				Id:            at.Id[:],")
		g.P("				DateCreated:   ", pkgTimestamppb.Ident("New"), "(at.Created),")
		g.P("				TenantId:      v.Tenant.Bytes(),")
		g.P("				ActorTenantId: ", pkgPdid.Ident("Nil"), ".Bytes(),")
		g.P("				ActorId:       ", pkgPdid.Ident("Nil"), ".Bytes(),")
		g.P("				TraceId:       []byte{},")
		g.P("				Action:        at.Action,")
		g.P("				ObjectId:      v.Object.Bytes(),")
		g.P("				Domain:        &domain,")
		g.P("				Patch:         []byte{},")
		g.P("				Value:         []byte{},")
		g.P("				Fields:        []uint32{},")
		g.P("				Digest:        sum,")
		g.P("				DigestFields:  v.DigestFields,")
		g.P("			}.Build()); err != nil {")
		g.P("				return n, err")
		g.P("			}")
		g.P("			n++")
		g.P("		}")
		if sealing {
			g.P("		if err := carrySeals(ctx, s.db, digest, rebound, tenants); err != nil {")
			g.P("			return n, err")
			g.P("		}")
		}
	}
	g.P("	}")
	g.P("")
	g.P("	return n, nil")
	g.P("}")
	g.P("")
}
