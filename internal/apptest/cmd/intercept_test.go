package cmd_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/core"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// stack is the served stack with `mws` where the interceptor layer goes, on
// this test's own database.
//
// It is the stack `cmd.Build` assembles, so what is asserted below is the
// layer in the position an app would put it in rather than one standing on its
// own.
func (b *built) stack(t *testing.T, mws ...app.Builder) app.Server {
	t.Helper()

	s, err := pd.NewSink(b.Ent, bare.WithMinter(pd.Minter()), bare.WithScope(pd.Wall()))
	require.NoError(t, err)

	// The watch the harness built, because a `Watch` without one is refused
	// before it reaches a stream and this is where the stream half is tested.
	v, err := app.Build(s.WithWatch(b.Watch), mws...)
	require.NoError(t, err)

	return v
}

// TestAnInterceptorRunsBetweenLayers.
//
// The layer takes what `grpc.NewServer` takes, which is the whole point: an
// interceptor already written for the wire runs between two layers without
// being written a second time against a different shape.
func TestAnInterceptorRunsBetweenLayers(t *testing.T) {
	t.Run("it is handed the call, and the method it is", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		var saw []string
		s := b.stack(t, core.Build(), pd.InterceptBuild([]grpc.UnaryServerInterceptor{
			func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
				saw = append(saw, info.FullMethod)
				return next(ctx, req)
			},
		}, nil), pd.GateBuild())

		v, err := s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		x.NotEmpty(v.GetId())

		// The Add, and the tenant read the gate does above it -- which is the
		// thing this layer does that a wire interceptor does not, and is why
		// where it is stacked is a decision.
		x.Contains(saw, app.RobotService_Add_FullMethodName)
		x.Contains(saw, app.TenantService_Get_FullMethodName)
	})

	t.Run("and what it refuses does not reach the database", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		s := b.stack(t, core.Build(), pd.InterceptBuild([]grpc.UnaryServerInterceptor{
			func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
				if info.FullMethod == app.RobotService_Add_FullMethodName {
					return nil, status.Error(codes.ResourceExhausted, "not today")
				}

				return next(ctx, req)
			},
		}, nil), pd.GateBuild())

		_, err := s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.ResourceExhausted, status.Code(err))

		n, err := b.Ent.Robot.Query().Count(ctx)
		x.NoError(err)
		x.Zero(n, "a refusal that still wrote the row is not a refusal")
	})

	t.Run("several run outermost first", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		var order []string
		mark := func(name string) grpc.UnaryServerInterceptor {
			return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
				order = append(order, name+" in")
				v, err := next(ctx, req)
				order = append(order, name+" out")

				return v, err
			}
		}

		s := b.stack(t, core.Build(), pd.InterceptBuild([]grpc.UnaryServerInterceptor{mark("a"), mark("b")}, nil))

		_, err := s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		x.Equal([]string{"a in", "b in", "b out", "a out"}, order)
	})

	// Nothing to run is no layer, so a deployment that assembles its
	// interceptors from configuration and ends up with none is the stack it
	// would have had.
	t.Run("nothing to run builds nothing", func(t *testing.T) {
		x := require.New(t)
		b, _ := build(t)

		s, err := pd.NewSink(b.Ent, bare.WithMinter(pd.Minter()))
		x.NoError(err)

		v, err := pd.InterceptBuild(nil, nil).Build(s)
		x.NoError(err)

		// Asserted by type rather than by value: two of these compare unequal
		// whatever they hold, since a struct with a func in it is never
		// `reflect.DeepEqual` to another. What is being said is that no layer
		// was put in front, and the type is what says it.
		_, wrapped := v.(pd.Intercept)
		x.False(wrapped)
		x.IsType(s, v)
	})
}

// fakeStream is a `grpc.ServerStreamingServer` that counts what was sent to
// it and says when the first one arrived. It is the whole of what the stream
// half needs: a stream an interceptor can be handed, and a second one it can
// put in the way.
type fakeStream struct {
	grpc.ServerStream

	ctx  context.Context
	sent atomic.Int64
	once sync.Once
	// first closes on the send that says the snapshot has been taken, which is
	// what the test waits for rather than spinning on the count.
	first chan struct{}
}

func newFakeStream(ctx context.Context) *fakeStream {
	return &fakeStream{ctx: ctx, first: make(chan struct{})}
}

func (s *fakeStream) Context() context.Context { return s.ctx }
func (s *fakeStream) RecvMsg(any) error        { return nil }

// Both, because which one is called says who is holding the stream. A caller
// with the typed stream calls Send; `grpc.GenericServerStream` -- which is what
// a stream handed back by an interceptor is wrapped in -- answers Send by
// calling SendMsg on what it wraps. Counting only the first is how this test
// managed to assert nothing.
func (s *fakeStream) Send(*app.RobotWatchResponse) error { return s.sending() }
func (s *fakeStream) SendMsg(any) error                  { return s.sending() }

func (s *fakeStream) sending() error {
	s.sent.Add(1)
	s.once.Do(func() { close(s.first) })

	return nil
}

// TestAStreamInterceptorMayPutItselfInTheWay.
//
// A stream interceptor is handed a `grpc.ServerStream` and is free to answer
// with another -- wrapping it is most of what the kind is for. So the handler
// has to receive what the interceptor passed on rather than the stream the
// layer started with, which is why the typed stream is rebuilt around whatever
// came back instead of being asserted back to the one it no longer is.
func TestAStreamInterceptorMayPutItselfInTheWay(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	s := b.stack(t, core.Build(), pd.InterceptBuild(nil, nil), pd.GateBuild())

	v, err := s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// Bounded, so that a snapshot which never comes is an assertion below
	// rather than a test that sits there: `Watch` waits on the broker once it
	// has sent, and nothing here is going to write again.
	wctx, cancel := context.WithTimeout(b.as(ctx), 10*time.Second)
	defer cancel()

	var info *grpc.StreamServerInfo
	mine := newFakeStream(wctx)
	out := newFakeStream(wctx)

	watched := b.stack(t, core.Build(), pd.InterceptBuild(nil, []grpc.StreamServerInterceptor{
		func(srv any, ss grpc.ServerStream, i *grpc.StreamServerInfo, next grpc.StreamHandler) error {
			info = i

			// Not `ss`: the handler must end up sending into this one.
			return next(srv, mine)
		},
	}), pd.GateBuild())

	// The snapshot is one send, and then there is nothing more to wait for --
	// the stream would sit on the broker until somebody wrote, and what is
	// being tested is which stream the snapshot went to.
	go func() {
		select {
		case <-mine.first:
			cancel()
		case <-wctx.Done():
		}
	}()

	err = watched.Robot().Watch(app.RobotWatchRequest_builder{
		Filters: []*app.RobotFilter{
			app.RobotFilter_builder{Ref: app.RobotRef_builder{Id: v.GetId()}.Build()}.Build(),
		},
	}.Build(), out)
	x.Error(err, "the stream ends when the context does")

	x.NotNil(info)
	x.Equal(app.RobotService_Watch_FullMethodName, info.FullMethod)
	x.True(info.IsServerStream)

	// What the handler sent went to the interceptor's stream and not to the
	// one the layer was called with.
	x.Positive(mine.sent.Load())
	x.Zero(out.sent.Load())
}
