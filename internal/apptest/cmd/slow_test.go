package cmd_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/lesomnus/payday/internal/apptest/server/core"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/lesomnus/payday/pdid"
)

// TestTheSlowLogNamesTheCallThatWasSlow.
//
// [cmd.Slow] is the app's worked example of an interceptor between two layers,
// and it is wired into the stack `Build` assembles. What is asserted here is
// the thing that made it worth putting there rather than on the wire: one
// `Add` is two calls at this seam, and each is logged under the method it
// actually is.
func TestTheSlowLogNamesTheCallThatWasSlow(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Everything is slow at zero, so what is left to observe is which calls
	// crossed and what they were called.
	said := &bytes.Buffer{}
	ctx = log.Into(ctx, slog.New(slog.NewJsonHandler(said, nil)))

	s := b.stack(t,
		core.Build(),
		pd.AuditBuild(),
		pd.InterceptBuild([]grpc.UnaryServerInterceptor{cmd.Slow(0)}, nil),
		pd.GateBuild())

	_, err := s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	var saw []string
	for _, line := range strings.Split(strings.TrimSpace(said.String()), "\n") {
		var v struct {
			Msg    string `json:"msg"`
			Method string `json:"rpc.method"`
		}
		x.NoError(json.Unmarshal([]byte(line), &v))
		x.Equal("slow", v.Msg)
		saw = append(saw, v.Method)
	}

	// The write, and the read the gate does above it -- which the wire cannot
	// tell apart, because there it is one `Add`.
	x.Contains(saw, app.RobotService_Add_FullMethodName)
	x.Contains(saw, app.TenantService_Get_FullMethodName)
}

// TestASlowLogSaysNothingAboutACallThatWasNot, so the example is a diagnostic
// rather than a line per call.
func TestASlowLogSaysNothingAboutACallThatWasNot(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	said := &bytes.Buffer{}
	ctx = log.Into(ctx, slog.New(slog.NewJsonHandler(said, nil)))

	s := b.stack(t,
		core.Build(),
		pd.AuditBuild(),
		pd.InterceptBuild([]grpc.UnaryServerInterceptor{cmd.Slow(time.Hour)}, nil),
		pd.GateBuild())

	_, err := s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	x.Empty(said.String())
}

// And what it wraps is answered unchanged: a diagnostic that ate a refusal
// would be a diagnostic that caused one.
func TestASlowLogChangesNothingAboutTheAnswer(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	s := b.stack(t,
		core.Build(),
		pd.InterceptBuild([]grpc.UnaryServerInterceptor{cmd.Slow(0)}, nil),
		pd.GateBuild())

	// A tenant this caller cannot see, which the gate refuses.
	_, err := s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: pdid.New(pd.TenantDomain).Bytes()}.Build(),
	}.Build())
	x.Error(err)

	v, err := s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.NotEmpty(v.GetId())
}

// TestTheServedStackHasTheInterceptorInIt.
//
// The tests above build their own stack, so every one of them would go on
// passing with the wiring taken out of `Build` -- and an example nothing runs
// is the thing this was written to stop being. So this walks the stack the app
// actually serves and looks for the layer.
func TestTheServedStackHasTheInterceptorInIt(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	found := false
	for s := b.Walled; s != nil; {
		if _, ok := s.(pd.Intercept); ok {
			found = true
			break
		}

		n, ok := s.(interface{ Next() app.Server })
		if !ok {
			break
		}

		s = n.Next()
	}

	x.True(found, "the served stack has no interceptor layer, so `Slow` runs nowhere")
}
