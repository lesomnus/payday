package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid"
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

// TestASecretThatIsNotBytesIsNotAnsweredWith, which is a test by compiling
// before it is one by asserting.
//
// `hideSeal` cleared each secret with its setter, and the argument was `nil`.
// That is a `[]byte`, so it held for exactly as long as every secret anybody
// had declared was a digest -- and `Seal.code` is the first that is not. `pd
// gen` succeeded; the build ended inside a generated file. See the comment on
// `Seal`, which is the other bug of this shape.
//
// What clears them now is `Clear` by field number, so the assertions below are
// about the annotation rather than about a type: a `string` secret does not
// come back, on the same paths a `bytes` one does not.
func TestASecretThatIsNotBytesIsNotAnsweredWith(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	code := "884-113"

	v, err := b.Walled.Seal().Add(b.as(ctx), app.SealAddRequest_builder{
		Alias:  "seal-01",
		Secret: []byte("hunter2"),
		Code:   code,
	}.Build())
	x.NoError(err)
	x.Empty(v.GetCode(), "an Add echoed the code back")
	x.Empty(v.GetSecret(), "an Add echoed the secret back")

	// Asked for by name, which is the caller trying hardest.
	got, err := b.Walled.Seal().Get(b.as(ctx), app.SealGetRequest_builder{
		Ref:    app.SealRef_builder{Id: v.GetId()}.Build(),
		Select: app.SealSelect_builder{All: z.Ptr(true), Code: z.Ptr(true), Secret: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Empty(got.GetCode(), "a Get answered with the code")
	x.Empty(got.GetSecret(), "a Get answered with the secret")
	x.Equal("seal-01", got.GetAlias(), "the rest of the row came back")

	// And it was stored, which is what says the two above are the layer
	// clearing a value rather than nothing having been written.
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)

	row, err := b.Ent.Seal.Get(ctx, k)
	x.NoError(err)
	x.Equal(code, row.Code, "the code was not stored at all")
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
	// At the content level that keeps the row (#92): what is under test is
	// what the trail holds of one.
	b, ctx := keeping(t, "value")

	secret := []byte("hunter2-and-something-long-enough-to-find")

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-02",
		Secret: secret,
	}.Build())
	x.NoError(err)

	// It really was stored -- otherwise this passes because nothing was written.
	k, err := entuuid.FromBytes(v.GetId())
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

	// And the other column, which is the same write from the other end and was
	// checked by nothing.
	//
	// `Audit.value` is the row as the write left it, and `hide<E>` is what keeps
	// a verifier out of it. `Audit.patch` is the document the write was compiled
	// from, and it carried whatever the request set -- so a `secret:` field
	// written by an RPC that exists to take one in and never hand one back sat
	// in the trail in full. The trail is a **served** entity: `AuditService` is
	// generated like any other, and the wall files a row under the tenant it is
	// about, so anybody there whose role reaches it could read the thing the
	// declaration exists to hide.
	//
	// A patch is asserted separately from a value because a `Patch` produces
	// both and an `Add` produces only the second: the write below is the one
	// that has a document.
	_, err = b.Walled.Robot().Patch(b.as(ctx), app.RobotPatchRequest_builder{
		Ref:              app.RobotRef_builder{Id: v.GetId()}.Build(),
		Secret:           secret,
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	vs, err = b.Ent.Audit.Query().All(ctx)
	x.NoError(err)

	patched := false
	for _, w := range vs {
		if len(w.Patch) == 0 {
			continue
		}

		patched = true
		x.NotContains(string(w.Patch), string(secret),
			"%s: the trail's patch holds the verifier", w.Action)
	}
	x.True(patched, "no row carried a patch, so the check above never looked at one")

	// And what is left of that write is still a record: the entries naming
	// anything else are kept, so dropping the secret one did not empty the
	// document.
	x.NotEmpty(vs, "nothing was recorded")
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

// TestASecretIsNotAnsweredWithByAnRpcOfTheAppsOwn: the layer clears a secret
// from whatever leaves it, not from the generated verbs' answers alone. The
// namesakes of `robot_svc.ext.proto` answer the robot as their handler read
// it, below the layer and so with its secret, one of each shape a method has;
// none of them carries it out, in process through the layer or over a
// connection. Until the layer looked at an RPC of the app's own, `Seal`
// answered the robot with its secret where `Get` answered without.
func TestASecretIsNotAnsweredWithByAnRpcOfTheAppsOwn(t *testing.T) {
	b, ctx := build(t)

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
		Secret: []byte("hunter2"),
	}.Build())
	require.NoError(t, err)
	ref := app.RobotRef_builder{Id: v.GetId()}.Build()

	// It was stored, which is what says the ones below are the layer clearing
	// a value rather than there being none.
	k, err := entuuid.FromBytes(v.GetId())
	require.NoError(t, err)
	row, err := b.Ent.Robot.Get(ctx, k)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), row.Secret)

	t.Run("in process", func(t *testing.T) {
		require.Equal(t, served(v.GetId()), inProcess(b.Walled.Robot())(t, b.as(ctx), ref))
	})
	t.Run("over the wire", func(t *testing.T) {
		call := overTheWire(app.NewClient(b.dialed(t, ctx)).Robot())
		require.Equal(t, served(v.GetId()), call(t, b.travels(ctx), ref))
	})
}

// TestASecretIsNotAnsweredWithThroughAnEdge: an edge is a row, answered in
// full where it was selected, so a Pairing's `lead` is a Robot and the
// secret's way out of a `Get` of the pairing. The layer clears what an
// answer can hold, so the pairing's own verbs clear it, though nothing of the
// pairing's is secret.
func TestASecretIsNotAnsweredWithThroughAnEdge(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	var refs []*app.RobotRef
	for _, alias := range []string{"arm-01", "arm-02"} {
		v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
			Alias:  alias,
			Secret: []byte("hunter2"),
		}.Build())
		x.NoError(err)
		refs = append(refs, v.Ref())
	}

	v, err := b.Walled.Pairing().Add(b.as(ctx), app.PairingAddRequest_builder{
		Lead:   refs[0],
		Follow: refs[1],
	}.Build())
	x.NoError(err)

	every := app.RobotSelect_builder{All: z.Ptr(true), Secret: z.Ptr(true)}.Build()
	got, err := b.Walled.Pairing().Get(b.as(ctx), app.PairingGetRequest_builder{
		Ref:    app.PairingRef_builder{Id: v.GetId()}.Build(),
		Select: app.PairingSelect_builder{All: z.Ptr(true), Lead: every, Follow: every}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("arm-01", got.GetLead().GetAlias(), "the edge was not read, so this proves nothing")
	x.Empty(got.GetLead().GetSecret(), "a Get answered the secret of the row its edge names")
	x.Empty(got.GetFollow().GetSecret(), "a Get answered the secret of the row its edge names")
}

// TestASecretIsNotAnsweredWithFromInsideAnAnswer: `Survey` answers the robot
// two messages deep and in each way a message holds another -- a list, a
// map's values, a oneof's case, a message inside itself -- each a copy read
// below the layer with its secret. None carries it out, in process or over a
// connection, and every copy is there, which is what says the walk reached
// each place rather than finding nothing.
func TestASecretIsNotAnsweredWithFromInsideAnAnswer(t *testing.T) {
	b, ctx := build(t)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-01",
		Secret: []byte("hunter2"),
	}.Build())
	require.NoError(t, err)

	held := func(t *testing.T, v *app.RobotSurvey) {
		x := require.New(t)
		vs := []*app.Robot{
			v.GetCell().GetRobots()[0],
			v.GetCell().GetWithin()[0].GetRobot(),
			v.GetCells()[0].GetRobot(),
			v.GetByAlias()["arm-01"].GetRobot(),
			v.GetFirst().GetRobot(),
		}
		for i, w := range vs {
			x.Equal("arm-01", w.GetAlias(), "copy %d is not there", i)
			x.Empty(w.GetSecret(), "copy %d carried the secret", i)
		}
	}

	t.Run("in process", func(t *testing.T) {
		got, err := b.Walled.Robot().Survey(b.as(ctx), v.Ref())
		require.NoError(t, err)
		held(t, got)
	})
	t.Run("over the wire", func(t *testing.T) {
		got, err := app.NewClient(b.dialed(t, ctx)).Robot().Survey(b.travels(ctx), v.Ref())
		require.NoError(t, err)
		held(t, got)
	})
}
