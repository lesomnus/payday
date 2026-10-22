package trail

import (
	"bytes"
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

// rowsOf is the manifest's rows as the fake store keeps them, every state
// among them.
func (s *fakeStore) rowsOf() []Archived {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.archived)
}

// put adds a row the way somebody who can write the database does: as they
// like, beside nothing.
func (s *fakeStore) put(v Archived) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.keys++
	v.Key = s.keys
	v.Created, v.Updated = time.Now(), time.Now()
	s.archived = append(s.archived, v)
}

// archiveBy moves every row into the archive as [Archive] does, by hand, but
// through the policy's own manifest: signed, as a pass writes.
func archiveBy(t *testing.T, p Policy, s *fakeStore) {
	x, err := p.start(s)
	require.NoError(t, err)

	_, _, err = drain(t.Context(), s, Scope{}, time.Now(), x.p.Archive, x.run, nil, x.turn, func(Row) fate { return archived })
	require.NoError(t, err)
}

// there answers whether the archive still holds a chunk.
func there(t *testing.T, a flob.Stores, c Chunk) bool {
	_, err := unwrapStores(a).Use(c.Namespace).Stat(t.Context(), c.Digest)
	if errors.Is(err, flob.ErrNotExist) {
		return false
	}
	require.NoError(t, err)

	return true
}

func monthOf(at time.Time) time.Time {
	at = at.UTC()
	return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// TestEveryRowIsSignedAsItIsWritten.
//
// Each state a row is ever in -- added, present, erased -- carries the key's
// signature over what it says and which row it is, whichever act wrote it.
func TestEveryRowIsSignedAsItIsWritten(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	key := newKey(t)
	s, p, tenant := manifestedArchive(t, key)
	_, err := p.Forget(ctx, s, []pdid.Id{tenant})
	x.NoError(err)
	_, err = p.PurgeTenant(ctx, s, tenant)
	x.NoError(err)

	states := map[string]int{}
	for _, v := range s.rowsOf() {
		states[v.State]++
		x.NotEmpty(v.Signature.Sig, "%s %s %s is not signed", v.State, v.Namespace, v.Digest)
		x.True(verified(v.Signature.Key, statement(v), v.Signature.Sig), "%s %s %s", v.State, v.Namespace, v.Digest)
		x.True(trusts([]crypto.PublicKey{key.Public()}, v.Signature.Key))
	}
	x.Positive(states[Present])
	x.Positive(states[Erased], "nothing was erased, so no erase was seen signed")

	v, err := p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)

	// And believed by the half of the key a verifier is given.
	v, err = Policy{Archive: p.Archive, Trust: []crypto.PublicKey{key.Public()}}.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)

	// A row being added is signed before its blob is written.
	k, err := s.Adding(ctx, tenant.String(), "an-intent", map[string]string{"a": "b"}, p.signing())
	x.NoError(err)
	for _, v := range s.rowsOf() {
		if v.Key == k {
			x.Equal(Adding, v.State)
			x.True(verified(v.Signature.Key, statement(v), v.Signature.Sig))
		}
	}
}

// TestARowChangedWithoutTheKeyIsFound.
//
// Somebody who can write both the database and the archive, after the latest
// checkpoint and without the key: whatever they make a row say together with
// its blob is a finding, and nothing an act decides by.
func TestARowChangedWithoutTheKeyIsFound(t *testing.T) {
	ctx := t.Context()
	long := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	setup := func(t *testing.T) (*fakeStore, Policy, pdid.Id, Chunk) {
		s, p, tenant := manifestedArchive(t, newKey(t))
		cs, err := chunksIn(ctx, p.Archive, tenant.String())
		require.NoError(t, err)
		require.NotEmpty(t, cs)

		return s, p, tenant, cs[0]
	}

	t.Run("a chunk and its row both made to look old", func(t *testing.T) {
		x := require.New(t)
		s, p, _, c := setup(t)

		old := c
		old.Month, old.First, old.Last = long, long, long.Add(day)
		x.NoError(p.Archive.Use(c.Namespace).Label(ctx, c.Digest, old.labels()))
		s.mu.Lock()
		for i, v := range s.archived {
			if v.Digest == string(c.Digest) {
				s.archived[i].Labels = labelsOf(old.labels())
			}
		}
		s.mu.Unlock()

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"forged"}, kinds(v), "the two agree, and the row says what the key never signed")
		x.Equal(c.Digest, v.Findings[0].Digest)

		p.Keep.Destroy = year
		p.Pass(ctx, s)
		x.True(there(t, p.Archive, c), "a pass destroyed a chunk by a row nobody signed")
	})

	t.Run("a chunk put there with a row to match", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant, _ := setup(t)

		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(35 * day), value: "planted"}
		c := Chunk{Namespace: tenant.String(), Kind: "person", Month: monthOf(r.created), Run: "x", Rows: 1, First: r.created, Last: r.created}
		d := plant(t, p.Archive, c, r.doc())
		s.put(Archived{Namespace: c.Namespace, Digest: string(d), Labels: labelsOf(c.labels()), State: Present})

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v), "and only that: its blob is the row's finding")
		x.Equal(d, v.Findings[0].Digest)

		x.Equal([]string{"a", "b", "c"}, values(t, func(fn func([]byte) error) error { return p.ReadTenant(ctx, s, tenant, fn) }),
			"what a row nobody signed accounts for was read as the trail")
	})

	t.Run("a chunk put there with a row to match is not folded in", func(t *testing.T) {
		x := require.New(t)

		// Two chunks of one tenant, kind and month, which a pass folds into
		// one -- and a third beside them that nobody wrote, with a row that
		// says it belongs. Folding it in would copy what it says into a chunk
		// the key signs.
		tenant := newTenant()
		month := monthOf(time.Now().AddDate(0, -3, 0))
		s := &fakeStore{}
		p := Policy{Archive: flob.NewMemStores(), Signer: newKey(t)}
		for i, v := range []string{"a", "b"} {
			s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: month.Add(time.Duration(10+2*i) * day), value: v})
			archiveBy(t, p, s)
		}

		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: month.Add(11 * day), value: "planted"}
		c := Chunk{Namespace: tenant.String(), Kind: "person", Month: month, Run: "x", Rows: 1, First: r.created, Last: r.created}
		d := plant(t, p.Archive, c, r.doc())
		s.put(Archived{Namespace: c.Namespace, Digest: string(d), Labels: labelsOf(c.labels()), State: Present})

		p.Pass(ctx, s)
		cs, err := chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)
		x.True(slices.ContainsFunc(cs, func(v Chunk) bool { return v.Rows == 2 }), "the fold")
		x.Equal([]string{"a", "b"}, values(t, func(fn func([]byte) error) error { return p.ReadTenant(ctx, s, tenant, fn) }),
			"what a row nobody signed accounts for was folded in")
	})

	t.Run("a row signed by a key nobody trusts", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant, _ := setup(t)

		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(35 * day), value: "planted"}
		c := Chunk{Namespace: tenant.String(), Kind: "person", Month: monthOf(r.created), Run: "x", Rows: 1, First: r.created, Last: r.created}
		d := plant(t, p.Archive, c, r.doc())

		row := Archived{Namespace: c.Namespace, Digest: string(d), Labels: labelsOf(c.labels()), State: Present}
		s.put(row)
		s.mu.Lock()
		last := len(s.archived) - 1
		sig, err := Policy{Signer: newKey(t)}.signRow(s.archived[last])
		x.NoError(err)
		s.archived[last].Signature = sig
		s.mu.Unlock()

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"forged"}, kinds(v))
		x.Contains(v.Findings[0].Note, "nobody trusts")
	})

	t.Run("a row copied, signature and all", func(t *testing.T) {
		x := require.New(t)
		s, p, _, c := setup(t)

		// Saying what the first says, word for word: the key signed that row,
		// and not this one.
		for _, v := range s.rowsOf() {
			if v.Digest == string(c.Digest) {
				s.put(v)
				s.mu.Lock()
				s.archived[len(s.archived)-1].Signature = v.Signature
				s.mu.Unlock()
			}
		}

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"forged"}, kinds(v))
		x.Equal(c.Digest, v.Findings[0].Digest)
	})

	t.Run("a chunk taken away beneath a row marked erased", func(t *testing.T) {
		x := require.New(t)
		s, p, _, c := setup(t)

		// Which the latest checkpoint is recomputed to match: the row was in it,
		// and is gone only from the next one.
		x.NoError(s.Mark(ctx, c.Namespace, string(c.Digest), Present, Erased, nobody))
		x.NoError(unwrapStores(p.Archive).Use(c.Namespace).Erase(ctx, c.Digest))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v), "an erase nobody signed")
		x.Equal(c.Digest, v.Findings[0].Digest)

		// And on the next pass, from the checkpoint before the latest.
		p.Pass(ctx, s)
		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v))
	})

	t.Run("an erase nobody signed is not settled", func(t *testing.T) {
		x := require.New(t)
		s, p, _, c := setup(t)

		backdate := func() {
			s.mu.Lock()
			for i := range s.archived {
				s.archived[i].Updated = s.archived[i].Updated.Add(-2 * day)
			}
			s.mu.Unlock()
		}

		// Its chunk gone, which would make it erased; settling it would be the
		// key saying so.
		x.NoError(s.Mark(ctx, c.Namespace, string(c.Digest), Present, Erasing, nobody))
		x.NoError(unwrapStores(p.Archive).Use(c.Namespace).Erase(ctx, c.Digest))
		backdate()
		x.NoError(p.settle(ctx, s), "settling tried, and was refused")
		p.Pass(ctx, s)
		x.Equal(Erasing, s.state(c.Namespace, c.Digest), "a pass signed an erase nobody made")

		// Nor by anything else that moves a row along.
		x.ErrorIs(s.Mark(ctx, c.Namespace, string(c.Digest), Erasing, Erased, p.signing()), errUnvouched)
		x.Equal(Erasing, s.state(c.Namespace, c.Digest))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v))

		// Its chunk there, which would undo it, as present and signed.
		s, p, _, c = setup(t)
		x.NoError(s.Mark(ctx, c.Namespace, string(c.Digest), Present, Erasing, nobody))
		backdate()
		p.Pass(ctx, s)
		x.Equal(Erasing, s.state(c.Namespace, c.Digest), "a pass signed what somebody's row said")
		x.True(there(t, p.Archive, c))
	})

	t.Run("an add nobody signed is not settled", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant, _ := setup(t)

		// A row being added, and a blob labelled with its intent, which a pass
		// would take for an add a crash stopped.
		_, err := s.Adding(ctx, tenant.String(), "planted", nil, nobody)
		x.NoError(err)
		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(35 * day), value: "planted"}
		c := Chunk{Namespace: tenant.String(), Kind: "person", Month: monthOf(r.created), Run: "x", Rows: 1, First: r.created, Last: r.created}
		l := c.labels()
		l.Set(labelIntent, "planted")
		m, err := p.Archive.Use(c.Namespace).Add(ctx, flob.Meta{Labels: l}, bytes.NewReader(gz(t, r.doc())))
		x.NoError(err)

		p.Pass(ctx, s)
		x.Len(adding(s), 1, "a pass settled an add nobody signed")
		x.Empty(s.state(c.Namespace, m.Digest))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v), "and only that: its blob is the row's finding")
	})
}

// keyless is an archive as a payday that kept it without a key left it: a
// tenant's chunk, the manifest's row for it with no signature, and a
// checkpoint over it that nobody signed.
func keyless(t *testing.T) (*fakeStore, Policy, Chunk, flob.Digest) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()
	r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(400 * day), value: "a"}
	c := Chunk{Namespace: tenant.String(), Kind: "person", Month: monthOf(r.created), Rows: 1, First: r.created, Last: r.created}

	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}}
	c.Digest = plant(t, p.Archive, c, r.doc())

	s := &fakeStore{}
	key, err := s.Adding(ctx, c.Namespace, "", labelsOf(c.labels()), nobody)
	x.NoError(err)
	x.NoError(s.Added(ctx, key, string(c.Digest), labelsOf(c.labels()), nobody))

	k, rows, err := s.Checkpoint(ctx, 0)
	x.NoError(err)

	return s, p, c, planted(t, p, checkpointOf(k, time.Now(), rows), nil)
}

// TestAnArchiveKeptWithoutAKeyIsAcceptedOnce.
//
// A deployment that archived without a key, and then has one (#115): its rows
// say nothing the key signed and neither do its checkpoints, so each is a
// finding and nothing an act decides by -- a chunk past its window is kept --
// and no act takes a checkpoint after them, which would leave them off the
// chain for good. An operator accepts once: the rows signed as they stand, and
// the checkpoint the database numbered last named by the acceptance, which
// takes in the history before the key. Accepting is signing, so a policy with
// keys to trust and none to sign with accepts nothing.
func TestAnArchiveKeptWithoutAKeyIsAcceptedOnce(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, p, c, first := keyless(t)

	key := newKey(t)
	p.Signer = key
	p.Keep.Destroy = year

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"unsigned"}, kinds(v))
	x.Len(v.Findings, 2, "the row and the checkpoint: %v", v.Findings)

	p.Pass(ctx, s)
	x.True(there(t, p.Archive, c), "a pass destroyed a chunk by a row nobody signed")
	x.Equal(1, latestOf(t, s), "a checkpoint was taken after a history nobody signed")

	s.add(fakeRow{tenant: newTenant().Uuid(), domain: person, created: ago(40 * day), value: "after the key"})
	_, err = Archive(ctx, s, p, Kinds{}, time.Now())
	x.ErrorIs(err, errUnkeyed)
	x.Equal(1, latestOf(t, s))

	_, err = accept(t, Policy{Archive: p.Archive, Trust: []crypto.PublicKey{key.Public()}}, s, Acceptance{Why: "an archive kept without a key"})
	x.ErrorIs(err, ErrNoKey, "accepted without the key")

	_, err = accept(t, p, s, Acceptance{Why: "an archive kept without a key until now"})
	x.NoError(err)
	x.Equal(2, latestOf(t, s))
	x.Equal(first, numbered(t, p, 2).c.Prev, "the acceptance did not take in the history before the key")

	v, err = p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)

	p.Pass(ctx, s)
	x.False(there(t, p.Archive, c), "a chunk outlived its window once its row was accepted")
	x.Equal(3, latestOf(t, s))
}

// TestAnAcceptanceGoesOnFromWhatTheDatabaseNumbered: of the checkpoints
// nobody signed that say they are the one the database numbered last, an
// acceptance of an archive kept without a key names the one the database's
// rows add up to -- not whichever is first by digest, which anybody who can
// write the checkpoint store can make theirs -- and the others stay findings.
// Before the database has numbered one, what the store keeps is nobody's
// history, and stops no act.
func TestAnAcceptanceGoesOnFromWhatTheDatabaseNumbered(t *testing.T) {
	t.Run("of two numbered alike", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()

		s, p, _, first := keyless(t)
		p.Signer = newKey(t)

		for i := 0; ; i++ {
			x.Less(i, 64, "no checkpoint first by digest")
			if planted(t, p, over(1), nil) < first {
				break
			}
		}

		_, err := accept(t, p, s, Acceptance{Why: "an archive kept without a key until now"})
		x.NoError(err)
		x.Equal(first, numbered(t, p, 2).c.Prev, "the acceptance went on from one the database never took")

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v), "%v", v.Findings)
	})
	t.Run("before the database numbered any", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()

		// Numbered as the database has numbered, and as its first will be.
		s := &fakeStore{}
		p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Signer: newKey(t)}
		planted(t, p, over(0), nil)
		planted(t, p, over(1), nil)

		x.NoError(oneMore(t, s, p), "what the store kept before the database numbered one stopped an act")
		x.Equal(1, latestOf(t, s))
		x.Empty(numbered(t, p, 1).c.Prev, "the first checkpoint went on from somebody's")

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v), "%v", v.Findings)
		x.Len(v.Findings, 2, "%v", v.Findings)
	})
}

// TestAnArchiveIsKeptWithAKey.
//
// There is no archive without a key (#115). A policy with one and no key to
// sign or trust with is refused, an act asked of one is refused before it
// writes anything, and a verifier -- the public half, and nothing to sign
// with -- holds every row and checkpoint to it, and takes no act.
func TestAnArchiveIsKeptWithAKey(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	key := newKey(t)
	s, p, tenant := manifestedArchive(t, key)
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(40 * day), value: "more"})

	none := Policy{Archive: p.Archive, Keep: p.Keep}
	x.ErrorIs(none.Valid(), ErrNoKey)

	before := s.rowsOf()
	acts := map[string]func() error{
		"archive": func() error {
			_, err := Archive(ctx, s, none, Kinds{}, time.Now())
			return err
		},
		"forget": func() error {
			_, err := none.Forget(ctx, s, []pdid.Id{tenant})
			return err
		},
		"purge": func() error {
			_, _, err := none.Purge(ctx, s, Before(time.Now()))
			return err
		},
		"a tenant's purge": func() error {
			_, err := none.PurgeTenant(ctx, s, tenant)
			return err
		},
		"discard": func() error {
			_, _, err := none.Collect(ctx, s, Kinds{}, time.Now())
			return err
		},
		"accept": func() error {
			_, err := accept(t, none, s, Acceptance{Why: "looked at"})
			return err
		},
	}
	for name, act := range acts {
		x.ErrorIs(act(), ErrNoKey, name)
	}
	none.Pass(ctx, s)
	x.Equal(before, s.rowsOf(), "an act without a key wrote the manifest")
	x.Equal(1, latestOf(t, s), "an act without a key took a checkpoint")

	// Nor does it take any row on no key's word: none is signed by a key it
	// trusts.
	v, err := none.Verify(ctx, s, false)
	x.NoError(err)
	x.False(v.Ok(), "a policy with no key believed the rows")
	x.Contains(kinds(v), "forged")

	verifier := Policy{Archive: p.Archive, Trust: []crypto.PublicKey{key.Public()}}
	x.NoError(verifier.Valid())
	x.Contains(verifier.String(), "nothing to sign with")
	v, err = verifier.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	_, err = Archive(ctx, s, verifier, Kinds{}, time.Now())
	x.ErrorIs(err, ErrNoKey)

	// A row nobody signed is a finding to it, as to the deployment.
	s.put(Archived{Namespace: tenant.String(), Digest: string(flob.DigestFromBytes([]byte("a chunk nobody wrote"))), State: Present})
	v, err = verifier.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"unsigned"}, kinds(v))

	// Nor does it write the manifest when a question would fill it: what it
	// would write, it has nothing to sign.
	fresh := Policy{Archive: flob.NewMemStores(), Trust: []crypto.PublicKey{key.Public()}}
	r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(40 * day)}
	plant(t, fresh.Archive, Chunk{Namespace: tenant.String(), Kind: "person", Month: monthOf(r.created), Rows: 1, First: r.created, Last: r.created}, r.doc())
	empty := &fakeStore{}
	_, _, err = fresh.Doomed(ctx, empty, Before(time.Now()))
	x.ErrorIs(err, ErrNoKey)
	x.Empty(empty.rowsOf(), "a verifier wrote rows nobody signed")
}

// TestWithNoManifestNoChunkIsTrusted: what an act decides by is the labels the
// manifest's signed rows recorded, and with no manifest there are none -- a
// chunk's own labels are whoever wrote them.
func TestWithNoManifestNoChunkIsTrusted(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()
	p := Policy{Archive: flob.NewMemStores(), Signer: signer}
	r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(400 * day)}
	plant(t, p.Archive, Chunk{Namespace: tenant.String(), Kind: "person", Month: monthOf(r.created), Rows: 1, First: r.created, Last: r.created}, r.doc())

	x2, err := p.start(nil)
	x.NoError(err)
	trusted, rest, err := x2.chunks(ctx, []string{tenant.String()}, false)
	x.NoError(err)
	x.Empty(trusted, "a chunk was decided by its own labels")
	x.Len(rest, 1)
}
