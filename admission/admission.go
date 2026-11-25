// Package admission decides whether an otherwise permitted write may commit.
//
// Install it with the generated pd.Admission option on the serving sink. The
// generated server supplies each changed row, its original request and locked
// previous image after gate, wall and database checks. A policy can only refuse;
// it cannot broaden scope or replace the write. A refusal also prevents the
// enclosing transaction from committing, including when application code ignores
// the returned error. Direct ent writes on the installed client are guarded.
//
// Policies receive copied, redacted messages and frame data. Context values,
// including transport credentials, are withheld; cancellation and the decision
// deadline remain. An adapter can call an external engine from this interface.
// It should have no side effects, honor cancellation, and keep its own client
// credentials outside the call. The deadline bounds the caller even when a
// policy ignores cancellation; the concurrency bound limits abandoned workers.
//
// Refusals log identity, operation and result code, without request values or
// policy error text. They are not Audit rows: the write's transaction rolls back.
// Applications that need durable refusal records can attach a structured log sink.
package admission

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/resource"
	"github.com/lesomnus/protobuf-patch/patchpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Call describes one row changed by a permitted operation. AddMany yields one
// call per row, in order; any refusal undoes the batch. Previous is nil for Add,
// Value is nil for Erase. Relations contain identifiers, not freshly loaded rows.
// Request is the original ask with secrets removed. Encoded patches and Any
// payloads are withheld; use Previous and Value to inspect the resulting change.
type Call struct {
	Frame    *frame.Frame
	Entity   string
	Object   pdid.Id
	Method   string
	By       string
	Request  proto.Message
	Previous proto.Message
	Value    proto.Message
}

// Policy returns nil to continue, or a gRPC status explaining the refusal.
// A plain error or panic is an unavailable policy, never permission to proceed.
type Policy interface {
	Admit(context.Context, Call) error
}
type Func func(context.Context, Call) error

func (f Func) Admit(ctx context.Context, c Call) error { return f(ctx, c) }

// Options is configured once on the generated sink. Entities is required; each
// pattern names a full protobuf entity or uses * for the entire package or name
// component, as frame.Covers does for methods: app.Job, app.*, *.*.
// Payday's internal recording entities are excluded from these patterns.
type Options struct {
	Entities []string
	// Timeout defaults to two seconds; a shorter caller deadline takes precedence.
	Timeout time.Duration
	// Limit defaults to 32 simultaneous decisions. A timed-out policy that ignores
	// cancellation retains its slot until it returns. Exhaustion refuses new work.
	Limit int
	// Asked says which calls the policy is asked about, by the outer method a
	// Call carries (Call.Method: the RPC the transport captured, or, for a
	// write with no outer method captured, the method the policy would have
	// been given). A call it answers false for is admitted at once: the policy
	// is not called, nothing is copied or cleared for it, and it takes no slot,
	// so a policy that is down or slow holds up only the calls it is asked
	// about. Nil asks about every call.
	//
	// It is called with the method alone, first, on the write's own goroutine
	// and inside its transaction, and nothing bounds it: it must be cheap and
	// must not block. A set of method names is what it is for.
	Asked func(method string) bool
	// Secrets adds fields on custom RPC messages. Generated entity and Add/Patch
	// secrets are included automatically and cannot be removed by this map.
	// An opaque bytes or JSON field containing secrets must be declared in full.
	Secrets map[protoreflect.FullName][]protoreflect.FieldNumber
	Log     *slog.Logger
}

// Entity connects a generated row to its Add, Patch and reference descriptors.
// Their field numbers need not agree; secrets are mapped by field name.
type Entity struct {
	Row      proto.Message
	Requests []proto.Message
}

// Select resolves entity patterns at startup, refusing empty or unmatched
// patterns so a misspelled protection cannot silently leave a table open.
func Select(patterns, names []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("admission: name at least one entity")
	}
	var out []string
	for _, p := range patterns {
		matched := false
		for _, n := range names {
			if frame.Covers("/"+p+"/*", "/"+n+"/*") {
				matched = true
				if !slices.Contains(out, n) {
					out = append(out, n)
				}
			}
		}
		if !matched {
			return nil, fmt.Errorf("admission: entity pattern %q matches no eligible entity", p)
		}
	}
	return out, nil
}

// Checker is the bounded, redacting half used by the generated adapter.
// Construct one per sink; policies may be called concurrently.
type Checker struct {
	policy  Policy
	asked   func(string) bool
	timeout time.Duration
	slots   chan struct{}
	secrets map[protoreflect.FullName][]protoreflect.FieldNumber
	log     *slog.Logger
}

func New(policy Policy, o Options, entities []Entity, secrets map[protoreflect.FullName][]protoreflect.FieldNumber) (*Checker, error) {
	if policy == nil {
		return nil, fmt.Errorf("admission: no policy")
	}
	if o.Timeout < 0 || o.Limit < 0 {
		return nil, fmt.Errorf("admission: timeout and limit must be positive")
	}
	if o.Timeout == 0 {
		o.Timeout = 2 * time.Second
	}
	if o.Limit == 0 {
		o.Limit = 32
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	r := map[protoreflect.FullName][]protoreflect.FieldNumber{}
	descriptors := map[protoreflect.FullName]protoreflect.MessageDescriptor{}
	for name, ns := range secrets {
		r[name] = slices.Clone(ns)
	}
	for _, e := range entities {
		md := e.Row.ProtoReflect().Descriptor()
		descriptors[md.FullName()] = md
		for _, req := range e.Requests {
			rd := req.ProtoReflect().Descriptor()
			descriptors[rd.FullName()] = rd
		}
	}
	for name, ns := range o.Secrets {
		md := descriptors[name]
		if md == nil {
			d, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
			if err != nil {
				return nil, fmt.Errorf("admission: unknown secret message %q", name)
			}
			md, _ = d.(protoreflect.MessageDescriptor)
		}
		if md == nil {
			return nil, fmt.Errorf("admission: %q is not a message", name)
		}
		for _, n := range ns {
			if md.Fields().ByNumber(n) == nil {
				return nil, fmt.Errorf("admission: unknown secret field %s.%d", name, n)
			}
		}
		r[name] = append(r[name], ns...)
	}
	for _, e := range entities {
		md := e.Row.ProtoReflect().Descriptor()
		for _, req := range e.Requests {
			rd := req.ProtoReflect().Descriptor()
			for _, n := range r[md.FullName()] {
				if f := md.Fields().ByNumber(n); f != nil {
					if rf := rd.Fields().ByName(f.Name()); rf != nil {
						r[rd.FullName()] = append(r[rd.FullName()], rf.Number())
					}
				}
			}
		}
	}

	// Encoded values hide their descriptors from recursive protobuf traversal.
	// Removing them prevents a secret from surviving inside a patch or Any.
	r["google.protobuf.Any"] = append(r["google.protobuf.Any"], 2)
	md := (&patchpb.Patch{}).ProtoReflect().Descriptor()
	for i := 0; i < md.Fields().Len(); i++ {
		r[md.FullName()] = append(r[md.FullName()], md.Fields().Get(i).Number())
	}
	return &Checker{policy: policy, asked: o.Asked, timeout: o.Timeout, slots: make(chan struct{}, o.Limit), secrets: r, log: o.Log}, nil
}

// decisionContext withholds arbitrary request values, especially bearer tokens
// in gRPC metadata and application-specific authentication state.
type decisionContext struct{ context.Context }

func (decisionContext) Value(any) any { return nil }

func (r *Checker) public(v proto.Message) proto.Message { return resource.Redact(v, r.secrets) }
func (r *Checker) principal(f *frame.Frame) *frame.Frame {
	out := *f
	out.Row = r.public(f.Row)
	if f.Caller != nil {
		v := *f.Caller
		v.Row = r.public(v.Row)
		out.Caller = &v
	}
	if !f.Scope.All() {
		out.Scope = frame.Only(f.Scope.Ids()...)
	}
	if !f.Grant.AnyTenant() {
		out.Grant = out.Grant.In(f.Grant.TenantIds()...)
	}
	if !f.Grant.AnySet() {
		out.Grant = out.Grant.Within(f.Grant.SetIds()...)
	}
	if !f.Grant.AnyAction() {
		out.Grant = out.Grant.To(f.Grant.Actions()...)
	}
	return &out
}

// Check copies and redacts the call before invoking policy. Frame comes from the
// verified context, not a caller-supplied Call field. Missing identity is refused;
// bootstrap must use the explicit generated bare.TrustAdmission context.
//
// A call of a method [Options.Asked] answers false for is admitted before any
// of that: it is none of the policy's business, so it spends none of what the
// policy's calls share. What the generated server does with an admission --
// counting the write off against the guard on direct ent writes, so that its
// transaction may commit -- it does with this one as with one the policy let
// through, since both are a nil here.
func (r *Checker) Check(ctx context.Context, c Call) (err error) {
	if r.asked != nil && !r.asked(c.Method) {
		return nil
	}
	f, ok := frame.From(ctx)
	// Capture log attributes before invoking policy: its copies can be changed.
	attrs := []any{"entity", c.Entity, "object", c.Object.String(), "method", c.Method, "operation", c.By}
	if ok {
		attrs = append(attrs, "actor", f.Actor.String())
		if f.Caller != nil {
			attrs = append(attrs, "caller", f.Caller.Actor.String())
		}
	}
	defer func() {
		if err != nil {
			r.log.WarnContext(ctx, "admission refused", append(attrs, "code", status.Code(err).String())...)
		}
	}()
	if !ok {
		return status.Error(codes.Unauthenticated, "admission requires an authenticated frame")
	}
	bounded, cancel := context.WithTimeout(decisionContext{ctx}, r.timeout)
	defer cancel()
	if bounded.Err() != nil {
		return status.FromContextError(bounded.Err()).Err()
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return status.Error(codes.ResourceExhausted, "admission policy is busy")
	}
	c.Frame = r.principal(f)
	c.Request = r.public(c.Request)
	c.Previous = r.public(c.Previous)
	c.Value = r.public(c.Value)
	decided := make(chan error, 1)
	go func() {
		var result error
		defer func() {
			if recover() != nil {
				result = status.Error(codes.Unavailable, "admission policy failed")
			}
			<-r.slots
			decided <- result
		}()
		result = r.policy.Admit(frame.Into(bounded, c.Frame), c)
	}()
	select {
	case <-bounded.Done():
		return status.FromContextError(bounded.Err()).Err()
	case err = <-decided:
		if bounded.Err() != nil {
			return status.FromContextError(bounded.Err()).Err()
		}
		if err == nil {
			return nil
		}
		if _, known := status.FromError(err); known {
			return err
		}
		return status.Error(codes.Unavailable, "admission policy failed")
	}
}
