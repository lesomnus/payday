package pdgen

import (
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
)

const (
	pkgSpin = protogen.GoImportPath("github.com/lesomnus/payday/spin")
	pkgLog  = protogen.GoImportPath("github.com/lesomnus/otx/log")
	pkgSlog = protogen.GoImportPath("log/slog")
)

// EmitOutbox writes the two halves that close the gap between a commit and a
// publish: the recorder that writes a row inside the transaction, and the loop
// that takes those rows and publishes them.
//
// It is generated rather than written in the runtime for the reason every other
// layer is: `ent.Client` and the predicates are the app's types and payday
// cannot name them. What is not generated is any judgement -- there is none
// here beyond "in order, then delete".
func EmitOutbox(g *protogen.GeneratedFile, s *Schema, p Paths) {
	// Refused by [CheckOwn] before this, for a real app; see there.
	if !s.Has(pdpb.Own_OWN_OUTBOX) {
		return
	}

	emitOutboxRecorder(g, p)
	emitDrain(g, p)
}

func emitOutboxRecorder(g *protogen.GeneratedFile, p Paths) {
	g.P("// OutboxRecorder answers with the recorder that writes each write to the")
	g.P("// queue, inside the transaction that makes it.")
	g.P("//")
	g.P("// It is what makes an event survive a process that stops between the commit")
	g.P("// and the publish. `WatchRecorder` remembers in memory and an interceptor")
	g.P("// publishes once the handler has answered; this writes a row, and [Drain]")
	g.P("// publishes it whenever somebody next gets round to it -- which may be after")
	g.P("// a restart, and that is the whole of the point.")
	g.P("//")
	g.P("// Both, for a deployment that wants both: the first is immediate and lossy,")
	g.P("// the second is durable and late. A subscriber sees the same row twice and")
	g.P("// the second time says what the first said, since what is sent is state.")
	g.P("//")
	g.P("//	sink, err := pd.NewSink(db, bare.WithRecorder(pd.OutboxRecorder()))")
	g.P("func OutboxRecorder() ", p.Bare.Ident("Recorder"), " { return outboxRecorder{} }")
	g.P("")
	g.P("type outboxRecorder struct{}")
	g.P("")
	g.P("var _ ", p.Bare.Ident("Recorder"), " = outboxRecorder{}")
	g.P("")

	g.P("// Record writes the row.")
	g.P("//")
	g.P("// Unlike the watch recorder it **refuses** when it cannot, which is the")
	g.P("// difference between the two in one line: this exists so that an event is")
	g.P("// not lost, and letting a write commit whose event could not be queued would")
	g.P("// lose exactly the one it was written to keep.")
	g.P("//")
	g.P("// It writes through `s.Db`, which is the client this transaction is running")
	g.P("// on, and not through a server: there are no RPCs on this entity, on")
	g.P("// purpose -- see the note in payday's outbox.proto.")
	g.P("func (outboxRecorder) Record(ctx ", pkgCtx.Ident("Context"), ", s ", p.Bare.Ident("Server"),
		", c ", p.Bare.Ident("Change"), ") error {")
	g.P("	k, err := ", pkgPdid.Ident("From"), "(keyBytes(c.Key))")
	g.P("	if err != nil {")
	g.P("		// A key this app does not make, so there is nothing an identifier")
	g.P("		// could say about it and nobody could act on the event.")
	g.P("		return nil")
	g.P("	}")
	g.P("")
	// Empty rather than nil, and that is not a style choice. This column is NOT
	// NULL, and a nil `[]byte` is SQL NULL to pgx while the SQLite driver makes
	// it an empty blob -- so a nil here is an insert that works in every test
	// and fails on the database the app is deployed on.
	// Through `hidden`, for the reason the watch recorder gives at length: the
	// redactor F15 added is about the **document**, and this queue holds the
	// same document. A row here is at rest in a table until something drains
	// it, and what it is drained into is whatever broker a deployment names.
	g.P("	doc := []byte{}")
	g.P("	if v := hidden(k, c.Patch); v != nil {")
	g.P("		b, err := ", pkgProto.Ident("Marshal"), "(v)")
	g.P("		if err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("")
	g.P("		doc = b")
	g.P("	}")
	g.P("")

	// The actor and the tenant, which are the frame's and are zero for a write
	// nobody asked for.
	g.P("	var actor, tenant ", pkgPdid.Ident("Id"))
	g.P("	if f, ok := ", pkgFrame.Ident("From"), "(ctx); ok {")
	g.P("		actor = f.Actor")
	g.P("		tenant = f.Tenant")
	g.P("	}")
	g.P("")
	g.P("	return s.Db.Outbox.Create().")
	g.P("		SetID(", pkgPdid.Ident("New"), "(OutboxDomain).Uuid()).")
	g.P("		SetTenantID(tenant.Uuid()).")
	g.P("		SetActorID(actor.Uuid()).")
	g.P("		SetMethod(c.Method).")
	g.P("		SetBy(c.By).")
	g.P("		SetObjectID(k.Uuid()).")
	g.P("		SetPatch(doc).")
	g.P("		Exec(ctx)")
	g.P("}")
	g.P("")
}

func emitDrain(g *protogen.GeneratedFile, p Paths) {
	outbox := p.Ent + "/outbox"

	g.P("// DrainSize is how many rows one pass takes.")
	g.P("//")
	g.P("// Bounded because a queue that has been building since a broker went down is")
	g.P("// exactly when a pass must not try to hold all of it, and small enough that")
	g.P("// a pass which fails republishes little.")
	g.P("const DrainSize = 256")
	g.P("")

	g.P("// Drain answers with the loop that publishes what the queue holds and takes")
	g.P("// it away.")
	g.P("//")
	g.P("// It is a [spin.Spinner], so it is handed to `spin.Run` with the stack:")
	g.P("//")
	g.P("//	go spin.Run(ctx, slices.Values([]any{pd.Drain(db, broker, time.Second)}))")
	g.P("//")
	g.P("// # It publishes before it deletes")
	g.P("//")
	g.P("// Which is the order that gives at-least-once, and the other order gives")
	g.P("// at-most-once. A process that stops between the two publishes those rows")
	g.P("// again on the next pass, and a subscriber reads the same state twice -- the")
	g.P("// second saying what the first said. That is only harmless because what")
	g.P("// travels is state and not a delta, and it is why an outbox is a table and a")
	g.P("// loop here rather than a project.")
	g.P("//")
	g.P("// # Two replicas publish twice")
	g.P("//")
	g.P("// Nothing here takes a lock, so two of these drain the same rows and publish")
	g.P("// them each. By the paragraph above that is not a correctness problem, and")
	g.P("// it is real work: a deployment that minds runs this on one replica, which")
	g.P("// is a line of wiring rather than a lease to get wrong. Said out loud here")
	g.P("// because the alternative is somebody discovering it from a graph.")
	g.P("func Drain(db *", p.Ent.Ident("Client"), ", b ", pkgWatch.Ident("Broker"),
		", every ", pkgTime.Ident("Duration"), ") ", pkgSpin.Ident("Spinner"), " {")
	g.P("	return drain{db, b, every}")
	g.P("}")
	g.P("")
	g.P("type drain struct {")
	g.P("	db    *", p.Ent.Ident("Client"))
	g.P("	b     ", pkgWatch.Ident("Broker"))
	g.P("	every ", pkgTime.Ident("Duration"))
	g.P("}")
	g.P("")
	g.P("var _ ", pkgSpin.Ident("Spinner"), " = drain{}")
	g.P("var _ ", pkgSpin.Ident("Named"), " = drain{}")
	g.P("")
	g.P("func (drain) SpinName() string { return \"outbox\" }")
	g.P("")
	g.P("func (d drain) Spin(ctx ", pkgCtx.Ident("Context"), ") error {")
	g.P("	return ", pkgSpin.Ident("Every"), "(d.every, d.pass)(ctx)")
	g.P("}")
	g.P("")

	g.P("// pass takes one batch, and keeps taking them for as long as it finds a full")
	g.P("// one -- otherwise a queue of ten thousand drains at one batch per tick and")
	g.P("// takes an hour to catch up from a minute of trouble.")
	g.P("//")
	g.P("// It answers nil for a failure rather than ending the loop, and logs it. A")
	g.P("// broker that is down or a database that blinked is a thing to try again;")
	g.P("// giving up would take the process down and stop serving requests that were")
	g.P("// fine. See the note on failure in `payday/spin` -- the loud direction is the")
	g.P("// default there, and this is one of the cases that means the other.")
	g.P("func (d drain) pass(ctx ", pkgCtx.Ident("Context"), ") error {")
	g.P("	for {")
	g.P("		n, err := d.once(ctx)")
	g.P("		if err != nil {")
	g.P("			", pkgLog.Ident("From"), "(ctx).ErrorContext(ctx, \"outbox\", ",
		pkgSlog.Ident("String"), "(\"error\", err.Error()))")
	g.P("			return nil")
	g.P("		}")
	g.P("		if n < DrainSize {")
	g.P("			return nil")
	g.P("		}")
	g.P("		if ctx.Err() != nil {")
	g.P("			return nil")
	g.P("		}")
	g.P("	}")
	g.P("}")
	g.P("")

	g.P("// once publishes one batch and answers with how many rows were in it.")
	g.P("func (d drain) once(ctx ", pkgCtx.Ident("Context"), ") (int, error) {")
	g.P("	// Oldest first, which is the order they were written in: an identifier")
	g.P("	// carries the millisecond it was minted and a sequence within it, so the")
	g.P("	// key **is** the order and there is no column to keep in step.")
	g.P("	vs, err := d.db.Outbox.Query().")
	g.P("		Order(", protogen.GoImportPath(outbox).Ident("ByID"), "()).")
	g.P("		Limit(DrainSize).")
	g.P("		All(ctx)")
	g.P("	if err != nil {")
	g.P("		return 0, err")
	g.P("	}")
	g.P("	if len(vs) == 0 {")
	g.P("		return 0, nil")
	g.P("	}")
	g.P("")
	g.P("	ks := make([]", pkgUuid2.Ident("UUID"), ", len(vs))")
	g.P("	for i, v := range vs {")
	g.P("		ks[i] = v.ID")
	g.P("		d.b.Publish(ctx, ", pkgWatch.Ident("Event"), "{")
	g.P("			Actor:  ", pkgPdid.Ident("Id"), "(v.ActorID),")
	g.P("			Tenant: ", pkgPdid.Ident("Id"), "(v.TenantID),")
	g.P("			Method: v.Method,")
	g.P("			Changes: []", pkgWatch.Ident("Change"), "{{")
	g.P("				Method: v.Method,")
	g.P("				By:     v.By,")
	g.P("				Key:    ", pkgPdid.Ident("Id"), "(v.ObjectID),")
	g.P("				Patch:  v.Patch,")
	g.P("			}},")
	g.P("		})")
	g.P("	}")
	g.P("")
	g.P("	// And only then. A row deleted before it was published is an event that")
	g.P("	// nothing will ever say again.")
	g.P("	if _, err := d.db.Outbox.Delete().")
	g.P("		Where(", protogen.GoImportPath(outbox).Ident("IDIn"), "(ks...)).")
	g.P("		Exec(ctx); err != nil {")
	g.P("		return 0, err")
	g.P("	}")
	g.P("")
	g.P("	return len(vs), nil")
	g.P("}")
	g.P("")
}
