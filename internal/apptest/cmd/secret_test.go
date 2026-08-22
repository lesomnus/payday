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
// Found by writing it against an identity store, where an argon2id hash sat in
// the trail of a deployment whose `CredentialService` is unregistered and
// closed precisely so that it could not be read.
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

// TestASecretIsNotStreamed is the read that cannot be narrowed by asking.
//
// A `WatchRequest` has no `select`, so an item carries the whole message and a
// caller has nothing to leave a column out with. Until the `Secret` layer wrote
// a `Watch` wrapper there was none, and a watchable entity that declared a
// secret **streamed it** -- to anybody the wall let read the row, on the one
// read where nobody had to ask.
//
// Both messages are checked, because they are produced by different code: the
// first is the snapshot the stream opens with, and the second is a write
// arriving. A wrapper on one and not the other would pass half this test.
func TestASecretIsNotStreamed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-01",
		Secret: []byte("hunter2"),
	}.Build())
	x.NoError(err)

	c0, c, stop := b.watching(t, app.RobotWatchRequest_builder{
		Filters: []*app.RobotFilter{
			app.RobotFilter_builder{Ref: app.RobotRef_builder{Id: v.GetId()}.Build()}.Build(),
		},
	}.Build())
	defer stop()

	res := next(t, c)
	x.Len(res.GetItems(), 1)
	x.Equal("arm-01", res.GetItems()[0].GetValue().GetAlias(), "the rest of the row travels")
	x.Empty(res.GetItems()[0].GetValue().GetSecret(), "the snapshot carried the secret")

	_, err = c0.Robot().Patch(b.travels(ctx), app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias:       z.Ptr("renamed"),
		DateUpdated: res.GetItems()[0].GetValue().GetDateUpdated(),
	}.Build())
	x.NoError(err)

	res = next(t, c)
	x.Len(res.GetItems(), 1)
	x.Equal("renamed", res.GetItems()[0].GetValue().GetAlias())
	x.Empty(res.GetItems()[0].GetValue().GetSecret(), "a write arriving carried the secret")

	// And it is still in the database, which is the half that was never the
	// problem.
	row, err := b.Ent.Robot.Query().Only(ctx)
	x.NoError(err)
	x.Equal([]byte("hunter2"), row.Secret)
}
