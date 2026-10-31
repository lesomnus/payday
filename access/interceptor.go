package access

import (
	"context"
	"net"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lesomnus/otx/log"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// Config is what the interceptor needs of the app.
type Config struct {
	// Subjects is whose personal data a message carries: the generated
	// `pd.Subjects`, which reads the `personal` declarations.
	Subjects func(proto.Message) []Subject

	// Refs is whose rows a request names: the generated `pd.SubjectRefs`. It
	// is what a refused call carried, since it carried no answer.
	Refs func(proto.Message) []Subject

	// Address is where a call came from, past the proxies the app trusts.
	// Nil records none.
	Address func(context.Context) string

	// Downloads are the methods that hand personal data out to be kept
	// somewhere else -- a file, an export -- by their full names. Each says
	// what it is for in [ReasonKey], or is refused, and its row is written
	// before its answer leaves.
	Downloads []string

	// Writer writes the rows.
	Writer *Writer

	// Now is the clock; nil is the wall's.
	Now func() time.Time
}

// Interceptor is the access record's interceptors, for the chain.
//
// After what says who is calling, since a row names them, and after what
// limits how often they may: a refusal is a row, and a flood of them refused
// there is not a flood of rows. Before the gate, since a call it refuses is
// recorded too, with what it asked for.
func (c Config) Interceptor() grpcx.Chain {
	return grpcx.Chain{
		Unary:  []grpc.UnaryServerInterceptor{c.Unary()},
		Stream: []grpc.StreamServerInterceptor{c.Stream()},
	}
}

// Unary is the interceptor for a call with one answer.
func (c Config) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		reason, err := c.reason(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}

		ctx, call := c.into(ctx, info.FullMethod)
		res, herr := handler(ctx, req)

		var answer, asked proto.Message
		if m, ok := res.(proto.Message); ok && herr == nil {
			answer = m
		}
		if m, ok := req.(proto.Message); ok {
			asked = m
		}
		if err := c.done(ctx, call, answer, asked, herr, reason); err != nil {
			return nil, err
		}

		return res, herr
	}
}

// Stream is the interceptor for a call that answers a stream. What it sends is
// read as it is sent, and said by rows as it goes rather than when it ends: a
// download's before each message leaves, and a read's queued within
// [Writer.Every] of it -- a watch open for an hour is a record of what it
// showed in that hour, not of the moment it closed.
func (c Config) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		reason, err := c.reason(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}

		ctx, call := c.into(ss.Context(), info.FullMethod)
		w := &watched{ServerStream: ss, ctx: ctx, c: c, call: call, reason: reason}
		herr := handler(srv, w)
		w.stop()
		call.Settle(herr == nil)

		// What it sent is sent: a row that cannot be written now is said in
		// the log, since there is nothing left to refuse.
		if err := c.record(ctx, call, w.request(), herr, reason); err != nil {
			log.From(ctx).ErrorContext(ctx, "access: a stream's record could not be written", "method", info.FullMethod, "err", err)
		}

		return herr
	}
}

type watched struct {
	grpc.ServerStream
	ctx    context.Context
	c      Config
	call   *Call
	reason string

	mu   sync.Mutex
	soon *time.Timer

	// asked is the request the handler read, which is what the stream was
	// asked for.
	asked proto.Message
}

func (w *watched) Context() context.Context { return w.ctx }

func (w *watched) RecvMsg(m any) error {
	if err := w.ServerStream.RecvMsg(m); err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if v, ok := m.(proto.Message); ok && w.asked == nil {
		w.asked = v
	}

	return nil
}

func (w *watched) request() proto.Message {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.asked
}

func (w *watched) SendMsg(m any) error {
	if v, ok := m.(proto.Message); ok && w.c.Subjects != nil {
		w.call.add(false, w.c.Subjects(v)...)
	}

	// A download's message leaves once a row says what it carries, which is a
	// statement for each message that carries anybody: what hands out many
	// rows sends them in few messages.
	if w.reason != "" {
		if err := w.c.record(w.ctx, w.call, w.request(), nil, w.reason); err != nil {
			return err
		}
	} else {
		w.later()
	}

	return w.ServerStream.SendMsg(m)
}

// later queues a row of what the stream has sent, a [Writer.Every] from now,
// unless one is due already: what one message carried is said within that of
// it being sent, however long the stream stays open after it.
func (w *watched) later() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.soon != nil {
		return
	}

	w.soon = time.AfterFunc(w.c.Writer.every(), func() {
		w.mu.Lock()
		w.soon = nil
		w.mu.Unlock()

		if err := w.c.record(w.ctx, w.call, w.request(), nil, ""); err != nil {
			log.From(w.ctx).ErrorContext(w.ctx, "access: a stream's record could not be written", "method", w.call.Method(), "err", err)
		}
	})
}

func (w *watched) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.soon != nil {
		w.soon.Stop()
		w.soon = nil
	}
}

// into is the call's [Call], as this configuration makes it.
func (c Config) into(ctx context.Context, method string) (context.Context, *Call) {
	ctx, call := Into(ctx, method)
	call.address = c.address(ctx)
	call.downloads = c.Downloads

	return ctx, call
}

// reason is what a download says it is for, and a refusal for one that says
// nothing, or nothing a row can hold. Anything else needs none.
func (c Config) reason(ctx context.Context, method string) (string, error) {
	if !slices.Contains(c.Downloads, method) {
		return "", nil
	}

	md, _ := metadata.FromIncomingContext(ctx)
	v := strings.TrimSpace(strings.Join(md.Get(ReasonKey), " "))
	switch {
	case v == "":
		return "", status.Errorf(codes.InvalidArgument,
			"%s hands personal data out: say what it is for, in %q", method, ReasonKey)
	case !utf8.ValidString(v):
		return "", status.Errorf(codes.InvalidArgument, "%s: what it is for is not text", ReasonKey)
	case len(v) > MaxReason:
		return "", status.Errorf(codes.InvalidArgument,
			"%s: what it is for is %d bytes, and a row holds %d", ReasonKey, len(v), MaxReason)
	}

	return v, nil
}

// done writes the call's rows, once it has ended, of what its answer carried --
// or, for a call that was refused, what its request named -- and of whatever
// else it carried that no row says yet.
func (c Config) done(ctx context.Context, call *Call, answer, asked proto.Message, herr error, reason string) error {
	call.Settle(herr == nil)
	switch {
	case answer != nil && c.Subjects != nil:
		call.add(false, c.Subjects(answer)...)
	case herr != nil && asked != nil && c.Refs != nil:
		call.add(false, c.Refs(asked)...)
	}

	return c.record(ctx, call, asked, herr, reason)
}

// record writes rows of what the call has carried that none says yet, a row
// for what was done to whose data:
//
//   - a write of the data its writes wrote, where no commit said it already --
//     a transaction that kept nothing, or one rolled back, whose call ended in
//     the refusal the row then says;
//   - a download of what a download carried, at once, refused when it cannot
//     be written;
//   - and a read of what anything else carried, queued.
//
// So a person whose data a call both wrote and showed is a row of the write,
// and a write refused before it wrote is a read of what it named, its refusal
// in the row's result and what it asked for in its action.
func (c Config) record(ctx context.Context, call *Call, asked proto.Message, herr error, reason string) error {
	at := c.now()
	result := ResultOf(herr)

	wrote, others := call.Unsaid()
	if len(wrote) > 0 {
		d := pdpb.AccessDetail_builder{Address: call.Address()}.Build()
		if err := c.Writer.Write(ctx, Records(ctx, call.Method(), Write, result, wrote, d, at)...); err != nil {
			log.From(ctx).ErrorContext(ctx, "access: a write's record could not be written after it", "method", call.Method(), "err", err)
		} else {
			call.Said(true, wrote...)
		}
	}

	// What a read or a download asked for is what it was: which rows, by what
	// it was searched with. A write's is on the trail.
	d := pdpb.AccessDetail_builder{Address: call.Address(), Reason: reason}.Build()
	if b := c.request(ctx, call, asked, herr); b != nil {
		d.SetRequest(b)
	}

	if reason == "" {
		if vs := call.take(); len(vs) > 0 {
			c.Writer.Queue(Records(ctx, call.Method(), Read, result, vs, d, at)...)
		}

		return nil
	}
	if len(others) == 0 {
		return nil
	}
	if err := c.Writer.Write(ctx, Records(ctx, call.Method(), Download, result, others, d, at)...); err != nil {
		if herr != nil {
			log.From(ctx).ErrorContext(ctx, "access: a refused download's record could not be written", "method", call.Method(), "err", err)
			return nil
		}

		log.From(ctx).ErrorContext(ctx, "access: a download was refused, since its record could not be written", "method", call.Method(), "err", err)
		return status.Error(codes.Unavailable, ErrUnrecorded.Error())
	}

	call.Said(false, others...)
	return nil
}

// request is what a row keeps of what the call asked for: a read's or a
// download's request, which is what says how a page of people came to be
// shown -- and nothing of one that wrote, or was refused.
//
// A write's request is the write, whose trail holds what it said and none of
// what the schema declared `personal`. A refused call carried nothing but
// what it asked for, and that is somebody's data as often as not -- the name
// and the number a refused write tried to write -- so its row names the rows
// it asked about (`Config.Refs`) and keeps no more. And it is the request as
// its message has it: what this build does not know of it is not kept, and
// one larger than [MaxRequest] is not kept at all, since a list or a search
// that size is not one a review reads and a row that size is a way to fill
// the database.
func (c Config) request(ctx context.Context, call *Call, asked proto.Message, herr error) []byte {
	if asked == nil || herr != nil || call.Writes() {
		return nil
	}

	v := proto.Clone(asked)
	dropUnknown(v.ProtoReflect())
	b, err := proto.Marshal(v)
	if err != nil {
		return nil
	}
	if len(b) > MaxRequest {
		log.From(ctx).WarnContext(ctx, "access: a request too large to keep", "method", call.Method(), "size", len(b))
		return nil
	}

	return b
}

// dropUnknown clears what `m` holds that its message does not declare, all the
// way down.
func dropUnknown(m protoreflect.Message) {
	if !m.IsValid() {
		return
	}

	m.SetUnknown(nil)
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList():
			if fd.Message() != nil {
				l := v.List()
				for i := range l.Len() {
					dropUnknown(l.Get(i).Message())
				}
			}
		case fd.IsMap():
			if fd.MapValue().Message() != nil {
				v.Map().Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool {
					dropUnknown(v.Message())
					return true
				})
			}
		case fd.Message() != nil:
			dropUnknown(v.Message())
		}

		return true
	})
}

// Peer is where a call came from as its connection says: the address at the
// other end of it, which is the caller's only when nothing stands between them.
// An app behind a proxy answers [Config.Address] with what it trusts of the
// proxy's headers instead.
func Peer(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}
	if v, ok := p.Addr.(*net.TCPAddr); ok {
		return v.IP.String()
	}

	return p.Addr.String()
}

func (c Config) address(ctx context.Context) string {
	if c.Address == nil {
		return ""
	}

	return c.Address(ctx)
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}

	return time.Now()
}

// Records is the rows one call is: one for each tenant whose data it carried,
// as the frame in `ctx` made it. A subject with no tenant of its own is the
// call's. It is what the interceptor writes, and what the generated recorder
// writes at a write's commit.
//
// A call an account makes as itself names the **account** as who made it
// (payday#184), since there is no holder to name: the row's actor is the
// account and its actor's tenant is Nil -- which no membership's row has, a
// holder being always some tenant's -- so a review tells one from the other by
// that column. Whose data it carried is filed as anybody's is, under the
// tenant the row it was in is held by, and what was in no tenant's row under
// none: the deployment's own record, which an operator reads.
func Records(ctx context.Context, method string, kind Kind, result string, subjects []Subject, d *pdpb.AccessDetail, at time.Time) []Record {
	var actor, held pdid.Id
	if f, ok := frame.From(ctx); ok {
		actor, held = f.Actor, f.Tenant
		if f.AsAccount() {
			actor, held = f.Account, pdid.Nil
		}
	}

	tenants := []pdid.Id{}
	of := map[pdid.Id][]pdid.Id{}
	for _, v := range subjects {
		t := v.Tenant
		if t.IsZero() {
			t = held
		}
		if _, ok := of[t]; !ok {
			tenants = append(tenants, t)
		}
		of[t] = append(of[t], v.Id)
	}

	out := make([]Record, 0, len(tenants))
	for _, t := range tenants {
		ids := of[t]
		out = append(out, Record{
			Tenant:      t,
			Actor:       actor,
			ActorTenant: held,
			Trace:       traceOf(ctx),
			Action:      method,
			Kind:        kind,
			Result:      result,
			At:          at.UTC(),
			Subjects:    ids[:min(len(ids), MaxSubjects)],
			Count:       len(ids),
			Detail:      d,
		})
	}

	return out
}

// ResultOf is how a call ended, as a row says it.
func ResultOf(err error) string {
	if err == nil {
		return "ok"
	}

	return status.Code(err).String()
}

func unique(vs []Subject) []Subject {
	seen := map[Subject]struct{}{}
	out := vs[:0:0]
	for _, v := range vs {
		if v.Id.IsZero() {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}

		seen[v] = struct{}{}
		out = append(out, v)
	}

	return out
}

// traceOf is the trace a call is part of, empty when it was not traced.
func traceOf(ctx context.Context) []byte {
	s := trace.SpanContextFromContext(ctx)
	if !s.HasTraceID() {
		return []byte{}
	}

	id := s.TraceID()
	return id[:]
}
