package cmd_test

import (
	"testing"

	"github.com/google/uuid"
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

// TestASecretIsNotInTheTrail, which is the path the option did not cover.
//
// The layer that clears these is in front of the sink; the recorder is behind
// it, reading the bare server on purpose so that a row is recorded as it was
// written rather than as somebody was allowed to see it. Right for every column
// but this one: a verifier in `value` is a second copy of it, in the one table
// nothing erases, readable by anybody who may read the trail.
//
// Found by writing it against roster, where an argon2id hash sat in the trail
// of a deployment whose `CredentialService` is unregistered and closed
// precisely so that it could not be read.
func TestASecretIsNotInTheTrail(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	secret := []byte("hunter2-and-something-long-enough-to-find")

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-02",
		Secret: secret,
	}.Build())
	x.NoError(err)

	// It really was stored -- otherwise this passes because nothing was written.
	k, err := uuid.FromBytes(v.GetId())
	x.NoError(err)

	row, err := b.Ent.Robot.Get(ctx, k)
	x.NoError(err)
	x.Equal(secret, row.Secret, "the secret was not stored at all")

	vs, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	x.NotEmpty(vs, "nothing was recorded, so this proves nothing")

	found := false
	for _, w := range vs {
		x.NotContains(string(w.Value), string(secret), "the trail holds the verifier")
		if len(w.Value) > 0 {
			found = true
		}
	}
	x.True(found, "no row carried a value, so the check above never looked at one")
}
