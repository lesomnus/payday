package gate

import (
	"context"

	"google.golang.org/grpc"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/grpcx"
)

// Interceptor works out what a call may see and puts it on the frame, so that
// everything behind reads it rather than working it out again.
//
// It is where a [Policy] is consulted, and it is consulted **once**. The wall
// runs once per query and a request makes several -- a Get reads a row and then
// an edge, an Apply reads, writes and reads back -- so a policy asked from in
// there would be asked several times for one call, over a network if it is the
// sort that has one. It is the same shape `auth` uses: an interceptor works out
// who the caller is, and no layer asks again.
//
// A nil policy is not a missing piece. The deployment then sees what payday
// shows on its own: everybody sees their own tenant, and nobody sees more.
//
// It must be installed **behind** whatever says who is calling, since it reads
// the actor. A call that arrives with no frame at all -- health, reflection,
// and whatever else is public -- is passed through untouched.
func Interceptor(p Policy) grpcx.Chain {
	return grpcx.Chain{
		Unary:  []grpc.UnaryServerInterceptor{Unary(p)},
		Stream: []grpc.StreamServerInterceptor{Stream(p)},
	}
}

func Unary(p Policy) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := Decide(ctx, p, info.FullMethod)
		if err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

func Stream(p Policy) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := Decide(ss.Context(), p, info.FullMethod)
		if err != nil {
			return err
		}

		return handler(srv, grpcx.StreamWithContext(ss, ctx))
	}
}

// Decide asks whatever there is to ask and answers with the context the handler
// is served with.
//
// It is exported because a batch of calls inside one call has to apply it per
// operation: what a caller may see is keyed on the method gRpc dispatched, and
// a batch arrives as one method carrying many. An interceptor cannot be called
// from inside a handler; a function can.
func Decide(ctx context.Context, p Policy, method string) (context.Context, error) {
	f, ok := frame.From(ctx)
	if !ok {
		// Nobody vouched for this, which for a method that got this far means
		// it is one served to anybody. There is nothing to decide and nothing
		// behind it that reads a scope.
		return ctx, nil
	}

	c := Call{Actor: f.Actor, Tenant: f.Tenant, Row: f.Row, Action: method}
	if p != nil {
		if err := p.May(ctx, c); err != nil {
			return nil, err
		}
	}

	t, err := holds(ctx, p, f, c)
	if err != nil {
		return nil, err
	}

	// The credential last, because it can only take away. A token naming every
	// tenant, held by somebody who may see one, still sees one.
	return frame.Into(ctx, f.WithScope(t.Meet(f.Grant))), nil
}

// holds is what the actor may see, before the credential is met with it.
//
// Without a policy it is their own tenant, and there is no caller it is not.
// **There is deliberately no superuser** -- nothing compares an identifier
// against a well-known one and answers "everything". What a deployment has to
// do for itself, it does through a server the wall was never installed on.
func holds(ctx context.Context, p Policy, f *frame.Frame, c Call) (frame.Tenants, error) {
	if p != nil {
		return p.Where(ctx, c)
	}

	return frame.Only(f.Tenant), nil
}
