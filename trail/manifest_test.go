package trail

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
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

func newKey(t *testing.T) crypto.Signer {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	return k
}

// nobody is what somebody who writes the database and holds no key signs a row
// with: nothing.
func nobody(_, _ Archived) (Signature, error) { return Signature{}, nil }

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
func manifestedArchive(t *testing.T, key crypto.Signer) (*fakeStore, Policy, pdid.Id) {
	tenant, other := newTenant(), newTenant()

	s := &fakeStore{}
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(40 * day), value: "a"})
	s.add(fakeRow{tenant: tenant.Uuid(), domain: robot, created: ago(40 * day), value: "b"})
	s.add(fakeRow{tenant: other.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(40 * day), value: "c"})

	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Signer: key}
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
// it says what the archive holds after every one; and each of them ends with a
// checkpoint, signed (#60).
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

	// One for the pass and one for each act, each naming the one before it,
	// signed, and believed by the half of the key a verifier is given.
	cs := []Checkpoint{}
	x.NoError(p.ReadCheckpoints(ctx, func(c Checkpoint) error { cs = append(cs, c); return nil }))
	x.Len(cs, 4)
	for i, c := range cs {
		x.Equal(i+1, c.K)
		x.Equal(c.Count, len(c.Blobs))
		if i > 0 {
			x.NotEmpty(c.Prev, "checkpoint %d names none before it", c.K)
		}
	}
	x.Equal(4, v.Checkpoint)
	v, err = Policy{Archive: p.Archive, Trust: []crypto.PublicKey{key.Public()}}.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)

	// The next pass, the next checkpoint.
	p.Pass(ctx, s)
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(5, v.Checkpoint)
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
		p := Policy{Archive: flob.NewMemStores()}
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: month.Add(10 * day), value: "a"})
		_, err := Archive(ctx, s, p, Kinds{}, time.Now())
		x.NoError(err)
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: month.Add(12 * day), value: "b"})
		_, err = Archive(ctx, s, p, Kinds{}, time.Now())
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

		path := blobPath(dir, cs[0].Digest)
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

// blobPath is where an [flob.OsStores] keeps a blob's bytes.
func blobPath(dir string, d flob.Digest) string {
	enc := d.Encoded()
	return filepath.Join(dir, "share", d.Algorithm().String(), enc[0:2], enc[2:4], enc[4:])
}

// values is the contents of what a read handed over, in order.
func values(t *testing.T, read func(fn func(doc []byte) error) error) []string {
	t.Helper()

	out := []string{}
	require.NoError(t, read(func(doc []byte) error {
		h, err := headOf(doc)
		require.NoError(t, err)

		b, err := base64.StdEncoding.DecodeString(h.Value)
		require.NoError(t, err)

		out = append(out, string(b))
		return nil
	}))
	slices.Sort(out)

	return out
}

// TestAnActDecidesByTheManifest.
//
// What the archive's credentials alone can change -- a chunk's labels, a chunk
// put there, a chunk's bytes -- changes nothing an act does: it decides by the
// labels the manifest recorded, reads only what the manifest accounts for, and
// what it writes again of the rest stays unaccounted for.
func TestAnActDecidesByTheManifest(t *testing.T) {
	ctx := t.Context()

	// relabel changes a chunk's labels the way somebody with the archive and
	// nothing else would.
	relabel := func(t *testing.T, a flob.Stores, c Chunk, fn func(*Chunk)) {
		v := c
		fn(&v)
		require.NoError(t, a.Use(c.Namespace).Label(ctx, c.Digest, v.labels()))
	}
	exists := func(t *testing.T, a flob.Stores, c Chunk) bool {
		_, err := a.Use(c.Namespace).Stat(ctx, c.Digest)
		if errors.Is(err, flob.ErrNotExist) {
			return false
		}
		require.NoError(t, err)

		return true
	}
	month := func(at time.Time) time.Time {
		at = at.UTC()
		return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	long := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("a chunk made to look old is not destroyed", func(t *testing.T) {
		x := require.New(t)

		tenant := newTenant()
		s := &fakeStore{}
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(40 * day), value: "a"})
		p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day, Destroy: year}}
		p.Pass(ctx, s)

		cs, err := chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)
		x.Len(cs, 1)
		c := cs[0]

		relabel(t, p.Archive, c, func(c *Chunk) { c.Month, c.First, c.Last = long, long, long.Add(day) })

		p.Pass(ctx, s)
		x.True(exists(t, p.Archive, c), "a pass destroyed a chunk by the labels on it")

		cut := Before(time.Now().Add(-year))
		doomed, _, err := p.Doomed(ctx, s, cut)
		x.NoError(err)
		x.Empty(doomed)
		gone, _, err := p.Purge(ctx, s, cut)
		x.NoError(err)
		x.Empty(gone)
		x.True(exists(t, p.Archive, c), "a purge destroyed a chunk by the labels on it")

		rs := receiptsIn(t, p.Archive)
		x.True(slices.ContainsFunc(rs, func(r Receipt) bool { return r.Act == "finding" && r.Findings["relabelled"] == 1 }),
			"the pass did not find it")
		x.False(slices.ContainsFunc(rs, func(r Receipt) bool { return r.Act == "destroy" || r.Act == "purge" }))
	})

	t.Run("nor one made to look new kept", func(t *testing.T) {
		x := require.New(t)

		tenant := newTenant()
		s := &fakeStore{}
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(400 * day), value: "a"})
		p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}}
		p.Pass(ctx, s)

		cs, err := chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)
		x.Len(cs, 1)
		c := cs[0]

		now := time.Now()
		relabel(t, p.Archive, c, func(c *Chunk) { c.Month, c.First, c.Last = month(now), now, now })

		// And the window shortened to a year.
		p.Keep.Destroy = year
		p.Pass(ctx, s)
		x.False(exists(t, p.Archive, c), "a chunk outlived its window by the labels on it")
	})

	t.Run("a chunk of everybody's is read by the tenants the manifest recorded", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, nil)

		cs, err := chunksIn(ctx, p.Archive, SharedNamespace)
		x.NoError(err)
		x.Len(cs, 1)

		stranger := newTenant()
		relabel(t, p.Archive, cs[0], func(c *Chunk) {
			c.Tenants = append(others(c.Tenants, tenant), stranger)
			slices.SortFunc(c.Tenants, byId)
		})

		x.Equal([]string{"a", "b", "c"}, values(t, func(fn func([]byte) error) error { return p.ReadTenant(ctx, s, tenant, fn) }))
		x.Empty(values(t, func(fn func([]byte) error) error { return p.ReadTenant(ctx, s, stranger, fn) }),
			"a tenant read what the labels on a chunk said was theirs")

		// What the archive says, which nothing checks.
		x.Equal([]string{"c"}, values(t, func(fn func([]byte) error) error { return ReadTenant(ctx, p.Archive, stranger, fn) }))
	})

	t.Run("a chunk put there is not read", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, nil)

		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(35 * day), value: "planted"}
		plant(t, p.Archive, Chunk{Namespace: tenant.String(), Kind: "person", Month: month(r.created), Run: "x", Rows: 1,
			First: r.created, Last: r.created}, r.doc())

		x.Equal([]string{"a", "b", "c"}, values(t, func(fn func([]byte) error) error { return p.ReadTenant(ctx, s, tenant, fn) }))
		x.Equal([]string{"a", "b", "c"}, values(t, func(fn func([]byte) error) error { return p.Read(ctx, s, fn) }))
		x.Contains(values(t, func(fn func([]byte) error) error { return ReadTenant(ctx, p.Archive, tenant, fn) }), "planted")
	})

	t.Run("a chunk whose bytes changed is not read", func(t *testing.T) {
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

		forged := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(40 * day), value: "forged"}
		path := blobPath(dir, cs[0].Digest)
		x.NoError(os.Chmod(path, 0o600))
		x.NoError(os.WriteFile(path, gz(t, forged.doc()), 0o600))

		err = p.ReadTenant(ctx, s, tenant, func([]byte) error { return nil })
		x.ErrorIs(err, errAltered)
		err = p.Read(ctx, s, func([]byte) error { return nil })
		x.ErrorIs(err, errAltered)

		x.Equal([]string{"forged"}, values(t, func(fn func([]byte) error) error { return ReadTenant(ctx, p.Archive, tenant, fn) }))
	})

	t.Run("forgetting blanks a chunk put there, and does not take it in", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, nil)

		who := uuid.NewV7()
		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), object: who, domain: person, created: ago(35 * day), value: "about them"}
		planted := plant(t, p.Archive, Chunk{Namespace: tenant.String(), Kind: "person", Month: month(r.created), Run: "x", Rows: 1,
			First: r.created, Last: r.created}, r.doc())

		_, err := p.Forget(ctx, s, []pdid.Id{pdid.Id(who)})
		x.NoError(err)

		in := docsIn(t, p.Archive)
		x.Contains(in, r.id)
		x.Empty(in[r.id]["value"], "what a chunk nobody accounts for held about them is still there")

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unaccounted"}, kinds(v), "what it was written again as is in the manifest")
		x.Len(v.Findings, 1)
		x.NotEqual(planted, v.Findings[0].Digest)
		x.Empty(s.state(tenant.String(), v.Findings[0].Digest))
	})

	t.Run("a tenant leaving takes what was put there with it, and takes none of it in", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, nil)

		// One in its namespace, and one of everybody's whose labels do not say
		// it is the tenant's and whose rows do.
		mine := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(35 * day), value: "mine"}
		plant(t, p.Archive, Chunk{Namespace: tenant.String(), Kind: "person", Month: month(mine.created), Run: "x", Rows: 1,
			First: mine.created, Last: mine.created}, mine.doc())

		a, b := newTenant(), newTenant()
		ids := []pdid.Id{a, b}
		slices.SortFunc(ids, byId)
		ours := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: a.Uuid(), domain: person, created: ago(35 * day), value: "ours"}
		plant(t, p.Archive, Chunk{Namespace: SharedNamespace, Kind: "person", Month: month(ours.created), Run: "x", Rows: 1,
			First: ours.created, Last: ours.created, Tenants: ids}, ours.doc())

		_, err := p.PurgeTenant(ctx, s, tenant)
		x.NoError(err)

		in := docsIn(t, p.Archive)
		x.NotContains(in, mine.id)
		x.Contains(in, ours.id, "the other tenant's evidence")
		x.Empty(in[ours.id]["value"])

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unaccounted"}, kinds(v), "what it was written again as is in the manifest")
		x.Len(v.Findings, 1)
		x.Equal(SharedNamespace, v.Findings[0].Namespace)
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
		key, err := s.Adding(ctx, tenant.String(), "the-intent", nil, p.signing())
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

		_, err := s.Adding(ctx, tenant.String(), "lost", nil, p.signing())
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
		x.NoError(s.Mark(ctx, c.Namespace, string(c.Digest), Present, Erasing, p.signing()))
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
		x.NoError(s.Mark(ctx, c.Namespace, string(c.Digest), Present, Erasing, nobody))
		p.Pass(ctx, s)

		cs, err = chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)
		x.True(slices.ContainsFunc(cs, func(v Chunk) bool { return v.Digest == c.Digest }), "a row in the database destroyed a chunk")
		x.Equal(Erasing, s.state(c.Namespace, c.Digest), "an erase younger than a day was undone")

		// And undone once nothing can be making it any more.
		s.mu.Lock()
		for i := range s.archived {
			s.archived[i].Updated = s.archived[i].Updated.Add(-2 * day)
		}
		s.mu.Unlock()
		p.Pass(ctx, s)
		x.Equal(Present, s.state(c.Namespace, c.Digest))
	})

	t.Run("an erase another pass is making", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, nil)
		cs, err := chunksIn(ctx, p.Archive, tenant.String())
		x.NoError(err)
		c := cs[0]

		// The other pass has marked it, and this one starts before it erases.
		x.NoError(s.Mark(ctx, c.Namespace, string(c.Digest), Present, Erasing, p.signing()))
		p.Pass(ctx, s)

		x.NoError(unwrapStores(p.Archive).Use(c.Namespace).Erase(ctx, c.Digest))
		x.NoError(s.Mark(ctx, c.Namespace, string(c.Digest), Erasing, Erased, p.signing()))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "the erase this pass took for a crash's: %v", v.Findings)
	})

	t.Run("a write of what another pass is erasing", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, nil)

		// Two passes moved the same rows, so the two chunks are one blob: the
		// second write finds the first's, and the first destroys it before
		// the second has said so.
		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(40 * day)}
		c := Chunk{Namespace: tenant.String(), Kind: "person", Month: r.created, Rows: 1, First: r.created, Last: r.created}

		first := p.manifested(s)
		m, err := first.Use(c.Namespace).Add(ctx, flob.Meta{Labels: c.labels()}, bytes.NewReader(gz(t, r.doc())))
		x.NoError(err)

		between := Policy{Archive: hooked{p.Archive, func() {
			x.NoError(first.Use(c.Namespace).Erase(ctx, m.Digest))
		}}}
		_, err = between.manifested(s).Use(c.Namespace).Add(ctx, flob.Meta{Labels: c.labels()}, bytes.NewReader(gz(t, r.doc())))
		x.ErrorIs(err, flob.ErrAlreadyExists)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "the second write outlived the blob it found: %v", v.Findings)
		x.Equal(Erased, s.state(c.Namespace, m.Digest))
	})
}

// hooked is an archive that runs `after` once a write has reached the store,
// and before the writer hears back: what another pass does in between.
type hooked struct {
	flob.Stores
	after func()
}

func (h hooked) Use(ns string) flob.Store { return hookedStore{h.Stores.Use(ns), h.after} }

type hookedStore struct {
	flob.Store
	after func()
}

func (h hookedStore) Add(ctx context.Context, m flob.Meta, r io.Reader) (flob.Meta, error) {
	v, err := h.Store.Add(ctx, m, r)
	h.after()

	return v, err
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

		// Which is also what a checkpoint being written looks like, for a
		// while: the database numbers it before the store keeps it.
		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "a checkpoint in flight is a finding: %v", v.Findings)

		s.mu.Lock()
		s.counterAt = s.counterAt.Add(-time.Hour)
		s.mu.Unlock()

		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unkept"}, kinds(v))
	})

	t.Run("a checkpoint by a key nobody trusts", func(t *testing.T) {
		x := require.New(t)
		key := newKey(t)
		s, p, _ := manifestedArchive(t, key)

		forger := p
		forger.Signer = newKey(t)
		forger.Pass(ctx, s)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"forged"}, kinds(v))
	})

	t.Run("a checkpoint with no signature after signed ones", func(t *testing.T) {
		x := require.New(t)
		s, p, _ := manifestedArchive(t, newKey(t))

		unsigned := p
		unsigned.Signer = nil
		unsigned.Pass(ctx, s)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v))
	})

	t.Run("a deployment that was given a key later", func(t *testing.T) {
		x := require.New(t)
		s, p, _ := manifestedArchive(t, nil)

		// Its checkpoints begin unsigned, which is no finding. Its rows were
		// written unsigned, which is one until an operator says they are
		// right: a row with no signature looks the same as one whose signature
		// somebody took off.
		p.Signer = newKey(t)
		p.Pass(ctx, s)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v))
		for _, f := range v.Findings {
			x.Zero(f.Checkpoint, "a checkpoint from before the key: %v", f)
		}

		_, err = p.Accept(ctx, s, "the rows from before the deployment had a key")
		x.NoError(err)
		v, err = p.Verify(ctx, s, false)
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

		// Until an operator accepts what the archive holds, which is numbered
		// after the checkpoint the store keeps, and goes on from there (#70).
		_, err = p.Accept(ctx, s, "a database restored from before the first pass")
		x.NoError(err)
		x.Equal(2, latestOf(t, s))
		for k := range was {
			x.Equal(Present, s.state(k.ns, k.d))
		}

		v, err = p.Verify(ctx, s, true)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)
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

// device is a signer whose private half does not come out: a TPM, an HSM, a
// cloud KMS, as the app hands one in.
type device struct{ k crypto.Signer }

func (d device) Public() crypto.PublicKey { return d.k.Public() }

func (d device) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return d.k.Sign(r, digest, opts)
}

// TestACheckpointIsSignedByWhatHoldsTheKey.
//
// A key read from a file, or one in a device that signs and gives nothing
// out: Ed25519, or the ECDSA and RSA a TPM signs with. Each is believed by its
// public half, and by nothing else.
func TestACheckpointIsSignedByWhatHoldsTheKey(t *testing.T) {
	ctx := t.Context()

	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rs, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	for name, key := range map[string]crypto.Signer{
		"ed25519":           newKey(t),
		"ecdsa":             ec,
		"rsa":               rs,
		"ecdsa in a device": device{ec},
		"rsa in a device":   device{rs},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			s, p, _ := manifestedArchive(t, key)

			v, err := Policy{Archive: p.Archive, Trust: []crypto.PublicKey{key.Public()}}.Verify(ctx, s, false)
			x.NoError(err)
			x.True(v.Ok(), "%v", v.Findings)
			x.Equal(1, v.Checkpoint)

			// Somebody else's key, of the same kind.
			forger := p
			switch key.Public().(type) {
			case *ecdsa.PublicKey:
				forger.Signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			case *rsa.PublicKey:
				forger.Signer, err = rsa.GenerateKey(rand.Reader, 2048)
			default:
				forger.Signer = newKey(t)
			}
			x.NoError(err)
			forger.Pass(ctx, s)

			v, err = p.Verify(ctx, s, false)
			x.NoError(err)
			x.Equal([]string{"forged"}, kinds(v))
		})
	}

	t.Run("a signature over other bytes", func(t *testing.T) {
		x := require.New(t)
		s, p, _ := manifestedArchive(t, device{ec})

		// The latest checkpoint, its count changed and its signature kept.
		store := p.checkpoints()
		ks, err := checkpointsIn(ctx, store)
		x.NoError(err)
		was, err := readCheckpoint(ctx, store, ks[1])
		x.NoError(err)

		v := was.s
		v.Checkpoint = bytes.Replace(v.Checkpoint, []byte(`"count":`), []byte(`"count":1`), 1)
		doc, err := json.Marshal(v)
		x.NoError(err)
		l := flob.Labels{}
		l.Set(labelFormat, formatCheckpoint)
		l.Set(labelK, "1")
		x.NoError(store.Erase(ctx, ks[1]))
		_, err = store.Add(ctx, flob.Meta{Labels: l}, bytes.NewReader(gzipped(t, doc)))
		x.NoError(err)

		got, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Contains(kinds(got), "forged")
	})

	t.Run("a key nothing can sign a checkpoint with is refused where the process comes up", func(t *testing.T) {
		x := require.New(t)

		small, err := rsa.GenerateKey(rand.Reader, 1024)
		x.NoError(err)
		p224, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
		x.NoError(err)

		for _, k := range []crypto.Signer{small, p224} {
			x.Error(Policy{Signer: k}.Valid())
			x.Error(Policy{Trust: []crypto.PublicKey{k.Public()}}.Valid())
		}
	})
}

func gzipped(t *testing.T, b []byte) []byte {
	buf := &bytes.Buffer{}
	z := gzip.NewWriter(buf)
	_, err := z.Write(b)
	require.NoError(t, err)
	require.NoError(t, z.Close())

	return buf.Bytes()
}

// TestAKeyIsWhatTheConfigurationReads.
func TestAKeyIsWhatTheConfigurationReads(t *testing.T) {
	x := require.New(t)

	equal := func(a, b crypto.PublicKey) bool {
		return a.(interface{ Equal(crypto.PublicKey) bool }).Equal(b)
	}

	doc, pub, err := GenerateKey()
	x.NoError(err)
	key, err := ParseKey(doc)
	x.NoError(err)
	k, err := ParsePublicKey(pub)
	x.NoError(err)
	x.True(equal(k, key.Public()))

	// Each kind of document openssl writes, and a public key as a PEM
	// document as well as base64.
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	x.NoError(err)
	rs, err := rsa.GenerateKey(rand.Reader, 2048)
	x.NoError(err)

	pkcs8 := func(k any) []byte {
		der, err := x509.MarshalPKCS8PrivateKey(k)
		x.NoError(err)
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	}
	sec1, err := x509.MarshalECPrivateKey(ec)
	x.NoError(err)

	for doc, want := range map[string]crypto.PublicKey{
		string(pkcs8(ec)): ec.Public(),
		string(pkcs8(rs)): rs.Public(),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1})):                             ec.Public(),
		string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rs)})): rs.Public(),
	} {
		key, err := ParseKey([]byte(doc))
		x.NoError(err)
		x.True(equal(key.Public(), want))

		der, err := x509.MarshalPKIXPublicKey(want)
		x.NoError(err)
		k, err := ParsePublicKey(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
		x.NoError(err)
		x.True(equal(k, want))

		v, err := PublicKeyString(want)
		x.NoError(err)
		k, err = ParsePublicKey(v)
		x.NoError(err)
		x.True(equal(k, want))
	}

	// What does not sign, or is not a key.
	x25519, err := ecdh.X25519().GenerateKey(rand.Reader)
	x.NoError(err)
	_, err = ParseKey(pkcs8(x25519))
	x.Error(err)
	_, err = ParseKey([]byte("not a key"))
	x.Error(err)
	_, err = ParseKey([]byte(pub))
	x.Error(err)
	_, err = ParsePublicKey("c2hvcnQ=")
	x.Error(err)
	_, err = ParsePublicKey(string(doc))
	x.Error(err, "a private key taken for a public one")
}
