package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/payday/internal/apptest"
)

// TestAPatchWhoseVersionMovedIsRefused is the compare-and-swap the generated
// `date_updated` field is: the version conflict docs/guide/errors.md says is
// not `pderr`'s to describe, answered with the code and the words a page acts
// on.
//
// The second half is the proof about the first. The refused patch is applied
// again with the version as it is now and lands, so the refusal was the test
// in the patch not holding rather than anything about the patch itself -- the
// same words, a different premise, a different answer.
func TestAPatchWhoseVersionMovedIsRefused(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	// Somebody else's write, which is what moves the version out from under a
	// client still holding the one it read.
	fresh, err := b.Walled.Robot().Patch(b.as(ctx), app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias:       z.Ptr("arm-02"),
		DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.NoError(err)

	// The row exists and the patch is well-formed; only its premise is stale.
	// FailedPrecondition and not NotFound, and not InvalidArgument -- a caller
	// acting on the code re-reads and shows the difference rather than drawing
	// a red line under a box.
	_, err = b.Walled.Robot().Patch(b.as(ctx), app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias:       z.Ptr("arm-03"),
		DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.Contains(err.Error(), "a test in the patch did not hold")

	// Refused means refused: the write it carried did not land.
	got, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{Id: v.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("arm-02", got.GetAlias())

	// The same patch, holding the version as it is now.
	got, err = b.Walled.Robot().Patch(b.as(ctx), app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias:       z.Ptr("arm-03"),
		DateUpdated: fresh.GetDateUpdated(),
	}.Build())
	x.NoError(err)
	x.Equal("arm-03", got.GetAlias())
}
