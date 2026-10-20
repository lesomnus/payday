package cmd_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/lesomnus/z"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/config"
	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/lesomnus/payday/pdid"
)

// A digest of the row each write left (payday#102): every row of the trail
// keeps one, under the deployment's key, and a check of the live rows finds
// the one somebody changed in the database past the servers.

// digestKey is a key as configuration gives one through the environment.
var digestKey = "base64:" + base64.StdEncoding.EncodeToString([]byte("a key of thirty-two bytes, or so"))

// digesting is the app with a digest key, and that key as a check reads it.
func digesting(t *testing.T) (*built, context.Context, *audit.Digest) {
	t.Helper()

	a := config.AuditConfig{Digest: config.AuditDigestConfig{Key: digestKey}}
	b, ctx := buildFrom(t, cmd.Config{
		Db:    dbOf(t),
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Audit: a,
	})

	cs, err := a.Contents()
	require.NoError(t, err)
	require.NotNil(t, cs.Digest)

	return b, ctx, cs.Digest
}

// TestEachRowOfTheTrailKeepsADigestOfTheRowItsWriteLeft: a byte of version, the
// key's fingerprint and the HMAC, over every field but the ones the robot
// declares secret and personal -- and a write that changed the row leaves
// another.
func TestEachRowOfTheTrailKeepsADigestOfTheRowItsWriteLeft(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	_, rows := b.kept(t, ctx)
	for _, row := range rows {
		x.Len(row.Digest, 1+8+32)
		x.Equal(audit.DigestVersion, row.Digest[0])
		x.Contains(row.DigestFields, uint32(4), "the alias is the robot's")
		x.NotContains(row.DigestFields, uint32(8), "the secret is nobody's to read")
		x.NotContains(row.DigestFields, uint32(32), "the keeper is a person's")
	}
	x.NotEqual(rows[0].Digest, rows[1].Digest, "the Patch left another row")

	got, err := pd.Digests(ctx, b.Ent, d)
	x.NoError(err)
	x.True(got.Ok(), "%v", got.Differs)
	x.Positive(got.Matches)
	x.Zero(got.KeyNotHeld)
}

// TestARowChangedPastTheServersDiffers: the alias written in the database
// itself is a row that is not what the last write the trail recorded left, and
// written again through a server it is.
func TestARowChangedPastTheServersDiffers(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, _ := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)
	x.NoError(b.Ent.Robot.UpdateOneId(k).SetAlias("arm-forged").Exec(ctx))

	got, err := pd.Digests(ctx, b.Ent, d)
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs)

	_, err = b.Ungated.Robot().Patch(ctx, app.RobotPatchRequest_builder{
		Ref:              app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias:            z.Ptr("arm-03"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	got, err = pd.Digests(ctx, b.Ent, d)
	x.NoError(err)
	x.True(got.Ok(), "a write the trail recorded is the row again: %v", got.Differs)
}

// TestAPersonalFieldIsOutsideTheDigest: the keeper changed in the database is
// not found, and that is the trade. A keyed hash of somebody's address would
// outlive their erasure for as long as the key does.
func TestAPersonalFieldIsOutsideTheDigest(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, _ := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)
	x.NoError(b.Ent.Robot.UpdateOneId(k).SetKeeper("somebody-else@example.com").Exec(ctx))

	got, err := pd.Digests(ctx, b.Ent, d)
	x.NoError(err)
	x.True(got.Ok())
}

// TestRowsMadeManyAtATimeAreDigestedToo: an AddMany's rows of the trail are
// kept for the commit and written together, and each keeps its digest.
func TestRowsMadeManyAtATimeAreDigestedToo(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	res, err := b.Ungated.Robot().AddMany(ctx, robots(b.Tenant, "arm-11", "arm-12", "arm-13"))
	x.NoError(err)
	for _, v := range res.GetItems() {
		rows := b.trailOf(t, ctx, v.GetId())
		x.Len(rows, 1)
		x.Len(rows[0].Digest, 1+8+32)
	}

	got, err := pd.Digests(ctx, b.Ent, d)
	x.NoError(err)
	x.True(got.Ok(), "%v", got.Differs)
}

// TestATrailWithNoKeyKeepsNoDigest: what a trail was before, and a check that
// is given no key is refused rather than finding nothing.
func TestATrailWithNoKeyKeepsNoDigest(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, rows := b.kept(t, ctx)
	for _, row := range rows {
		x.Empty(row.Digest)
		x.Empty(row.DigestFields)
	}

	_, err := pd.Digests(ctx, b.Ent, nil)
	x.Error(err)
}
