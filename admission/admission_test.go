package admission_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lesomnus/payday/admission"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/protobuf-patch/patchpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func identity() context.Context {
	return frame.Into(context.Background(), frame.New(pdid.New(2), pdid.New(1), frame.Whole()))
}
func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestSelectRequiresEveryPatternToMatch(t *testing.T) {
	names := []string{"app.Job", "app.Robot", "other.Job"}
	for _, pattern := range []string{"app.Job", "app.*", "*.Job", "*.*"} {
		v, err := admission.Select([]string{pattern}, names)
		require.NoError(t, err)
		require.NotEmpty(t, v)
	}
	for _, patterns := range [][]string{nil, {"app.Missing"}, {"app.*", "missing.*"}, {"app.J*"}} {
		_, err := admission.Select(patterns, names)
		require.Error(t, err)
	}
}

func TestSecretFieldsAreMappedByNameAndPolicyCannotChangeAuthority(t *testing.T) {
	x := require.New(t)
	field := func(name string, n int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(n), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}
	}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{Name: proto.String("admission_test.proto"), Package: proto.String("test"), Syntax: proto.String("proto3"), MessageType: []*descriptorpb.DescriptorProto{
		{Name: proto.String("Row"), Field: []*descriptorpb.FieldDescriptorProto{field("secret", 2), field("extra", 3), field("name", 4)}},
		{Name: proto.String("Add"), Field: []*descriptorpb.FieldDescriptorProto{field("secret", 7), field("extra", 8), field("name", 9)}},
	}}, nil)
	x.NoError(err)
	row := dynamicpb.NewMessage(fd.Messages().Get(0))
	ask := dynamicpb.NewMessage(fd.Messages().Get(1))
	for _, v := range []proto.Message{row, ask} {
		m := v.ProtoReflect()
		for _, name := range []protoreflect.Name{"secret", "extra", "name"} {
			m.Set(m.Descriptor().Fields().ByName(name), protoreflect.ValueOfString(string(name)))
		}
		m.SetUnknown([]byte{0xa0, 0x06, 0x01})
	}
	f, _ := frame.From(identity())
	f.Row = row
	f.Caller = &frame.Principal{Actor: pdid.New(2), Row: row}
	f.Scope = frame.Only(f.Tenant)
	f.Grant = frame.Whole().In(f.Tenant).To("/app.Job/Patch")
	ctx := metadata.NewIncomingContext(frame.Into(context.Background(), f), metadata.Pairs("authorization", "Bearer secret"))
	var seen admission.Call
	check, err := admission.New(admission.Func(func(ctx context.Context, c admission.Call) error {
		seen = c
		if md, ok := metadata.FromIncomingContext(ctx); ok && len(md) > 0 {
			return errors.New("credentials leaked")
		}
		for _, v := range []proto.Message{c.Frame.Row, c.Frame.Caller.Row, c.Request, c.Previous, c.Value} {
			m := v.ProtoReflect()
			if m.Has(m.Descriptor().Fields().ByName("secret")) || m.Has(m.Descriptor().Fields().ByName("extra")) || len(m.GetUnknown()) != 0 {
				return errors.New("secret leaked")
			}
		}
		c.Frame.Scope.Ids()[0] = pdid.Nil
		c.Frame.Grant.TenantIds()[0] = pdid.Nil
		c.Frame.Grant.Actions()[0] = "*"
		c.Request.ProtoReflect().Set(c.Request.ProtoReflect().Descriptor().Fields().ByName("name"), protoreflect.ValueOfString("changed"))
		return nil
	}), admission.Options{Log: quiet(), Secrets: map[protoreflect.FullName][]protoreflect.FieldNumber{"test.Row": {3}}}, []admission.Entity{{Row: row, Requests: []proto.Message{ask}}}, map[protoreflect.FullName][]protoreflect.FieldNumber{"test.Row": {2}})
	x.NoError(err)
	x.NoError(check.Check(ctx, admission.Call{Request: ask, Previous: row, Value: row}))
	x.NotNil(seen.Frame.Caller)
	x.Equal(f.Tenant, f.Scope.Ids()[0])
	x.Equal(f.Tenant, f.Grant.TenantIds()[0])
	x.Equal("/app.Job/Patch", f.Grant.Actions()[0])
	x.Equal("name", ask.Get(ask.Descriptor().Fields().ByName("name")).String())
	x.NotEmpty(row.GetUnknown())
}

func TestEncodedSecretsAreWithheld(t *testing.T) {
	a, err := anypb.New(wrapperspb.String("secret"))
	require.NoError(t, err)
	p := &patchpb.Patch{}
	p.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
	check, err := admission.New(admission.Func(func(_ context.Context, c admission.Call) error {
		require.Empty(t, c.Request.(*anypb.Any).GetValue())
		require.Empty(t, c.Previous.ProtoReflect().GetUnknown())
		return nil
	}), admission.Options{Log: quiet()}, nil, nil)
	require.NoError(t, err)
	require.NoError(t, check.Check(identity(), admission.Call{Request: a, Previous: p}))
	require.NotEmpty(t, a.GetValue())
}

func TestDeadlineAndBoundedAbandonedPolicies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		release := make(chan struct{})
		defer close(release)
		check, err := admission.New(admission.Func(func(context.Context, admission.Call) error { <-release; return nil }), admission.Options{Timeout: time.Second, Limit: 1, Log: quiet()}, nil, nil)
		x.NoError(err)
		start := time.Now()
		x.Equal(codes.DeadlineExceeded, status.Code(check.Check(identity(), admission.Call{})))
		x.Equal(time.Second, time.Since(start))
		x.Equal(codes.ResourceExhausted, status.Code(check.Check(identity(), admission.Call{})))
	})
}

func TestCallerDeadlineAndFailClosedResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		check, err := admission.New(admission.Func(func(ctx context.Context, _ admission.Call) error { <-ctx.Done(); return nil }), admission.Options{Log: quiet()}, nil, nil)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(identity(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		require.Equal(t, codes.DeadlineExceeded, status.Code(check.Check(ctx, admission.Call{})))
		require.Equal(t, 50*time.Millisecond, time.Since(start))
	})
	for _, kind := range []string{"deny", "error", "panic"} {
		t.Run(kind, func(t *testing.T) {
			var logs bytes.Buffer
			check, err := admission.New(admission.Func(func(context.Context, admission.Call) error {
				switch kind {
				case "deny":
					return status.Error(codes.PermissionDenied, "private-policy-text")
				case "panic":
					panic("private-policy-text")
				default:
					return errors.New("private-policy-text")
				}
			}), admission.Options{Log: slog.New(slog.NewJSONHandler(&logs, nil))}, nil, nil)
			require.NoError(t, err)
			want := codes.Unavailable
			if kind == "deny" {
				want = codes.PermissionDenied
			}
			require.Equal(t, want, status.Code(check.Check(identity(), admission.Call{Entity: "app.Job"})))
			require.Contains(t, logs.String(), "admission refused")
			require.Contains(t, logs.String(), "app.Job")
			require.NotContains(t, logs.String(), "private-policy-text")
		})
	}
	check, err := admission.New(admission.Func(func(context.Context, admission.Call) error { t.Error("unframed policy ran"); return nil }), admission.Options{Log: quiet()}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(check.Check(context.Background(), admission.Call{})))
}

// TestACallThePolicyIsNotAskedAboutSpendsNothing: a policy that is a network
// call, asked about some methods and not others, could hold every slot while
// it hung, and a write it was never asked about was refused for want of one
// (lesomnus/roster#305). With `Asked`, a call of a method it answers false for
// is admitted at once: no slot, no copy, no policy. Without it, the same call
// waits its turn as before, and is refused when there is none.
func TestACallThePolicyIsNotAskedAboutSpendsNothing(t *testing.T) {
	const asked, other = "/app.Job/Patch", "/app.Cell/Add"

	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		release := make(chan struct{})
		seen := make(chan string, 4)
		policy := admission.Func(func(_ context.Context, c admission.Call) error {
			seen <- c.Method
			<-release
			return nil
		})

		for _, tt := range []struct {
			name  string
			asked func(string) bool
			want  codes.Code
		}{
			{"asked about one method", func(m string) bool { return m == asked }, codes.OK},
			{"asked about every call", nil, codes.ResourceExhausted},
		} {
			check, err := admission.New(policy, admission.Options{Timeout: time.Minute, Limit: 1, Log: quiet(), Asked: tt.asked}, nil, nil)
			x.NoError(err)

			held := make(chan error, 1)
			go func() { held <- check.Check(identity(), admission.Call{Method: asked}) }()
			synctest.Wait()
			x.Equal(asked, <-seen, tt.name)

			x.Equal(tt.want, status.Code(check.Check(identity(), admission.Call{Method: other})), tt.name)
			synctest.Wait()
			x.Empty(seen, "%s: the policy was asked about a call it is not asked about", tt.name)

			release <- struct{}{}
			x.NoError(<-held)
		}
		close(release)
	})

	// Asked is asked first, with the method alone: a call it is not asked
	// about is admitted even with no frame to check it by.
	check, err := admission.New(admission.Func(func(context.Context, admission.Call) error { t.Error("the policy ran"); return nil }),
		admission.Options{Log: quiet(), Asked: func(m string) bool { return m == asked }}, nil, nil)
	require.NoError(t, err)
	require.NoError(t, check.Check(context.Background(), admission.Call{Method: other}))
	require.Equal(t, codes.Unauthenticated, status.Code(check.Check(context.Background(), admission.Call{Method: asked})))
}
