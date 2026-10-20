package pdgen

import (
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
)

// emitDigests writes `Digests`, the check of the live rows against the trail
// (payday#102): every row of every kind written through the servers, read a
// page at a time the way a Get reads it, and compared with the digest the
// newest row of the trail in the database about it keeps.
//
// Comparing is the audit package's. What only generation knows is the kinds,
// their tables, which of their rows are live, and the trail's own table --
// which is all this writes.
func emitDigests(g *protogen.GeneratedFile, s *Schema, p Paths) {
	a := s.Own(pdpb.Own_OWN_AUDIT)
	if a == nil {
		return
	}
	trailPkg := p.Ent + "/" + protogen.GoImportPath(a.EntPkg())

	// The kinds a write reaches through a server, which is what the recorder
	// sees. payday's own tables of the trail, the outbox and the archive are
	// written beneath it, and a kind with no RPC to write it is written by
	// hand, if at all, and never recorded.
	var kinds []*Entity
	for _, v := range s.Sorted() {
		switch v.Own {
		case pdpb.Own_OWN_AUDIT, pdpb.Own_OWN_OUTBOX, pdpb.Own_OWN_ARCHIVED:
			continue
		}
		if !v.Rpcs().HasAdd() && !v.Rpcs().HasPatch() {
			continue
		}

		kinds = append(kinds, v)
	}

	g.P("// Digests checks every live row of every kind written through the servers")
	g.P("// against the newest row of the trail in the database about it")
	g.P("// (payday#102): each digested again, as a Get reads it, and compared with")
	g.P("// the digest that row keeps. A row somebody changed past the servers is one")
	g.P("// that differs. It changes nothing, and reads a page of a kind at a time.")
	g.P("func Digests(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", d *", pkgAudit.Ident("Digest"), ") (", pkgAudit.Ident("Rows"), ", error) {")
	g.P("	out := ", pkgAudit.Ident("Rows"), "{}")
	for _, v := range kinds {
		g.P("	if err := digests", v.GoName(), "(ctx, db, d, &out); err != nil {")
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

		g.P("func digests", e, "(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", d *", pkgAudit.Ident("Digest"), ", out *", pkgAudit.Ident("Rows"), ") error {")
		g.P("	var after ", pkgUuid.Ident("UUID"))
		g.P("	for {")
		if v.HasErasedField() {
			// Live, which is what the newest row of the trail about it is a
			// write's and not an Erase's.
			g.P("		q := db.", e, ".Query().Where(", entPkg.Ident("IdGT"), "(after), ",
				entPkg.Ident(pascal(v.GetErasedField().Name())+"IsNil"), "())")
		} else {
			g.P("		q := db.", e, ".Query().Where(", entPkg.Ident("IdGT"), "(after))")
		}
		g.P("		", p.Bare.Ident(e+"SelectInit"), "(q, nil)")
		g.P("")
		g.P("		vs, err := q.Order(", entPkg.Ident("ById"), "()).Limit(", pkgAudit.Ident("RowsPage"), ").All(ctx)")
		g.P("		if err != nil {")
		g.P("			return err")
		g.P("		}")
		g.P("		if len(vs) == 0 {")
		g.P("			return nil")
		g.P("		}")
		g.P("")
		g.P("		rows := make([]", pkgAudit.Ident("Live"), ", len(vs))")
		g.P("		for i, v := range vs {")
		g.P("			rows[i] = ", pkgAudit.Ident("Live"), "{Object: ", pkgPdid.Ident("Id"), "(v.Id), Row: v.Proto()}")
		g.P("		}")
		g.P("		if err := ", pkgAudit.Ident("CheckRows"), "(ctx, d, rows, newestDigests(db), out); err != nil {")
		g.P("			return err")
		g.P("		}")
		g.P("")
		g.P("		after = vs[len(vs)-1].Id")
		g.P("	}")
		g.P("}")
		g.P("")
	}

	g.P("// newestDigests is what the newest row of the trail in the database about")
	g.P("// each thing keeps of its digest: the rows about them read newest first, and")
	g.P("// the first of each thing's kept.")
	g.P("func newestDigests(db *", p.Ent.Ident("Client"), ") ", pkgAudit.Ident("Newest"), " {")
	g.P("	return func(ctx ", pkgCtx.Ident("Context"), ", ids []", pkgPdid.Ident("Id"), ") (map[", pkgPdid.Ident("Id"), "]", pkgAudit.Ident("Recorded"), ", error) {")
	g.P("		ks := make([]", pkgUuid.Ident("UUID"), ", len(ids))")
	g.P("		for i, v := range ids {")
	g.P("			ks[i] = ", pkgUuid.Ident("UUID"), "(v)")
	g.P("		}")
	g.P("")
	g.P("		vs, err := db.", a.GoName(), ".Query().")
	g.P("			Where(", trailPkg.Ident("ObjectIdIn"), "(ks...)).")
	g.P("			Order(", trailPkg.Ident("ByDateCreated"), "(", pkgEntSql.Ident("OrderDesc"), "()), ", trailPkg.Ident("ById"), "(", pkgEntSql.Ident("OrderDesc"), "())).")
	g.P("			Select(", trailPkg.Ident("FieldObjectId"), ", ", trailPkg.Ident("FieldDigest"), ", ", trailPkg.Ident("FieldDigestFields"), ").")
	g.P("			All(ctx)")
	g.P("		if err != nil {")
	g.P("			return nil, err")
	g.P("		}")
	g.P("")
	g.P("		out := map[", pkgPdid.Ident("Id"), "]", pkgAudit.Ident("Recorded"), "{}")
	g.P("		for _, v := range vs {")
	g.P("			k := ", pkgPdid.Ident("Id"), "(v.ObjectId)")
	g.P("			if _, ok := out[k]; !ok {")
	g.P("				out[k] = ", pkgAudit.Ident("Recorded"), "{Digest: v.Digest, Fields: v.DigestFields}")
	g.P("			}")
	g.P("		}")
	g.P("")
	g.P("		return out, nil")
	g.P("	}")
	g.P("}")
	g.P("")
}
