package pdgen

import (
	"slices"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdpb"
)

const (
	pkgPast   = protogen.GoImportPath("github.com/lesomnus/payday/past")
	pkgHeader = protogen.GoImportPath("github.com/lesomnus/payday/header")
)

// EmitPast writes `pd.Past`, the app's past as the `past` package writes and
// reads it (payday#151): a `past.Store` over the app's Event and Fact.
//
// Generated for the reason the trail's store and the access record's sink
// are. A write of the past goes through the app's own servers in the
// operation's transaction -- so that the wall, the trail and every recorder
// meet it as they meet any other write -- and a read through its ent client,
// and payday can name neither. What is here is the mechanical half: a request
// built from a row, a row from what ent read, an Add or a page of AddMany, a
// Patch. Every judgement is the package's: what an event is filed under,
// which facts a fact supersedes, how a value is written, which rows a read
// takes and whose.
//
// Only into an app that has payday's Event and Fact, which is one that
// declares a past -- [Schema.checkPast] refuses the copies without the
// declaration -- and only beside the trail, whose readers it reuses: the
// tenant of a subject is found the way the recorder finds a row's, and an act
// beneath the servers goes on the trail through `Record`.
//
// And the one writer of them: every context the store hands the servers is
// marked for the Audit layer, which refuses a write of either that is not
// ([emitAuditPast]).
func EmitPast(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	ev, fact := pastGlued(s)
	if ev == nil {
		return
	}

	e, f, t := ev.GoName(), fact.GoName(), s.Tenant.GoName()
	evPkg := p.Ent + "/" + protogen.GoImportPath(ev.EntPkg())
	factPkg := p.Ent + "/" + protogen.GoImportPath(fact.EntPkg())
	pred := p.Ent + "/predicate"

	g.P("// Past is this app's past as the `past` package writes and reads it")
	g.P("// (payday#151): its ", e, " and ", f, ", written through `next` -- the stack an")
	g.P("// operation writes through -- on `drv`, and read through ent on `drv`.")
	g.P("// `content` is the trail's (`audit.content`, `audit.digest.key`), what an")
	g.P("// act of the past beneath the servers is recorded with: see [Record].")
	g.P("//")
	g.P("//	drv, tx, err := dialect.BeginTx(ctx, s.drv)")
	g.P("//	...")
	g.P("//	st, err := pd.Past(s.Next(), drv, content)")
	g.P("//	ctx, op, err := past.Begin(ctx, st, past.Event{Subject: id, Value: moved})")
	g.P("//")
	g.P("// `next` is put on `drv` here rather than trusted to be, so that every")
	g.P("// write of the past is in the transaction its operation is; it answers")
	g.P("// an error for a stack a layer of which cannot be put on another driver,")
	g.P("// whose writes would go around that layer.")
	g.P("//")
	g.P("// Its writes are the only ones of ", e, " and ", f, " the Audit layer lets")
	g.P("// through, wherever in `next` that layer is: each is marked for it, on the")
	g.P("// context handed to the servers and no other ([pastWrite]).")
	g.P("func Past(next ", root.Ident("Server"), ", drv ", pkgDialect.Ident("Driver"), ", content ", pkgAudit.Ident("Contents"),
		") (", pkgPast.Ident("Store"), ", error) {")
	g.P("	s, err := ", pkgEnttx.Ident("Rebind"), "(next, drv)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return pastStore{next: s, drv: drv, db: ", p.Ent.Ident("NewClient"), "(", p.Ent.Ident("Driver"), "(drv)), content: content}, nil")
	g.P("}")
	g.P("")
	g.P("type pastStore struct {")
	g.P("	next    ", root.Ident("Server"))
	g.P("	drv     ", pkgDialect.Ident("Driver"))
	g.P("	db      *", p.Ent.Ident("Client"))
	g.P("	content ", pkgAudit.Ident("Contents"))
	g.P("}")
	g.P("")
	g.P("var _ ", pkgPast.Ident("Store"), " = pastStore{}")
	g.P("")
	g.P("// Kinds is [EventKinds].")
	g.P("func (pastStore) Kinds() map[string]", pkgReflect.Ident("MessageType"), " { return EventKinds }")
	g.P("")
	g.P("func (s pastStore) Driver() ", pkgDialect.Ident("Driver"), " { return s.drv }")
	g.P("")

	g.P("// AddEvents writes each event through the ", e, " server: an Add for one, an")
	g.P("// AddMany of at most a page for more. On a context marked as the past's")
	g.P("// write, which is what the Audit layer lets one through on.")
	g.P("func (s pastStore) AddEvents(ctx ", pkgCtx.Ident("Context"), ", vs []", pkgPast.Ident("EventRow"), ") ([]", pkgPdid.Ident("Id"), ", error) {")
	g.P("	ctx = pastWriting(ctx)")
	g.P("")
	g.P("	reqs := make([]*", root.Ident(e+"AddRequest"), ", len(vs))")
	g.P("	for i, v := range vs {")
	g.P("		req := ", root.Ident(e+"AddRequest_builder"), "{")
	g.P("			Tenant:      ", root.Ident(t+"Ref_builder"), "{Id: v.Tenant.Bytes()}.Build(),")
	g.P("			Kind:        v.Kind,")
	g.P("			OccurredAt:  ", pkgTimestamppb.Ident("New"), "(v.At),")
	g.P("			Reason:      v.Reason,")
	g.P("			DateCreated: ", pkgTimestamppb.Ident("New"), "(v.Recorded),")
	g.P("			Value:       v.Value,")
	g.P("		}.Build()")
	g.P("		if !v.Id.IsZero() {")
	g.P("			req.SetId(v.Id.Bytes())")
	g.P("		}")
	g.P("		if !v.Actor.IsZero() {")
	g.P("			req.SetActorId(v.Actor.Bytes())")
	g.P("		}")
	g.P("		if !v.Subject.IsZero() {")
	g.P("			req.SetSubjectId(v.Subject.Bytes())")
	g.P("		}")
	g.P("		if !v.Cause.IsZero() {")
	g.P("			req.SetCauseId(v.Cause.Bytes())")
	g.P("		}")
	g.P("		if len(v.Trace) > 0 {")
	g.P("			req.SetTraceId(v.Trace)")
	g.P("		}")
	g.P("		reqs[i] = req")
	g.P("	}")
	g.P("")
	g.P("	out := make([]", pkgPdid.Ident("Id"), ", 0, len(reqs))")
	g.P("	if len(reqs) == 1 {")
	g.P("		v, err := s.next.", e, "().Add(ctx, reqs[0])")
	g.P("		if err != nil {")
	g.P("			return nil, err")
	g.P("		}")
	g.P("		id, err := ", pkgPdid.Ident("From"), "(v.GetId())")
	g.P("		if err != nil {")
	g.P("			return nil, err")
	g.P("		}")
	g.P("")
	g.P("		return append(out, id), nil")
	g.P("	}")
	g.P("	for page := range ", pkgSlices.Ident("Chunk"), "(reqs, ", p.Bare.Ident(e+"AddManyMax"), ") {")
	g.P("		res, err := s.next.", e, "().AddMany(ctx, ", root.Ident(e+"AddManyRequest_builder"), "{Items: page}.Build())")
	g.P("		if err != nil {")
	g.P("			return nil, err")
	g.P("		}")
	g.P("		for _, v := range res.GetItems() {")
	g.P("			id, err := ", pkgPdid.Ident("From"), "(v.GetId())")
	g.P("			if err != nil {")
	g.P("				return nil, err")
	g.P("			}")
	g.P("			out = append(out, id)")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	g.P("// AddFacts writes each fact through the ", f, " server, as [pastStore.AddEvents]")
	g.P("// writes events.")
	g.P("func (s pastStore) AddFacts(ctx ", pkgCtx.Ident("Context"), ", vs []", pkgPast.Ident("FactRow"), ") error {")
	g.P("	ctx = pastWriting(ctx)")
	g.P("")
	g.P("	reqs := make([]*", root.Ident(f+"AddRequest"), ", len(vs))")
	g.P("	for i, v := range vs {")
	g.P("		field, domain := uint32(v.Field), uint32(v.Domain)")
	g.P("		reqs[i] = ", root.Ident(f+"AddRequest_builder"), "{")
	g.P("			Tenant:      ", root.Ident(t+"Ref_builder"), "{Id: v.Tenant.Bytes()}.Build(),")
	g.P("			SubjectId:   v.Subject.Bytes(),")
	g.P("			Key:         v.Key,")
	g.P("			Value:       v.Value,")
	g.P("			FieldNumber: &field,")
	g.P("			Domain:      &domain,")
	g.P("			ValidFrom:   ", pkgTimestamppb.Ident("New"), "(v.From),")
	g.P("			EventId:     v.Event.Bytes(),")
	g.P("			DateCreated: ", pkgTimestamppb.Ident("New"), "(v.Recorded),")
	g.P("		}.Build()")
	g.P("	}")
	g.P("")
	g.P("	if len(reqs) == 1 {")
	g.P("		_, err := s.next.", f, "().Add(ctx, reqs[0])")
	g.P("		return err")
	g.P("	}")
	g.P("	for page := range ", pkgSlices.Ident("Chunk"), "(reqs, ", p.Bare.Ident(f+"AddManyMax"), ") {")
	g.P("		if _, err := s.next.", f, "().AddMany(ctx, ", root.Ident(f+"AddManyRequest_builder"), "{Items: page}.Build()); err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")

	g.P("// Supersede is a Patch of each fact, held to the version it was read at,")
	g.P("// on a context marked as the past's write.")
	g.P("func (s pastStore) Supersede(ctx ", pkgCtx.Ident("Context"), ", vs []", pkgPast.Ident("FactRow"), ", at ", pkgTime.Ident("Time"),
		", by ", pkgPdid.Ident("Id"), ") error {")
	g.P("	ctx = pastWriting(ctx)")
	g.P("")
	g.P("	for _, v := range vs {")
	g.P("		if _, err := s.next.", f, "().Patch(ctx, ", root.Ident(f+"PatchRequest_builder"), "{")
	g.P("			Ref:          ", root.Ident(f+"Ref_builder"), "{Id: v.Id.Bytes()}.Build(),")
	g.P("			DateUpdated:  ", pkgTimestamppb.Ident("New"), "(v.Version),")
	g.P("			SupersededAt: ", pkgTimestamppb.Ident("New"), "(at),")
	g.P("			SupersededBy: by.Bytes(),")
	g.P("		}.Build()); err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")

	g.P("// Events reads events through ent, by the predicates the package built.")
	g.P("func (s pastStore) Events(ctx ", pkgCtx.Ident("Context"), ", q ", pkgPast.Ident("Select"), ") ([]", pkgPast.Ident("EventRow"), ", error) {")
	g.P("	query := s.db.", e, ".Query()")
	g.P("	for _, v := range q.Where {")
	g.P("		query = query.Where(", pred.Ident(e), "(v))")
	g.P("	}")
	g.P("	for _, v := range q.Order {")
	g.P("		query = query.Order(", evPkg.Ident("OrderOption"), "(v))")
	g.P("	}")
	g.P("	if q.Limit > 0 {")
	g.P("		query = query.Limit(q.Limit)")
	g.P("	}")
	g.P("")
	g.P("	vs, err := query.All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	out := make([]", pkgPast.Ident("EventRow"), ", len(vs))")
	g.P("	for i, v := range vs {")
	g.P("		r := ", pkgPast.Ident("EventRow"), "{")
	g.P("			Id:       ", pkgPdid.Ident("Id"), "(v.Id),")
	g.P("			Tenant:   ", pkgPdid.Ident("Id"), "(v.TenantId),")
	g.P("			Kind:     v.Kind,")
	g.P("			At:       v.OccurredAt,")
	g.P("			Reason:   v.Reason,")
	g.P("			Trace:    v.TraceId,")
	g.P("			Value:    v.Value,")
	g.P("			Recorded: v.DateCreated,")
	g.P("		}")
	g.P("		if v.ActorId != nil {")
	g.P("			r.Actor = ", pkgPdid.Ident("Id"), "(*v.ActorId)")
	g.P("		}")
	g.P("		if v.SubjectId != nil {")
	g.P("			r.Subject = ", pkgPdid.Ident("Id"), "(*v.SubjectId)")
	g.P("		}")
	g.P("		if v.CauseId != nil {")
	g.P("			r.Cause = ", pkgPdid.Ident("Id"), "(*v.CauseId)")
	g.P("		}")
	g.P("		out[i] = r")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	g.P("// Facts reads facts through ent, by the predicates the package built.")
	g.P("func (s pastStore) Facts(ctx ", pkgCtx.Ident("Context"), ", q ", pkgPast.Ident("Select"), ") ([]", pkgPast.Ident("FactRow"), ", error) {")
	g.P("	query := s.db.", f, ".Query()")
	g.P("	for _, v := range q.Where {")
	g.P("		query = query.Where(", pred.Ident(f), "(v))")
	g.P("	}")
	g.P("	for _, v := range q.Order {")
	g.P("		query = query.Order(", factPkg.Ident("OrderOption"), "(v))")
	g.P("	}")
	g.P("	if q.Limit > 0 {")
	g.P("		query = query.Limit(q.Limit)")
	g.P("	}")
	g.P("")
	g.P("	vs, err := query.All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	out := make([]", pkgPast.Ident("FactRow"), ", len(vs))")
	g.P("	for i, v := range vs {")
	g.P("		r := ", pkgPast.Ident("FactRow"), "{")
	g.P("			Id:         ", pkgPdid.Ident("Id"), "(v.Id),")
	g.P("			Tenant:     ", pkgPdid.Ident("Id"), "(v.TenantId),")
	g.P("			Subject:    ", pkgPdid.Ident("Id"), "(v.SubjectId),")
	g.P("			Domain:     ", pkgPdid.Ident("Domain"), "(v.Domain),")
	g.P("			Field:      ", pkgReflect.Ident("FieldNumber"), "(v.FieldNumber),")
	g.P("			Key:        v.Key,")
	g.P("			Value:      v.Value,")
	g.P("			From:       v.ValidFrom,")
	g.P("			Recorded:   v.DateCreated,")
	g.P("			Event:      ", pkgPdid.Ident("Id"), "(v.EventId),")
	g.P("			Version:    v.DateUpdated,")
	g.P("			Superseded: v.SupersededAt,")
	g.P("		}")
	g.P("		if v.SupersededBy != nil {")
	g.P("			r.SupersededBy = ", pkgPdid.Ident("Id"), "(*v.SupersededBy)")
	g.P("		}")
	g.P("		out[i] = r")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	g.P("// Describe is [described]: what the trail withholds of a row is what a")
	g.P("// fact withholds of a field.")
	g.P("func (pastStore) Describe(subject ", pkgPdid.Ident("Id"), ") (", pkgPast.Ident("Description"), ", bool) {")
	g.P("	md, secret, personal := described(subject)")
	g.P("	if md == nil {")
	g.P("		return ", pkgPast.Ident("Description"), "{}, false")
	g.P("	}")
	g.P("")
	g.P("	return ", pkgPast.Ident("Description"), "{Message: md, Secret: secret, Personal: personal}, true")
	g.P("}")
	g.P("")

	g.P("// TenantOf reads each row as a check of the rows reads it, past the wall,")
	g.P("// and its tenant as the recorder finds the one a write is filed under")
	g.P("// ([subject]): the same answer to the same question, which is whose a row is.")
	g.P("func (s pastStore) TenantOf(ctx ", pkgCtx.Ident("Context"), ", ids []", pkgPdid.Ident("Id"), ") (map[", pkgPdid.Ident("Id"), "]", pkgPdid.Ident("Id"), ", error) {")
	g.P("	srv, err := ", p.Bare.Ident("NewServer"), "(s.db)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	byKind := map[", pkgPdid.Ident("Domain"), "][]", pkgPdid.Ident("Id"), "{}")
	g.P("	for _, v := range ids {")
	g.P("		byKind[v.Domain()] = append(byKind[v.Domain()], v)")
	g.P("	}")
	g.P("")
	g.P("	out := map[", pkgPdid.Ident("Id"), "]", pkgPdid.Ident("Id"), "{}")
	g.P("	for _, d := range ", pkgSlices.Ident("Sorted"), "(", pkgMaps.Ident("Keys"), "(byKind)) {")
	g.P("		for page := range ", pkgSlices.Ident("Chunk"), "(byKind[d], ", pkgAudit.Ident("RowsPage"), ") {")
	g.P("			var rows []", pkgAudit.Ident("Live"))
	g.P("			global := false")
	g.P("			switch d {")
	for _, v := range s.Sorted() {
		if v.Records() {
			// payday's own records, which are about rows and are none.
			continue
		}
		if !v.IsGlobal && !v.Serves() && !v.Checked() {
			// A kind behind the wall no server writes and the trail does not
			// read, whose tenant [subject] is never asked for.
			continue
		}

		entPkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())
		g.P("			case ", v.GoName(), "Domain:")
		g.P("				rows, err = rows", v.GoName(), "(ctx, s.db, ", entPkg.Ident("IdIn"), "(uuids(page)...))")
		if v.IsGlobal {
			g.P("				global = true")
		}
	}
	g.P("			default:")
	g.P("				return nil, ", pkgFmt.Ident("Errorf"), "(\"%s is of a kind whose tenant this app cannot say, so it is the subject of no past\", page[0])")
	g.P("			}")
	g.P("			if err != nil {")
	g.P("				return nil, err")
	g.P("			}")
	g.P("")
	g.P("			for _, v := range rows {")
	g.P("				if global {")
	g.P("					out[v.Object] = ", pkgPdid.Ident("Nil"))
	g.P("					continue")
	g.P("				}")
	g.P("")
	g.P("				t, _, err := subject(ctx, srv, v.Object, v.Row, false)")
	g.P("				if err != nil {")
	g.P("					return nil, err")
	g.P("				}")
	g.P("				if t == ", pkgUuid.Ident("Nil"), "() {")
	g.P("					// Behind the wall and reaching no tenant -- a first hop")
	g.P("					// left empty -- which the wall shows to nobody: as no row.")
	g.P("					continue")
	g.P("				}")
	g.P("				out[v.Object] = ", pkgPdid.Ident("Id"), "(t)")
	g.P("			}")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")

	g.P("// Redact blanks the reason of an event through ent -- an event has no")
	g.P("// Patch -- and puts that on the trail as `action`, in the same transaction:")
	g.P("// a line by whoever `ctx` is framed as, with the event's digest as it is")
	g.P("// now and its seal made again ([Record]).")
	g.P("func (s pastStore) Redact(ctx ", pkgCtx.Ident("Context"), ", id ", pkgPdid.Ident("Id"), ", action string) error {")
	g.P("	n, err := s.db.", e, ".Update().Where(", evPkg.Ident("IdEQ"), "(", pkgUuid.Ident("UUID"), "(id))).SetReason(\"\").Save(ctx)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("	if n == 0 {")
	g.P("		return ", pkgStatus.Ident("Error"), "(", pkgCodes.Ident("NotFound"), ", \"", e, " not found\")")
	g.P("	}")
	g.P("")
	g.P("	_, err = Record(ctx, s.db, s.content, action, []", pkgPdid.Ident("Id"), "{id})")
	g.P("	return err")
	g.P("}")
	g.P("")

	emitPastNames(g, s, p)
	emitPastExpiry(g, s, p, root)
}

// emitPastNames writes `pd.PastNames`, what names the people and things a
// sentence of the past names, as they are called when it is read: the
// `past.Names` an app has without writing one of its own.
//
// Generated for the reason `pd.Past` is: a row is read through the app's own
// ent client, narrowed by its own wall, and payday can name neither. What a
// row is called is not decided here either -- it is `header.Of(row).Title()`,
// which is what every other reader of a row it has no type for shows -- and
// whose data saying it shows is what the schema already declared: the row's
// own, when the field the title was read off is `personal`.
//
// Of the entities that have something to be called by, an `alias` or a
// `name`, and not payday's records, which have neither. An identifier of any
// other kind is named by nothing, and a sentence says it as itself.
func emitPastNames(g *protogen.GeneratedFile, s *Schema, p Paths) {
	type titled struct {
		v           *Entity
		alias, name bool
	}
	var vs []titled
	for _, v := range s.Sorted() {
		if v.Records() {
			continue
		}

		fs := v.Descriptor().Fields()
		alias, name := fs.ByName("alias"), fs.ByName("name")
		has := func(fd protoreflect.FieldDescriptor) bool {
			return fd != nil && fd.Kind() == protoreflect.StringKind && !fd.IsList() && !fd.IsMap()
		}
		if !has(alias) && !has(name) {
			continue
		}
		vs = append(vs, titled{
			v:     v,
			alias: has(alias) && slices.Contains(v.Personal, string(alias.Name())),
			name:  has(name) && slices.Contains(v.Personal, string(name.Name())),
		})
	}

	g.P("// PastNames is what names the people and things a sentence of this app's")
	g.P("// past names, as they are called when it is read (payday#151): a row of any")
	g.P("// entity with a name or an alias, by `header.Of(row).Title()` -- its name,")
	g.P("// else its alias -- read on `drv` through the wall, so that a sentence names")
	g.P("// nothing its reader could not read for themselves.")
	g.P("//")
	g.P("// A title read off a field the entity declares `personal` -- a holder's --")
	g.P("// is that person's data, and it answers the row as whose it is, which the")
	g.P("// `past` package puts on the access record of the call that said it. An")
	g.P("// identifier of a row that is gone, of one the caller may not see, or of a")
	g.P("// kind with nothing to be called by is named by nothing.")
	g.P("//")
	g.P("//	r := sentences.Read(ctx, pd.PastNames(drv), locale, zone)")
	g.P("func PastNames(drv ", pkgDialect.Ident("Driver"), ") ", pkgPast.Ident("Names"), " {")
	g.P("	return pastNames{db: ", p.Ent.Ident("NewClient"), "(", p.Ent.Ident("Driver"), "(drv))}")
	g.P("}")
	g.P("")
	g.P("type pastNames struct{ db *", p.Ent.Ident("Client"), " }")
	g.P("")
	g.P("var _ ", pkgPast.Ident("Names"), " = pastNames{}")
	g.P("")
	g.P("// Name reads the row `id` names through the wall, and answers what it is")
	g.P("// called and -- where that was read off a field declared `personal` -- the")
	g.P("// row, as whose data saying it is.")
	g.P("func (n pastNames) Name(ctx ", pkgCtx.Ident("Context"), ", id ", pkgPdid.Ident("Id"), ") (string, []", pkgAccess.Ident("Subject"), ", error) {")
	g.P("	var row ", pkgProto.Ident("Message"))
	g.P("	var alias, name bool")
	g.P("	switch id.Domain() {")
	for _, t := range vs {
		e := t.v.GoName()
		entPkg := p.Ent + "/" + protogen.GoImportPath(t.v.EntPkg())
		g.P("	case ", e, "Domain:")
		g.P("		p, err := ", p.Bare.Ident(e+"Narrow"), "(ctx, Wall(), ", entPkg.Ident("IdEQ"), "(id.Uuid()))")
		g.P("		if err != nil {")
		g.P("			return \"\", nil, err")
		g.P("		}")
		g.P("		v, err := n.db.", e, ".Query().Where(p).Only(ctx)")
		g.P("		if err != nil {")
		g.P("			return pastUnnamed(err)")
		g.P("		}")
		g.P("		row, alias, name = v.Proto(), ", t.alias, ", ", t.name)
	}
	g.P("	default:")
	g.P("		return \"\", nil, nil")
	g.P("	}")
	g.P("")
	g.P("	h := ", pkgHeader.Ident("Of"), "(row)")
	g.P("	if h.Name != \"\" && name || h.Name == \"\" && h.Alias != \"\" && alias {")
	g.P("		return h.Title(), []", pkgAccess.Ident("Subject"), "{{Id: id, Tenant: h.Tenant}}, nil")
	g.P("	}")
	g.P("")
	g.P("	return h.Title(), nil, nil")
	g.P("}")
	g.P("")
	g.P("// pastUnnamed is a row that could not be read: named by nothing when there")
	g.P("// is none to read -- gone, or behind the wall -- and the error otherwise.")
	g.P("func pastUnnamed(err error) (string, []", pkgAccess.Ident("Subject"), ", error) {")
	g.P("	if ", p.Ent.Ident("IsNotFound"), "(err) {")
	g.P("		return \"\", nil, nil")
	g.P("	}")
	g.P("")
	g.P("	return \"\", nil, err")
	g.P("}")
	g.P("")
}

// emitPastExpiry writes what the past's expiry asks of the store, and the two
// things `past.Expirer` is built from (payday#151): every column of the app's
// that names an event, the deletes beneath the servers and the act that puts
// them on the trail, the counts a preview is made of; and `PastIn` and
// `PastTenants`, a tenant's transaction and the tenants there are.
//
// # The columns, which only the schema knows
//
// An event goes once no row that stays names it, so the expiry has to know
// every column that can: the past's own, which payday's templates mark, and the
// app's, which it marks itself -- `(payday.field) = {event: true}`, read into
// [Entity.Pointers]. Each is written here by the names ent gives its table and
// its column, so a table renamed is a compile error and not a check that asks
// about a table nobody has.
//
// # Deletes beneath the servers
//
// Neither entity has an Erase: an event is never changed and a fact only
// superseded, so their rows leave by expiry or with their tenant and nothing a
// caller asks for. So the deletes are ent's, and every row they take is put on
// the trail as the act that took it ([RecordErased]), in the same transaction.
func emitPastExpiry(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	ev, fact := s.Own(pdpb.Own_OWN_EVENT), s.Own(pdpb.Own_OWN_FACT)
	e, f := ev.GoName(), fact.GoName()
	evPkg := p.Ent + "/" + protogen.GoImportPath(ev.EntPkg())
	factPkg := p.Ent + "/" + protogen.GoImportPath(fact.EntPkg())
	pred := p.Ent + "/predicate"

	g.P("// Pointers is [pastPointers].")
	g.P("func (pastStore) Pointers() []", pkgPast.Ident("Pointer"), " { return pastPointers }")
	g.P("")
	g.P("// pastPointers is every column of this app's that names an event,")
	g.P("// `(payday.field) = {event: true}`: what the expiry asks of each event it")
	g.P("// would take, whether a row that stays names it.")
	g.P("var pastPointers = []", pkgPast.Ident("Pointer"), "{")
	for _, v := range s.Sorted() {
		entPkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())
		for _, name := range v.Pointers {
			g.P("	{Domain: ", v.GoName(), "Domain, Table: ", entPkg.Ident("Table"), ", Column: ", entPkg.Ident("Field"+pascal(name)),
				", Own: ", pkgPdpb.Ident("Own_"+v.Own.String()), "},")
		}
	}
	g.P("}")
	g.P("")

	for _, v := range []struct {
		name, kind string
		pkg        protogen.GoImportPath
	}{{e, "Events", evPkg}, {f, "Facts", factPkg}} {
		g.P("// Delete", v.kind, " deletes the rows `ids` names through ent, a page at a time:")
		g.P("// ", v.name, " has no Erase, and its rows leave by expiry or with their tenant.")
		g.P("// What it takes is put on the trail by [pastStore.Erased].")
		g.P("func (s pastStore) Delete", v.kind, "(ctx ", pkgCtx.Ident("Context"), ", ids []", pkgPdid.Ident("Id"), ") (int, error) {")
		g.P("	n := 0")
		g.P("	for page := range ", pkgSlices.Ident("Chunk"), "(ids, ", pkgAudit.Ident("RowsPage"), ") {")
		g.P("		k, err := s.db.", v.name, ".Delete().Where(", v.pkg.Ident("IdIn"), "(uuids(page)...)).Exec(ctx)")
		g.P("		n += k")
		g.P("		if err != nil {")
		g.P("			return n, err")
		g.P("		}")
		g.P("	}")
		g.P("")
		g.P("	return n, nil")
		g.P("}")
		g.P("")
		g.P("// Count", v.kind, " counts the rows `where` takes, through ent.")
		g.P("func (s pastStore) Count", v.kind, "(ctx ", pkgCtx.Ident("Context"), ", where []func(*", pkgEntSql.Ident("Selector"), ")) (int, error) {")
		g.P("	query := s.db.", v.name, ".Query()")
		g.P("	for _, v := range where {")
		g.P("		query = query.Where(", pred.Ident(v.name), "(v))")
		g.P("	}")
		g.P("")
		g.P("	return query.Count(ctx)")
		g.P("}")
		g.P("")
	}

	g.P("// Erased puts on the trail the rows `ids`, deleted beneath the servers, as the")
	g.P("// act `action` ([RecordErased]): by whoever `ctx` is framed as, filed under")
	g.P("// its tenant, in the same transaction.")
	g.P("func (s pastStore) Erased(ctx ", pkgCtx.Ident("Context"), ", action string, ids []", pkgPdid.Ident("Id"), ") error {")
	g.P("	_, err := RecordErased(ctx, s.db, s.content, action, ids)")
	g.P("	return err")
	g.P("}")
	g.P("")

	g.P("// PastIn is where the past's expiry does a tenant's (payday#151): `f`, in a")
	g.P("// transaction of its own on `drv`, with the context framed as the")
	g.P("// deployment's work in `tenant` -- nobody's, and of that tenant alone -- and a")
	g.P("// store of the past through `ungated` put on that transaction. Committed when")
	g.P("// `f` answers nil, and rolled back otherwise. What `past.Expirer.In` is.")
	g.P("//")
	g.P("// `ungated` is the stack with no wall, as for every sweep: an expiry is the")
	g.P("// deployment acting on itself, and nobody's in the tenant. Never one a caller")
	g.P("// reaches.")
	g.P("//")
	g.P("// What `f` is handed writes the past through the store and no other way:")
	g.P("// the store marks what it hands the servers, and the context `f` is given")
	g.P("// is no write of the past's, so a row `f` writes by hand with it is refused")
	g.P("// as any other is ([pastWrite]).")
	g.P("func PastIn(ungated ", root.Ident("Server"), ", drv ", pkgDialect.Ident("Driver"), ", content ", pkgAudit.Ident("Contents"), ") func(ctx ",
		pkgCtx.Ident("Context"), ", tenant ", pkgPdid.Ident("Id"), ", f func(ctx ", pkgCtx.Ident("Context"), ", s ", pkgPast.Ident("Store"), ") error) error {")
	g.P("	return func(ctx ", pkgCtx.Ident("Context"), ", tenant ", pkgPdid.Ident("Id"), ", f func(ctx ", pkgCtx.Ident("Context"), ", s ", pkgPast.Ident("Store"), ") error) error {")
	g.P("		ctx = ", pkgFrame.Ident("Into"), "(ctx, ", pkgFrame.Ident("New"), "(", pkgPdid.Ident("Nil"), ", tenant, ", pkgFrame.Ident("Grant"), "{}).WithScope(", pkgFrame.Ident("Only"), "(tenant)))")
	g.P("")
	g.P("		tdrv, tx, err := ", pkgDialect.Ident("BeginTx"), "(ctx, drv)")
	g.P("		if err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("		defer tx.Rollback()")
	g.P("")
	g.P("		s, err := Past(ungated, tdrv, content)")
	g.P("		if err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("		if err := f(ctx, s); err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("")
	g.P("		return tx.Commit()")
	g.P("	}")
	g.P("}")
	g.P("")

	g.P("// PastTenants is every tenant with a past, a page of at most `limit` after")
	g.P("// `after` in the order of their identifiers: what `past.Expirer.Tenants` is.")
	g.P("// One seek per tenant, the next `tenant_id` of the ", e, " after the last, which")
	g.P("// its index on the tenant answers without reading the rows between. A fact")
	g.P("// names the event that wrote it, which stays while it does, so a tenant with")
	g.P("// facts has events.")
	g.P("func PastTenants(db *", p.Ent.Ident("Client"), ") func(ctx ", pkgCtx.Ident("Context"), ", after ", pkgPdid.Ident("Id"),
		", limit int) ([]", pkgPdid.Ident("Id"), ", error) {")
	g.P("	return func(ctx ", pkgCtx.Ident("Context"), ", after ", pkgPdid.Ident("Id"), ", limit int) ([]", pkgPdid.Ident("Id"), ", error) {")
	g.P("		out := []", pkgPdid.Ident("Id"), "{}")
	g.P("		last := after.Uuid()")
	g.P("		for len(out) < limit {")
	g.P("			v, err := db.", e, ".Query().")
	g.P("				Where(", pred.Ident(e), "(", pkgEntSql.Ident("FieldGT"), "(", evPkg.Ident("FieldTenantId"), ", last))).")
	g.P("				Order(", p.Ent.Ident("Asc"), "(", evPkg.Ident("FieldTenantId"), ")).")
	g.P("				First(ctx)")
	g.P("			switch {")
	g.P("			case ", p.Ent.Ident("IsNotFound"), "(err):")
	g.P("				return out, nil")
	g.P("			case err != nil:")
	g.P("				return out, err")
	g.P("			}")
	g.P("")
	g.P("			out = append(out, ", pkgPdid.Ident("Id"), "(v.TenantId))")
	g.P("			last = v.TenantId")
	g.P("		}")
	g.P("")
	g.P("		return out, nil")
	g.P("	}")
	g.P("}")
	g.P("")
}

// pastGlued is payday's Event and Fact in a schema `pd gen` writes the past's
// store into, and nil and nil in any other: one that declares a past, beside
// the tenant and the trail the store reuses.
//
// One answer for the store and the Audit layer's half of it
// ([emitAuditPast]), because each is wrong without the other: refusals with no
// store are a past nothing can write, and a store with no refusals is one
// anybody can write around.
func pastGlued(s *Schema) (*Entity, *Entity) {
	ev, fact := s.Own(pdpb.Own_OWN_EVENT), s.Own(pdpb.Own_OWN_FACT)
	if ev == nil || fact == nil || s.Tenant == nil || !s.Has(pdpb.Own_OWN_AUDIT) {
		return nil, nil
	}

	return ev, fact
}

// emitAuditPast writes the Audit layer's half of the past (payday#151): its
// Event and its Fact, which refuse every write but the ones `pd.Past`'s store
// hands the servers, and the key that store marks those with.
//
// # Why payday seals these, when it leaves which verbs are internal to an app
//
// The server guide leaves a verb only an app's own operations call to the app,
// since which they are is a property of the app and not of its schema. These
// two are payday's, and so is what makes a row of them right. A fact is the
// canonical JSON of one field, withheld where the field is personal, and
// supersedes what it replaces: one written by hand is a row no read of the
// past can be sure of, a state at a moment or a diff saying what nothing did.
// An event's actor is the caller's frame, and a request that names one is who
// did it, forged. And with no policy -- the app `pd new` writes has none --
// every caller in a tenant reaches both Adds. So the rule is the schema's
// after all, and it is generated rather than left for each app to find.
//
// # Why a key on the context
//
// A layer cannot tell what wrote a request by what it is handed: a call over
// the wire, an operation of a batch and the store's own write are each a
// context and a message. What only the store can do is put a value on the
// context whose type nothing outside the generated package can name -- not an
// app's code, not a batch's dispatch, not gRPC -- so that is what is read. An
// identity of the caller would be a credential, which somebody can hold; this
// is a line of code, which only the store is.
//
// # Where it is
//
// In the Audit layer, beside the trail's own refusals and for their reason: it
// is the layer every stack of an app is built with, the ungated one too, so
// whatever enters a stack above it -- a call, a batch, the deployment's own
// tool -- meets it. The store's writes carry their mark down through every
// layer between, so a layer that records the past may be anywhere: above the
// gate, as rove's domain layer is, or beneath Audit, as apptest's core is.
// What it does not reach is beneath it: a layer of the app's under Audit
// writes past it, as it writes past the trail's refusals.
//
// # What it does not touch
//
// Reads, which the wall and the policy answer as they do any entity's. And the
// writes the store makes beneath the servers -- a reason redacted, what an
// expiry takes -- which no server sees, and which the store puts on the trail
// as the acts they are.
func emitAuditPast(g *protogen.GeneratedFile, s *Schema, root protogen.GoImportPath) {
	ev, fact := pastGlued(s)
	if ev == nil {
		return
	}

	for _, v := range []*Entity{ev, fact} {
		name := v.GoName()
		lower := "audit" + name
		r := v.Rpcs()

		g.P("// ", name, " is the past's ", name, " as the Audit layer serves it: read as any")
		g.P("// entity is, and written by the `past` package alone ([pastWrite]).")
		g.P("func (s Audit) ", name, "() ", root.Ident(name+"ServiceServer"), " {")
		g.P("	return ", lower, "{s, s.Next().", name, "()}")
		g.P("}")
		g.P("")
		g.P("type ", lower, " struct {")
		g.P("	Audit")
		g.P("	", root.Ident(name+"ServiceServer"))
		g.P("}")
		g.P("")

		// Every verb that writes, of the ones the entity has: what a template
		// of payday's declares is what is served, so it is read off it rather
		// than listed, and a verb a later template adds is refused with the
		// rest rather than left open beside them.
		for _, m := range []struct {
			rpc, in, out string
			has          bool
		}{
			{"Add", name + "AddRequest", name, r.HasAdd()},
			{"AddMany", name + "AddManyRequest", name + "AddManyResponse", r.HasAddMany()},
			{"Patch", name + "PatchRequest", name, r.HasPatch()},
			{"Apply", name + "ApplyRequest", name, r.HasPatch()},
			{"Erase", name + "Ref", name + "EraseResponse", r.HasErase()},
		} {
			if !m.has {
				continue
			}

			g.P("func (s ", lower, ") ", m.rpc, "(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(m.in),
				") (*", root.Ident(m.out), ", error) {")
			g.P("	if !isPastWrite(ctx) {")
			g.P("		return nil, errPast()")
			g.P("	}")
			g.P("")
			g.P("	return s.", name, "ServiceServer.", m.rpc, "(ctx, req)")
			g.P("}")
			g.P("")
		}
	}

	g.P("// pastWrite marks a context the past's store hands the servers (`Past`):")
	g.P("// the one thing the Audit layer lets a write of ", ev.GoName(), " or ", fact.GoName(), " through")
	g.P("// on (payday#151).")
	g.P("//")
	g.P("// Unexported, so that nothing outside this package can put one on a")
	g.P("// context: not an app's code, not a batch, not a call over the wire. And")
	g.P("// the store marks the contexts it hands the servers and no other -- not the")
	g.P("// one an operation goes on with, nor the one `PastIn` hands its work -- so")
	g.P("// a mark goes no further than the write it is on.")
	g.P("type pastWrite struct{}")
	g.P("")
	g.P("// pastWriting is `ctx` as the past's store hands it to the servers.")
	g.P("func pastWriting(ctx ", pkgCtx.Ident("Context"), ") ", pkgCtx.Ident("Context"), " {")
	g.P("	return ", pkgCtx.Ident("WithValue"), "(ctx, pastWrite{}, pastWrite{})")
	g.P("}")
	g.P("")
	g.P("// isPastWrite is whether `ctx` is one the past's store handed the servers.")
	g.P("func isPastWrite(ctx ", pkgCtx.Ident("Context"), ") bool {")
	g.P("	_, ok := ctx.Value(pastWrite{}).(pastWrite)")
	g.P("	return ok")
	g.P("}")
	g.P("")
	g.P("// errPast is PermissionDenied, to every caller alike. Not the trail's")
	g.P("// Unimplemented: these verbs are served, to the store's writes. What is")
	g.P("// refused is what wrote the request, which no credential changes -- so the")
	g.P("// sentence says what does write one.")
	g.P("func errPast() error {")
	g.P("	return ", pkgStatus.Ident("Error"), "(", pkgCodes.Ident("PermissionDenied"), ",")
	g.P("		\"a past is written by the past package: an event by past.Begin, and a fact by the op it answers\")")
	g.P("}")
	g.P("")
}
