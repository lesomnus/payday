package pdgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdpb"
)

const (
	pkgAccount = protogen.GoImportPath("github.com/lesomnus/payday/account")
	pkgAuth    = protogen.GoImportPath("github.com/lesomnus/payday/auth")
)

// Accounts is what an app declared of its accounts (payday#184): the holder
// field that names the account a holder is a membership of, and the methods an
// account calls as itself. Nil for an app that declared none, which is every
// app whose people are each one tenant's.
type Accounts struct {
	// Field is the holder's field that holds the account's identifier, as the
	// declaration resolved: by its number, with its name held to agree.
	Field graph.Field

	// At is the file that declared it, as buf names it.
	At string

	// Methods are the methods marked `(payday.method) = {account: {}}`, in the
	// order the files declare them.
	Methods []AccountMethod

	// rpcs are every RPC of every entity's service: what the gate puts the
	// question in front of, since a call an account makes as itself is
	// refused all of them but its own.
	rpcs []interceptRpc
}

// AccountMethod is one method an account calls as itself.
type AccountMethod struct {
	// Full is the method as gRPC knows it: "/app.InvitationService/Accept".
	Full string

	// Service and Method are its Go names, "InvitationService" and "Accept",
	// and Const the constant protoc-gen-go-grpc names it by.
	Service string
	Method  string
	Const   string

	// At is the file that declares it.
	At string
}

// accountsDeclared is the option as one file wrote it, before it is resolved
// against the holder.
type accountsDeclared struct {
	v  *pdpb.Accounts
	at string
}

// readAccounts is what the app's files say of accounts, and nil when none
// does.
func readAccounts(files []*protogen.File) (*accountsDeclared, error) {
	var v *accountsDeclared
	for _, f := range files {
		if !f.Generate {
			continue
		}

		a, _ := proto.GetExtension(f.Desc.Options(), pdpb.E_App).(*pdpb.App)
		if !a.HasAccounts() {
			continue
		}
		if v != nil {
			return nil, fmt.Errorf(
				"%s and %s both declare accounts; they are said once, in the file that says the package is the app's",
				v.at, f.Desc.Path())
		}

		v = &accountsDeclared{v: a.GetAccounts(), at: f.Desc.Path()}
	}

	return v, nil
}

// readAccountMethods is every method of the app's own files that says
// `(payday.method)`, and refuses one that says nothing with it.
func readAccountMethods(files []*protogen.File) ([]AccountMethod, error) {
	var vs []AccountMethod
	for _, f := range files {
		if !f.Generate {
			continue
		}

		for _, svc := range f.Services {
			for _, m := range svc.Methods {
				opts := m.Desc.Options()
				if opts == nil || !proto.HasExtension(opts, pdpb.E_Method) {
					continue
				}

				v, _ := proto.GetExtension(opts, pdpb.E_Method).(*pdpb.Method)
				if !v.HasAccount() {
					// A declaration that does nothing is one somebody relies on.
					return nil, fmt.Errorf(
						"%s: (payday.method) says nothing, and a method it is on is served as it was without it; "+
							"`{account: {}}` is the one thing it says", m.Desc.FullName())
				}

				vs = append(vs, AccountMethod{
					Full:    "/" + string(svc.Desc.FullName()) + "/" + string(m.Desc.Name()),
					Service: svc.GoName,
					Method:  m.GoName,
					Const:   svc.GoName + "_" + m.GoName + "_FullMethodName",
					At:      f.Desc.Path(),
				})
			}
		}
	}

	return vs, nil
}

// checkAccounts resolves the declaration against payday's holder, and refuses
// a method marked as an account's own in an app that has no accounts.
//
// Everything refused is a resolver that would serve a call as somebody it is
// not, quietly: a field renamed under the declaration resolves calls by
// another column, a field of another type matches nothing or the wrong rows,
// and a personal one is kept off the trail as a value -- which is the record of
// who moved a membership from one account to another.
func (s *Schema) checkAccounts(d *accountsDeclared, methods []AccountMethod, files []*protogen.File) error {
	if d == nil {
		if len(methods) > 0 {
			return fmt.Errorf(
				"%s: %s is marked an account's own, and this app declares no accounts -- a call is served as one "+
					"tenant's holder, and there is no account to serve it as. Say which holder field names one, "+
					"on the file that says the package is the app's:\n\n"+
					"    option (payday.app) = {accounts: {field: {name: \"account\", number: <its number>}}};",
				methods[0].At, methods[0].Full)
		}

		return nil
	}

	holder := s.Own(pdpb.Own_OWN_HOLDER)
	if holder == nil {
		return fmt.Errorf("%s: accounts: there is no Holder in this schema to be a membership of one", d.at)
	}

	p, err := referred(holder, d.v.GetField(), "accounts: field")
	if err != nil {
		return fmt.Errorf("%s: %w", d.at, err)
	}

	f, ok := p.(graph.Field)
	switch {
	case !ok:
		return fmt.Errorf(
			"%s: accounts: field: %q of %s is an edge, and an account is not a row of this app's: "+
				"it is the identity provider's, named here by its identifier, in a `bytes` field of `TYPE_UUID`",
			d.at, p.Name(), holder.FullName())

	case p.Number() == holder.Key().Number():
		return fmt.Errorf("%s: accounts: field: %q is the holder's own key, which names the membership and not the account",
			d.at, p.Name())

	case graph.IsCollection(f):
		return fmt.Errorf("%s: accounts: field: %q holds many values, and a membership is of one account", d.at, p.Name())

	case f.Type() != ormpb.Type_TYPE_UUID:
		return fmt.Errorf(
			"%s: accounts: field: %q is %s, and an account is named by its identifier: a `bytes` field of "+
				"`(orm.field) = {type: TYPE_UUID}`, nullable if a holder may be no account's",
			d.at, p.Name(), strings.ToLower(strings.TrimPrefix(f.Type().String(), "TYPE_")))

	case f.IsVersion() || f.IsErased():
		return fmt.Errorf("%s: accounts: field: %q is stamped by the server, and names no account", d.at, p.Name())
	}

	for _, v := range holder.Secrets {
		if v == p.Name() {
			return fmt.Errorf(
				"%s: accounts: field: %q is declared secret, which is a verifier nothing answers with; an account "+
					"is an identifier a call is resolved by, and a membership is answered with it", d.at, p.Name())
		}
	}
	for _, v := range holder.Personal {
		if v == p.Name() {
			return fmt.Errorf(
				"%s: accounts: field: %q is declared personal, so the trail would keep that it was set and not to "+
					"what; it is an identifier, as an edge is, and which account a membership was moved to is the "+
					"record. Say `personal: false`, or nothing", d.at, p.Name())
		}
	}

	s.Accounts = &Accounts{Field: f, At: d.at, Methods: methods, rpcs: interceptRpcs(s, files)}

	return nil
}

// checkAccountIndex is a warning about an account field no index serves: a
// call is resolved by it, so without one every call an account makes reads
// every holder there is.
func checkAccountIndex(s *Schema) []string {
	if s.Accounts == nil {
		return nil
	}

	holder := s.Own(pdpb.Own_OWN_HOLDER)
	name := s.Accounts.Field.Name()
	if holder == nil || covered(holder, []string{name}) {
		return nil
	}

	return []string{fmt.Sprintf(
		"%s: accounts: no index of %s begins with %s, so every call an account makes reads every holder to find its memberships.\n"+
			"  declare it in an overlay of the holder:\n\n"+
			"    option (orm.message) = {indexes: [{name: %q, refs: [%s]}]};",
		s.Accounts.At, holder.FullName(), name, name, refsOf(holder, []string{name}))}
}

// EmitAccounts writes what an app that declares accounts resolves a call by
// and reads an account's memberships with, and what a layer serving a method
// an account calls as itself declares what it reads with. Nothing for an app
// that declares none.
//
// The wall's half -- that a call an account makes as itself reads nothing it
// did not declare -- is [EmitWall]'s, and the gate's -- that it reaches no
// method but its own -- is [EmitGate]'s.
func EmitAccounts(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	a := s.Accounts
	holder := s.Own(pdpb.Own_OWN_HOLDER)
	tenant := s.Own(pdpb.Own_OWN_TENANT)
	if a == nil || holder == nil || tenant == nil || len(holder.Via) == 0 {
		return
	}

	ctx, id := pkgCtx.Ident("Context"), pkgPdid.Ident("Id")
	field := pascal(a.Field.Name())
	hp, tp := p.EntPkg(holder), p.EntPkg(tenant)
	hop := pascal(holder.Via[0])

	g.P("// AccountMethods are the methods an account calls as itself, outside any")
	g.P("// tenant (payday#184), by the names gRPC knows them by: the ones the schema")
	g.P("// marks `(payday.method) = {account: {}}`.")
	g.P("var AccountMethods = []string{")
	for _, m := range a.Methods {
		g.P("	", root.Ident(m.Const), ",")
	}
	g.P("}")
	g.P("")
	g.P("var accountMethods = func() map[string]bool {")
	g.P("	vs := make(map[string]bool, len(AccountMethods))")
	g.P("	for _, v := range AccountMethods {")
	g.P("		vs[v] = true")
	g.P("	}")
	g.P("")
	g.P("	return vs")
	g.P("}()")
	g.P("")
	g.P("// AccountMethod reports whether `method` is one of [AccountMethods]: what a")
	g.P("// call an account makes as itself may be of (`frame.OfAccount`), and what")
	g.P("// [Gate] holds every such call to.")
	g.P("func AccountMethod(method string) bool { return accountMethods[method] }")
	g.P("")

	g.P("// HolderAccount is the account `v` is a membership of, as the holder carries")
	g.P("// it in `", a.Field.Name(), "`, and false for one that names none -- or was read")
	g.P("// without the field.")
	g.P("func HolderAccount(v *", root.Ident(holder.GoName()), ") (", id, ", bool) {")
	g.P("	a, err := ", pkgPdid.Ident("From"), "(v.Get", field, "())")
	g.P("	if err != nil || a.IsZero() {")
	g.P("		return ", pkgPdid.Ident("Nil"), ", false")
	g.P("	}")
	g.P("")
	g.P("	return a, true")
	g.P("}")
	g.P("")

	g.P("// Memberships answers an account's memberships out of `db`, past the wall:")
	g.P("// the holders that name it in `", a.Field.Name(), "` and are still there, oldest")
	g.P("// first. It is what [AccountResolver] resolves a call by -- working out who is")
	g.P("// calling happens before there is anybody to be walled by -- and what an")
	g.P("// app asks for the tenants an account is in.")
	g.P("//")
	g.P("// Hand it the deployment's own client and nothing a caller reaches: what it")
	g.P("// answers is every tenant's.")
	g.P("func Memberships(db *", p.Ent.Ident("Client"), ") ", pkgAccount.Ident("Memberships"), " { return memberships{db} }")
	g.P("")
	g.P("type memberships struct{ db *", p.Ent.Ident("Client"), " }")
	g.P("")
	g.P("func (m memberships) Of(ctx ", ctx, ", of ", id, ", in ", pkgAccount.Ident("Selector"), ", limit int) ([]", pkgAccount.Ident("Membership"), ", error) {")
	g.P("	if of.IsZero() {")
	g.P("		// Nobody's: a holder that names no account is no account's membership.")
	g.P("		return nil, nil")
	g.P("	}")
	g.P("")
	g.P("	q := m.db.", holder.GoName(), ".Query().Where(", hp.Ident(field), "(of.Uuid()))")
	for f := range holder.Fields() {
		if f.IsErased() {
			g.P("	q = q.Where(", hp.Ident(pascal(f.Name())+"IsNil"), "())")
		}
	}
	g.P("	if !in.Id.IsZero() {")
	g.P("		q = q.Where(", hp.Ident(hop+"Id"), "(in.Id.Uuid()))")
	g.P("	}")
	g.P("	if in.Alias != \"\" {")
	g.P("		q = q.Where(", hp.Ident("Has"+hop+"With"), "(", tp.Ident("Alias"), "(in.Alias)))")
	g.P("	}")
	g.P("	q = q.Order(", hp.Ident("ByDateCreated"), "(), ", hp.Ident("ById"), "())")
	g.P("	if limit > 0 {")
	g.P("		q = q.Limit(limit)")
	g.P("	}")
	g.P("")
	g.P("	vs, err := q.Select(", hp.Ident("FieldId"), ", ", hp.Ident("Field"+hop+"Id"), ").All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	out := make([]", pkgAccount.Ident("Membership"), ", len(vs))")
	g.P("	for i, v := range vs {")
	g.P("		out[i] = ", pkgAccount.Ident("Membership"), "{Holder: ", id, "(v.Id), Tenant: ", id, "(v.", hop, "Id)}")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	g.P("// AccountResolver is the resolver of an app whose holders are memberships of")
	g.P("// accounts, in front of the one it has: a credential `names` says is an")
	g.P("// account's is served as its membership of the tenant the call names in")
	g.P("// `header` -- by alias or identifier -- or its only one, and as the account")
	g.P("// itself for [AccountMethods]; any other is `next`'s, which is also asked")
	g.P("// about the membership a call was resolved to. See `account.Resolver`.")
	g.P("//")
	g.P("// `db` is the deployment's own client, past the wall, for the reason")
	g.P("// [Memberships] gives.")
	g.P("func AccountResolver(db *", p.Ent.Ident("Client"), ", header string, names func(", pkgAuth.Ident("Identity"), ") bool, next ", pkgAuth.Ident("Resolver"), ") ", pkgAccount.Ident("Resolver"), " {")
	g.P("	return ", pkgAccount.Ident("Resolver"), "{")
	g.P("		Header:      header,")
	g.P("		Names:       names,")
	g.P("		Methods:     AccountMethod,")
	g.P("		Memberships: Memberships(db),")
	g.P("		Next:        next,")
	g.P("	}")
	g.P("}")
	g.P("")

	emitReads(g, s, p)
}

// emitReads writes what a layer serving a method an account calls as itself
// says it reads with, which is what the wall reads for such a call instead of
// a tenant.
func emitReads(g *protogen.GeneratedFile, s *Schema, p Paths) {
	pred := p.Ent + "/predicate"
	ctx, id := pkgCtx.Ident("Context"), pkgPdid.Ident("Id")

	g.P("// Reads is what a method an account calls as itself reads through the walled")
	g.P("// servers (payday#184): for each entity it names, which of its rows the")
	g.P("// account may see -- a predicate, as the wall's are, given the account -- and")
	g.P("// of every entity it does not name, none. Global ones too: a row behind no")
	g.P("// wall is not therefore the account's.")
	g.P("//")
	g.P("// A row read is read with whatever its edges point at, as under a tenant's")
	g.P("// frame: declare the rows, and select of them what the method answers with.")
	g.P("type Reads struct {")
	for _, v := range s.Sorted() {
		g.P("	", v.GoName(), " func(ctx ", ctx, ", account ", id, ") (", pred.Ident(v.GoName()), ", error)")
	}
	g.P("}")
	g.P("")
	g.P("// readsKey is where [AccountReads] keeps what it was told.")
	g.P("type readsKey struct{}")
	g.P("")
	g.P("// AccountReads answers with `ctx` reading what `r` says, for the call an")
	g.P("// account makes as itself to `method` -- which has to be one of")
	g.P("// [AccountMethods]. It is the layer serving that method's to call, before it")
	g.P("// reads, and what the wall reads for that call instead of a tenant:")
	g.P("//")
	g.P("//	ctx, err := pd.AccountReads(ctx, app.InvitationService_Accept_FullMethodName, pd.Reads{")
	g.P("//		Invitation: func(ctx context.Context, account pdid.Id) (predicate.Invitation, error) {")
	g.P("//			return invitation.Account(account.Uuid()), nil")
	g.P("//		},")
	g.P("//	})")
	g.P("//")
	g.P("// A method that reads nothing through the walled servers declares that too,")
	g.P("// with an empty Reads: `pd doctor` reports a method an account calls as")
	g.P("// itself that declares nothing, since without a declaration it reads")
	g.P("// nothing and is very likely a method somebody forgot to finish.")
	g.P("//")
	g.P("// For a call served as a membership it changes nothing: that call reads")
	g.P("// what its tenant's wall says.")
	g.P("func AccountReads(ctx ", ctx, ", method string, r Reads) (", ctx, ", error) {")
	g.P("	if !AccountMethod(method) {")
	g.P("		return ctx, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("Internal"),
		", \"%s is no method an account calls as itself, and what such a call reads is said for the method it is\", method)")
	g.P("	}")
	g.P("")
	g.P("	return ", pkgCtx.Ident("WithValue"), "(ctx, readsKey{}, &r), nil")
	g.P("}")
	g.P("")
	g.P("// accountReads is the account a call is framed as and what its layer said it")
	g.P("// reads -- nothing, when it said nothing -- and false for any other call.")
	g.P("func accountReads(ctx ", ctx, ") (", id, ", *Reads, bool) {")
	g.P("	a, ok := ", pkgFrame.Ident("AccountOf"), "(ctx)")
	g.P("	if !ok {")
	g.P("		return ", pkgPdid.Ident("Nil"), ", nil, false")
	g.P("	}")
	g.P("")
	g.P("	r, _ := ctx.Value(readsKey{}).(*Reads)")
	g.P("	if r == nil {")
	g.P("		r = &Reads{}")
	g.P("	}")
	g.P("")
	g.P("	return a, r, true")
	g.P("}")
	g.P("")
}

// accountBranch writes the first thing a scope says of an entity: what a call
// an account makes as itself reads of it -- what its layer declared, or none --
// before anything is asked about a tenant, which such a call is in none of.
func accountBranch(g *protogen.GeneratedFile, s *Schema, v *Entity, p Paths) {
	pred := p.Ent + "/predicate"

	if s.Accounts == nil {
		// No account calls anything as itself here, and a frame of one that a
		// test or a hand-written resolver made reads nothing at all.
		g.P("	if _, ok := ", pkgFrame.Ident("AccountOf"), "(ctx); ok {")
		g.P("		return nothing[", pred.Ident(v.GoName()), "](), nil")
		g.P("	}")
		g.P("")
		return
	}

	g.P("	if a, r, ok := accountReads(ctx); ok {")
	g.P("		if r.", v.GoName(), " == nil {")
	g.P("			return nothing[", pred.Ident(v.GoName()), "](), nil")
	g.P("		}")
	g.P("")
	g.P("		return r.", v.GoName(), "(ctx, a)")
	g.P("	}")
	g.P("")
}

// emitNothing writes the predicate that matches no row, which is what a call
// an account makes as itself reads of an entity its layer did not name.
func emitNothing(g *protogen.GeneratedFile) {
	g.P("// nothing is the predicate that matches no row: what a call an account makes")
	g.P("// as itself reads of an entity it did not say it reads (payday#184).")
	g.P("func nothing[P ~func(*", pkgEntSql.Ident("Selector"), ")]() P {")
	g.P("	return P(func(s *", pkgEntSql.Ident("Selector"), ") { s.Where(", pkgEntSql.Ident("False"), "()) })")
	g.P("}")
	g.P("")
}

// emitGateAccounts writes, for an app that declares accounts, the question the
// gate asks before anything else of every method of every entity's service:
// whether a call an account makes as itself may be of it (`gate.AsAccount`).
//
// Every method, and not only the ones an account could find useful: a method
// left out is one a frame of an account reaches, and the generated ones are
// exactly the ones that read a tenant's rows. One wrapper per service,
// installed by the gate's own accessor so that it is in front of what the gate
// says of the service as well.
func emitGateAccounts(g *protogen.GeneratedFile, s *Schema, root protogen.GoImportPath, served map[string]bool) {
	a := s.Accounts
	if a == nil {
		return
	}

	by := map[string][]interceptRpc{}
	var order []string
	for _, v := range a.rpcs {
		if _, ok := by[v.Entity]; !ok {
			order = append(order, v.Entity)
		}
		by[v.Entity] = append(by[v.Entity], v)
	}

	for _, name := range order {
		svc := root.Ident(name + "ServiceServer")
		wrap := "accounts" + name

		if !served[name] {
			// Nothing else the gate says of this service, so this is the whole
			// of its accessor.
			g.P("func (s Gate) ", name, "() ", svc, " {")
			g.P("	return ", wrap, "{s.Next().", name, "()}")
			g.P("}")
			g.P("")
		}

		g.P("// ", wrap, " is ", name, "Service as the gate serves it to a call an account")
		g.P("// makes as itself: refused every method but the ones the schema marks the")
		g.P("// account's own. See [gate.AsAccount].")
		g.P("type ", wrap, " struct{ ", svc, " }")
		g.P("")

		for _, v := range by[name] {
			if v.Stream {
				g.P("func (s ", wrap, ") ", v.Rpc, "(req *", root.Ident(v.In),
					", out ", pkgGrpc.Ident("ServerStreamingServer"), "[", root.Ident(v.Out), "]) error {")
				g.P("	if err := ", pkgGate.Ident("AsAccount"), "(out.Context(), ", root.Ident(v.Const), ", AccountMethod); err != nil {")
				g.P("		return err")
				g.P("	}")
				g.P("")
				g.P("	return s.", name, "ServiceServer.", v.Rpc, "(req, out)")
				g.P("}")
				g.P("")
				continue
			}

			g.P("func (s ", wrap, ") ", v.Rpc, "(ctx ", pkgCtx.Ident("Context"),
				", req *", root.Ident(v.In), ") (*", root.Ident(v.Out), ", error) {")
			g.P("	if err := ", pkgGate.Ident("AsAccount"), "(ctx, ", root.Ident(v.Const), ", AccountMethod); err != nil {")
			g.P("		return nil, err")
			g.P("	}")
			g.P("")
			g.P("	return s.", name, "ServiceServer.", v.Rpc, "(ctx, req)")
			g.P("}")
			g.P("")
		}
	}
}

// accountRefused writes the gate's refusal of a holder write that says which
// account the holder is a membership of.
//
// That is what a call is resolved by: a request that could set it could make
// a holder of its own tenant somebody else's membership -- served as that
// account whenever it names the tenant, and its "only membership" no longer
// its only one -- and one that could move it could take over a membership
// another account holds. So it is the deployment's to write, through the
// server it holds, the way a stamp is; a `Patch` and an `Apply` are refused
// it as well as an `Add`, which a stamp is not, because moving it is the
// dangerous half.
func accountRefused(g *protogen.GeneratedFile, s *Schema, holder *Entity, kind string) {
	if s.Accounts == nil || holder == nil || holder.Own != pdpb.Own_OWN_HOLDER {
		return
	}

	f := s.Accounts.Field
	name := f.Name()
	at := pascal(name)
	why := "is the account this membership is of, and is written by the deployment through the server it holds, never by a request"

	switch kind {
	case "add":
		for _, v := range holder.Stamped {
			if v == name {
				// [stamps] refuses it already, in the words a stamp is refused in.
				return
			}
		}
		g.P("	if ", asserted(f), " {")

	case "patch":
		cond := "req.Has" + at + "()"
		if f.IsNullable() {
			cond += " || req.Get" + at + "Null()"
		}
		g.P("	if ", cond, " {")

	case "apply":
		g.P("	if _, ok := plan.WriteTo(", f.Number(), "); ok {")
	}

	g.P("		return nil, ", pkgPderr.Ident("Invalidf"), "(", strconv.Quote(name), ", ", strconv.Quote(why), ")")
	g.P("	}")
	g.P("")
}
