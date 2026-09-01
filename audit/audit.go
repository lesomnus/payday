// Package audit is the trail of what was changed, and by whom.
//
// It is two things that do not look alike, and the difference is the design.
//
// The **recorder** is not a layer of the stack at all. It is handed to the
// generated servers, which call it from inside the transaction that makes a
// write -- so every Rpc that changes anything is on the trail without anybody
// having listed them, and one added later is on it without anybody remembering
// to. A layer in front cannot do this: `Patch` and `Apply` are two Rpcs and one
// write, and they become one *below* anything that can be stacked on top.
//
// The **layer** is here for the other half. The trail is written by the app and
// read by people, and nobody gets to write one by hand: a trail a deployment
// can edit is evidence of nothing.
//
// # Where it sits, and why it is below everything
//
// A trail should say what was written, and the layers in front normalize on the
// way down -- an alias is trimmed and folded before it is stored. A trail kept
// in front of that would say the caller wrote " Johnny " into a row that holds
// "johnny".
//
// # What is here and what is generated
//
// What a row of the trail says is here. Assembling it into the app's own
// AuditAddRequest, and the layer that refuses the writes, are generated: both
// name types payday cannot.
package audit

import (
	"context"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// Row is one line of the trail, as payday decides it.
//
// It is the values and not the message, because the message is the app's --
// generated from payday's schema merged with whatever the app added. What is
// generated is the assembly; what is decided is here.
type Row struct {
	// Tenant is who the write was on behalf of, and Actor is who made it.
	// Both are zero for a write nobody asked for, which is what a deployment
	// does to itself before it serves anything.
	Tenant pdid.Id
	Actor  pdid.Id

	// Trace is the trace the request belonged to, empty if it was not traced.
	// Sixteen bytes as OpenTelemetry spells one, which is not a Uuid however
	// alike they look.
	Trace []byte

	// Action is the Rpc the caller asked for.
	Action string

	// Object is the row that changed.
	Object pdid.Id

	// Patch is the document the write was compiled from, and empty for a write
	// that was not one.
	Patch []byte

	// Counterpart is the other tenant this write was about, and zero for
	// nearly every one. It is what [Concerning] put in the context.
	Counterpart pdid.Id
}

// CounterpartBytes is [Row.Counterpart] as a request carries it, and **nil**
// when there is none.
//
// Nil rather than sixteen zero bytes, because the column is nullable and the
// difference is what the row says: a zero identifier is a value in the column
// that decides who may read the row, and NULL is the absence of one. They
// narrow the same -- no scope holds the zero identifier -- and only one of them
// is true.
func (v Row) CounterpartBytes() []byte {
	if v.Counterpart.IsZero() {
		return nil
	}

	return v.Counterpart.Bytes()
}

// concerning is where [Concerning] keeps it.
type concerning struct{}

// Concerning says that the write about to happen is about `id` as well as
// whatever it is written on.
//
// # What it is for
//
// A write with two sides. A row moves from one tenant to another and the record
// is filed under where it ended up, so the tenant that let it go cannot see the
// event that took it away -- the one row it most needs is the one it is not a
// party to. This names that other party, and the wall on the trail counts it.
//
// It is deliberately not "the previous tenant". What the pair means is the
// operation's to know: a transfer says where the row came from, and something
// else may mean something else by it. What payday decides is only that both
// tenants may read the row.
//
// # Why a context and not an argument
//
// Because the write is several calls below whoever knows. An operation that
// means something -- a Transfer -- is a layer, and what it does is call the
// generated Patch underneath; the recorder runs below that again, inside the
// transaction. Nothing in between has a parameter to carry this, and giving
// every generated write one would be paying for it everywhere for the sake of
// the one place that has something to say.
//
// It travels the way the frame and the trace already do, which is what [Of]
// reads, so this is the same seam rather than a second one.
//
// # It is server-side, and that is the whole of the rule
//
// A caller cannot reach this: there is no request field for it, and a layer is
// the only thing that can put it in a context. What names a tenant here grants
// that tenant a read of this row, so it has to stay something the deployment's
// own code says.
func Concerning(ctx context.Context, id pdid.Id) context.Context {
	return context.WithValue(ctx, concerning{}, id)
}

// counterpart is what [Concerning] said, or zero.
func counterpart(ctx context.Context) pdid.Id {
	v, _ := ctx.Value(concerning{}).(pdid.Id)
	return v
}

// Of answers with the line the trail holds for one write.
//
// `method` is what the **caller** asked for and not the leg of it that wrote.
// One call writes through several servers -- adding a tenant writes the holder
// that comes with it -- and an Rpc written by hand ends in an Apply nobody
// called by that name. "Who renamed this" has to answer with Rename.
//
// `key` is the row as the server holds it, which for payday is always a uuid:
// an entity keyed otherwise is a decision about what the trail should say for
// it, so it is refused here rather than written as something that is not an
// identifier.
func Of(ctx context.Context, method string, key any, patch proto.Message) (Row, error) {
	object, err := Identifier(key)
	if err != nil {
		return Row{}, err
	}

	doc, err := document(patch)
	if err != nil {
		return Row{}, status.Errorf(codes.Internal, "hold the patch of %s: %s", method, err)
	}

	v := Row{
		Trace:       traceOf(ctx),
		Action:      method,
		Object:      object,
		Patch:       doc,
		Counterpart: counterpart(ctx),
	}
	if f, ok := frame.From(ctx); ok {
		v.Actor = f.Actor
		v.Tenant = f.Tenant
	}

	return v, nil
}

// Identifier is the key of the row that changed, as the trail holds one.
//
// Exported because a recorder has to know **which entity** a change is about
// before it decides what of the patch it may keep -- a field declared `secret:`
// belongs to one entity, and the only thing that says which is the identifier's
// own domain byte. [Of] answers that too, and one call later than the decision
// has to be made.
func Identifier(key any) (pdid.Id, error) {
	switch v := key.(type) {
	case pdid.Id:
		return v, nil
	case uuid.UUID:
		// What the generated servers hold, since that is the type ent was
		// given. That it is also one of payday's is what pdid says about the
		// bytes rather than about the type.
		return pdid.Id(v), nil
	case [16]byte:
		return pdid.Id(v), nil
	}

	return pdid.Nil, status.Errorf(codes.Internal,
		"the trail holds what changed by identifier, and this one is a %T", key)
}

// document is the patch as the trail holds it, byte for byte.
//
// A trail is written once and read by somebody asking what happened, so it
// keeps what was said rather than a reading of it; whoever asks unmarshals it.
//
// No bytes, then, and **never no value**. The column holds bytes and says it
// always has some, and a nil slice reaches the database as NULL through one
// driver and as an empty string through another -- a difference that does not
// show up until the engine that is not the one the tests run on refuses the row.
func document(v proto.Message) ([]byte, error) {
	if v == nil || !v.ProtoReflect().IsValid() {
		return []byte{}, nil
	}

	return proto.Marshal(v)
}

// traceOf is the trace a write happened inside, so that a line of the trail and
// the record of the call that made it can be put beside each other.
//
// Empty when the call was not traced, which is not an error: a deployment that
// runs no telemetry still keeps a trail.
func traceOf(ctx context.Context) []byte {
	s := trace.SpanContextFromContext(ctx)
	if !s.HasTraceID() {
		return []byte{}
	}

	id := s.TraceID()
	return id[:]
}
