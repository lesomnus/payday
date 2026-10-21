package trail

import (
	"bytes"
	"compress/gzip"
	"crypto"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// latestOf is the number of the latest checkpoint the store has numbered.
func latestOf(t *testing.T, s *fakeStore) int {
	t.Helper()

	k, _, err := s.Latest(t.Context())
	require.NoError(t, err)

	return k
}

// writtenBy is the row of the one chunk an act wrote into a namespace: present,
// and first in the checkpoint that act ended in.
func writtenBy(t *testing.T, s *fakeStore, ns string, k int) Archived {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	vs := slices.DeleteFunc(slices.Clone(s.archived), func(v Archived) bool {
		return v.Namespace != ns || v.State != Present || v.Since != k
	})
	require.Len(t, vs, 1)

	return vs[0]
}

// TestEveryActEndsInACheckpoint.
//
// Between two checkpoints, somebody who can write both the database and the
// archive can change the two together, and only a checkpoint says what they
// were. A pass ended in one and nothing else did, so what an act did by hand
// was out of reach until the next pass (#60). Every act that changes the
// archive ends in one now, and one that changes nothing -- a question, or an
// act with nothing to do -- takes none.
func TestEveryActEndsInACheckpoint(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	key := newKey(t)
	s, p, tenant := manifestedArchive(t, key)
	x.Equal(1, latestOf(t, s), "the pass")

	_, _, err := p.Doomed(ctx, s, Before(time.Now()))
	x.NoError(err)
	_, err = p.PlanTenantPurge(ctx, s, tenant)
	x.NoError(err)
	_, err = p.Preview(ctx, s, tenant, Tenant{})
	x.NoError(err)
	x.NoError(p.Read(ctx, s, func([]byte) error { return nil }))
	_, _, err = p.Purge(ctx, s, Before(time.Now().AddDate(-10, 0, 0)))
	x.NoError(err)
	x.Equal(1, latestOf(t, s), "what changed nothing took a checkpoint")

	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(2 * day), value: "d"})
	_, err = Archive(ctx, s, p, Kinds{}, time.Now())
	x.NoError(err)
	x.Equal(2, latestOf(t, s), "archive")

	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(2 * day), value: "e"})
	_, _, err = p.Collect(ctx, s, Kinds{}, time.Now())
	x.NoError(err)
	x.Equal(3, latestOf(t, s), "a discard, for its receipt")

	_, err = p.Forget(ctx, s, []pdid.Id{tenant})
	x.NoError(err)
	x.Equal(4, latestOf(t, s), "forget")

	_, _, err = p.Purge(ctx, s, Before(time.Now()))
	x.NoError(err)
	x.Equal(5, latestOf(t, s), "purge")

	_, err = p.PurgeTenant(ctx, s, tenant)
	x.NoError(err)
	x.Equal(6, latestOf(t, s), "purge-tenant")

	_, err = p.Accept(ctx, s, "nothing was wrong, and an acceptance says so")
	x.NoError(err)
	x.Equal(7, latestOf(t, s), "accept")

	v, err := p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(7, v.Checkpoint, "the store does not keep the latest")
}

// TestAChunkAnActWroteIsFoundWhenItIsTakenAwayWithItsRow.
//
// The first thing #60 named: a blob added since the latest checkpoint, taken
// away together with its row, left nothing to say it had been there. Now the
// act that wrote it ended in a checkpoint that has it, and its rows no longer
// add up to it.
//
// And it stays found. With a checkpoint at the end of every act, the latest
// two -- which are what the rows are compared with -- are soon others, so no
// checkpoint is taken over rows that no longer add up to the latest until an
// operator accepts them; that acceptance is a checkpoint of its own, and says
// why.
func TestAChunkAnActWroteIsFoundWhenItIsTakenAwayWithItsRow(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	key := newKey(t)
	s, p, tenant := manifestedArchive(t, key)
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(2 * day), value: "d"})
	_, err := Archive(ctx, s, p, Kinds{}, time.Now())
	x.NoError(err)
	x.Equal(2, latestOf(t, s))

	// Somebody who can write both: the chunk, and its row.
	gone := writtenBy(t, s, tenant.String(), 2)
	s.mu.Lock()
	s.archived = slices.DeleteFunc(s.archived, func(v Archived) bool { return v.Key == gone.Key })
	s.mu.Unlock()
	x.NoError(p.Archive.Use(gone.Namespace).Erase(ctx, flob.Digest(gone.Digest)))

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"rewritten"}, kinds(v))

	// Act after act, and a pass: each does what it does, and none of them is
	// a checkpoint over what was changed.
	for range 3 {
		_, err := p.Forget(ctx, s, []pdid.Id{newTenant()})
		x.ErrorIs(err, errRewritten)
	}
	p.Pass(ctx, s)
	x.Equal(2, latestOf(t, s))

	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.Contains(kinds(v), "rewritten", "taken away, and two acts later nothing says so")

	// Until an operator has looked.
	const why = "the chunk was taken away by hand; the rows it held are in the backup"
	_, err = p.Accept(ctx, s, why)
	x.NoError(err)
	x.Equal(3, latestOf(t, s))

	cs := []Checkpoint{}
	x.NoError(p.ReadCheckpoints(ctx, func(c Checkpoint) error { cs = append(cs, c); return nil }))
	x.Equal(why, cs[len(cs)-1].Accepted)
	x.Empty(cs[0].Accepted)

	// Clean at once: the rows are compared with the acceptance, and not with
	// the one before it, which is what the operator accepted they no longer
	// add up to. It was `rewritten` until the next act took a checkpoint.
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(3, v.Checkpoint)

	// And the next acts are compared with that one.
	_, err = p.Forget(ctx, s, []pdid.Id{newTenant()})
	x.NoError(err)
	x.Equal(4, latestOf(t, s))
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
}

// TestARowPutBackToAddingTakesNothingIn.
//
// The second thing #60 named: a row put back into a state it was in, with the
// signature it had then. Put back into `adding`, it is an add in flight to
// anybody who reads it, and a blob labelled with its intent is taken in under
// it -- somebody's blob, signed into the manifest by the next pass, folded into
// a chunk of the deployment's, or written again as one by a forget.
//
// The checkpoint the act ended in has the row's blob, so the rows no longer add
// up to it, and while they do not, nothing is settled and no intent is trusted.
func TestARowPutBackToAddingTakesNothingIn(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// Two chunks of a month that is over, so that a pass folds what it trusts
	// of it.
	key := newKey(t)
	s, p, tenant := manifestedArchive(t, key)
	for _, v := range []string{"d", "e"} {
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(80 * day), value: v})
		_, err := Archive(ctx, s, p, Kinds{}, time.Now())
		x.NoError(err)
	}

	// Back to what it was while it was being added, with the signature that
	// state had -- an Ed25519 signature is the same every time, so this is
	// the one somebody who read the database then would have kept. And since
	// cleared, as if no checkpoint had seen it.
	was := writtenBy(t, s, tenant.String(), 3)
	back := was
	back.State, back.Digest, back.Since = Adding, "", 0
	sig, err := p.signRow(back)
	x.NoError(err)
	back.Signature = sig
	s.mu.Lock()
	for i, v := range s.archived {
		if v.Key == was.Key {
			s.archived[i] = back
		}
	}
	s.mu.Unlock()

	// The deployment's chunk taken away, and theirs in its place, labelled as
	// the add's blob would be: a row about the tenant, which a forget of it
	// would write again.
	planted := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), object: tenant.Uuid(), domain: person, created: ago(80 * day), value: "theirs"}
	x.NoError(p.Archive.Use(was.Namespace).Erase(ctx, flob.Digest(was.Digest)))
	buf := &bytes.Buffer{}
	z := gzip.NewWriter(buf)
	_, err = z.Write(append(planted.doc(), '\n'))
	x.NoError(err)
	x.NoError(z.Close())
	theirs, err := p.Archive.Use(was.Namespace).Add(ctx, flob.Meta{Labels: labelsFrom(back.Labels)}, buf)
	x.NoError(err)

	p.Pass(ctx, s)
	_, err = p.Forget(ctx, s, []pdid.Id{tenant})
	x.ErrorIs(err, errRewritten)

	s.mu.Lock()
	for _, v := range s.archived {
		if v.Key == was.Key {
			x.Equal(Adding, v.State, "their blob was taken in under the row")
		}
	}
	s.mu.Unlock()
	x.Empty(s.state(was.Namespace, theirs.Digest), "their blob is in the manifest")

	// Nor is what it says in anything the manifest accounts for.
	read, err := said(t, func(fn func(doc []byte) error) error { return p.Read(ctx, s, fn) })
	x.NoError(err)
	x.NotContains(read, planted.id, "what their blob said is read as the deployment's")

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.Contains(kinds(v), "rewritten")
	x.Equal(3, latestOf(t, s), "a checkpoint was taken over it")
}

// TestAQuestionTakesNoCheckpoint.
//
// What only says what an act would do writes no checkpoint, even when asking
// it is the first time the manifest is filled from what the archive holds: the
// act that follows is what covers that.
func TestAQuestionTakesNoCheckpoint(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()
	asks := map[string]func(Policy, *fakeStore) error{
		"doomed": func(p Policy, s *fakeStore) error {
			_, _, err := p.Doomed(ctx, s, Before(time.Now()))
			return err
		},
		"plan a tenant's purge": func(p Policy, s *fakeStore) error {
			_, err := p.PlanTenantPurge(ctx, s, tenant)
			return err
		},
	}
	for name, ask := range asks {
		a := flob.NewMemStores()
		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(400 * day)}
		plant(t, a, Chunk{Namespace: tenant.String(), Kind: "person", Month: monthOf(r.created), Run: "before", Rows: 1, First: r.created, Last: r.created}, r.doc())

		s := &fakeStore{}
		p := Policy{Archive: a}
		x.NoError(ask(p, s), name)

		rows, err := s.Rows(ctx)
		x.NoError(err)
		x.NotEmpty(rows, "%s: the manifest was not filled", name)
		x.Zero(latestOf(t, s), "%s took a checkpoint", name)

		_, err = p.PurgeTenant(ctx, s, tenant)
		x.NoError(err)
		x.Equal(1, latestOf(t, s), name)
	}
}

// TestArchiveWritesAsThePolicySays.
//
// It was handed only the archive (#59), so what it wrote was in the clear in a
// deployment that seals, and its rows were unsigned -- a finding until an
// operator accepted them -- in one that signs. It is handed the policy now, and
// writes as a pass does.
func TestArchiveWritesAsThePolicySays(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()
	s := &fakeStore{}
	r := s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(day), value: "what it says"})

	keys, _ := sealing(t)
	p := Policy{Archive: flob.NewMemStores(), Keys: keys, Signer: newKey(t)}
	n, err := Archive(ctx, s, p, Kinds{}, time.Now())
	x.NoError(err)
	x.Equal(1, n)

	in := raw(t, p.Archive)[r.id]
	x.NotNil(in)
	x.NotContains(in, "value")
	x.Contains(in, SealedMember)

	v, err := p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(1, v.Checkpoint, "it ended in no checkpoint")

	t.Run("and an archive is still what it needs", func(t *testing.T) {
		_, err := Archive(t.Context(), s, Policy{}, Kinds{}, time.Now())
		require.Error(t, err)
		require.False(t, errors.Is(err, errRewritten))
	})
}

// TestADatabaseRestoredFromBeforeTheLatestCheckpointGoesOnOnceAccepted.
//
// A database restored from a backup older than the latest checkpoint has
// numbered fewer checkpoints than the store keeps, and no checkpoint is taken
// while it is: one would take a number one already has. That much is the
// point -- a database rolled back is found, and every act says so -- but the
// acceptance that was to end it was refused for the same reason, so nothing
// took a checkpoint again (#70). An acceptance is now numbered after the
// newest the store keeps, and names it as the one before: the checkpoints from
// after the backup stay what the archive held, and the database goes on from
// there.
func TestADatabaseRestoredFromBeforeTheLatestCheckpointGoesOnOnceAccepted(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	key := newKey(t)
	s, p, tenant := manifestedArchive(t, key)
	x.Equal(1, latestOf(t, s))

	// The backup.
	s.mu.Lock()
	backup, counter, at := slices.Clone(s.archived), s.counter, s.counterAt
	s.mu.Unlock()

	// Life goes on after it: two acts, and their checkpoints.
	for _, v := range []string{"after the backup", "and after that"} {
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(2 * day), value: v})
		_, err := Archive(ctx, s, p, Kinds{}, time.Now())
		x.NoError(err)
	}
	x.Equal(3, latestOf(t, s))
	third := numbered(t, p, 3).d

	// The database restored from the backup, and the archive and the
	// checkpoint store not.
	s.mu.Lock()
	s.archived, s.counter, s.counterAt = backup, counter, at
	s.mu.Unlock()

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"behind", "unaccounted"}, kinds(v))

	// Every act says so, and takes none.
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(2 * day), value: "after the restore"})
	_, err = Archive(ctx, s, p, Kinds{}, time.Now())
	x.ErrorIs(err, errBehind)
	p.Pass(ctx, s)
	x.Equal(1, latestOf(t, s))

	// An acceptance is a signature, as it was.
	unkeyed := p
	unkeyed.Signer, unkeyed.Trust = nil, []crypto.PublicKey{key.Public()}
	_, err = unkeyed.Accept(ctx, s, "restored from the backup")
	x.Error(err)
	x.Equal(1, latestOf(t, s))

	const why = "the database restored from the backup of 2026-10-01; the chunks since are ours"
	_, err = p.Accept(ctx, s, why)
	x.NoError(err)

	// After the store's newest, and naming it.
	x.Equal(4, latestOf(t, s))
	c := numbered(t, p, 4).c
	x.Equal(why, c.Accepted)
	x.Equal(third, c.Prev)

	v, err = p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(4, v.Checkpoint)

	// And the next act takes the next one, compared with the acceptance.
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(2 * day), value: "and on"})
	_, err = Archive(ctx, s, p, Kinds{}, time.Now())
	x.NoError(err)
	x.Equal(5, latestOf(t, s))

	v, err = p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(5, v.Checkpoint)
}
