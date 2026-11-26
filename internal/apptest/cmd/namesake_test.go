package cmd_test

import (
	"context"
	"errors"
	"io"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/internal/pdgen/shadow"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// RobotService/Seal, Reading, Job and Joint are called what other entities of
// this app are called (`proto/ext/app/robot_svc.ext.proto`), one of each shape
// a method has. Every type a layer writes around a service embeds the layer's
// struct beside it, and the struct has a method for every entity, so these were
// promoted from neither and the app did not compile. That it compiles is most
// of the claim; what is left is that they are served through every layer, and
// refused where the gate's rule for them says -- the rule an RPC of the app's
// own gets, neither more nor less.

// exported is every exported name `t` answers to: its fields, promoted ones
// included, and the methods of it and of a pointer to it.
func exported(t reflect.Type) []string {
	var vs []string
	for _, f := range reflect.VisibleFields(t) {
		if f.IsExported() {
			vs = append(vs, f.Name)
		}
	}
	for _, t := range []reflect.Type{t, reflect.PointerTo(t)} {
		for m := range t.Methods() {
			vs = append(vs, m.Name)
		}
	}
	slices.Sort(vs)

	return slices.Compact(vs)
}

// TestALayerAnswersToNothingTheGeneratorDoesNotKnow holds the generator's
// knowledge of each layer's struct to the struct: a name it does not know is
// an RPC of that name the generated type around a service is not forwarded,
// and the app that declares one does not compile. A method a layer gains next
// month fails here first rather than in that app.
func TestALayerAnswersToNothingTheGeneratorDoesNotKnow(t *testing.T) {
	layers := map[string]reflect.Type{
		"Audit":     reflect.TypeFor[pd.Audit](),
		"Gate":      reflect.TypeFor[pd.Gate](),
		"Intercept": reflect.TypeFor[pd.Intercept](),
		"Secret":    reflect.TypeFor[pd.Secret](),
	}

	// Every layer the generator says it embeds is one this test asks about.
	require.Equal(t, shadow.Layers(), slices.Sorted(maps.Keys(layers)))

	var entities []string
	for m := range reflect.TypeFor[app.Server]().Methods() {
		entities = append(entities, m.Name)
	}

	for name, typ := range layers {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, shadow.Names(name, entities), exported(typ))
		})
	}
}

// TestTheBareServerAnswersToNothingTheGeneratorDoesNotKnow is the same of the
// ORM's server, whose fields the generator cannot forward around and so
// refuses an RPC by: every field of `bare.Store` is one it knows, and the
// store has no exported method, which would be a name it knows nothing of.
func TestTheBareServerAnswersToNothingTheGeneratorDoesNotKnow(t *testing.T) {
	store := reflect.TypeFor[bare.Store]()

	var fields []string
	for _, f := range reflect.VisibleFields(store) {
		if f.IsExported() {
			fields = append(fields, f.Name)
		}
	}
	slices.Sort(fields)
	require.Equal(t, shadow.BareFields, fields)
	require.Equal(t, 0, reflect.PointerTo(store).NumMethod())

	// And beside it, in a server, the unimplemented service and nothing else.
	var embedded []string
	for f := range reflect.TypeFor[bare.RobotServiceServer]().Fields() {
		embedded = append(embedded, f.Name)
	}
	require.Equal(t, []string{"Store", "UnimplementedRobotServiceServer"}, embedded)
}

// stream is each of the three stream shapes in one, in process: it is sent
// `in`, and keeps what it is answered.
type stream[In, Out any] struct {
	grpc.ServerStream
	ctx context.Context

	in  []*In
	out []*Out
}

func (s *stream[In, Out]) Context() context.Context     { return s.ctx }
func (s *stream[In, Out]) SetHeader(metadata.MD) error  { return nil }
func (s *stream[In, Out]) SendHeader(metadata.MD) error { return nil }
func (s *stream[In, Out]) SetTrailer(metadata.MD)       {}
func (s *stream[In, Out]) Send(v *Out) error            { s.out = append(s.out, v); return nil }
func (s *stream[In, Out]) SendAndClose(v *Out) error    { return s.Send(v) }
func (s *stream[In, Out]) SendMsg(v any) error          { return s.Send(v.(*Out)) }
func (s *stream[In, Out]) RecvMsg(v any) error          { return errors.ErrUnsupported }
func (s *stream[In, Out]) Recv() (*In, error) {
	if len(s.in) == 0 {
		return nil, io.EOF
	}
	v := s.in[0]
	s.in = s.in[1:]

	return v, nil
}

// namesakes calls each of the four on `robots` with `ref`, twice for a
// stream that is sent several, and answers with the identifiers of the robots
// each answered, or its error.
type namesakes func(t *testing.T, ctx context.Context, ref *app.RobotRef) map[string]result

type result struct {
	ids  [][]byte
	code codes.Code
}

func answered(err error, vs ...*app.Robot) result {
	r := result{code: status.Code(err)}
	for _, v := range vs {
		if v != nil {
			r.ids = append(r.ids, v.GetId())
		}
	}

	return r
}

// inProcess is the four called on the server this app builds, in process.
func inProcess(robots app.RobotServiceServer) namesakes {
	return func(t *testing.T, ctx context.Context, ref *app.RobotRef) map[string]result {
		out := map[string]result{}

		v, err := robots.Seal(ctx, ref)
		out["Seal"] = answered(err, v)

		r := &stream[app.RobotRef, app.Robot]{ctx: ctx}
		err = robots.Reading(ref, r)
		out["Reading"] = answered(err, r.out...)

		j := &stream[app.RobotRef, app.RobotJobResponse]{ctx: ctx, in: []*app.RobotRef{ref, ref}}
		err = robots.Job(j)
		var items []*app.Robot
		for _, v := range j.out {
			items = append(items, v.GetItems()...)
		}
		out["Job"] = answered(err, items...)

		b := &stream[app.RobotRef, app.Robot]{ctx: ctx, in: []*app.RobotRef{ref, ref}}
		err = robots.Joint(b)
		out["Joint"] = answered(err, b.out...)

		return out
	}
}

// overTheWire is the four called over a connection to it.
func overTheWire(robots app.RobotServiceClient) namesakes {
	return func(t *testing.T, ctx context.Context, ref *app.RobotRef) map[string]result {
		x := require.New(t)
		out := map[string]result{}

		v, err := robots.Seal(ctx, ref)
		out["Seal"] = answered(err, v)

		out["Reading"] = func() result {
			s, err := robots.Reading(ctx, ref)
			x.NoError(err)

			var vs []*app.Robot
			for {
				v, err := s.Recv()
				if errors.Is(err, io.EOF) {
					return answered(nil, vs...)
				}
				if err != nil {
					return answered(err)
				}
				vs = append(vs, v)
			}
		}()

		out["Job"] = func() result {
			s, err := robots.Job(ctx)
			x.NoError(err)
			for range 2 {
				if err := s.Send(ref); err != nil {
					break
				}
			}

			v, err := s.CloseAndRecv()
			return answered(err, v.GetItems()...)
		}()

		out["Joint"] = func() result {
			s, err := robots.Joint(ctx)
			x.NoError(err)

			var vs []*app.Robot
			for range 2 {
				if err := s.Send(ref); err != nil {
					break
				}
				v, err := s.Recv()
				if err != nil {
					return answered(err)
				}
				vs = append(vs, v)
			}
			x.NoError(s.CloseSend())
			if _, err := s.Recv(); !errors.Is(err, io.EOF) {
				return answered(err)
			}

			return answered(nil, vs...)
		}()

		return out
	}
}

// served is what the four answer when each is let through: the robot, once
// for each time it was named.
func served(id []byte) map[string]result {
	return map[string]result{
		"Seal":    {ids: [][]byte{id}},
		"Reading": {ids: [][]byte{id}},
		"Job":     {ids: [][]byte{id, id}},
		"Joint":   {ids: [][]byte{id, id}},
	}
}

func refused(code codes.Code) map[string]result {
	return map[string]result{
		"Seal":    {code: code},
		"Reading": {code: code},
		"Job":     {code: code},
		"Joint":   {code: code},
	}
}

// TestAnRpcCalledWhatAnEntityIsCalledIsServedAndRefusedAsAnyOther: in
// process, the gate's rule for each is the one it declared -- `direct`, so a
// call somebody makes on the actor's behalf is refused and the actor's own is
// served -- and over a connection, what a credential allows is asked of it by
// its own name, refused by one granted something else and served by one
// granted it.
func TestAnRpcCalledWhatAnEntityIsCalledIsServedAndRefusedAsAnyOther(t *testing.T) {
	b, ctx := build(t)

	// Installed before the server is built, since that is what reads it: Plain,
	// narrowed to the methods the call names in `apptest-grant`.
	plain := auth.Plain()
	b.Auth = auth.HandlerFunc(func(ctx context.Context) (auth.Identity, error) {
		id, err := plain.Handle(ctx)
		if err != nil {
			return id, err
		}

		md, _ := metadata.FromIncomingContext(ctx)
		if vs := md.Get("apptest-grant"); len(vs) > 0 {
			id.Grant = frame.Whole().To(vs...)
		}

		return id, nil
	})

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	require.NoError(t, err)
	ref := app.RobotRef_builder{Id: v.GetId()}.Build()

	t.Run("in process", func(t *testing.T) {
		call := inProcess(b.Walled.Robot())

		require.Equal(t, served(v.GetId()), call(t, b.as(ctx), ref))

		f := frame.New(b.Holder, b.Tenant, frame.Whole()).WithScope(frame.Only(b.Tenant))
		f.Caller = &frame.Principal{Actor: pdid.New(pd.HolderDomain), Tenant: b.Tenant}
		require.Equal(t, refused(codes.PermissionDenied), call(t, frame.Into(ctx, f), ref))
	})

	t.Run("over the wire", func(t *testing.T) {
		call := overTheWire(app.NewClient(b.dialed(t, ctx)).Robot())
		as := b.travels(ctx)

		require.Equal(t, served(v.GetId()), call(t, as, ref))

		methods := []string{
			app.RobotService_Seal_FullMethodName,
			app.RobotService_Reading_FullMethodName,
			app.RobotService_Job_FullMethodName,
			app.RobotService_Joint_FullMethodName,
		}
		granted := metadata.AppendToOutgoingContext(as, "apptest-grant", app.RobotService_Get_FullMethodName)
		require.Equal(t, refused(codes.PermissionDenied), call(t, granted, ref))

		granted = as
		for _, m := range methods {
			granted = metadata.AppendToOutgoingContext(granted, "apptest-grant", m)
		}
		require.Equal(t, served(v.GetId()), call(t, granted, ref))
	})
}
