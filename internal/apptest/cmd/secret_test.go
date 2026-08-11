package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/payday/internal/apptest"
)

// A field declared `(payday.field).secret` does not come back.
//
// Until the option existed there was nowhere in a schema to say that a
// verifier -- a password hash, an API key hash -- is written and never
// answered with. Apps said it at registration instead, by leaving a whole
// generated service off their server: a heavier hammer, said in the app that
// happens to hold the field rather than beside the field.

// TestASecretIsNotAnsweredWith, on every path that answers with a row.
func TestASecretIsNotAnsweredWith(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-01",
		Secret: []byte("hunter2"),
	}.Build())
	x.NoError(err)
	x.Empty(v.GetSecret(), "an Add echoed the secret back")

	// Asked for by name, which is the caller trying hardest.
	got, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
		Ref:    v.Ref(),
		Select: app.RobotSelect_builder{All: z.Ptr(true), Secret: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Empty(got.GetSecret(), "a Get answered with the secret")
	x.Equal("arm-01", got.GetAlias(), "the rest of the row came back")

	vs, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{}.Build())
	x.NoError(err)
	x.NotEmpty(vs.GetItems())
	for _, w := range vs.GetItems() {
		x.Empty(w.GetSecret(), "a List item carried the secret")
	}
}

// TestTheWriteHalfStillWorks, which is the half that was never the problem.
//
// The value is in the database and the app can read it there; what the layer
// removes is its arriving on a wire because somebody asked for every column.
func TestTheWriteHalfStillWorks(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-01",
		Secret: []byte("hunter2"),
	}.Build())
	x.NoError(err)

	v, err := b.Ent.Robot.Query().Only(ctx)
	x.NoError(err)
	x.Equal([]byte("hunter2"), v.Secret, "the write did not land")
}
