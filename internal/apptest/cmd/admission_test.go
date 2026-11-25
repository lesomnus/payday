package cmd_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/admission"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/frame"
	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func admitted(t *testing.T, b *built, policy admission.Policy, o admission.Options) app.Server {
	t.Helper()
	if len(o.Entities) == 0 {
		o.Entities = []string{"app.Robot"}
	}
	sink, err := pd.NewSink(b.Ent, bare.WithMinter(pd.Minter()), bare.WithRecorder(pd.Recorder(audit.Contents{})), bare.WithScope(pd.Wall()), pd.Admission(policy, o))
	require.NoError(t, err)
	stack, err := app.Build(sink, pd.AuditBuild(), pd.SecretBuild(), pd.GateBuild())
	require.NoError(t, err)
	return stack
}

func TestAdmissionHasBothPrincipalsAndRedactsEveryImage(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	var calls []admission.Call
	s := admitted(t, b, admission.Func(func(_ context.Context, c admission.Call) error {
		calls = append(calls, c)
		for _, v := range []proto.Message{c.Frame.Row, c.Frame.Caller.Row, c.Previous, c.Value} {
			if v != nil && len(v.(*app.Robot).GetSecret()) != 0 {
				return status.Error(codes.Internal, "secret image leaked")
			}
		}
		switch v := c.Request.(type) {
		case *app.RobotAddRequest:
			if len(v.GetSecret()) != 0 {
				return status.Error(codes.Internal, "secret request leaked")
			}
		case *app.RobotPatchRequest:
			if len(v.GetSecret()) != 0 {
				return status.Error(codes.Internal, "secret patch leaked")
			}
		}
		if old, ok := c.Previous.(*app.Robot); ok && old.GetAlias() == "locked" {
			return status.Error(codes.PermissionDenied, "approved rows are immutable")
		}
		return nil
	}), admission.Options{})
	f, _ := frame.From(b.as(ctx))
	f.Row = app.Robot_builder{Secret: []byte("person-secret")}.Build()
	f.Caller = &frame.Principal{Actor: pdid.New(pd.HolderDomain), Tenant: b.Tenant, Row: app.Robot_builder{Secret: []byte("device-secret")}.Build()}
	f.Presence = "operator-a"
	ctx = frame.Into(ctx, f)
	req := app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "locked", Secret: []byte("row-secret")}.Build()
	robot, err := s.Robot().Add(ctx, req)
	x.NoError(err)
	x.Len(calls, 1)
	x.Equal("app.Robot", calls[0].Entity)
	x.Equal(f.Actor, calls[0].Frame.Actor)
	x.Equal(f.Caller.Actor, calls[0].Frame.Caller.Actor)
	x.Equal("operator-a", calls[0].Frame.Presence)
	x.Nil(calls[0].Previous)
	x.NotNil(calls[0].Value)
	x.NotEmpty(req.GetSecret())
	_, err = s.Robot().Patch(ctx, app.RobotPatchRequest_builder{Ref: robot.Ref(), DateUpdated: robot.GetDateUpdated(), Alias: z.Ptr("edited"), Secret: []byte("new-secret")}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.Len(calls, 2)
	got, err := s.Robot().Get(ctx, robot.Ref().Pick())
	x.NoError(err)
	x.Equal("locked", got.GetAlias())
	id, err := pdid.From(robot.GetId())
	x.NoError(err)
	x.Equal(1, b.onTheTrail(t, ctx, []pdid.Id{id}), "the refused change has no audit row")
	raw, err := b.Ent.Robot.Get(ctx, uuid.UUID(robot.GetId()))
	x.NoError(err)
	x.Equal([]byte("row-secret"), raw.Secret)
}

func TestAdmissionCannotExpandGateOrWall(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	robot := b.robot(t, ctx, b.Tenant, "private")
	calls := 0
	s := admitted(t, b, admission.Func(func(context.Context, admission.Call) error { calls++; return nil }), admission.Options{})
	stranger := frame.New(pdid.New(pd.HolderDomain), pdid.New(pd.TenantDomain), frame.Whole())
	stranger.Scope = frame.Only(stranger.Tenant)
	_, err := s.Robot().Erase(frame.Into(ctx, stranger), app.RobotRef_builder{Id: robot}.Build())
	x.NoError(err)
	x.Zero(calls)
	// A stamped input is rejected at the generated gate, before admission.
	_, err = s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "stamp", DateAttested: timestamppb.Now()}.Build())
	x.Error(err)
	x.Zero(calls)
	_, err = s.Robot().Get(b.as(ctx), app.RobotRef_builder{Id: robot}.Build().Pick())
	x.NoError(err)
}

func TestAdmissionOuterRequestAndJoinedRefusal(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	calls := 0
	s := admitted(t, b, admission.Func(func(_ context.Context, c admission.Call) error {
		calls++
		if c.Method != "/app.JobService/Complete" || c.By != app.RobotService_Add_FullMethodName || c.Request.(*wrapperspb.StringValue).GetValue() != "complete" {
			return status.Error(codes.Internal, "wrong request")
		}
		return status.Error(codes.PermissionDenied, "station is not qualified")
	}), admission.Options{})
	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	x.NoError(err)
	defer tx.Rollback()
	sv, err := enttx.Rebind(s, drv)
	x.NoError(err)
	_, err = pd.AdmissionUnary(b.as(ctx), wrapperspb.String("complete"), &grpc.UnaryServerInfo{FullMethod: "/app.JobService/Complete"}, func(ctx context.Context, _ any) (any, error) {
		return sv.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "operation"}.Build())
	})
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.Equal(1, calls)
	x.Error(tx.Commit())
	x.Zero(b.Ent.Robot.Query().CountX(ctx))
}

func TestAdmissionTimeoutRollsBackAndDirectEntCannotBypass(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	s := admitted(t, b, admission.Func(func(ctx context.Context, _ admission.Call) error { <-ctx.Done(); return nil }), admission.Options{Timeout: 20 * time.Millisecond})
	_, err := s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "timeout"}.Build())
	x.Equal(codes.DeadlineExceeded, status.Code(err))
	x.Zero(b.Ent.Robot.Query().CountX(ctx))
	_, err = b.Ent.Robot.Create().SetId(pdid.New(pd.RobotDomain).Uuid()).SetTenantId(b.Tenant.Uuid()).SetAlias("bypass").SetSecret([]byte{}).SetDateUpdated(time.Now()).Save(ctx)
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.Zero(b.Ent.Robot.Query().CountX(ctx))
}

func TestAdmissionRejectsUnknownAndInternalEntitySelection(t *testing.T) {
	b, _ := build(t)
	for _, name := range []string{"app.Missing", "app.Audit", "app.Outbox"} {
		_, err := pd.NewSink(b.Ent, pd.Admission(admission.Func(func(context.Context, admission.Call) error { return nil }), admission.Options{Entities: []string{name}}))
		require.Error(t, err)
	}
}

// TestACallAdmissionIsNotAskedAboutIsAdmittedAsOneLetThrough: a call of a
// method `admission.Options.Asked` answers false for is admitted without the
// policy, and the generated server does with it what it does with one the
// policy let through. Its write is counted off against the guard on direct
// ent writes, so its transaction commits; and a transaction it joins that
// another write already had refused still does not. A write with no outer
// method captured -- no AdmissionUnary, no bare.Origin -- is asked about by
// the method the policy would have been given: the RPC that wrote.
func TestACallAdmissionIsNotAskedAboutIsAdmittedAsOneLetThrough(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	asked := []string{}
	called := 0
	s := admitted(t, b, admission.Func(func(context.Context, admission.Call) error {
		called++
		return status.Error(codes.PermissionDenied, "not this one")
	}), admission.Options{Asked: func(m string) bool {
		asked = append(asked, m)
		return m != app.RobotService_Add_FullMethodName
	}})
	add := func(ctx context.Context, s app.Server, alias string) error {
		_, err := s.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: alias}.Build())
		return err
	}

	x.NoError(add(b.as(ctx), s, "not-asked"))
	x.Equal([]string{app.RobotService_Add_FullMethodName}, asked, "asked by the RPC that wrote")
	x.Zero(called)
	x.Equal(1, b.Ent.Robot.Query().CountX(ctx), "the write committed past the guard")

	// Asked about the outer method, where one was captured.
	_, err := pd.AdmissionUnary(b.as(ctx), wrapperspb.String("x"), &grpc.UnaryServerInfo{FullMethod: "/app.JobService/Complete"}, func(ctx context.Context, _ any) (any, error) {
		return nil, add(ctx, s, "asked")
	})
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.Equal(1, called)

	// And a transaction already refused stays refused, whatever joins it.
	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	x.NoError(err)
	defer tx.Rollback()
	sv, err := enttx.Rebind(s, drv)
	x.NoError(err)
	_, err = pd.AdmissionUnary(b.as(ctx), wrapperspb.String("x"), &grpc.UnaryServerInfo{FullMethod: "/app.JobService/Complete"}, func(ctx context.Context, _ any) (any, error) {
		return nil, add(ctx, sv, "refused")
	})
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.NoError(add(b.as(ctx), sv, "joined"))
	x.Error(tx.Commit())
	x.Equal(1, b.Ent.Robot.Query().CountX(ctx))
}
