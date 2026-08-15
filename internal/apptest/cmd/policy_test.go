package cmd_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
)

// console is a [gate.Policy], which payday has none of and cannot have: it is
// the seam for what payday does not decide, so every implementation there will
// ever be belongs to an application.
//
// This one is the shape `docs/TENANCY.md` describes -- a second binary, on an
// address only an operator can reach, serving the same app with a different
// answer to the same two questions. It reads every tenant and writes to none.
//
// Read-only is not incidental. A policy that answered `frame.Everything` and
// allowed writes would be a deployment where one mistake in an operator's
// console reaches every customer's rows, and there is no wall behind it to stop
// that -- `Where` is what the wall narrows by.
type console struct{}

func (console) May(_ context.Context, c gate.Call) error {
	// By suffix, which is enough for an app whose services are all generated
	// and is not a rule payday states: what a method does is the app's to know.
	switch {
	case strings.HasSuffix(c.Action, "/Get"),
		strings.HasSuffix(c.Action, "/List"),
		strings.HasSuffix(c.Action, "/Watch"):
		return nil
	}

	return status.Errorf(codes.PermissionDenied,
		"%s: this console reads and does not write", c.Action)
}

func (console) Where(context.Context, gate.Call) (frame.Tenants, error) {
	return frame.Everything, nil
}

// TestAPolicyIsWhatASecondBinaryIsBuiltFrom.
//
// `Server.Policy` is a field rather than a configuration setting for the reason
// `Server.Auth` is: what a caller may see is not a line in a YAML file that a
// mistake can widen. A deployment that wants an operator's path runs this app
// again with a different policy on a different address.
//
// Both halves are asserted against the same rows, through a real connection,
// because that is where the interceptor is -- calling `gate.Decide` by hand
// would test the function and not the wiring.
func TestAPolicyIsWhatASecondBinaryIsBuiltFrom(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Somebody else's, which the caller below is not in and cannot see.
	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)
	_, err = b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
		Alias:  "theirs",
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "ours",
	}.Build())
	x.NoError(err)

	// The customer-facing binary first, so that what the policy changes is read
	// against what it is like without one rather than asserted on its own.
	as := b.travels(ctx)

	c := app.NewRobotServiceClient(pdtest.Serve(t, b.grpc(t)))
	vs, err := c.List(as, app.RobotListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1, "without a policy a caller sees their own tenant")

	// And the operator's, which is the same app, the same database and the same
	// caller.
	b.Policy = console{}

	c = app.NewRobotServiceClient(pdtest.Serve(t, b.grpc(t)))
	vs, err = c.List(as, app.RobotListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 2, "`Where` is what the wall narrows by")

	// The other half of the same policy. A console that could write would be
	// one mistake away from every customer's rows, since there is no wall
	// behind it: `Where` said there was nothing to narrow.
	_, err = c.Add(as, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "written-by-the-console",
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.Contains(err.Error(), "reads and does not write")
}
