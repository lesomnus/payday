package pdgen

import (
	"google.golang.org/protobuf/compiler/protogen"
)

const (
	pkgWatch  = protogen.GoImportPath("github.com/lesomnus/payday/watch")
	pkgGrpc   = protogen.GoImportPath("google.golang.org/grpc")
	pkgSlices = protogen.GoImportPath("slices")
)

// emitWatch writes the stream that answers with what a caller may see, now and
// as it changes.
//
// Three things happen in an order that matters, and the order is the reason
// this is generated rather than copied. Subscribing comes **before** the first
// read, or a change that lands between them is lost with nothing to say it was.
// Then the snapshot, so a client does not have to List and then subscribe and
// race the two. Then, per event, the row is read back and sent as it is now.
//
// The read is what keeps the wall out of this: a row is fetched through the
// same server every other read goes through, with the caller's own context, so
// one they may not see comes back NotFound and is never sent. The filters are
// the caller's own and are tested here, which is a different kind of thing --
// getting a filter wrong shows a caller a row of *theirs* they asked not to
// see, and getting the wall wrong shows them somebody else's.
func emitWatch(g *protogen.GeneratedFile, v *Entity, p Paths, root protogen.GoImportPath) {
	e := v.GoName()

	g.P("// ", e, "Service is the prefix of every RPC of that service, which is how a")
	g.P("// change is known to be about a ", e, ". A service is named for the entity it")
	g.P("// is about, so the name carries it.")
	g.P("var ", e, "Service = ", pkgWatch.Ident("ServiceOf"), "(", root.Ident(e+"Service_Get_FullMethodName"), ")")
	g.P("")

	g.P("// Watch answers with the ", e, "s this caller may see, as they are now and as")
	g.P("// they change.")
	g.P("//")
	g.P("// What is sent is **state and never a delta**, which is what makes a stream")
	g.P("// that missed something still correct: the next item about a row carries the")
	g.P("// whole of it, so a client converges rather than replays. It is also what")
	g.P("// makes the first message safe to duplicate against the ones after it.")
	g.P("func (s sink", e, ") Watch(req *", root.Ident(e+"WatchRequest"),
		", out ", pkgGrpc.Ident("ServerStreamingServer"), "[", root.Ident(e+"WatchResponse"), "]) error {")
	g.P("	ctx := out.Context()")
	g.P("")
	g.P("	// A watch with no filters is the whole table, forever. It is the one")
	g.P("	// shape that has no cap at all, so it is the one shape refused.")
	g.P("	fs := req.GetFilters()")
	g.P("	switch {")
	g.P("	case len(fs) == 0:")
	g.P("		return ", pkgStatus.Ident("Error"), "(", pkgCodes.Ident("InvalidArgument"), ",")
	g.P("			\"filters: a watch says which rows it is about; one that says nothing is the whole table, for as long as it is open\")")
	g.P("	case len(fs) > ", e, "FilterLimit:")
	g.P("		return ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("InvalidArgument"), ",")
	g.P("			\"filters: %d of them, and %d is the most one watch carries\", len(fs), ", e, "FilterLimit)")
	g.P("	}")
	g.P("")
	g.P("	// Resolved before anything is subscribed to, so a name that names")
	g.P("	// nothing is an answer rather than a stream that quietly watches none.")
	g.P("	watching, err := s.watch", e, "Keys(ctx, fs)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("")
	g.P("	var snapshot func(", pkgWatch.Ident("Seen"), ") error")
	g.P("	if !req.GetSkipSnapshot() {")
	g.P("		snapshot = func(sent ", pkgWatch.Ident("Seen"), ") error { return s.watchNow(ctx, req, out, sent) }")
	g.P("	}")
	g.P("")
	g.P("	if s.w == nil {")
	g.P("		return ", pkgStatus.Ident("Error"), "(", pkgCodes.Ident("Unimplemented"), ",")
	g.P("			\"this deployment publishes no changes; see WithWatch\")")
	g.P("	}")
	g.P("")
	g.P("	return ", pkgWatch.Ident("Stream"), "(ctx, s.w, ", e, "Service, snapshot,")
	g.P("		func(ks map[", pkgPdid.Ident("Id"), "]string, sent ", pkgWatch.Ident("Seen"), ") error {")
	g.P("			items := make([]*", root.Ident(e+"WatchItem"), ", 0, len(ks))")
	g.P("			for k, action := range ks {")
	g.P("				u, err := s.watchRead(ctx, watching, k)")
	g.P("				if err != nil {")
	g.P("					return err")
	g.P("				}")
	g.P("				if u == nil && !sent[k] {")
	g.P("					// Not theirs, or not what they asked for, and they")
	g.P("					// have never been told about it. A row that never")
	g.P("					// matched is not news.")
	g.P("					continue")
	g.P("				}")
	g.P("")
	g.P("				sent[k] = u != nil")
	g.P("				items = append(items, ", root.Ident(e+"WatchItem_builder"), "{")
	g.P("					Id:     k.Bytes(),")
	g.P("					Value:  u,")
	g.P("					Action: action,")
	g.P("				}.Build())")
	g.P("			}")
	g.P("			if len(items) == 0 {")
	g.P("				return nil")
	g.P("			}")
	g.P("")
	g.P("			return out.Send(", root.Ident(e+"WatchResponse_builder"), "{Items: items}.Build())")
	g.P("		})")
	g.P("}")
	g.P("")

	// The snapshot, through the same List a caller would have called.
	g.P("// watchNow sends what matches right now, through the same List a caller")
	g.P("// would have called -- so what a stream begins with and what a list answers")
	g.P("// cannot disagree, and a client does not have to do both and race them.")
	g.P("func (s sink", e, ") watchNow(")
	g.P("	ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(e+"WatchRequest"),
		", out ", pkgGrpc.Ident("ServerStreamingServer"), "[", root.Ident(e+"WatchResponse"), "],")
	g.P("	sent ", pkgWatch.Ident("Seen"), ",")
	g.P(") error {")
	g.P("	after := \"\"")
	g.P("	for {")
	g.P("		res, err := s.List(ctx, ", root.Ident(e+"ListRequest_builder"), "{")
	g.P("			Filters: req.GetFilters(),")
	g.P("			After:   after,")
	g.P("		}.Build())")
	g.P("		if err != nil {")
	g.P("			return err")
	g.P("		}")
	g.P("")
	g.P("		items := make([]*", root.Ident(e+"WatchItem"), ", 0, len(res.GetItems()))")
	g.P("		for _, u := range res.GetItems() {")
	g.P("			k, err := ", pkgPdid.Ident("From"), "(u.GetId())")
	g.P("			if err != nil {")
	g.P("				return err")
	g.P("			}")
	g.P("")
	g.P("			sent[k] = true")
	g.P("			// No action: this is not something anybody asked for, it is")
	g.P("			// what is already there.")
	g.P("			items = append(items, ", root.Ident(e+"WatchItem_builder"), "{Id: u.GetId(), Value: u}.Build())")
	g.P("		}")
	g.P("		if len(items) > 0 {")
	g.P("			if err := out.Send(", root.Ident(e+"WatchResponse_builder"), "{Items: items}.Build()); err != nil {")
	g.P("				return err")
	g.P("			}")
	g.P("		}")
	g.P("")
	g.P("		if after = res.GetNext(); after == \"\" {")
	g.P("			return nil")
	g.P("		}")
	g.P("	}")
	g.P("}")
	g.P("")

	// The read-back, which is where the wall does its work.
	g.P("// watchRead answers with the row as it is now, or nil when it is no longer")
	g.P("// one this caller may see -- erased, walled off, or no longer matching what")
	g.P("// they asked for. The three are deliberately indistinguishable to a caller:")
	g.P("// a stream that told them apart would be saying which rows stopped being")
	g.P("// theirs, which is the thing the wall is for.")
	g.P("//")
	g.P("// The Get is what keeps the wall out of this file. It goes through the same")
	g.P("// server every other read does, with the context of the caller who asked, so")
	g.P("// a row they may not see comes back NotFound and is never sent.")
	g.P("func (s sink", e, ") watchRead(")
	g.P("	ctx ", pkgCtx.Ident("Context"), ", watching []", pkgPdid.Ident("Id"), ", k ", pkgPdid.Ident("Id"), ",")
	g.P(") (*", root.Ident(e), ", error) {")
	g.P("	// Not one of the rows this stream is about. Asked before the read, so a")
	g.P("	// busy table costs a stream nothing for the rows it does not watch.")
	g.P("	if !", pkgSlices.Ident("Contains"), "(watching, k) {")
	g.P("		return nil, nil")
	g.P("	}")
	g.P("")
	g.P("	v, err := s.Get(ctx, ", root.Ident(e+"GetRequest_builder"), "{")
	g.P("		Ref: ", root.Ident(e+"Ref_builder"), "{Id: k.Bytes()}.Build(),")
	g.P("	}.Build())")
	g.P("	if err != nil {")
	g.P("		if ", pkgStatus.Ident("Code"), "(err) == ", pkgCodes.Ident("NotFound"), " {")
	g.P("			return nil, nil")
	g.P("		}")
	g.P("")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return v, nil")
	g.P("}")
	g.P("")

	g.P("// watch", e, "Keys is the rows a stream is about, resolved once when it opens.")
	g.P("//")
	g.P("// A filter names a row and a row is named several ways -- by identifier, or")
	g.P("// by whatever unique index the schema declared. Resolving them here rather")
	g.P("// than comparing them per event does three things: the comparison afterwards")
	g.P("// is an identifier against an identifier, a name that names nothing is")
	g.P("// refused when the stream opens rather than silently watching nothing, and a")
	g.P("// row renamed while the stream is open goes on being the row that was asked")
	g.P("// for -- which is what somebody watching a thing meant.")
	g.P("func (s sink", e, ") watch", e, "Keys(")
	g.P("	ctx ", pkgCtx.Ident("Context"), ", fs []*", root.Ident(e+"Filter"), ",")
	g.P(") ([]", pkgPdid.Ident("Id"), ", error) {")
	g.P("	ks := make([]", pkgPdid.Ident("Id"), ", 0, len(fs))")
	g.P("	for i, f := range fs {")
	g.P("		if !f.HasRef() {")
	g.P("			return nil, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("InvalidArgument"), ",")
	g.P("				\"filters[%d]: a watch says which rows it is about by naming them\", i)")
	g.P("		}")
	g.P("")
	g.P("		v, err := s.Get(ctx, ", root.Ident(e+"GetRequest_builder"), "{Ref: f.GetRef()}.Build())")
	g.P("		if err != nil {")
	g.P("			return nil, err")
	g.P("		}")
	g.P("")
	g.P("		k, err := ", pkgPdid.Ident("From"), "(v.GetId())")
	g.P("		if err != nil {")
	g.P("			return nil, err")
	g.P("		}")
	g.P("")
	g.P("		ks = append(ks, k)")
	g.P("	}")
	g.P("")
	g.P("	return ks, nil")
	g.P("}")
	g.P("")
}

// EmitWatchRecorder writes the second recorder: the one that remembers a write
// so that an interceptor can publish it once the call has succeeded.
//
// It never refuses. Every recorder is required by default -- the write fails
// with it -- and that is right for a trail and wrong for this: an event nobody
// could publish is not a reason to undo the thing it was about.
func EmitWatchRecorder(g *protogen.GeneratedFile, s *Schema, p Paths) {
	any := false
	for _, v := range s.Entities {
		if v.Watch {
			any = true
			break
		}
	}
	if !any {
		return
	}

	g.P("// WatchRecorder answers with the recorder that remembers a write for `w`.")
	g.P("//")
	g.P("// It is the other end of the hook the trail hangs off, and it wants the")
	g.P("// opposite thing from that moment: a trail row has to hold or fall with the")
	g.P("// write, so it is written there and then; an event has to be published only")
	g.P("// if the write survived, so nothing is published here at all.")
	g.P("//")
	g.P("//	sink, err := pd.NewSink(db, bare.WithRecorder(pd.WatchRecorder(w)))")
	g.P("func WatchRecorder(w *", pkgWatch.Ident("Watch"), ") ", p.Bare.Ident("Recorder"), " {")
	g.P("	return watchRecorder{w}")
	g.P("}")
	g.P("")
	g.P("type watchRecorder struct {")
	g.P("	w *", pkgWatch.Ident("Watch"))
	g.P("}")
	g.P("")
	g.P("var _ ", p.Bare.Ident("Recorder"), " = watchRecorder{}")
	g.P("")
	g.P("func (r watchRecorder) Record(ctx ", pkgCtx.Ident("Context"), ", _ ", p.Bare.Ident("Server"),
		", c ", p.Bare.Ident("Change"), ") error {")
	g.P("	k, err := ", pkgPdid.Ident("From"), "(keyBytes(c.Key))")
	g.P("	if err != nil {")
	g.P("		// A key this app does not make. Nothing to publish about it, and")
	g.P("		// nothing worth failing a write for.")
	g.P("		return nil")
	g.P("	}")
	g.P("")
	// Through `hidden`, which is the trail's redactor and is not the trail's
	// alone. It was written for F15 -- a `secret:` column reached `Audit.patch`
	// because the declaration only cleared `Audit.value` -- and the same
	// document goes to two more places from this same `Change`. Fixing the one
	// the finding named and leaving its two siblings marshalling the raw patch
	// is fixing the instance rather than the defect.
	//
	// Nothing carries it off the box today: `watchpg` deliberately sends no
	// patch, `memory` is this process, and a `WatchItem` holds the row re-read
	// per subscriber rather than the document. The point is that none of those
	// is a property of **this** line, and the first broker that does carry a
	// patch would be carrying verifiers.
	g.P("	var doc []byte")
	g.P("	if v := hidden(k, c.Patch); v != nil {")
	g.P("		if b, err := ", pkgProto.Ident("Marshal"), "(v); err == nil {")
	g.P("			doc = b")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	r.w.Note(ctx, ", pkgWatch.Ident("Change"), "{")
	g.P("		Method: c.Method,")
	g.P("		By:     c.By,")
	g.P("		Key:    k,")
	g.P("		Patch:  doc,")
	g.P("	})")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")
	g.P("// keyBytes is a key as the sixteen bytes an identifier is.")
	g.P("func keyBytes(v any) []byte {")
	g.P("	switch u := v.(type) {")
	g.P("	case ", pkgUuid2.Ident("UUID"), ":")
	g.P("		return u[:]")
	g.P("	case [16]byte:")
	g.P("		return u[:]")
	g.P("	}")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")
}
