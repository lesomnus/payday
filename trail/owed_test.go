package trail

import (
	"fmt"
	"os"
	"testing"
	"time"

	"uuid"

	"filippo.io/age"
	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// about is a deployment with a person's rows in the archive and in the
// database: one archived a month and a half ago, one written yesterday.
func about(t *testing.T) (*fakeStore, Policy, pdid.Id, pdid.Id) {
	t.Helper()

	tenant := newTenant()
	who := uuid.NewV7()
	s := &fakeStore{}
	s.archivedAbout = s.add(fakeRow{tenant: tenant.Uuid(), object: who, domain: person, created: ago(45 * day), value: "what they said, archived"}).id
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(45 * day), value: "somebody else"})
	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Signer: newKey(t)}
	p.Pass(t.Context(), s)
	s.add(fakeRow{tenant: tenant.Uuid(), object: who, domain: person, created: ago(day), value: "what they said since"})

	return s, p, tenant, pdid.Id(who)
}

// sayingAbout is whether anything the trail keeps about `who` still says something:
// in the database, and in the archive.
func sayingAbout(t *testing.T, s *fakeStore, p Policy, who pdid.Id) (bool, bool) {
	t.Helper()

	db := false
	s.mu.Lock()
	for _, r := range s.rows {
		if pdid.Id(r.object) == who && r.value != "" {
			db = true
		}
	}
	s.mu.Unlock()

	v, _ := docsIn(t, p.Archive)[s.archivedAbout]["value"].(string)

	return db, v != ""
}

// TestAnErasureDecidedInATransactionIsOwedThenPaid (#115).
//
// Asked inside an app's transaction, Forget blanks the database's rows there
// and owes the archive's half, writing nothing the archive keeps: no chunk,
// no receipt, no checkpoint, which a rollback could not take back. Paid once
// the transaction has committed, the archive's half is done, with its
// receipts and a checkpoint, and the debt goes. Paid twice, nothing changes.
func TestAnErasureDecidedInATransactionIsOwedThenPaid(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, p, _, who := about(t)
	before, _, err := s.Latest(ctx)
	x.NoError(err)
	receipts := len(receiptsIn(t, p.Archive))

	s.inTx = true
	got, err := p.Forget(ctx, s, []pdid.Id{who})
	x.NoError(err)
	x.Equal(1, got.Owed, "the archive's half is not owed")
	x.Equal(1, got.Rows, "the database's row about them is not reached")
	db, archived := sayingAbout(t, s, p, who)
	x.False(db, "the database still says something about them")
	x.True(archived, "the archive was written inside the transaction")
	x.Len(s.owed, 1)
	x.Len(receiptsIn(t, p.Archive), receipts, "a receipt was written inside the transaction")
	k, _, err := s.Latest(ctx)
	x.NoError(err)
	x.Equal(before, k, "a checkpoint was numbered inside the transaction")

	// Committed.
	s.inTx = false
	paid, err := p.Pay(ctx, s)
	x.NoError(err)
	x.Equal(1, paid.Objects)
	x.Equal(1, paid.Archived)
	x.Zero(paid.Owed)
	_, archived = sayingAbout(t, s, p, who)
	x.False(archived, "the archive still says something about them")
	x.Empty(s.owed, "a debt paid is still owed")
	forgets := 0
	for _, r := range receiptsIn(t, p.Archive)[receipts:] {
		if r.Act == "forget" {
			forgets++
		}
	}
	x.Equal(2, forgets, "the erasure paid has no receipts, of the database and of the archive")
	k, _, err = s.Latest(ctx)
	x.NoError(err)
	x.Equal(before+1, k, "paying ended in no checkpoint")

	again, err := p.Pay(ctx, s)
	x.NoError(err)
	x.Zero(again.Objects, "paid twice")
	k2, _, err := s.Latest(ctx)
	x.NoError(err)
	x.Equal(k, k2, "paying nothing took a checkpoint")

	v, err := p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
}

// TestAnErasureOwedIsPaidByTheNextPass: a process that stops between the
// commit and paying loses nothing -- the debt was written in the transaction,
// and the next pass pays it before it moves anything.
func TestAnErasureOwedIsPaidByTheNextPass(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, p, _, who := about(t)
	s.inTx = true
	_, err := p.Forget(ctx, s, []pdid.Id{who})
	x.NoError(err)
	s.inTx = false

	// Whatever paid it died with the process; the next pass is another.
	p.Pass(ctx, s)
	db, archived := sayingAbout(t, s, p, who)
	x.False(db)
	x.False(archived, "the pass did not pay what was owed")
	x.Empty(s.owed)
}

// TestAnErasureAHoldKeepsStaysOwedUntilItLifts: a hold on part of an erasure
// keeps those rows as they were and the debt with them, saying whose hold --
// and the pass that finds the hold lifted pays the rest, with nobody asking
// again.
func TestAnErasureAHoldKeepsStaysOwedUntilItLifts(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, p, tenant, who := about(t)
	hold := map[pdid.Id]Tenant{tenant: {Hold: onHold()}}
	p.Tenants = answers(hold)

	s.inTx = true
	got, err := p.Forget(ctx, s, []pdid.Id{who})
	x.NoError(err)
	x.Equal(1, got.Owed)
	x.True(got.Held.Any(), "a hold kept nothing of the database's half")
	s.inTx = false

	paid, err := p.Pay(ctx, s)
	x.NoError(err)
	x.Zero(paid.Objects, "a debt a hold keeps part of was paid")
	x.Equal(1, paid.Owed)
	x.Contains(paid.Held.By, tenant)
	db, archived := sayingAbout(t, s, p, who)
	x.True(db, "what the hold is on was blanked")
	x.True(archived, "what the hold is on was blanked")

	owing, err := p.Owing(ctx, s)
	x.NoError(err)
	x.Equal(1, owing.Objects)
	x.Equal(map[pdid.Id]int{tenant: 1}, owing.Held, "what is owed under the hold, by whose")
	x.False(owing.Oldest.IsZero())

	// Lifted: the next pass pays it.
	delete(hold, tenant)
	p.Pass(ctx, s)
	db, archived = sayingAbout(t, s, p, who)
	x.False(db)
	x.False(archived, "a hold lifted, and the debt still not paid")
	owing, err = p.Owing(ctx, s)
	x.NoError(err)
	x.Zero(owing.Objects)
}

// TestForgettingOutsideATransactionOwesAndPaysAtOnce: outside a transaction
// there is nothing to wait for, and nothing is left owed.
func TestForgettingOutsideATransactionOwesAndPaysAtOnce(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, p, _, who := about(t)
	got, err := p.Forget(ctx, s, []pdid.Id{who})
	x.NoError(err)
	x.Zero(got.Owed)
	x.Equal(1, got.Rows)
	x.Equal(1, got.Archived)
	x.Empty(s.owed, "an erasure done is still owed")
	db, archived := sayingAbout(t, s, p, who)
	x.False(db)
	x.False(archived)
}

// TestEveryOtherActIsRefusedInATransaction: an act that writes the archive is
// refused inside a transaction, before it writes anything, and a question is
// answered.
func TestEveryOtherActIsRefusedInATransaction(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, p, tenant := manifestedArchive(t, newKey(t))
	state := func() string {
		s.mu.Lock()
		defer s.mu.Unlock()

		blobs, err := inventory(ctx, p.Archive)
		x.NoError(err)
		ls, err := listCheckpoints(ctx, p.checkpoints())
		x.NoError(err)

		return fmt.Sprint(s.counter, s.rows, s.archived, s.owed, len(blobs), len(ls))
	}
	before := state()
	whom, err := age.GenerateX25519Identity()
	x.NoError(err)

	s.inTx = true
	for name, act := range map[string]func() error{
		"pay":              func() error { _, err := p.Pay(ctx, s); return err },
		"a tenant's purge": func() error { _, err := p.PurgeTenant(ctx, s, tenant); return err },
		"a retirement":     func() error { _, err := p.RetireTenant(ctx, s, tenant); return err },
		"a return":         func() error { _, err := p.Return(ctx, s, tenant); return err },
		"purge":            func() error { _, _, err := p.Purge(ctx, s, Before(time.Now())); return err },
		"discard":          func() error { _, _, err := p.Collect(ctx, s, Kinds{}, time.Now()); return err },
		"archive":          func() error { _, err := Archive(ctx, s, p, Kinds{}, time.Now()); return err },
		"accept": func() error {
			_, err := p.Accept(ctx, s, Acceptance{Of: "sha256:00", By: "an operator", Why: "looked at"})
			return err
		},
		"an export": func() error {
			_, err := p.ExportTenant(ctx, s, tenant, Export{By: "an operator", Why: "a case", To: []age.Recipient{whom.Recipient()}}, ioDiscard{})
			return err
		},
	} {
		x.ErrorIs(act(), ErrInTx, name)
		x.Equal(before, state(), "%s wrote inside a transaction", name)
	}
	p.Pass(ctx, s)
	x.Equal(before, state(), "a pass wrote inside a transaction")

	// Questions are answered.
	_, err = p.Verify(ctx, s, false)
	x.NoError(err)
	_, _, err = p.Doomed(ctx, s, Before(time.Now()))
	x.NoError(err)
	_, err = p.PlanTenantPurge(ctx, s, tenant)
	x.NoError(err)
	_, err = p.PlanTenantRetire(ctx, s, tenant)
	x.NoError(err)
	_, err = p.Retired(ctx, s)
	x.NoError(err)
	_, err = p.Owing(ctx, s)
	x.NoError(err)
	x.NoError(p.ReadTenant(ctx, s, tenant, func([]byte) error { return nil }))
	x.Equal(before, state(), "a question wrote")
}

// ioDiscard is a writer that keeps nothing, for an act refused before it
// writes.
type ioDiscard struct{}

func (ioDiscard) Write(b []byte) (int, error) { return len(b), nil }

// TestAnErasureTheArchiveCouldNotFinishStaysOwed: a chunk whose bytes are not
// its digest is left for Verify, and an erasure that could not reach it is
// not done -- so it stays owed, for the pay after the chunk is settled.
func TestAnErasureTheArchiveCouldNotFinishStaysOwed(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()
	who := uuid.NewV7()
	dir := t.TempDir()
	s := &fakeStore{}
	r := s.add(fakeRow{tenant: tenant.Uuid(), object: who, domain: person, created: ago(45 * day), value: "what they said"})
	p := Policy{Archive: flob.NewOsStores(dir), Keep: Keep{Retain: 30 * day}, Signer: newKey(t)}
	p.Pass(ctx, s)

	cs, err := chunksIn(ctx, p.Archive, tenant.String())
	x.NoError(err)
	x.Len(cs, 1)
	r.value = "what somebody else says they said"
	path := blobPath(dir, cs[0].Digest)
	x.NoError(os.Chmod(path, 0o600))
	x.NoError(os.WriteFile(path, gz(t, r.doc()), 0o600))

	s.inTx = true
	_, err = p.Forget(ctx, s, []pdid.Id{pdid.Id(who)})
	x.NoError(err)
	s.inTx = false

	_, err = p.Pay(ctx, s)
	x.ErrorIs(err, ErrAltered)
	x.Len(s.owed, 1, "an erasure the archive could not finish was taken for paid")
}
