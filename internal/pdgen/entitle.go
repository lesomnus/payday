package pdgen

import (
	"fmt"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/schema"
)

const pkgEntitle = protogen.GoImportPath("github.com/lesomnus/payday/entitle")

// Entitlements is what an app declared of its copy of its tenants'
// entitlements (payday#185): that payday's `Entitlement` is among its
// entities, and the domain it takes.
//
//	option (payday.app) = {entitlements: {domain: 43}};
//
// It is nil for an app that declared none, which is every app until it does.
type Entitlements struct {
	Domain uint8

	// At is the file that declared it, as the schema names it, for a refusal
	// to point at.
	At string
}

// CheckEntitlements refuses a number the copy cannot be given: none, one that
// does not fit, one that is payday's, and one `app` gives the past, the
// notifications or payday's Archived and Access -- `app` being what the app
// declares of those, wherever it declares them.
//
// The one rule, asked by the two readers [CheckPastDomains] is asked by, for
// its reason: `pd gen` writes the copy at this number before anything is
// compiled, and [Read] holds the compiled schema to it. Whether an entity of
// the app's own holds it is the whole schema's to say, and [Read] says it.
func CheckEntitlements(e *pdpb.Entitlements, app *pdpb.App) error {
	if e == nil {
		return nil
	}

	switch n := e.GetDomain(); {
	case n == 0:
		return fmt.Errorf(
			"entitlements: says no `domain:`, and an entity's domain is what every identifier of it carries; "+
				"say one in %d..%d -- `entitlements: {domain: 43}`", AppDomainMin, AppDomainMax)
	case n > 255:
		return fmt.Errorf("entitlements: domain: %d does not fit in the one byte an identifier carries; it must be %d..%d",
			n, AppDomainMin, AppDomainMax)
	case n < AppDomainMin || n > AppDomainMax:
		return fmt.Errorf(
			"entitlements: domain: %d is payday's: it numbers what every app has from 1 up and from 255 down, "+
				"and keeps 1..%d and %d..255 for that. The copy is numbered like the app's own entities, %d..%d",
			n, AppDomainMin-1, AppDomainMax+1, AppDomainMin, AppDomainMax)
	}

	for _, held := range []struct {
		name string
		n    uint32
	}{
		{"past.event", app.GetPast().GetEvent()},
		{"past.fact", app.GetPast().GetFact()},
		{"notify.notification", app.GetNotify().GetNotification()},
		{"notify.receipt", app.GetNotify().GetReceipt()},
		{"domains.archived", app.GetDomains().GetArchived()},
		{"domains.access", app.GetDomains().GetAccess()},
	} {
		if held.n == e.GetDomain() {
			return fmt.Errorf(
				"entitlements: domain: %d is what `%s` gives, and a domain is one entity's: an identifier that "+
					"could be either is one that says nothing. Give the copy a number nothing holds",
				e.GetDomain(), held.name)
		}
	}

	return nil
}

// readEntitlements is what the schema's own files declare of the copy, and nil
// when none does: read off the compiled option, for the reason [readPast] is.
// `held` is what the same schema declares of the past, the notifications and
// payday's numbered entities, whose numbers the copy may not take.
func readEntitlements(files []*protogen.File, held *pdpb.App) (*Entitlements, error) {
	var v *Entitlements
	for _, f := range files {
		if !f.Generate {
			continue
		}

		a, _ := proto.GetExtension(f.Desc.Options(), pdpb.E_App).(*pdpb.App)
		if !a.HasEntitlements() {
			continue
		}
		if v != nil {
			return nil, fmt.Errorf(
				"%s and %s both declare entitlements; they are said once, in the file that says the package is the app's",
				v.At, f.Desc.Path())
		}
		if err := CheckEntitlements(a.GetEntitlements(), held); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Desc.Path(), err)
		}

		v = &Entitlements{Domain: uint8(a.GetEntitlements().GetDomain()), At: f.Desc.Path()}
	}

	return v, nil
}

// checkEntitlements refuses a copy that is not what the schema declared: one
// with no declaration behind it, one at a number other than the one declared,
// and a declared number another entity holds -- each a table numbered by
// nothing the schema says, which `pd gen` writes none of and buf run by hand
// over a tree whose declaration moved does. See [Schema.checkPast].
func (s *Schema) checkEntitlements() error {
	features, err := schema.Features()
	if err != nil {
		return err
	}

	for _, v := range s.Entities {
		if features[v.Own] != schema.DirEntitle {
			continue
		}

		switch {
		case s.Entitlements == nil:
			return fmt.Errorf(
				"%s is payday's %s, which is copied into an app that declares entitlements, and this one "+
					"declares none. `pd gen` takes the copy out when the declaration goes; declare it again "+
					"if it is still wanted:\n\n"+
					"    option (payday.app) = {entitlements: {domain: %d}};",
				v.FullName(), v.Own, v.Domain)

		case v.Domain != s.Entitlements.Domain:
			return fmt.Errorf(
				"%s says domain %d, and %s declares %d for it. The copy is numbered from the declaration "+
					"by `pd gen`, so one that says otherwise was not written by it: run `pd gen`, which writes it again",
				v.FullName(), v.Domain, s.Entitlements.At, s.Entitlements.Domain)
		}
	}
	if s.Entitlements == nil {
		return nil
	}

	for _, v := range s.Entities {
		if features[v.Own] == schema.DirEntitle || v.Domain != s.Entitlements.Domain {
			continue
		}

		return fmt.Errorf(
			"%s declares domain %d, and %s gives it to the copy of entitlements; a domain is one entity's, "+
				"so an old identifier would start lying about what it named. Give the copy a number nothing holds",
			v.FullName(), v.Domain, s.Entitlements.At)
	}

	return nil
}

// EmitEntitlements writes the copy of an app that declares entitlements as the
// `entitle` package reads and keeps it (payday#185): `Entitlements`, what a
// read in a transaction reads, and `EntitlementCopy`, what `entitle.Follower`
// writes through. Nothing at all for an app that declares none.
//
// Generated for the reason the past's store is: the copy is a table of the
// app's, read and written through its ent client, and payday can name
// neither. What is here is the mechanical half -- a query, a row, a statement
// -- and every judgement is the package's: which grant decides, what a version
// allows, what is taken out and when.
//
// Its writes are beneath the servers, since the copy has none, and each is put
// on the trail through `Record` and `RecordErased` in its transaction: the
// kind says `trail: {recorded: {}}`, so a row nothing on the trail accounts
// for is what a check of the rows finds.
func EmitEntitlements(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	v := s.Own(pdpb.Own_OWN_ENTITLEMENT)
	if v == nil || s.Tenant == nil || !s.Has(pdpb.Own_OWN_AUDIT) || !s.Has(pdpb.Own_OWN_ARCHIVED) {
		return
	}

	e := v.GoName()
	ep := p.EntPkg(v)
	tp := p.EntPkg(s.Tenant)
	pred := p.Ent + "/predicate"
	ctx, id, drv := pkgCtx.Ident("Context"), pkgPdid.Ident("Id"), pkgDialect.Ident("Driver")
	row, value := pkgEntitle.Ident("Row"), pkgEntitle.Ident("Value")
	enum := func(name string) protogen.GoIdent { return root.Ident(e + "Value_ENTITLEMENT_VALUE_" + name) }

	g.P("// Entitlements is this app's copy of what its tenants are entitled to")
	g.P("// (payday#185), read on `drv`: what `entitle.Has`, `entitle.Limit`,")
	g.P("// `entitle.Get` and `entitle.All` read -- on the transaction of the write")
	g.P("// they decide, so that a limit's lock is held until that write commits.")
	g.P("//")
	g.P("//	drv, tx, err := dialect.BeginTx(ctx, s.drv)")
	g.P("//	...")
	g.P("//	limit, err := entitle.Limit(ctx, pd.Entitlements(drv), \"acme.robots\")")
	g.P("func Entitlements(drv ", drv, ") ", pkgEntitle.Ident("Reader"), " {")
	g.P("	return entitlementReader{drv: drv, db: ", p.Ent.Ident("NewClient"), "(", p.Ent.Ident("Driver"), "(drv))}")
	g.P("}")
	g.P("")
	g.P("type entitlementReader struct {")
	g.P("	drv ", drv)
	g.P("	db  *", p.Ent.Ident("Client"))
	g.P("}")
	g.P("")
	g.P("func (r entitlementReader) Driver() ", drv, " { return r.drv }")
	g.P("")
	g.P("// Rows reads one tenant's rows of the copy, of one key or of all, in the")
	g.P("// order of their identifiers, and locks them `FOR UPDATE` when `lock` --")
	g.P("// outside SQLite, whose transactions are written one at a time.")
	g.P("func (r entitlementReader) Rows(ctx ", ctx, ", tenant ", id, ", key string, lock bool) ([]", row, ", error) {")
	g.P("	q := r.db.", e, ".Query().Where(", ep.Ident("TenantId"), "(tenant.Uuid()))")
	g.P("	if key != \"\" {")
	g.P("		q = q.Where(", ep.Ident("Key"), "(key))")
	g.P("	}")
	g.P("	q = q.Order(", ep.Ident("ById"), "())")
	g.P("	if lock && r.drv.Dialect() != ", pkgDialect.Ident("SQLite"), " {")
	g.P("		q.Modify(func(s *", pkgEntsql.Ident("Selector"), ") { s.ForUpdate() })")
	g.P("	}")
	g.P("")
	g.P("	vs, err := q.All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	out := make([]", row, ", len(vs))")
	g.P("	for i, v := range vs {")
	g.P("		out[i] = entitlementRow(v)")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")
	g.P("// entitlementRow is a row of the copy as the `entitle` package reads one.")
	g.P("func entitlementRow(v *", p.Ent.Ident(e), ") ", row, " {")
	g.P("	return ", row, "{")
	g.P("		Id:      ", id, "(v.Id),")
	g.P("		Tenant:  ", id, "(v.TenantId),")
	g.P("		Key:     v.Key,")
	g.P("		Value:   entitlementValue(v.Value, v.Amount),")
	g.P("		From:    entitlementFrom(v.ValidFrom),")
	g.P("		Until:   v.ValidUntil,")
	g.P("		Grant:   v.GrantId,")
	g.P("		Version: v.GrantVersion,")
	g.P("	}")
	g.P("}")
	g.P("")
	g.P("// entitlementFrom is when a row's grant begins: from always, for one that")
	g.P("// says no beginning.")
	g.P("func entitlementFrom(v *", pkgTime.Ident("Time"), ") ", pkgTime.Ident("Time"), " {")
	g.P("	if v == nil {")
	g.P("		return ", pkgTime.Ident("Time"), "{}")
	g.P("	}")
	g.P("")
	g.P("	return *v")
	g.P("}")
	g.P("")
	g.P("// entitlementValue is what a row's value and amount say, by the names of")
	g.P("// the enum's values: none, for a value this build does not know.")
	g.P("func entitlementValue(v int32, n int64) ", value, " {")
	g.P("	switch ", root.Ident(e+"Value"), "(v) {")
	g.P("	case ", enum("OFF"), ":")
	g.P("		return ", pkgEntitle.Ident("Off"))
	g.P("	case ", enum("ON"), ":")
	g.P("		return ", pkgEntitle.Ident("On"))
	g.P("	case ", enum("LIMIT"), ":")
	g.P("		return ", pkgEntitle.Ident("Limited"), "(n)")
	g.P("	case ", enum("UNLIMITED"), ":")
	g.P("		return ", pkgEntitle.Ident("Unlimited"))
	g.P("	}")
	g.P("")
	g.P("	return ", pkgEntitle.Ident("None"))
	g.P("}")
	g.P("")
	g.P("// entitlementColumns is a value as a row holds it.")
	g.P("func entitlementColumns(v ", value, ") (", root.Ident(e+"Value"), ", int64) {")
	g.P("	switch v.Kind() {")
	g.P("	case ", pkgEntitle.Ident("KindOff"), ":")
	g.P("		return ", enum("OFF"), ", 0")
	g.P("	case ", pkgEntitle.Ident("KindOn"), ":")
	g.P("		return ", enum("ON"), ", 0")
	g.P("	case ", pkgEntitle.Ident("KindLimit"), ":")
	g.P("		n, _ := v.Number()")
	g.P("		return ", enum("LIMIT"), ", n")
	g.P("	case ", pkgEntitle.Ident("KindUnlimited"), ":")
	g.P("		return ", enum("UNLIMITED"), ", 0")
	g.P("	}")
	g.P("")
	g.P("	return ", enum("UNSPECIFIED"), ", 0")
	g.P("}")
	g.P("")

	g.P("// EntitlementCopy is the copy as `entitle.Follower` keeps it, on `drv`:")
	g.P("// each `In` a transaction of its own, framed as the deployment's work in")
	g.P("// the tenant, and each write put on the trail with `content` -- the")
	g.P("// trail's (`audit.content`, `audit.digest.key`), so that its digest is the")
	g.P("// one a check of the rows makes again.")
	g.P("//")
	g.P("//	s.Spin = append(s.Spin, entitle.Follower{")
	g.P("//		Source: pdpb.NewGrantServiceClient(conn),")
	g.P("//		Copy:   pd.EntitlementCopy(drv, content),")
	g.P("//	})")
	g.P("func EntitlementCopy(drv ", drv, ", content ", pkgAudit.Ident("Contents"), ") ", pkgEntitle.Ident("Copy"), " {")
	g.P("	return entitlementCopy{drv: drv, db: ", p.Ent.Ident("NewClient"), "(", p.Ent.Ident("Driver"), "(drv)), content: content}")
	g.P("}")
	g.P("")
	g.P("type entitlementCopy struct {")
	g.P("	drv     ", drv)
	g.P("	db      *", p.Ent.Ident("Client"))
	g.P("	content ", pkgAudit.Ident("Contents"))
	g.P("}")
	g.P("")
	g.P("// In runs `f` in a transaction of its own on `drv`, with the context framed")
	g.P("// as the deployment's work in `tenant` -- nobody's, and of that tenant")
	g.P("// alone -- and commits when `f` answers nil.")
	g.P("func (c entitlementCopy) In(ctx ", ctx, ", tenant ", id, ", f func(ctx ", ctx, ", s ", pkgEntitle.Ident("Store"), ") error) error {")
	g.P("	ctx = ", pkgFrame.Ident("Into"), "(ctx, ", pkgFrame.Ident("New"), "(", pkgPdid.Ident("Nil"), ", tenant, ", pkgFrame.Ident("Grant"), "{}).WithScope(", pkgFrame.Ident("Only"), "(tenant)))")
	g.P("")
	g.P("	tdrv, tx, err := ", pkgDialect.Ident("BeginTx"), "(ctx, c.drv)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("	defer tx.Rollback()")
	g.P("")
	g.P("	s := entitlementStore{")
	g.P("		entitlementReader: entitlementReader{drv: tdrv, db: ", p.Ent.Ident("NewClient"), "(", p.Ent.Ident("Driver"), "(tdrv))},")
	g.P("		content:           c.content,")
	g.P("	}")
	g.P("	if err := f(ctx, s); err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("")
	g.P("	return tx.Commit()")
	g.P("}")
	g.P("")
	g.P("// Tenants is every tenant the copy holds a row of, a page of at most")
	g.P("// `limit` after `after` in the order of their identifiers: one seek per")
	g.P("// tenant, the next `tenant_id` after the last, which the index on the")
	g.P("// tenant and the key answers without reading the rows between.")
	g.P("func (c entitlementCopy) Tenants(ctx ", ctx, ", after ", id, ", limit int) ([]", id, ", error) {")
	g.P("	out := []", id, "{}")
	g.P("	last := after.Uuid()")
	g.P("	for len(out) < limit {")
	g.P("		v, err := c.db.", e, ".Query().")
	g.P("			Where(", pred.Ident(e), "(", pkgEntsql.Ident("FieldGT"), "(", ep.Ident("FieldTenantId"), ", last))).")
	g.P("			Order(", p.Ent.Ident("Asc"), "(", ep.Ident("FieldTenantId"), ")).")
	g.P("			First(ctx)")
	g.P("		switch {")
	g.P("		case ", p.Ent.Ident("IsNotFound"), "(err):")
	g.P("			return out, nil")
	g.P("		case err != nil:")
	g.P("			return out, err")
	g.P("		}")
	g.P("")
	g.P("		out = append(out, ", id, "(v.TenantId))")
	g.P("		last = v.TenantId")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	g.P("// entitlementStore is the copy in one transaction, as `entitle.Follower`")
	g.P("// writes it.")
	g.P("type entitlementStore struct {")
	g.P("	entitlementReader")
	g.P("	content ", pkgAudit.Ident("Contents"))
	g.P("}")
	g.P("")
	g.P("// Grant reads the row that copies `grant`, locked as [entitlementReader.Rows]")
	g.P("// locks one.")
	g.P("func (s entitlementStore) Grant(ctx ", ctx, ", grant []byte) (", row, ", bool, error) {")
	g.P("	q := s.db.", e, ".Query().Where(", ep.Ident("GrantId"), "(grant))")
	g.P("	if s.drv.Dialect() != ", pkgDialect.Ident("SQLite"), " {")
	g.P("		q.Modify(func(s *", pkgEntsql.Ident("Selector"), ") { s.ForUpdate() })")
	g.P("	}")
	g.P("")
	g.P("	v, err := q.Only(ctx)")
	g.P("	switch {")
	g.P("	case ", p.Ent.Ident("IsNotFound"), "(err):")
	g.P("		return ", row, "{}, false, nil")
	g.P("	case err != nil:")
	g.P("		return ", row, "{}, false, err")
	g.P("	}")
	g.P("")
	g.P("	return entitlementRow(v), true, nil")
	g.P("}")
	g.P("")
	g.P("// Tenant answers whether `tenant` is one of the app's tenants.")
	g.P("func (s entitlementStore) Tenant(ctx ", ctx, ", tenant ", id, ") (bool, error) {")
	g.P("	return s.db.", s.Tenant.GoName(), ".Query().Where(", tp.Ident("Id"), "(tenant.Uuid())).Exist(ctx)")
	g.P("}")
	g.P("")
	g.P("// Put writes `r` -- a row of its own when its Id is zero -- and puts it on")
	g.P("// the trail as `entitle.Applied`, in the same transaction.")
	g.P("func (s entitlementStore) Put(ctx ", ctx, ", r ", row, ") (", id, ", error) {")
	g.P("	value, amount := entitlementColumns(r.Value)")
	g.P("	now := ", pkgTime.Ident("Now"), "()")
	g.P("")
	g.P("	id := r.Id")
	g.P("	if id.IsZero() {")
	g.P("		id = ", pkgPdid.Ident("New"), "(", e, "Domain)")
	g.P("		q := s.db.", e, ".Create().")
	g.P("			SetId(id.Uuid()).")
	g.P("			SetTenantId(r.Tenant.Uuid()).")
	g.P("			SetKey(r.Key).")
	g.P("			SetValue(int32(value)).")
	g.P("			SetAmount(amount).")
	g.P("			SetGrantId(r.Grant).")
	g.P("			SetGrantVersion(r.Version).")
	g.P("			SetValidUntil(r.Until).")
	g.P("			SetDateUpdated(now).")
	g.P("			SetDateCreated(now)")
	g.P("		if !r.From.IsZero() {")
	g.P("			q.SetValidFrom(r.From)")
	g.P("		}")
	g.P("		if err := q.Exec(ctx); err != nil {")
	g.P("			return ", pkgPdid.Ident("Nil"), ", err")
	g.P("		}")
	g.P("	} else {")
	g.P("		q := s.db.", e, ".UpdateOneId(id.Uuid()).")
	g.P("			SetValue(int32(value)).")
	g.P("			SetAmount(amount).")
	g.P("			SetGrantVersion(r.Version).")
	g.P("			SetValidUntil(r.Until).")
	g.P("			SetDateUpdated(now)")
	g.P("		if r.From.IsZero() {")
	g.P("			q.ClearValidFrom()")
	g.P("		} else {")
	g.P("			q.SetValidFrom(r.From)")
	g.P("		}")
	g.P("		if err := q.Exec(ctx); err != nil {")
	g.P("			return ", pkgPdid.Ident("Nil"), ", err")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	if _, err := Record(ctx, s.db, s.content, ", pkgEntitle.Ident("Applied"), ", []", id, "{id}); err != nil {")
	g.P("		return ", pkgPdid.Ident("Nil"), ", err")
	g.P("	}")
	g.P("")
	g.P("	return id, nil")
	g.P("}")
	g.P("")
	g.P("// Forget deletes the rows `ids` and puts each on the trail as gone, as")
	g.P("// `entitle.Forgot`, in the same transaction.")
	g.P("func (s entitlementStore) Forget(ctx ", ctx, ", ids []", id, ") error {")
	g.P("	n, err := s.db.", e, ".Delete().Where(", ep.Ident("IdIn"), "(uuids(ids)...)).Exec(ctx)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("	if n != len(ids) {")
	g.P("		return ", pkgFmt.Ident("Errorf"), "(\"forget %d rows of the copy, and %d were there\", len(ids), n)")
	g.P("	}")
	g.P("")
	g.P("	_, err = RecordErased(ctx, s.db, s.content, ", pkgEntitle.Ident("Forgot"), ", ids)")
	g.P("	return err")
	g.P("}")
	g.P("")
}
