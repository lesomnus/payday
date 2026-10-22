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
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
)

// A digest of the row each write left (payday#102): every row of the trail
// keeps one, under the deployment's key, and a check of the live rows finds
// the one somebody changed in the database past the servers.

// digestKey is a key as configuration gives one through the environment.
var digestKey = "base64:" + base64.StdEncoding.EncodeToString([]byte("a key of thirty-two bytes, or so"))

// rowDigest is the length of a digest of the row a write left, bound to its
// line (payday#115): a version byte and a byte of flags, and a fingerprint and
// a MAC each for the line and for the row.
const rowDigest = 2 + 2*(8+32)

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

// TestEachRowOfTheTrailKeepsADigestOfTheRowItsWriteLeft: a byte of version,
// one of flags, the key's fingerprint and the HMAC, over every field but the
// ones the robot declares personal -- its secret among them (payday#115) --
// and a write that changed the row leaves another. The robot declares a field
// personal, so each write sealed the row, and says so.
func TestEachRowOfTheTrailKeepsADigestOfTheRowItsWriteLeft(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, rows := b.kept(t, ctx)
	for _, row := range rows {
		x.Len(row.Digest, rowDigest)
		x.Equal(audit.DigestVersion, row.Digest[0])
		x.True(audit.Sealing(row.Digest), "the write sealed the row")
		x.Contains(row.DigestFields, uint32(4), "the alias is the robot's")
		x.Contains(row.DigestFields, uint32(8), "a keyed MAC of the secret is no copy of it, and no way to test a guess at it")
		x.NotContains(row.DigestFields, uint32(32), "the keeper is a person's")
	}
	x.NotEqual(rows[0].Digest, rows[1].Digest, "the Patch left another row")

	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)
	seal, err := b.Ent.RowSeal.Get(ctx, k)
	x.NoError(err, "a robot declares a field personal, and nothing sealed it")
	x.Equal(audit.SealVersion, seal.Sum[0])
	x.Contains(seal.Fields, uint32(32), "the seal covers the keeper")
	x.Equal(b.Tenant.Uuid(), seal.TenantId, "filed where its line is")

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

// TestAPersonalFieldChangedPastTheServersIsFoundByTheSeal: the keeper is
// outside the trail's digest -- a keyed hash of somebody's address on a line
// would outlive their erasure for as long as the line does -- and inside the
// row's seal, which lives as long as the row: so the keeper changed in the
// database is found (payday#115), where before it passed every check.
func TestAPersonalFieldChangedPastTheServersIsFoundByTheSeal(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, _ := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)
	x.NoError(b.Ent.Robot.UpdateOneId(k).SetKeeper("somebody-else@example.com").Exec(ctx))

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs)

	// And the seal taken off is as much a finding: the line says its write
	// sealed the row.
	_, err = b.Ungated.Robot().Patch(ctx, app.RobotPatchRequest_builder{
		Ref:              app.RobotRef_builder{Id: v.GetId()}.Build(),
		Keeper:           z.Ptr("somebody-else@example.com"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)
	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "a write the trail recorded is the row again: %+v", got)

	x.NoError(b.Ent.RowSeal.DeleteOneId(k).Exec(ctx))
	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs)
}

// TestASecretChangedPastTheServersDiffers: the trail's digest covers a field
// declared secret (payday#115), so a credential somebody rewrote in the
// database is found, as any other field is. Of a seal, which declares a secret
// and no field personal: nothing but the digest vouches for it.
func TestASecretChangedPastTheServersDiffers(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, err := b.Ungated.Seal().Add(ctx, app.SealAddRequest_builder{Alias: "door", Secret: []byte("the verifier")}.Build())
	x.NoError(err)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)
	rows := b.trailOf(t, ctx, v.GetId())
	x.Len(rows, 1)
	x.Contains(rows[0].DigestFields, uint32(8), "the secret is covered")
	x.False(audit.Sealing(rows[0].Digest), "nothing of it is personal, so it is not sealed")

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)

	x.NoError(b.Ent.Seal.UpdateOneId(k).SetSecret([]byte("somebody else's")).Exec(ctx))
	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs)
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
		x.Len(rows[0].Digest, rowDigest)
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

	p := trail.Policy{Archive: flob.NewMemStores(), Signer: pdtest.Signer(t)}
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
	p := trail.Policy{Archive: flob.NewMemStores(), Keep: trail.Keep{Retain: time.Nanosecond}, Keys: keys, Signer: pdtest.Signer(t)}
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
	x.Len(rows[0].Digest, rowDigest)
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
	x.ErrorContains(err, "is no row")
	x.Empty(b.trailOf(t, ctx, id.Bytes()))
}

// TestAKindTheAppRecordsIsChecked: a tally is written through ent alone, and
// says its writes are recorded (`trail: {recorded: {}}`), so a check reads it
// as any kind. Written and not recorded, it is a row nothing on the trail is
// about; recorded, it is on the trail under its own tenant, and matches.
func TestAKindTheAppRecordsIsChecked(t *testing.T) {
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
	x.Equal([]pdid.Id{id}, got.NotOnTrail, "a write the app did not record")

	n, err := pd.Record(ctx, b.Ent, audit.Contents{Digest: d}, "/test/Count", []pdid.Id{id})
	x.NoError(err)
	x.Equal(1, n)

	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)

	rows := b.trailOf(t, ctx, id.Bytes())
	x.Len(rows, 1)
	x.Equal(b.Tenant.Uuid(), rows[0].TenantId, "filed under the row's own tenant")
	x.Contains(rows[0].DigestFields, uint32(9), "the token is a secret, and a keyed MAC of it is no copy of it")
}

// TestAKindKeptOffTheTrailIsNotRead: a pulse says its writes are kept off the
// trail, with why (`trail: {off: ...}`), so a check does not read it, and
// `Record` passes over it.
func TestAKindKeptOffTheTrailIsNotRead(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	now := time.Now()
	id := pdid.New(pd.PulseDomain)
	x.NoError(b.Ent.Pulse.Create().
		SetId(id.Uuid()).
		SetProcess("one").
		SetDateSeen(now).
		SetDateCreated(now).
		Exec(ctx))

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)

	n, err := pd.Record(ctx, b.Ent, audit.Contents{Digest: d}, "/test/Beat", []pdid.Id{id})
	x.NoError(err)
	x.Zero(n)
	x.Empty(b.trailOf(t, ctx, id.Bytes()))
}

// The rest is payday#115: what anybody who can write the database can make of
// a line of the trail, so that the row beside it says nothing, is a finding
// rather than a count -- and so is what was removed past the servers.

// TestARowBroughtBackFromAnEraseDiffers: a robot erased softly through the
// servers keeps a line whose digest is of the row the erase left, stamp and
// all; brought back in the database, it is not that row.
func TestARowBroughtBackFromAnEraseDiffers(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, _ := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)
	_, err = b.Ungated.Robot().Erase(ctx, app.RobotRef_builder{Id: v.GetId()}.Build())
	x.NoError(err)

	rows := b.trailOf(t, ctx, v.GetId())
	x.Len(rows[len(rows)-1].Digest, rowDigest, "an erase that left the row keeps a digest of it")

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "an erased row is a row the trail accounts for: %+v", got)

	// Changed while it stays erased, it is found: a check reads every row
	// there is, and a restore would otherwise bring back whatever it was made
	// into.
	x.NoError(b.Ent.Robot.UpdateOneId(k).SetAlias("arm-forged").Exec(ctx))
	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs)

	x.NoError(b.Ent.Robot.UpdateOneId(k).SetAlias(v.GetAlias()).ClearDateErased().Exec(ctx))
	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs, "brought back")
}

// TestALineWithoutItsDigestIsAFinding: a row changed in the database, and the
// digest of its newest line taken off, says nothing -- which is the finding.
// A deployment told digests began later than a line that keeps one is refused
// rather than counting everything before then as from before.
func TestALineWithoutItsDigestIsAFinding(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, rows := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)
	x.NoError(b.Ent.Robot.UpdateOneId(k).SetAlias("arm-forged").Exec(ctx))
	x.NoError(b.Ent.Audit.UpdateOneId(rows[len(rows)-1].Id).SetDigest([]byte{}).Exec(ctx))

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.NoDigest)
	x.False(got.Ok())

	_, err = pd.Digests(ctx, b.Ent, d.Since(time.Now()), trail.Policy{})
	x.ErrorContains(err, "audit.digest.since")
}

// TestACheckWithTheWrongKeyFindsEverything: a check handed a key other than
// the one the trail was written under, or of a deployment that wrote it with
// none, does not pass -- and one told the key was let go counts the rows.
func TestACheckWithTheWrongKeyFindsEverything(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)
	b.kept(t, ctx)

	other, err := audit.NewDigest([]byte("another key of thirty-two bytes!"))
	x.NoError(err)
	got, err := pd.Digests(ctx, b.Ent, other, trail.Policy{})
	x.NoError(err)
	x.NotEmpty(got.KeyNotHeld)
	x.Equal([]audit.Fingerprint{d.Fingerprint()}, got.Unheld)
	x.False(got.Ok())

	letGo, err := other.Retire(d.Fingerprint())
	x.NoError(err)
	got, err = pd.Digests(ctx, b.Ent, letGo, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)
	x.Positive(got.Retired)

	// And a deployment that writes with no key at all.
	nokey, ctx := build(t)
	nokey.kept(t, ctx)
	got, err = pd.Digests(ctx, nokey.Ent, d, trail.Policy{})
	x.NoError(err)
	x.NotEmpty(got.NoDigest)
	x.False(got.Ok())
}

// TestADigestCopiedOntoTheNewestLineIsForged: the robot put back to what its
// Add left, and the Add's digest copied onto the Patch's line, is a line the
// digest is not of.
func TestADigestCopiedOntoTheNewestLineIsForged(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, rows := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)
	x.NoError(b.Ent.Robot.UpdateOneId(k).SetAlias(v.GetAlias()).SetDateUpdated(v.GetDateUpdated().AsTime()).Exec(ctx))

	add, patch := rows[0], rows[1]
	x.NoError(b.Ent.Audit.UpdateOneId(patch.Id).SetDigest(add.Digest).SetDigestFields(add.DigestFields).Exec(ctx))

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Forged)
	x.False(got.Ok())
}

// TestARowRemovedPastTheServersIsMissing: a robot deleted in the database is a
// thing the trail says is a row and is not. One the app removed on purpose,
// and recorded as the act that removed it, is not; nor is a reading erased
// hard through the servers, whose line says the row went.
func TestARowRemovedPastTheServersIsMissing(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, _ := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)

	r, err := b.Ungated.Reading().Add(ctx, reading(v.GetId(), 21))
	x.NoError(err)
	_, err = b.Ungated.Reading().Erase(ctx, app.ReadingRef_builder{Id: r.GetId()}.Build())
	x.NoError(err)

	x.NoError(b.Ent.Robot.DeleteOneId(k).Exec(ctx))

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Missing)
	x.False(got.Ok())

	_, err = pd.Record(ctx, b.Ent, audit.Contents{Digest: d}, "/test/Forget", []pdid.Id{pdid.Id(k)})
	x.ErrorContains(err, "is no row", "that is RecordErased's")

	n, err := pd.RecordErased(ctx, b.Ent, audit.Contents{Digest: d}, "/test/Forget", []pdid.Id{pdid.Id(k)})
	x.NoError(err)
	x.Equal(1, n)

	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)

	still, _ := b.kept(t, ctx)
	_, err = pd.RecordErased(ctx, b.Ent, audit.Contents{Digest: d}, "/test/Forget", []pdid.Id{pdid.Id(still.GetId())})
	x.ErrorContains(err, "is still a row")
}

// TestAKindACheckDoesNotReadIsSaidWithWhy: a pulse is kept off the trail, and
// what a check answers says so, every time, with the reason the schema gave.
func TestAKindACheckDoesNotReadIsSaidWithWhy(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Contains(got.Skipped, audit.Skipped{Kind: "pulse", Why: "a heartbeat each process writes every few seconds; a record of each is noise"})
}

// TestARowNobodyWroteSinceItsWindowIsCarriedForward: every line about a robot
// leaves the database and then the archive at the end of its window, and the
// pass carries what the newest said of the row onto a line of the database's
// own -- so a check finds the robot on the trail, and still finds it changed
// when it is. Without the key the pass carries nothing, and the check finds
// the robot not on the trail.
func TestARowNobodyWroteSinceItsWindowIsCarriedForward(t *testing.T) {
	x := require.New(t)

	for _, carries := range []bool{true, false} {
		b, ctx, d := digesting(t)
		v, _ := b.kept(t, ctx)
		k, err := entuuid.FromBytes(v.GetId())
		x.NoError(err)

		p := trail.Policy{Archive: flob.NewMemStores(), Keep: trail.Keep{Retain: time.Nanosecond, Destroy: 2 * time.Nanosecond}, Signer: pdtest.Signer(t)}
		if carries {
			p.Digest = d
		}
		x.NoError(p.Valid())
		p.Pass(ctx, pd.TrailStore(b.Ent))

		got, err := pd.Digests(ctx, b.Ent, d, p)
		x.NoError(err)
		if !carries {
			x.Contains(got.NotOnTrail, pdid.Id(k))
			continue
		}

		x.True(got.Ok(), "%+v", got)
		x.Zero(got.Archived, "every line it read is one the pass carried into the database")
		rows := b.trailOf(t, ctx, v.GetId())
		x.Len(rows, 1)
		x.Equal(trail.CarriedAction, rows[0].Action)

		// And the row's seal went with its line (payday#115): bound to the
		// new one, it vouches for the keeper still.
		x.NoError(b.Ent.Robot.UpdateOneId(k).SetKeeper("somebody-else@example.com").Exec(ctx))
		got, err = pd.Digests(ctx, b.Ent, d, p)
		x.NoError(err)
		x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs, "the seal was not carried with the line")
		x.NoError(b.Ent.Robot.UpdateOneId(k).SetKeeper(keeperAfter).Exec(ctx))

		x.NoError(b.Ent.Robot.UpdateOneId(k).SetAlias("arm-forged").Exec(ctx))
		got, err = pd.Digests(ctx, b.Ent, d, p)
		x.NoError(err)
		x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs, "carried as it was recorded, not made again of the row")
	}
}
