package pdgen

import (
	"strconv"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
)

var (
	pkgAccess = protogen.GoImportPath("github.com/lesomnus/payday/access")
	pkgJson   = protogen.GoImportPath("encoding/json")
)

// EmitAccess writes what the access record needs of the app (payday#111): who
// a message's personal data is of, read off the `personal` declarations; what
// writes a row of it, at once or at a write's commit; and what reads one back
// out of the archive. What decides is the `access` package; what only
// generation knows is the app's messages and its tables.
func EmitAccess(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	acc := s.Own(pdpb.Own_OWN_ACCESS)
	if acc == nil || !s.Has(pdpb.Own_OWN_AUDIT) {
		return
	}

	emitSubjects(g, s, root)
	emitAccessSink(g, p, acc)
	emitAccessRecorder(g, s, p, root)

	g.P("// AccessOf is a reader of the archive's documents that hands over the")
	g.P("// access record's rows as the messages they are, and passes over the")
	g.P("// trail's: what `policy.Read` and `policy.ReadTenant` are handed for them.")
	g.P("// [TrailOf] is the other half, and passes over these.")
	g.P("func AccessOf(fn func(*", root.Ident(acc.GoName()), ") error) func(doc []byte) error {")
	g.P("	return func(doc []byte) error {")
	g.P("		if !accessDoc(doc) {")
	g.P("			return nil")
	g.P("		}")
	g.P("")
	g.P("		v := &", root.Ident(acc.GoName()), "{}")
	g.P("		if err := ", pkgProtojson.Ident("Unmarshal"), "(doc, v); err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("")
	g.P("		return fn(v)")
	g.P("	}")
	g.P("}")
	g.P("")
	g.P("// accessDoc answers whether an archived document is a row of the access")
	g.P("// record, which its domain says.")
	g.P("func accessDoc(doc []byte) bool {")
	g.P("	var h struct {")
	g.P("		Domain uint32 `json:\"domain\"`")
	g.P("	}")
	g.P("")
	g.P("	return ", pkgJson.Ident("Unmarshal"), "(doc, &h) == nil && h.Domain == uint32(", acc.GoName(), "Domain)")
	g.P("}")
	g.P("")
}

// emitSubjects writes `Subjects` and `SubjectRefs`, which the access record's
// interceptor is handed: whose personal data a message carries, and whose rows
// a request names.
//
// # From the declarations, and nothing else
//
// A message holds somebody's personal data when it holds a field declared
// `personal` (#92), and that message's key is whose. So the answer of any call
// is read the same way -- a Get, a list of a hundred, an edge two hops into a
// select -- without an app saying of each RPC what it shows. What the
// declarations cannot see, an app says (`access.Saw`).
func emitSubjects(g *protogen.GeneratedFile, s *Schema, root protogen.GoImportPath) {
	g.P("// personal is what a message holding a field declared `personal` is to the")
	g.P("// access record: its key, those fields, and how to read the tenant it is in.")
	g.P("type personal struct {")
	g.P("	key    ", pkgReflect.Ident("FieldDescriptor"))
	g.P("	fields []", pkgReflect.Ident("FieldDescriptor"))
	g.P("	tenant func(", pkgReflect.Ident("Message"), ") ", pkgPdid.Ident("Id"))
	g.P("}")
	g.P("")
	g.P("// carries answers whether `m` holds a value of any of the fields.")
	g.P("func (p personal) carries(m ", pkgReflect.Ident("Message"), ") bool {")
	g.P("	for _, fd := range p.fields {")
	g.P("		if m.Has(fd) {")
	g.P("			return true")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return false")
	g.P("}")
	g.P("")
	g.P("// personals is every message that holds a field declared `personal`, by its")
	g.P("// full name; refs is the reference to each that a request names a row by,")
	g.P("// and the field of it that is the row's key.")
	g.P("var (")
	g.P("	personals = map[", pkgReflect.Ident("FullName"), "]personal{}")
	g.P("	refs      = map[", pkgReflect.Ident("FullName"), "]", pkgReflect.Ident("FieldDescriptor"), "{}")
	g.P(")")
	g.P("")
	g.P("func init() {")
	for _, v := range s.Sorted() {
		if len(v.PersonalNumbers) == 0 || v.Records() {
			continue
		}

		g.P("	{")
		g.P("		md := (&", root.Ident(v.GoName()), "{}).ProtoReflect().Descriptor()")
		g.P("		fs := md.Fields()")
		g.P("		personals[md.FullName()] = personal{")
		g.P("			key: fs.ByNumber(", strconv.Itoa(int(v.Key().Descriptor().Number())), "),")
		g.P("			fields: []", pkgReflect.Ident("FieldDescriptor"), "{")
		for _, n := range v.PersonalNumbers {
			g.P("				fs.ByNumber(", strconv.Itoa(int(n)), "),")
		}
		g.P("			},")
		switch {
		case v.IsGlobal:
			// Behind no wall: the call's tenant is the subject's.
		case len(v.Columns) > 0:
			g.P("			tenant: func(m ", pkgReflect.Ident("Message"), ") ", pkgPdid.Ident("Id"), " {")
			g.P("				return accessIdOf(m.Get(fs.ByName(", strconv.Quote(v.Columns[0]), ")).Bytes())")
			g.P("			},")
		case v.Stamp != "":
			// What the path reaches, kept on the row: the tenant, as a column
			// holds it.
			g.P("			tenant: func(m ", pkgReflect.Ident("Message"), ") ", pkgPdid.Ident("Id"), " {")
			g.P("				return accessIdOf(m.Get(fs.ByName(", strconv.Quote(v.Stamp), ")).Bytes())")
			g.P("			},")
		case len(v.Via) == 1:
			g.P("			tenant: func(m ", pkgReflect.Ident("Message"), ") ", pkgPdid.Ident("Id"), " {")
			g.P("				return accessKeyOf(m.Get(fs.ByName(", strconv.Quote(v.Via[0]), ")).Message())")
			g.P("			},")
		}
		g.P("		}")
		if v.Rpcs().HasGet() {
			g.P("		r := (&", root.Ident(v.GoName()+"Ref"), "{}).ProtoReflect().Descriptor()")
			g.P("		refs[r.FullName()] = r.Fields().ByName(\"id\")")
		}
		g.P("	}")
	}
	g.P("}")
	g.P("")

	g.P("// Subjects is whose personal data `m` carries (payday#111): the key of every")
	g.P("// message in it that holds a field declared `personal`, and the tenant that")
	g.P("// message names -- what the access record's interceptor is handed as")
	g.P("// `access.Config.Subjects`.")
	g.P("func Subjects(m ", pkgProto.Ident("Message"), ") []", pkgAccess.Ident("Subject"), " {")
	g.P("	out := []", pkgAccess.Ident("Subject"), "{}")
	g.P("	if m != nil {")
	g.P("		subjectsIn(m.ProtoReflect(), true, &out)")
	g.P("	}")
	g.P("")
	g.P("	return out")
	g.P("}")
	g.P("")
	g.P("// SubjectRefs is whose rows the request `m` names by key: what a call that")
	g.P("// was refused carried, having no answer -- `access.Config.Refs`.")
	g.P("func SubjectRefs(m ", pkgProto.Ident("Message"), ") []", pkgAccess.Ident("Subject"), " {")
	g.P("	out := []", pkgAccess.Ident("Subject"), "{}")
	g.P("	if m != nil {")
	g.P("		refsIn(m.ProtoReflect(), true, &out)")
	g.P("	}")
	g.P("")
	g.P("	return out")
	g.P("}")
	g.P("")
	g.P("// envelopes are the fields payday packs a message into an `Any` in: a")
	g.P("// batch's operations and answers, and a watch's events. What one holds is")
	g.P("// read unpacked, and nothing inside it is unpacked again. An `Any` anywhere")
	g.P("// else is the app's own data, which no declaration describes: it is passed")
	g.P("// over, and a tenant it names is never taken for whose a row is.")
	g.P("var envelopes = map[", pkgReflect.Ident("FullName"), "]bool{")
	for _, v := range []struct {
		msg   string
		field string
	}{
		{"Op", "request"},
		{"BatchResponse", "results"},
		{"WatchEvent", "value"},
		{"WatchEvent", "previous"},
	} {
		g.P("	(&", pkgPdpb.Ident(v.msg), "{}).ProtoReflect().Descriptor().Fields().ByName(", strconv.Quote(v.field), ").FullName(): true,")
	}
	g.P("}")
	g.P("")
	g.P("// unpacked is what the `Any` the field `fd` holds carries, when `fd` is an")
	g.P("// envelope and unpacking is still allowed; nil otherwise.")
	g.P("func unpacked(fd ", pkgReflect.Ident("FieldDescriptor"), ", m ", pkgReflect.Ident("Message"), ", unpack bool) ", pkgReflect.Ident("Message"), " {")
	g.P("	if !unpack || !envelopes[fd.FullName()] {")
	g.P("		return nil")
	g.P("	}")
	g.P("	v, ok := m.Interface().(*", pkgAnypb.Ident("Any"), ")")
	g.P("	if !ok {")
	g.P("		return nil")
	g.P("	}")
	g.P("	u, err := v.UnmarshalNew()")
	g.P("	if err != nil {")
	g.P("		return nil")
	g.P("	}")
	g.P("")
	g.P("	return u.ProtoReflect()")
	g.P("}")
	g.P("")
	g.P("// isAny answers whether `m` is an `Any`, which is read only as an envelope.")
	g.P("func isAny(m ", pkgReflect.Ident("Message"), ") bool {")
	g.P("	_, ok := m.Interface().(*", pkgAnypb.Ident("Any"), ")")
	g.P("	return ok")
	g.P("}")
	g.P("")
	g.P("func subjectsIn(m ", pkgReflect.Ident("Message"), ", unpack bool, out *[]", pkgAccess.Ident("Subject"), ") {")
	g.P("	if !m.IsValid() || isAny(m) {")
	g.P("		return")
	g.P("	}")
	g.P("	if p, ok := personals[m.Descriptor().FullName()]; ok && p.carries(m) {")
	g.P("		if id := accessIdOf(m.Get(p.key).Bytes()); !id.IsZero() {")
	g.P("			v := ", pkgAccess.Ident("Subject"), "{Id: id}")
	g.P("			if p.tenant != nil {")
	g.P("				v.Tenant = p.tenant(m)")
	g.P("			}")
	g.P("			*out = append(*out, v)")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	m.Range(func(fd ", pkgReflect.Ident("FieldDescriptor"), ", v ", pkgReflect.Ident("Value"), ") bool {")
	g.P("		eachMessage(fd, v, func(m ", pkgReflect.Ident("Message"), ") {")
	g.P("			if u := unpacked(fd, m, unpack); u != nil {")
	g.P("				subjectsIn(u, false, out)")
	g.P("				return")
	g.P("			}")
	g.P("			subjectsIn(m, unpack, out)")
	g.P("		})")
	g.P("		return true")
	g.P("	})")
	g.P("}")
	g.P("")
	g.P("func refsIn(m ", pkgReflect.Ident("Message"), ", unpack bool, out *[]", pkgAccess.Ident("Subject"), ") {")
	g.P("	if !m.IsValid() || isAny(m) {")
	g.P("		return")
	g.P("	}")
	g.P("	if fd, ok := refs[m.Descriptor().FullName()]; ok && fd != nil && m.Has(fd) {")
	g.P("		if id := accessIdOf(m.Get(fd).Bytes()); !id.IsZero() {")
	g.P("			*out = append(*out, ", pkgAccess.Ident("Subject"), "{Id: id})")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	m.Range(func(fd ", pkgReflect.Ident("FieldDescriptor"), ", v ", pkgReflect.Ident("Value"), ") bool {")
	g.P("		eachMessage(fd, v, func(m ", pkgReflect.Ident("Message"), ") {")
	g.P("			if u := unpacked(fd, m, unpack); u != nil {")
	g.P("				refsIn(u, false, out)")
	g.P("				return")
	g.P("			}")
	g.P("			refsIn(m, unpack, out)")
	g.P("		})")
	g.P("		return true")
	g.P("	})")
	g.P("}")
	g.P("")
	g.P("// eachMessage calls `fn` for every message the field `fd` holds in `v`.")
	g.P("func eachMessage(fd ", pkgReflect.Ident("FieldDescriptor"), ", v ", pkgReflect.Ident("Value"), ", fn func(", pkgReflect.Ident("Message"), ")) {")
	g.P("	switch {")
	g.P("	case fd.IsList():")
	g.P("		if fd.Message() == nil {")
	g.P("			return")
	g.P("		}")
	g.P("		l := v.List()")
	g.P("		for i := range l.Len() {")
	g.P("			fn(l.Get(i).Message())")
	g.P("		}")
	g.P("	case fd.IsMap():")
	g.P("		if fd.MapValue().Message() == nil {")
	g.P("			return")
	g.P("		}")
	g.P("		v.Map().Range(func(_ ", pkgReflect.Ident("MapKey"), ", v ", pkgReflect.Ident("Value"), ") bool {")
	g.P("			fn(v.Message())")
	g.P("			return true")
	g.P("		})")
	g.P("	case fd.Message() != nil:")
	g.P("		fn(v.Message())")
	g.P("	}")
	g.P("}")
	g.P("")
	g.P("// accessIdOf is an identifier as a row holds one, and zero for bytes that")
	g.P("// are none.")
	g.P("func accessIdOf(b []byte) ", pkgPdid.Ident("Id"), " {")
	g.P("	v, err := ", pkgPdid.Ident("From"), "(b)")
	g.P("	if err != nil {")
	g.P("		return ", pkgPdid.Ident("Nil"))
	g.P("	}")
	g.P("")
	g.P("	return v")
	g.P("}")
	g.P("")
	g.P("// accessKeyOf is the key of the row a message names by its first field, as")
	g.P("// an edge holds the row it points at, and zero for none.")
	g.P("func accessKeyOf(m ", pkgReflect.Ident("Message"), ") ", pkgPdid.Ident("Id"), " {")
	g.P("	if !m.IsValid() {")
	g.P("		return ", pkgPdid.Ident("Nil"))
	g.P("	}")
	g.P("	fd := m.Descriptor().Fields().ByNumber(1)")
	g.P("	if fd == nil || fd.Kind() != ", pkgReflect.Ident("BytesKind"), " {")
	g.P("		return ", pkgPdid.Ident("Nil"))
	g.P("	}")
	g.P("")
	g.P("	return accessIdOf(m.Get(fd).Bytes())")
	g.P("}")
	g.P("")
}

// emitAccessSink writes what writes the access record's rows: `AccessSink`,
// which an app hands its `access.Writer`, and what the recorder writes at a
// commit with.
func emitAccessSink(g *protogen.GeneratedFile, p Paths, acc *Entity) {
	e := acc.GoName()

	g.P("// AccessSink writes rows of the access record (payday#111): what an app")
	g.P("// hands its `access.Writer` as `Sink`.")
	g.P("//")
	g.P("//	w := &access.Writer{Sink: pd.AccessSink(db)}")
	g.P("func AccessSink(db *", p.Ent.Ident("Client"), ") func(", pkgCtx.Ident("Context"), ", []", pkgAccess.Ident("Record"), ") error {")
	g.P("	return func(ctx ", pkgCtx.Ident("Context"), ", rs []", pkgAccess.Ident("Record"), ") error {")
	g.P("		return writeAccess(ctx, db, rs)")
	g.P("	}")
	g.P("}")
	g.P("")
	g.P("// writeAccess writes rows of the access record through `db`, a statement for")
	g.P("// every few hundred.")
	g.P("func writeAccess(ctx ", pkgCtx.Ident("Context"), ", db *", p.Ent.Ident("Client"), ", rs []", pkgAccess.Ident("Record"), ") error {")
	g.P("	qs := make([]*", p.Ent.Ident(e+"Create"), ", 0, len(rs))")
	g.P("	for _, r := range rs {")
	g.P("		id := r.Id")
	g.P("		if id.IsZero() {")
	g.P("			id = ", pkgPdid.Ident("New"), "(", e, "Domain)")
	g.P("		}")
	g.P("")
	g.P("		// Never nil, for the reason on [notNull].")
	g.P("		value := []byte{}")
	g.P("		if r.Detail != nil {")
	g.P("			b, err := ", pkgProto.Ident("Marshal"), "(r.Detail)")
	g.P("			if err != nil {")
	g.P("				return err")
	g.P("			}")
	g.P("			value = notNull(b)")
	g.P("		}")
	g.P("		subjects := make([][]byte, len(r.Subjects))")
	g.P("		for i, v := range r.Subjects {")
	g.P("			subjects[i] = v.Bytes()")
	g.P("		}")
	g.P("")
	g.P("		qs = append(qs, db.", e, ".Create().")
	g.P("			SetId(id.Uuid()).")
	g.P("			SetTenantId(r.Tenant.Uuid()).")
	g.P("			SetActorId(r.Actor.Uuid()).")
	g.P("			SetActorTenantId(r.ActorTenant.Uuid()).")
	g.P("			SetTraceId(notNull(r.Trace)).")
	g.P("			SetAction(r.Action).")
	g.P("			SetKind(string(r.Kind)).")
	g.P("			SetResult(r.Result).")
	g.P("			SetDateCreated(r.At).")
	g.P("			SetValue(value).")
	g.P("			SetDomain(uint32(", e, "Domain)).")
	g.P("			SetSubjects(subjects).")
	g.P("			SetSubjectCount(uint32(r.Count)))")
	g.P("	}")
	g.P("")
	g.P("	for vs := range ", pkgSlices.Ident("Chunk"), "(qs, 500) {")
	g.P("		if err := db.", e, ".CreateBulk(vs...).Exec(ctx); err != nil {")
	g.P("			return ", pkgFmt.Ident("Errorf"), "(\"write the access record: %w\", err)")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")
}

// emitAccessRecorder writes `AccessRecorder`, which puts a write's row of the
// access record in the write's own transaction, at its commit -- as the trail
// is put there -- so that what holds the write holds its record.
//
// # Whose data a write wrote
//
// The people whose data the row held before the write or holds after it: a
// write to a row that holds somebody's data is a write of it, whichever field
// it changed, and an erase of one is too. And the row itself, by its key, when
// neither holds any but the write cleared a field declared `personal`. The rows
// are the images `bare` captures for a recorder that asks, which this one does
// of the kinds that hold anybody's data and of no other.
func emitAccessRecorder(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	g.P("// AccessRecorder is the recorder that puts a write's row of the access")
	g.P("// record in the write's own transaction (payday#111): one of the recorders")
	g.P("// an app hands its servers, beside `Recorder`.")
	g.P("//")
	g.P("//	rec := bare.Recorders{pd.Recorder(content), pd.AccessRecorder()}")
	g.P("//")
	g.P("// It is told of every write of the call the interceptor put in the context,")
	g.P("// and writes a row at each commit of the data the call's writes wrote that")
	g.P("// no row says yet. Where the transaction keeps nothing, it writes the row")
	g.P("// as the write is made, in the write's transaction all the same: what holds")
	g.P("// the write holds its record, and a write whose row cannot be written is")
	g.P("// refused with it. Either way the call says the row once it has answered")
	g.P("// (`access.Call.Settle`); what the call showed is the interceptor's to")
	g.P("// record.")
	g.P("func AccessRecorder() ", p.Bare.Ident("Recorder"), " { return accessRecorder{} }")
	g.P("")
	g.P("type accessRecorder struct{}")
	g.P("")
	g.P("// accessKept is what a transaction keeps for a call that writes in it: that")
	g.P("// the call's row is written at its commit.")
	g.P("type accessKept struct{ call *", pkgAccess.Ident("Call"), " }")
	g.P("")
	g.P("// Snapshots asks for the rows a write of a kind that holds anybody's personal")
	g.P("// data left and erased, which are what say whose it was.")
	g.P("func (accessRecorder) Snapshots(method string) ", p.Bare.Ident("SnapshotOptions"), " {")
	g.P("	switch method {")
	left, erased := []any{}, []any{}
	for _, v := range s.Sorted() {
		if len(v.PersonalNumbers) == 0 || v.Records() {
			continue
		}
		for _, op := range []struct {
			name    string
			enabled bool
		}{
			{"Add", v.Rpcs().HasAdd()},
			{"AddMany", v.Rpcs().HasAddMany()},
			{"Patch", v.Rpcs().HasPatch()},
			{"Apply", v.Rpcs().HasPatch()},
		} {
			if op.enabled {
				left = append(left, root.Ident(v.GoName()+"Service_"+op.name+"_FullMethodName"))
			}
		}
		if v.Rpcs().HasErase() {
			erased = append(erased, root.Ident(v.GoName()+"Service_Erase_FullMethodName"))
		}
	}
	for _, c := range []struct {
		methods []any
		opts    string
	}{{left, "{Value: true}"}, {erased, "{Previous: true}"}} {
		if len(c.methods) == 0 {
			continue
		}
		g.P(append(append([]any{"	case "}, joined(c.methods)...), ":")...)
		g.P("		return ", p.Bare.Ident("SnapshotOptions"), c.opts)
	}
	g.P("	}")
	g.P("")
	g.P("	return ", p.Bare.Ident("SnapshotOptions"), "{}")
	g.P("}")
	g.P("")
	g.P("func (accessRecorder) Record(ctx ", pkgCtx.Ident("Context"), ", s ", p.Bare.Ident("Server"), ", c ", p.Bare.Ident("Change"), ") error {")
	g.P("	call, ok := ", pkgAccess.Ident("Of"), "(ctx)")
	g.P("	if !ok {")
	g.P("		return nil")
	g.P("	}")
	g.P("")
	g.P("	key, err := ", pkgAudit.Ident("Identifier"), "(c.Key)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("")
	g.P("	written := ", pkgSlices.Ident("Concat"), "(Subjects(c.Previous), Subjects(c.Value))")
	g.P("	if md, _, personal := described(key); len(written) == 0 && touches(c.Patch, md, personal) {")
	g.P("		written = []", pkgAccess.Ident("Subject"), "{{Id: key}}")
	g.P("	}")
	g.P("	call.Wrote(written...)")
	g.P("")
	g.P("	db := s.Store.Db")
	g.P("	k, ok := ", pkgDialect.Ident("KeepOf"), "(db.Driver())")
	g.P("	if !ok {")
	g.P("		// Nothing waits for a commit here: written now, in the write's")
	g.P("		// transaction, which takes the row along if it goes.")
	g.P("		now := call.Unkept(written...)")
	g.P("		if len(now) == 0 {")
	g.P("			return nil")
	g.P("		}")
	g.P("")
	g.P("		d := ", pkgPdpb.Ident("AccessDetail_builder"), "{Address: call.Address()}.Build()")
	g.P("		if err := writeAccess(ctx, db, ", pkgAccess.Ident("Records"), "(ctx, call.Method(), ", pkgAccess.Ident("Write"), ", \"ok\", now, d, ", pkgTime.Ident("Now"), "())); err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("")
	g.P("		call.Kept(now...)")
	g.P("		return nil")
	g.P("	}")
	g.P("")
	g.P("	k.Value(accessKept{call}, func() any {")
	g.P("		k.BeforeCommit(func(at ", pkgCtx.Ident("Context"), ") error {")
	g.P("			wrote, _ := call.Unsaid()")
	g.P("			if len(wrote) == 0 {")
	g.P("				return nil")
	g.P("			}")
	g.P("")
	g.P("			d := ", pkgPdpb.Ident("AccessDetail_builder"), "{Address: call.Address()}.Build()")
	g.P("			rs := ", pkgAccess.Ident("Records"), "(ctx, call.Method(), ", pkgAccess.Ident("Write"), ", \"ok\", wrote, d, ", pkgTime.Ident("Now"), "())")
	g.P("			if err := writeAccess(at, db, rs); err != nil {")
	g.P("				return err")
	g.P("			}")
	g.P("")
	g.P("			// Kept, not said: the commit these rows are written ahead of")
	g.P("			// may still be refused, and takes them along if it is.")
	g.P("			call.Kept(wrote...)")
	g.P("			return nil")
	g.P("		})")
	g.P("		return struct{}{}")
	g.P("	})")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")
	g.P("// touches answers whether the patch `p` set or cleared any of `fields`.")
	g.P("func touches(p *", pkgPatchpb.Ident("Patch"), ", md ", pkgReflect.Ident("MessageDescriptor"), ", fields []", pkgReflect.Ident("FieldDescriptor"), ") bool {")
	g.P("	if p == nil || md == nil {")
	g.P("		return false")
	g.P("	}")
	g.P("	for _, n := range ", pkgAudit.Ident("FieldsOf"), "(p, md) {")
	g.P("		for _, fd := range fields {")
	g.P("			if uint32(fd.Number()) == n {")
	g.P("				return true")
	g.P("			}")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	return false")
	g.P("}")
	g.P("")
}

// joined is `vs` as the cases of one `case`, a comma between each.
func joined(vs []any) []any {
	out := make([]any, 0, 2*len(vs))
	for i, v := range vs {
		if i > 0 {
			out = append(out, ",\n\t\t")
		}
		out = append(out, v)
	}

	return out
}

// emitAccessRows writes the access record's half of the trail's store: its
// rows read beside the trail's own, so that the passes that keep the trail
// keep them, as a kind of their own.
//
// # One order for both
//
// A pass reads a store a batch at a time, oldest first, and carries on from
// the last row it read. Both tables order by when a row was written and then
// by its identifier, which is a UUID in both, so a batch of either is merged
// with the other's by the same order and the pass carries on from the last of
// either. Which table a row is of is in the type of its key.
func emitAccessRows(g *protogen.GeneratedFile, p Paths, acc *Entity, accPkg protogen.GoImportPath) {
	e := acc.GoName()

	g.P("// accessKey is the key of a row of the access record, as this store gives")
	g.P("// it out: a type of its own, so that what deletes by it deletes from the")
	g.P("// table it is of.")
	g.P("type accessKey ", pkgUuid.Ident("UUID"))
	g.P("")
	g.P("// accessOf narrows the access record to a scope, and is nil for a scope that")
	g.P("// is not about its kind, or that is about objects: an access row is about")
	g.P("// none.")
	g.P("func (s trailStore) accessOf(k ", pkgTrail.Ident("Scope"), ") *", p.Ent.Ident(e+"Query"), " {")
	g.P("	if !k.Has(", e, "Domain) || len(k.Objects) > 0 {")
	g.P("		return nil")
	g.P("	}")
	g.P("")
	g.P("	q := s.db.", e, ".Query()")
	g.P("	t := k.Tenant.Uuid()")
	g.P("	switch k.Whose {")
	g.P("	case ", pkgTrail.Ident("FiledUnder"), ":")
	g.P("		q = q.Where(", accPkg.Ident("TenantIdEQ"), "(t))")
	g.P("	case ", pkgTrail.Ident("Alone"), ":")
	g.P("		q = q.Where(accessAlone(t))")
	g.P("	case ", pkgTrail.Ident("Together"), ":")
	g.P("		q = q.Where(", accPkg.Ident("Or"), "(", accPkg.Ident("TenantIdEQ"), "(t), ", accPkg.Ident("ActorTenantIdEQ"), "(t)), ", accPkg.Ident("Not"), "(accessAlone(t)))")
	g.P("	case ", pkgTrail.Ident("Sharing"), ":")
	g.P("		q = q.Where(", accPkg.Ident("TenantIdEQ"), "(t), ", accPkg.Ident("Not"), "(accessAlone(t)))")
	g.P("	}")
	g.P("")
	g.P("	return q")
	g.P("}")
	g.P("")
	g.P("// accessAlone is an access row filed under `t` that names no other tenant:")
	g.P("// the caller is of `t`, or is nobody.")
	g.P("func accessAlone(t ", pkgUuid.Ident("UUID"), ") ", (p.Ent + "/predicate").Ident(e), " {")
	g.P("	return ", accPkg.Ident("And"), "(", accPkg.Ident("TenantIdEQ"), "(t), ", accPkg.Ident("ActorTenantIdIn"), "(t, ", pkgUuid.Ident("Nil"), "()))")
	g.P("}")
	g.P("")
	g.P("// accessRows is the access record's rows of a scope written before `at`,")
	g.P("// past `after`, oldest first: what Older and Heads merge with the trail's.")
	g.P("// With `docs`, each as the document the archive keeps.")
	g.P("func (s trailStore) accessRows(ctx ", pkgCtx.Ident("Context"), ", k ", pkgTrail.Ident("Scope"), ", at ", pkgTime.Ident("Time"),
		", after ", pkgTrail.Ident("Cursor"), ", limit int, docs bool) (", pkgTrail.Ident("Rows"), ", error) {")
	g.P("	q := s.accessOf(k)")
	g.P("	if q == nil {")
	g.P("		return nil, nil")
	g.P("	}")
	g.P("")
	g.P("	q = q.Where(", accPkg.Ident("DateCreatedLT"), "(at))")
	g.P("	if after.Key != nil {")
	g.P("		id, err := cursorOf(after.Key)")
	g.P("		if err != nil {")
	g.P("			return nil, err")
	g.P("		}")
	g.P("")
	emitAfter(g, "		", accPkg, "after.Created", "id")
	g.P("	}")
	g.P("	q = q.Order(", p.Ent.Ident("Asc"), "(", accPkg.Ident("FieldDateCreated"), ", ", accPkg.Ident("FieldId"), ")).Limit(limit)")
	g.P("	if !docs {")
	g.P("		q = q.Select(", accPkg.Ident("FieldId"), ", ", accPkg.Ident("FieldDateCreated"), ", ", accPkg.Ident("FieldTenantId"), ", ", accPkg.Ident("FieldActorTenantId"), ").", e, "Query")
	g.P("	}")
	g.P("")
	g.P("	vs, err := q.All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	out := make(", pkgTrail.Ident("Rows"), ", 0, len(vs))")
	g.P("	for _, v := range vs {")
	g.P("		r := ", pkgTrail.Ident("Row"), "{")
	g.P("			Key:     accessKey(v.Id),")
	g.P("			Domain:  ", e, "Domain,")
	g.P("			Created: v.DateCreated,")
	g.P("			Tenants: ", pkgTrail.Ident("TenantsOf"), "(v.TenantId, v.ActorTenantId, nil),")
	g.P("		}")
	g.P("		if docs {")
	g.P("			b, err := ", pkgProtojson.Ident("Marshal"), "(v.Proto())")
	g.P("			if err != nil {")
	g.P("				return nil, err")
	g.P("			}")
	g.P("			r.Doc = b")
	g.P("		}")
	g.P("")
	g.P("		out = append(out, r)")
	g.P("	}")
	g.P("")
	g.P("	return out, nil")
	g.P("}")
	g.P("")
	g.P("// mergeRows is two tables' oldest rows as one batch, in the order each was")
	g.P("// read in, and at most `limit` of them.")
	g.P("func mergeRows(a, b ", pkgTrail.Ident("Rows"), ", limit int) ", pkgTrail.Ident("Rows"), " {")
	g.P("	out := make(", pkgTrail.Ident("Rows"), ", 0, min(len(a)+len(b), limit))")
	g.P("	for len(out) < limit && (len(a) > 0 || len(b) > 0) {")
	g.P("		if len(b) == 0 || (len(a) > 0 && rowBefore(a[0], b[0])) {")
	g.P("			out = append(out, a[0])")
	g.P("			a = a[1:]")
	g.P("			continue")
	g.P("		}")
	g.P("")
	g.P("		out = append(out, b[0])")
	g.P("		b = b[1:]")
	g.P("	}")
	g.P("")
	g.P("	return out")
	g.P("}")
	g.P("")
	g.P("// rowBefore answers whether `a` was written before `b`, by the order a pass")
	g.P("// reads in: when, then the identifier.")
	g.P("func rowBefore(a, b ", pkgTrail.Ident("Row"), ") bool {")
	g.P("	if c := a.Created.Compare(b.Created); c != 0 {")
	g.P("		return c < 0")
	g.P("	}")
	g.P("")
	g.P("	x, _ := cursorOf(a.Key)")
	g.P("	y, _ := cursorOf(b.Key)")
	g.P("	return x.Compare(y) < 0")
	g.P("}")
	g.P("")
}
