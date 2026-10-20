package cmd_test

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/lesomnus/flob"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/config"
	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
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

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
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

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs)

	_, err = b.Ungated.Robot().Patch(ctx, app.RobotPatchRequest_builder{
		Ref:              app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias:            z.Ptr("arm-03"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
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

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
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

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
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

	_, err := pd.Digests(ctx, b.Ent, nil, trail.Policy{})
	x.Error(err)
}

// The rest is payday#108: a check that can say "not on the trail" only of a
// row nothing on the trail accounts for -- which is what a row somebody added
// in the database past the servers is.

// TestARowWhoseTrailWasArchivedIsCheckedAgainstTheArchive: the newest row of
// the trail about it has left the database, and the archive's is read. Without
// the archive it is a row the trail says nothing about, and so is every row
// nobody wrote since the window passed it.
func TestARowWhoseTrailWasArchivedIsCheckedAgainstTheArchive(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, _ := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)

	p := trail.Policy{Archive: flob.NewMemStores()}
	n, err := trail.Archive(ctx, pd.TrailStore(b.Ent), p, trail.Kinds{}, time.Now().Add(time.Hour))
	x.NoError(err)
	x.NotZero(n)
	x.Zero(must(b.Ent.Audit.Query().Count(ctx)), "the trail is all in the archive, so this proves nothing")

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Contains(got.NotOnTrail, pdid.Id(k), "with no archive to read, its trail is gone")

	got, err = pd.Digests(ctx, b.Ent, d, p)
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)
	x.Equal(got.Read, got.Archived, "a row was checked against the database's trail, which is empty")
	x.Positive(got.Matches)

	// Changed past the servers since, it is found as it is while its trail
	// is in the database.
	x.NoError(b.Ent.Robot.UpdateOneId(k).SetAlias("arm-forged").Exec(ctx))
	got, err = pd.Digests(ctx, b.Ent, d, p)
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs)
}

// TestASealedArchiveIsCheckedWithNoTenantsKey: what a row of the archive says
// is sealed to its tenant's key, which the process holds only the public half
// of; its digest is beside it in the clear, and a check reads that and nothing
// else.
func TestASealedArchiveIsCheckedWithNoTenantsKey(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)
	b.kept(t, ctx)

	master, err := age.GenerateX25519Identity()
	x.NoError(err)
	keys := &trail.Keys{Store: flob.NewMemStores(), Master: []age.Recipient{master.Recipient()}}
	p := trail.Policy{Archive: flob.NewMemStores(), Keep: trail.Keep{Retain: time.Nanosecond}, Keys: keys}
	x.NoError(p.Valid())

	s := pd.TrailStore(b.Ent)
	p.Pass(ctx, s)
	x.Zero(must(b.Ent.Audit.Query().Count(ctx)), "the window moved nothing")

	err = p.ReadTenant(ctx, s, b.Tenant, pd.TrailOf(func(*app.Audit) error { return nil }))
	x.ErrorIs(err, trail.ErrSealed, "what the rows say is readable without the tenant's key, so this proves nothing")

	got, err := pd.Digests(ctx, b.Ent, d, p)
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)
	x.Equal(got.Read, got.Archived)
	x.Positive(got.Matches)
}

// unrecorded is a robot written through the servers, and then nothing on the
// trail about it: which is, to a check, a row somebody added in the database
// past them.
func (b *built) unrecorded(t *testing.T, ctx context.Context) pdid.Id {
	t.Helper()
	x := require.New(t)

	v, _ := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)

	_, err = b.Ent.Audit.Delete().Where(entaudit.ObjectId(k)).Exec(ctx)
	x.NoError(err)

	return pdid.Id(k)
}

// TestARowNothingOnTheTrailIsAboutIsAFinding: named, as a row that differs is,
// and the check is not Ok.
func TestARowNothingOnTheTrailIsAboutIsAFinding(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "a deployment written through its servers alone has nothing to find: %+v", got)

	id := b.unrecorded(t, ctx)
	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{id}, got.NotOnTrail)
	x.Empty(got.Differs)
	x.False(got.Ok())
}

// TestARowWrittenBeneathTheServersIsRecordedAsTheActThatWroteIt: a restore, or
// whatever else an app writes past its servers on purpose, is put on the trail
// by `Record` -- one row each, by the act's name, with the digest of the row as
// it is and nothing of what it holds -- and then checked as any row is.
func TestARowWrittenBeneathTheServersIsRecordedAsTheActThatWroteIt(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	id := b.unrecorded(t, ctx)
	n, err := pd.Record(ctx, b.Ent, audit.Contents{Digest: d}, "/test/Restore", []pdid.Id{id, id})
	x.NoError(err)
	x.Equal(1, n, "an identifier named twice is one row")

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)

	rows := b.trailOf(t, ctx, id.Bytes())
	x.Len(rows, 1)
	x.Equal("/test/Restore", rows[0].Action)
	x.Equal(b.Tenant.Uuid(), rows[0].TenantId, "filed under the row's own tenant")
	x.Len(rows[0].Digest, 1+8+32)
	x.Empty(rows[0].Patch, "what the act wrote is the export's, not the trail's")
	x.Empty(rows[0].Value)
	x.NotContains(rows[0].DigestFields, uint32(32), "the keeper is a person's")
}

// TestADifferenceSomebodyAcceptedIsARecordedAct: a migration rewrote a column,
// an operator looked, and what the rows are now is put on the trail by the act
// of accepting them. #102 said it should be an act and not a flag; this is the
// act.
func TestADifferenceSomebodyAcceptedIsARecordedAct(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, _ := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)
	x.NoError(b.Ent.Robot.UpdateOneId(k).SetAlias("arm-migrated").Exec(ctx))

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs)

	n, err := pd.Record(ctx, b.Ent, audit.Contents{Digest: d}, "/test/Accept", got.Differs)
	x.NoError(err)
	x.Equal(1, n)

	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)

	rows := b.trailOf(t, ctx, v.GetId())
	x.Equal("/test/Accept", rows[len(rows)-1].Action)
}

// TestARecordedActHoldsOrFallsWithItsTransaction: written in the transaction
// that wrote the rows, it is on the trail when they are, and not when they are
// not.
func TestARecordedActHoldsOrFallsWithItsTransaction(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)
	id := b.unrecorded(t, ctx)

	tx, err := b.Ent.Tx(ctx)
	x.NoError(err)
	_, err = pd.Record(ctx, tx.Client(), audit.Contents{Digest: d}, "/test/Restore", []pdid.Id{id})
	x.NoError(err)
	x.NoError(tx.Rollback())
	x.Empty(b.trailOf(t, ctx, id.Bytes()))

	tx, err = b.Ent.Tx(ctx)
	x.NoError(err)
	_, err = pd.Record(ctx, tx.Client(), audit.Contents{Digest: d}, "/test/Restore", []pdid.Id{id})
	x.NoError(err)
	x.NoError(tx.Commit())
	x.Len(b.trailOf(t, ctx, id.Bytes()), 1)
}

// TestRecordIsRefusedWhatItCannotSayAnythingTrueAbout: an act with no name, and
// a row that is not there to be read.
func TestRecordIsRefusedWhatItCannotSayAnythingTrueAbout(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)
	id := b.unrecorded(t, ctx)

	_, err := pd.Record(ctx, b.Ent, audit.Contents{Digest: d}, "", []pdid.Id{id})
	x.Error(err)

	robot, ok := pdid.DomainOf("robot")
	x.True(ok)
	_, err = pd.Record(ctx, b.Ent, audit.Contents{Digest: d}, "/test/Restore", []pdid.Id{pdid.New(robot)})
	x.ErrorContains(err, "no live row")
	x.Empty(b.trailOf(t, ctx, id.Bytes()))
}

// TestAKindNoServerWritesIsNotRead: a tally is written through ent alone, so it
// is never on the trail and a check has nothing to say about it -- it is not
// read at all, rather than read and found on nothing -- and `Record` passes
// over it.
func TestAKindNoServerWritesIsNotRead(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	now := time.Now()
	id := pdid.New(pd.TallyDomain)
	x.NoError(b.Ent.Tally.Create().
		SetId(id.Uuid()).
		SetTenantId(b.Tenant.Uuid()).
		SetCount(1).
		SetToken([]byte("held for readback")).
		SetDateCreated(now).
		SetDateUpdated(now).
		Exec(ctx))

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)

	n, err := pd.Record(ctx, b.Ent, audit.Contents{Digest: d}, "/test/Restore", []pdid.Id{id})
	x.NoError(err)
	x.Zero(n)
	x.Empty(b.trailOf(t, ctx, id.Bytes()))
}
