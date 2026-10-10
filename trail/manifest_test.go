package trail

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	return k
}

// kinds is what a verification found, by kind.
func kinds(v Verified) []string {
	out := []string{}
	for _, f := range v.Findings {
		out = append(out, f.Kind)
	}
	slices.Sort(out)

	return slices.Compact(out)
}

// plant adds a blob the way somebody who is not a pass would: past the
// manifest, with the labels that make a pass take it for a chunk.
func plant(t *testing.T, a flob.Stores, c Chunk, docs ...[]byte) flob.Digest {
	buf := &bytes.Buffer{}
	z := gzip.NewWriter(buf)
	for _, doc := range docs {
		_, err := z.Write(append(doc, '\n'))
		require.NoError(t, err)
	}
	require.NoError(t, z.Close())

	m, err := a.Use(c.Namespace).Add(t.Context(), flob.Meta{Labels: c.labels()}, buf)
	require.NoError(t, err)

	return m.Digest
}

// manifestedArchive is a store whose rows are old enough to be on their way
// out, a policy that moves them, and the pass that did: a tenant's rows, and
// one another tenant wrote into.
func manifestedArchive(t *testing.T, key ed25519.PrivateKey) (*fakeStore, Policy, pdid.Id) {
	tenant, other := newTenant(), newTenant()

	s := &fakeStore{}
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(40 * day), value: "a"})
	s.add(fakeRow{tenant: tenant.Uuid(), domain: robot, created: ago(40 * day), value: "b"})
	s.add(fakeRow{tenant: other.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(40 * day), value: "c"})

	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Key: key}
	p.Pass(t.Context(), s)

	return s, p, tenant
}

func (s *fakeStore) state(ns string, d flob.Digest) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := ""
	for _, v := range s.archived {
		if v.Namespace == ns && v.Digest == string(d) {
			out = v.State
		}
	}

	return out
}

// TestEveryChangeIsInTheManifest.
//
// A pass and each of the acts taken by hand go through the manifest, so that
// it says what the archive holds after every one; and a pass ends with a
// checkpoint, signed.
func TestEveryChangeIsInTheManifest(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	key := newKey(t)
	s, p, tenant := manifestedArchive(t, key)

	inv, err := inventory(ctx, p.Archive)
	x.NoError(err)
	x.NotEmpty(inv)
	for k := range inv {
		x.Equal(Present, s.state(k.ns, k.d), "%s %s is not in the manifest", k.ns, k.d)
	}

	v, err := p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(v.Blobs, v.Hashed)
	x.Equal(1, v.Checkpoint, "the pass left no checkpoint")

	_, err = p.Forget(ctx, s, []pdid.Id{tenant})
	x.NoError(err)
	_, _, err = p.Purge(ctx, s, Before(time.Now()))
	x.NoError(err)
	_, err = p.PurgeTenant(ctx, s, tenant)
	x.NoError(err)

	v, err = p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)

	// Signed, and believed by the half of the key a verifier is given.
	cs := []Checkpoint{}
	x.NoError(p.ReadCheckpoints(ctx, func(c Checkpoint) error { cs = append(cs, c); return nil }))
	x.Len(cs, 1)
	x.Equal(cs[0].Count, len(cs[0].Blobs))
	v, err = Policy{Archive: p.Archive, Trust: []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}}.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)

	// The next pass, the next checkpoint.
	p.Pass(ctx, s)
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(2, v.Checkpoint)
}

// TestAnArchiveFromBeforeTheManifestIsTakenAsItIs.
func TestAnArchiveFromBeforeTheManifestIsTakenAsItIs(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()
	a := flob.NewMemStores()
	r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(400 * day)}
	old, err := put(ctx, a, Chunk{Namespace: tenant.String(), Kind: "person", Month: time.Now().AddDate(-1, -2, 0), Run: "before",
		Rows: 1, First: r.created, Last: r.created}, [][]byte{r.doc()})
	x.NoError(err)

	s := &fakeStore{}
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(40 * day)})
	p := Policy{Archive: a, Keep: Keep{Retain: 30 * day}}
	p.Pass(ctx, s)

	x.Equal(Present, s.state(tenant.String(), old), "what was there before was not taken in")

	v, err := p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
}

// TestWhatChangesBehindTheManifestsBackIsFound.
//
// A chunk taken away, one put there, one relabelled, one whose bytes changed:
// each is a finding, and a pass does not fold or rewrite what the manifest
// does not account for.
func TestWhatChangesBehindTheManifestsBackIsFound(t *testing.T) {
	ctx := t.Context()

	setup := func(t *testing.T) (*fakeStore, Policy, Chunk) {
		s, p, tenant := manifestedArchive(t, newKey(t))
		cs, err := chunksIn(ctx, p.Archive, tenant.String())
		require.NoError(t, err)
		require.NotEmpty(t, cs)

		return s, p, cs[0]
	}

	t.Run("a chunk taken away", func(t *testing.T) {
		x := require.New(t)
		s, p, c := setup(t)

		x.NoError(p.Archive.Use(c.Namespace).Erase(ctx, c.Digest))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"missing"}, kinds(v))
		x.Equal(c.Digest, v.Findings[0].Digest)
	})

	t.Run("a chunk put there", func(t *testing.T) {
		x := require.New(t)

		// Two chunks of one tenant, kind and month, which a fold makes one --
		// and a third that nobody wrote. The first two by hand, which folds
		// nothing: a pass would fold them before there was a third.
		tenant := newTenant()
		month := time.Now().AddDate(0, -3, 0)
		month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)

		s := &fakeStore{}
		p := Policy{Archive: flob.NewMemStores(), Key: newKey(t)}
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: month.Add(10 * day), value: "a"})
		_, err := Archive(ctx, s, Kinds{}, time.Now(), p.Archive)
		x.NoError(err)
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: month.Add(12 * day), value: "b"})
		_, err = Archive(ctx, s, Kinds{}, time.Now(), p.Archive)
		x.NoError(err)

		cs, err := chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)
		x.Len(cs, 2)

		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: month.Add(11 * day), value: "planted"}
		planted := plant(t, p.Archive, Chunk{Namespace: tenant.String(), Kind: "person", Month: month, Run: "x", Rows: 1,
			First: r.created, Last: r.created}, r.doc())

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unaccounted"}, kinds(v))

		// The month is folded -- without the chunk nobody wrote.
		p.Pass(ctx, s)
		cs, err = chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)
		x.Len(cs, 2, "the two chunks were not folded, or the planted one was folded in")
		x.True(slices.ContainsFunc(cs, func(v Chunk) bool { return v.Digest == planted }), "a pass took the planted chunk in")
		x.True(slices.ContainsFunc(cs, func(v Chunk) bool { return v.Rows == 2 }), "the fold")

		rs := receiptsIn(t, p.Archive)
		x.True(slices.ContainsFunc(rs, func(r Receipt) bool { return r.Act == "finding" && r.Findings["unaccounted"] == 1 }),
			"a pass found it and kept no record of finding it")

		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unaccounted"}, kinds(v), "the fold is not in the manifest")

		// An operator who has looked, and says the archive is right.
		_, err = p.Accept(ctx, s, "restored from the backup of 2026-10-01")
		x.NoError(err)
		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)
	})

	t.Run("a chunk relabelled", func(t *testing.T) {
		x := require.New(t)
		s, p, c := setup(t)

		l := c.labels()
		l.Set(labelLast, time.Now().AddDate(-5, 0, 0).Format(time.RFC3339Nano))
		x.NoError(p.Archive.Use(c.Namespace).Label(ctx, c.Digest, l))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"relabelled"}, kinds(v))
	})

	t.Run("a chunk's bytes", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		tenant := newTenant()
		s := &fakeStore{}
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(40 * day), value: "a"})
		p := Policy{Archive: flob.NewOsStores(dir), Keep: Keep{Retain: 30 * day}}
		p.Pass(ctx, s)

		cs, err := chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)
		x.Len(cs, 1)

		enc := cs[0].Digest.Encoded()
		path := filepath.Join(dir, "share", cs[0].Digest.Algorithm().String(), enc[0:2], enc[2:4], enc[4:])
		b, err := os.ReadFile(path)
		x.NoError(err)
		x.NoError(os.Chmod(path, 0o600))
		x.NoError(os.WriteFile(path, bytes.ToUpper(b), 0o600))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "a light verification read bytes")

		v, err = p.Verify(ctx, s, true)
		x.NoError(err)
		x.Equal([]string{"altered"}, kinds(v))
	})
}

// TestWhatIsInFlightIsNoFindingAndACrashIsSettled.
//
// A write is a row in adding and a blob labelled with its intent; an erase is a
// row in erasing. Neither is a finding while it is in flight, and a crash that
// stops one is settled by the next pass from what the store holds.
func TestWhatIsInFlightIsNoFindingAndACrashIsSettled(t *testing.T) {
	ctx := t.Context()

	t.Run("a write", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, nil)

		// Stopped after the blob and before the row says so.
		key, err := s.Adding(ctx, tenant.String(), "the-intent", nil)
		x.NoError(err)
		c := Chunk{Namespace: tenant.String(), Kind: "person", Month: time.Now(), Rows: 1}
		l := c.labels()
		l.Set(labelIntent, "the-intent")
		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), domain: person, created: ago(time.Hour)}
		m, err := p.Archive.Use(c.Namespace).Add(ctx, flob.Meta{Labels: l}, bytes.NewReader(gz(t, r.doc())))
		x.NoError(err)
		_ = key

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "a write in flight is a finding: %v", v.Findings)

		p.Pass(ctx, s)
		x.Equal(Present, s.state(tenant.String(), m.Digest), "the next pass did not settle it")
	})

	t.Run("a write that never happened", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, nil)

		_, err := s.Adding(ctx, tenant.String(), "lost", nil)
		x.NoError(err)
		p.Pass(ctx, s)
		x.Len(adding(s), 1, "a write younger than a day may still be being made")

		s.mu.Lock()
		for i := range s.archived {
			s.archived[i].Created = s.archived[i].Created.Add(-2 * day)
		}
		s.mu.Unlock()
		p.Pass(ctx, s)
		x.Empty(adding(s))
	})

	t.Run("an erase", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, nil)
		cs, err := chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)
		c := cs[0]

		// Stopped after the blob went and before the row says so.
		x.NoError(s.Mark(ctx, c.Namespace, string(c.Digest), Present, Erasing))
		x.NoError(unwrapStores(p.Archive).Use(c.Namespace).Erase(ctx, c.Digest))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "an erase in flight is a finding: %v", v.Findings)

		p.Pass(ctx, s)
		x.Equal(Erased, s.state(c.Namespace, c.Digest), "the next pass did not settle it")
	})

	t.Run("an erase the database alone asks for", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, nil)
		cs, err := chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)
		c := cs[0]

		// Somebody with the database and nothing else, and a chunk nowhere
		// near the end of its window.
		x.NoError(s.Mark(ctx, c.Namespace, string(c.Digest), Present, Erasing))
		p.Pass(ctx, s)

		cs, err = chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)
		x.True(slices.ContainsFunc(cs, func(v Chunk) bool { return v.Digest == c.Digest }), "a row in the database destroyed a chunk")
		x.Equal(Present, s.state(c.Namespace, c.Digest))
	})
}

func adding(s *fakeStore) []Archived {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.DeleteFunc(slices.Clone(s.archived), func(v Archived) bool { return v.State != Adding })
}

func gz(t *testing.T, docs ...[]byte) []byte {
	buf := &bytes.Buffer{}
	z := gzip.NewWriter(buf)
	for _, doc := range docs {
		_, err := z.Write(append(doc, '\n'))
		require.NoError(t, err)
	}
	require.NoError(t, z.Close())

	return buf.Bytes()
}

// TestACheckpointIsWhatTheArchiveHeld.
//
// The latest checkpoint is recomputed from the manifest's rows, so the
// manifest cannot be rewritten under it unseen; and a checkpoint is believed by
// its signature.
func TestACheckpointIsWhatTheArchiveHeld(t *testing.T) {
	ctx := t.Context()

	t.Run("a row taken out of the database", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, newKey(t))
		cs, err := chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)

		// The chunk and its row both, by somebody who can write the two.
		x.NoError(unwrapStores(p.Archive).Use(cs[0].Namespace).Erase(ctx, cs[0].Digest))
		s.mu.Lock()
		s.archived = slices.DeleteFunc(s.archived, func(v Archived) bool { return v.Digest == string(cs[0].Digest) })
		s.mu.Unlock()

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"rewritten"}, kinds(v), "the archive and the manifest agree, and the checkpoint does not")
	})

	t.Run("the latest checkpoint taken away", func(t *testing.T) {
		x := require.New(t)
		s, p, _ := manifestedArchive(t, newKey(t))

		ks, err := checkpointsIn(ctx, p.checkpoints())
		x.NoError(err)
		x.NoError(p.checkpoints().Erase(ctx, ks[1]))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unkept"}, kinds(v))
	})

	t.Run("a checkpoint by a key nobody trusts", func(t *testing.T) {
		x := require.New(t)
		key := newKey(t)
		s, p, _ := manifestedArchive(t, key)

		forger := p
		forger.Key = newKey(t)
		forger.Pass(ctx, s)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"forged"}, kinds(v))
	})

	t.Run("a checkpoint with no signature after signed ones", func(t *testing.T) {
		x := require.New(t)
		s, p, _ := manifestedArchive(t, newKey(t))

		unsigned := p
		unsigned.Key = nil
		unsigned.Pass(ctx, s)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v))
	})

	t.Run("a deployment that was given a key later", func(t *testing.T) {
		x := require.New(t)
		s, p, _ := manifestedArchive(t, nil)

		p.Key = newKey(t)
		p.Pass(ctx, s)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)
	})

	t.Run("a manifest emptied beside checkpoints", func(t *testing.T) {
		x := require.New(t)
		s, p, _ := manifestedArchive(t, nil)
		was, err := inventory(ctx, p.Archive)
		x.NoError(err)

		// A database restored from before the first pass.
		s.mu.Lock()
		s.archived, s.counter = nil, 0
		s.mu.Unlock()

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Contains(kinds(v), "emptied")

		p.Pass(ctx, s)
		for k := range was {
			x.Empty(s.state(k.ns, k.d), "the manifest was filled from an archive that has checkpoints")
		}
		ks, err := checkpointsIn(ctx, p.checkpoints())
		x.NoError(err)
		x.Len(ks, 1, "a checkpoint was written under a number one already has")

		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.Contains(kinds(v), "behind")
		x.Contains(kinds(v), "unaccounted", "what the archive held before is in a manifest nobody filled")
	})

	t.Run("on a store of their own", func(t *testing.T) {
		x := require.New(t)

		worm := flob.NewMemStores().Use("checkpoints")
		s := &fakeStore{}
		s.add(fakeRow{tenant: newTenant().Uuid(), domain: person, created: ago(40 * day)})
		p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Checkpoints: worm}
		p.Pass(ctx, s)

		ks, err := checkpointsIn(ctx, worm)
		x.NoError(err)
		x.Len(ks, 1)
		nss, err := namespaces(ctx, p.Archive)
		x.NoError(err)
		x.NotContains(nss, CheckpointNamespace)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)
	})
}

// TestTwoPassesAtOnce.
//
// Nothing locks a pass, as nothing ever did: two at once are put in order by
// the manifest's rows, and neither is a finding for the other.
func TestTwoPassesAtOnce(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s := &fakeStore{}
	for range 50 {
		s.add(fakeRow{tenant: newTenant().Uuid(), domain: person, created: ago(40 * day)})
	}
	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day, Destroy: 35 * day}}

	wg := sync.WaitGroup{}
	for range 2 {
		wg.Go(func() { p.Pass(ctx, s) })
	}
	wg.Wait()

	v, err := p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.False(slices.ContainsFunc(receiptsIn(t, p.Archive), func(r Receipt) bool { return r.Act == "finding" }),
		"a pass found the other one")
}

// TestAKeyIsWhatTheConfigurationReads.
func TestAKeyIsWhatTheConfigurationReads(t *testing.T) {
	x := require.New(t)

	doc, pub, err := GenerateKey()
	x.NoError(err)

	key, err := ParseKey(doc)
	x.NoError(err)
	k, err := ParsePublicKey(pub)
	x.NoError(err)
	x.True(k.Equal(key.Public()))

	_, err = ParseKey([]byte("not a key"))
	x.Error(err)
	_, err = ParsePublicKey("c2hvcnQ=")
	x.Error(err)
}
