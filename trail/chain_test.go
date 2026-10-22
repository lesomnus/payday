package trail

import (
	"bytes"
	"context"
	"crypto"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"
)

// chained is a deployment `n` passes old, its checkpoints in `store` -- the
// archive's own with nil -- each signed.
func chained(t *testing.T, n int, store flob.Store) (*fakeStore, Policy) {
	s := &fakeStore{}
	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Signer: newKey(t), Checkpoints: store}
	for i := range n {
		s.add(fakeRow{tenant: newTenant().Uuid(), domain: person, created: ago(40 * day), value: strconv.Itoa(i)})
		p.Pass(t.Context(), s)
	}

	return s, p
}

// rewrite writes a checkpoint again as `fn` makes it, signed by the
// deployment's key -- as somebody who held the process while it did could --
// and answers its digest. It does not take the first one away.
func rewrite(t *testing.T, p Policy, was stored, fn func(c *Checkpoint)) flob.Digest {
	c := was.c
	fn(&c)

	b, err := json.Marshal(c)
	require.NoError(t, err)
	v := signed{Checkpoint: b}
	v.Key, v.Sig, err = sign(p.Signer, b)
	require.NoError(t, err)
	doc, err := json.Marshal(v)
	require.NoError(t, err)

	l := flob.Labels{}
	l.Set(labelFormat, formatCheckpoint)
	l.Set(labelK, strconv.Itoa(c.K))
	m, err := p.checkpoints().Add(t.Context(), flob.Meta{Labels: l}, bytes.NewReader(gzipped(t, doc)))
	require.NoError(t, err)

	return m.Digest
}

// numbered is the checkpoint the store keeps under a number, whole.
func numbered(t *testing.T, p Policy, k int) stored {
	ch, err := p.readChain(t.Context())
	require.NoError(t, err)
	vs := ch.numbered(k)
	require.Len(t, vs, 1)

	v, err := ch.whole(t.Context(), vs[0])
	require.NoError(t, err)

	return v
}

// TestEachCheckpointNamesTheOneBefore.
//
// So the checkpoints are a chain: one taken away, written again as something
// else, or put out of order is a finding however long ago it was written --
// where before only the latest two were checked, by recomputing them from the
// rows. A checkpoint's number is what it says, which is signed, and not what
// its labels say.
func TestEachCheckpointNamesTheOneBefore(t *testing.T) {
	ctx := t.Context()

	t.Run("a chain", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 3, nil)

		ch, err := p.readChain(ctx)
		x.NoError(err)
		vs := ch.ordered()
		x.Len(vs, 3)
		x.Empty(vs[0].c.Prev)
		x.Equal(vs[0].d, vs[1].c.Prev)
		x.Equal(vs[1].d, vs[2].c.Prev)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)
		x.Equal(3, v.Checkpoint)
	})

	t.Run("one taken away", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 5, nil)

		two := numbered(t, p, 2)
		x.NoError(p.checkpoints().Erase(ctx, two.d))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unkept"}, kinds(v))
		x.Equal(two.d, v.Findings[0].Digest)
		x.Contains(v.Findings[0].Note, "checkpoint 3")
	})

	t.Run("one written again as something else", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 5, nil)

		// What the archive held two days before, said to be less than it
		// was, and signed: too old to be recomputed from the rows.
		two := numbered(t, p, 2)
		x.NoError(p.checkpoints().Erase(ctx, two.d))
		rewrite(t, p, two, func(c *Checkpoint) {
			c.Blobs = c.Blobs[1:]
			c.Count = len(c.Blobs)
			c.Digest = digestOf(c.Blobs)
		})

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unkept"}, kinds(v), "the one after it names what it was")
		x.Equal(two.d, v.Findings[0].Digest)
	})

	t.Run("on a store that keeps both", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 5, WriteOnce(flob.NewMemStores().Use("checkpoints")))

		two := numbered(t, p, 2)
		x.ErrorIs(p.checkpoints().Erase(ctx, two.d), ErrWriteOnce)
		rewrite(t, p, two, func(c *Checkpoint) { c.At = c.At.Add(time.Second) })

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"rewritten"}, kinds(v))
		x.Equal(2, v.Findings[0].Checkpoint)
	})

	t.Run("one relabelled", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 5, nil)

		// As a later one, and as one the database has not numbered yet, which
		// would stop a pass keeping checkpoints if a pass believed it.
		two := numbered(t, p, 2)
		for _, k := range []int{4, 9} {
			l := flob.Labels{}
			l.Set(labelFormat, formatCheckpoint)
			l.Set(labelK, strconv.Itoa(k))
			x.NoError(p.checkpoints().Label(ctx, two.d, l))

			v, err := p.Verify(ctx, s, false)
			x.NoError(err)
			x.Equal([]string{"relabelled"}, kinds(v))
			x.Equal(2, v.Findings[0].Checkpoint)
		}

		p.Pass(ctx, s)
		x.Equal(numbered(t, p, 5).d, numbered(t, p, 6).c.Prev, "the next checkpoint went by the labels")
	})

	t.Run("the one before is named by what it says", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 4, nil)

		// The second labelled as the fourth, and the fourth as one ahead of
		// the database: by its labels, the second is the newest there is.
		label := func(d flob.Digest, k int) {
			l := flob.Labels{}
			l.Set(labelFormat, formatCheckpoint)
			l.Set(labelK, strconv.Itoa(k))
			x.NoError(p.checkpoints().Label(ctx, d, l))
		}
		two, four := numbered(t, p, 2), numbered(t, p, 4)
		label(two.d, 4)
		label(four.d, 7)

		// By what each says, the fourth is the newest below the fifth,
		// whatever its labels say.
		p.Pass(ctx, s)
		x.NotEqual(two.d, numbered(t, p, 5).c.Prev, "the next checkpoint named one by its labels")
		x.Equal(four.d, numbered(t, p, 5).c.Prev)
	})

	t.Run("one whose labels were taken off", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 3, nil)

		// So it is listed as nothing, and still in the store under its digest.
		two := numbered(t, p, 2)
		x.NoError(p.checkpoints().Label(ctx, two.d, flob.Labels{}))
		ks, err := checkpointsIn(ctx, p.checkpoints())
		x.NoError(err)
		x.NotContains(ks, 2)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"relabelled"}, kinds(v))
		x.Equal(2, v.Findings[0].Checkpoint)
		x.Contains(v.Findings[0].Note, "do not say it is a checkpoint")
	})

	t.Run("one that names a later one", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 3, nil)

		two, three := numbered(t, p, 2), numbered(t, p, 3)
		x.NoError(p.checkpoints().Erase(ctx, two.d))
		rewrite(t, p, two, func(c *Checkpoint) { c.Prev = three.d })

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Contains(kinds(v), "reordered")
		x.Contains(kinds(v), "unkept", "the one after it names what it was")
	})

	t.Run("checkpoints from before they were chained", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 2, nil)

		// The second as a version before this one wrote it, naming nothing.
		two := numbered(t, p, 2)
		x.NoError(p.checkpoints().Erase(ctx, two.d))
		d := rewrite(t, p, two, func(c *Checkpoint) { c.Prev = "" })

		p.Pass(ctx, s)
		x.Equal(d, numbered(t, p, 3).c.Prev)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)
	})

	t.Run("one a crash stopped", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 2, nil)

		// Numbered and never kept: the next one goes past it, and names the
		// one before. The number it went past is a finding, since one taken
		// away looks the same (#80).
		_, _, err := s.Checkpoint(ctx, 0)
		x.NoError(err)
		p.Pass(ctx, s)
		x.Equal(numbered(t, p, 2).d, numbered(t, p, 4).c.Prev)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unkept"}, kinds(v))
		x.Equal(3, v.Findings[0].Checkpoint)
		x.Contains(v.Findings[0].Note, "checkpoint 4 names 2")

		// Until an operator, who knows of the crash, accepts it.
		_, err = accept(t, p, s, Acceptance{Why: "a pass the deployment's restart stopped"})
		x.NoError(err)
		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)
	})

	t.Run("one the store lost, and the next went past it", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 3, nil)

		// The store restored from a copy made before its latest checkpoint,
		// and the restored deployment's first pass taken at once (#80): the
		// one it takes names the newest the store keeps.
		x.NoError(p.checkpoints().Erase(ctx, numbered(t, p, 3).d))
		p.Pass(ctx, s)
		x.Equal(numbered(t, p, 2).d, numbered(t, p, 4).c.Prev)

		v, err := p.Verify(ctx, s, true)
		x.NoError(err)
		x.Equal([]string{"unkept"}, kinds(v))
		x.Equal(3, v.Findings[0].Checkpoint)

		_, err = accept(t, p, s, Acceptance{Why: "the checkpoint store restored from a copy made before checkpoint 3"})
		x.NoError(err)
		v, err = p.Verify(ctx, s, true)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)

		// One lost after the acceptance is found again.
		p.Pass(ctx, s)
		x.NoError(p.checkpoints().Erase(ctx, numbered(t, p, 6).d))
		p.Pass(ctx, s)
		x.Equal(numbered(t, p, 5).d, numbered(t, p, 7).c.Prev)

		v, err = p.Verify(ctx, s, true)
		x.NoError(err)
		x.Equal([]string{"unkept"}, kinds(v))
		x.Equal(6, v.Findings[0].Checkpoint)
	})
}

// wrapped is a store with something wrapped around it, as a cache or a
// tracer would be.
type wrapped struct{ flob.Store }

func (w wrapped) Unwrap() flob.Store { return w.Store }

// TestAWriteOnceStoreKeepsWhatItIsGiven.
//
// It adds, and refuses the rest; it says so through what is wrapped around
// it; and a policy keeps its checkpoints on one as on any other store, and
// says so where the process comes up.
func TestAWriteOnceStoreKeepsWhatItIsGiven(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s := WriteOnce(flob.NewMemStores().Use("checkpoints"))
	m, err := s.Add(ctx, flob.Meta{}, strings.NewReader("a checkpoint"))
	x.NoError(err)
	_, err = s.Add(ctx, flob.Meta{}, strings.NewReader("a checkpoint"))
	x.ErrorIs(err, flob.ErrAlreadyExists)

	x.ErrorIs(s.Erase(ctx, m.Digest), ErrWriteOnce)
	x.ErrorIs(s.Label(ctx, m.Digest, flob.Labels{}), ErrWriteOnce)
	_, err = s.Stat(ctx, m.Digest)
	x.NoError(err)

	x.True(IsWriteOnce(s))
	x.True(IsWriteOnce(wrapped{s}))
	x.False(IsWriteOnce(flob.NewMemStores().Use("checkpoints")))
	x.False(IsWriteOnce(wrapped{flob.NewMemStores().Use("checkpoints")}))

	f, p := chained(t, 2, WriteOnce(flob.NewMemStores().Use("checkpoints")))
	x.NoError(p.Valid())
	v, err := p.Verify(ctx, f, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(2, v.Checkpoint)
	x.Contains(p.String(), "write-once")

	x.NotContains(Policy{Archive: p.Archive, Checkpoints: flob.NewMemStores().Use("checkpoints")}.String(), "write-once")
}

// planted is a checkpoint put in the store by somebody who can write it and
// not by a pass: labelled as one, numbered as they like, and signed by `key`,
// or by nobody with nil.
func planted(t *testing.T, p Policy, c Checkpoint, key crypto.Signer) flob.Digest {
	t.Helper()

	b, err := json.Marshal(c)
	require.NoError(t, err)
	v := signed{Checkpoint: b}
	if key != nil {
		v.Key, v.Sig, err = sign(key, b)
		require.NoError(t, err)
	}
	doc, err := json.Marshal(v)
	require.NoError(t, err)

	l := flob.Labels{}
	l.Set(labelFormat, formatCheckpoint)
	l.Set(labelK, strconv.Itoa(c.K))
	m, err := p.checkpoints().Add(t.Context(), flob.Meta{Labels: l}, bytes.NewReader(gzipped(t, doc)))
	require.NoError(t, err)

	return m.Digest
}

// over is a checkpoint that says the archive held nothing, numbered k.
func over(k int) Checkpoint {
	return Checkpoint{K: k, At: time.Now().UTC(), Blobs: []Kept{}, Digest: digestOf(nil)}
}

// oneMore moves one more row into the archive, which is an act that ends in
// a checkpoint.
func oneMore(t *testing.T, s *fakeStore, p Policy) error {
	s.add(fakeRow{tenant: newTenant().Uuid(), domain: person, created: ago(40 * day), value: "one more"})
	_, err := Archive(t.Context(), s, p, Kinds{}, time.Now())

	return err
}

// TestACheckpointNobodyVouchesForDecidesNothing.
//
// Whoever can write the checkpoint store -- the archive's own, by default --
// can put a checkpoint there, numbered as they like. Without the key it is
// one nobody signed, or one by a key nobody trusts: a finding, and nothing
// else. It is not what makes the database behind, not what the latest rows
// are compared with, not what the next checkpoint is numbered after or names,
// and not what says which gaps were accepted.
func TestACheckpointNobodyVouchesForDecidesNothing(t *testing.T) {
	ctx := t.Context()

	t.Run("numbered ahead of the database", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 3, nil)
		three := numbered(t, p, 3)

		// As high as the manifest numbers: an acceptance numbered after it
		// would be one it refuses.
		planted(t, p, over(1<<32-1), nil)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v), "%v", v.Findings)

		x.NoError(oneMore(t, s, p), "a checkpoint nobody signed stopped an act")
		x.Equal(4, latestOf(t, s))
		x.Equal(three.d, numbered(t, p, 4).c.Prev)

		_, err = accept(t, p, s, Acceptance{Why: "looked at"})
		x.NoError(err)
		x.Equal(5, latestOf(t, s), "an acceptance numbered after a checkpoint nobody signed")
		x.Equal(numbered(t, p, 4).d, numbered(t, p, 5).c.Prev)
	})

	t.Run("numbered as the latest", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 3, nil)

		// Saying the archive held nothing, which the rows do not add up to.
		planted(t, p, over(3), nil)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v), "%v", v.Findings)
		x.NotContains(v.Findings, Finding{Kind: "rewritten", Checkpoint: 3, Note: "it kept 0 blobs; of them the manifest has lost 0, and gained 3 it never had"})

		x.NoError(oneMore(t, s, p), "the rows were held to a checkpoint nobody signed")
		x.Equal(4, latestOf(t, s))
	})

	t.Run("signed by a key nobody trusts", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 3, nil)

		// Naming one the store does not keep, and labelled as another: what
		// it says of the chain is nobody's word either.
		c := over(1000)
		c.Prev = flob.DigestFromBytes([]byte("nothing the store keeps"))
		d := planted(t, p, c, newKey(t))
		l := flob.Labels{}
		l.Set(labelFormat, formatCheckpoint)
		l.Set(labelK, "7")
		x.NoError(p.checkpoints().Label(ctx, d, l))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"forged"}, kinds(v), "%v", v.Findings)

		x.NoError(oneMore(t, s, p))
		x.Equal(4, latestOf(t, s))
	})

	t.Run("newer than the one before", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 3, nil)
		three := numbered(t, p, 3)

		// Two numbered and never kept -- passes a crash stopped -- and one
		// nobody signed, saying it is the second of them.
		for range 2 {
			_, _, err := s.Checkpoint(ctx, 0)
			x.NoError(err)
		}
		planted(t, p, over(5), nil)

		p.Pass(ctx, s)
		x.Equal(three.d, numbered(t, p, 6).c.Prev, "the next named one nobody signed")

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unkept", "unsigned"}, kinds(v), "%v", v.Findings)
	})

	t.Run("saying the archive held something", func(t *testing.T) {
		x := require.New(t)

		// A deployment with nothing to archive yet, whose first checkpoint is
		// over nothing (#81).
		s := &fakeStore{}
		p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Signer: newKey(t)}
		p.Pass(ctx, s)

		c := over(7)
		c.Blobs = []Kept{{Namespace: newTenant().String(), Digest: flob.DigestFromBytes([]byte("a chunk"))}}
		c.Count, c.Digest = 1, digestOf(c.Blobs)
		planted(t, p, c, nil)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v), "%v", v.Findings)
	})

	t.Run("saying it was an acceptance", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 3, nil)

		// A number the next went past, which only an acceptance quiets.
		x.NoError(p.checkpoints().Erase(ctx, numbered(t, p, 3).d))
		p.Pass(ctx, s)
		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unkept"}, kinds(v))

		c := over(4)
		c.Accepted = &Accepted{Acceptance: Acceptance{By: "nobody", Why: "nothing to see"}}
		planted(t, p, c, nil)

		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.Contains(kinds(v), "unkept", "an acceptance nobody signed quieted a number skipped over")
	})

	t.Run("an acceptance of the latest number", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 3, nil)

		// A row the latest two were taken over, taken away: neither adds up.
		s.mu.Lock()
		first := s.archived[0]
		s.archived = slices.DeleteFunc(s.archived, func(v Archived) bool { return v.Key == first.Key })
		s.mu.Unlock()

		// And one nobody signed, saying it accepted the rows as they are now.
		rows, err := s.At(ctx, 3)
		x.NoError(err)
		c := checkpointOf(3, time.Now(), rows)
		c.Accepted = &Accepted{Acceptance: Acceptance{By: "nobody", Why: "nothing to see"}}
		planted(t, p, c, nil)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(slices.ContainsFunc(v.Findings, func(f Finding) bool { return f.Kind == "rewritten" && f.Checkpoint == 2 }),
			"the one before the latest went uncompared for an acceptance nobody signed: %v", v.Findings)
	})
}

// racing is a store whose next checkpoint has another act's beside it: one
// numbered after it and written before it, or with `first`, one numbered and
// written in full once the chain was read and before it was numbered.
type racing struct {
	*fakeStore
	p     Policy
	first bool
	done  bool
}

func (r *racing) Checkpoint(ctx context.Context, after int) (int, []Archived, error) {
	if r.first && !r.done {
		r.done = true
		if _, err := r.p.checkpoint(ctx, r.fakeStore, nil); err != nil {
			return 0, nil, err
		}
	}

	k, rows, err := r.fakeStore.Checkpoint(ctx, after)
	if err == nil && !r.done {
		r.done = true
		if _, err := r.p.checkpoint(ctx, r.fakeStore, nil); err != nil {
			return k, rows, err
		}
	}

	return k, rows, err
}

// TestTwoCheckpointsAtOnceAreNoGap.
//
// Every act ends in a checkpoint, and acts run in more than one process: two
// can be numbered one after the other, and the second written before the
// first. Each then names the one before the first, which is no gap, since the
// store keeps the number it went past -- and still is one once the first is
// taken away.
func TestTwoCheckpointsAtOnceAreNoGap(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, p := chained(t, 2, nil)
	_, err := p.checkpoint(ctx, &racing{fakeStore: s, p: p}, nil)
	x.NoError(err)
	x.Equal(numbered(t, p, 2).d, numbered(t, p, 3).c.Prev)
	x.Equal(numbered(t, p, 2).d, numbered(t, p, 4).c.Prev)

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)

	x.NoError(p.checkpoints().Erase(ctx, numbered(t, p, 3).d))
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"unkept"}, kinds(v))
	x.Equal(3, v.Findings[0].Checkpoint)

	t.Run("one kept meanwhile is the one named", func(t *testing.T) {
		x := require.New(t)

		// The other act's numbered and kept after this one read the chain,
		// and before this one was numbered: this one names it.
		s, p := chained(t, 2, nil)
		_, err := p.checkpoint(ctx, &racing{fakeStore: s, p: p, first: true}, nil)
		x.NoError(err)
		x.Equal(numbered(t, p, 3).d, numbered(t, p, 4).c.Prev)
	})
}

// TestAKeyRotatedOutNeedNotBeTrusted.
//
// The checkpoints a key signed before it was rotated out are named, down the
// chain, by the first one the next key signed, and are what they were without
// the old key being trusted. So the old key leaves `trust` once the next has
// signed a checkpoint and the rows it signed have been accepted again, and
// what it signs after that -- a key that leaked, say -- is forged, and decides
// nothing.
func TestAKeyRotatedOutNeedNotBeTrusted(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	old, next := newKey(t), newKey(t)
	s := &fakeStore{}
	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Signer: old}
	for i := range 2 {
		s.add(fakeRow{tenant: newTenant().Uuid(), domain: person, created: ago(40 * day), value: strconv.Itoa(i)})
		p.Pass(ctx, s)
	}

	// Rotated: the next key signs, and the old one is trusted until the next
	// has signed a checkpoint.
	p.Signer, p.Trust = next, []crypto.PublicKey{old.Public()}
	x.NoError(oneMore(t, s, p))
	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)

	// Then not at all. Its checkpoints are what the chain says they were; its
	// rows are not on any chain, and are found until they are accepted again.
	p.Trust = nil
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.NotEmpty(v.Findings)
	for _, f := range v.Findings {
		x.NotEmpty(f.Namespace, "a checkpoint the old key signed is a finding: %v", f)
		x.Equal("forged", f.Kind)
	}

	_, err = accept(t, p, s, Acceptance{Why: "the rows the old key signed, signed again with the next"})
	x.NoError(err)
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)

	// What the old key signs now is nobody's word.
	planted(t, p, over(1000), old)
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"forged"}, kinds(v), "%v", v.Findings)
	x.NoError(oneMore(t, s, p))
}

// TestACheckpointIsReadOnlyAsItsDigest: what a checkpoint names as the one
// before it is a digest, and a checkpoint vouches for the one it names only if
// what is read under that digest is what was there.
func TestACheckpointIsReadOnlyAsItsDigest(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	s, p := chained(t, 3, flob.NewOsStores(dir).Use(CheckpointNamespace))
	one, two := numbered(t, p, 1), numbered(t, p, 2)

	// The second's bytes made the first's, under the second's name.
	path := func(d flob.Digest) string {
		found := ""
		x.NoError(filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() && e.Name() == "blob" && strings.Contains(p, d.Encoded()[4:]) && strings.Contains(p, "repos") {
				found = p
			}
			return err
		}))
		x.NotEmpty(found, "where %s is kept", d)

		return found
	}
	b, err := os.ReadFile(path(one.d))
	x.NoError(err)
	x.NoError(os.WriteFile(path(two.d), b, 0o640))

	// A full verification reads every checkpoint again, and a process that
	// had not read it before reads it in any verification.
	v, err := p.Verify(ctx, s, true)
	x.NoError(err)
	x.Equal([]string{"forged"}, kinds(v), "%v", v.Findings)
	x.Contains(v.Findings[0].Note, "not its digest")

	known.Clear()
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"forged"}, kinds(v), "%v", v.Findings)
}

// opening is a store that counts what it is asked to open.
type opening struct {
	flob.Store
	n *atomic.Int64
}

func (o opening) Unwrap() flob.Store { return o.Store }

func (o opening) Open(ctx context.Context, d flob.Digest) (io.ReadSeekCloser, flob.Info, error) {
	o.n.Add(1)
	return o.Store.Open(ctx, d)
}

// TestWhatAProcessReadIsNotReadAgain: a checkpoint the process read before is
// had from memory -- an act, and a verification, ask the store for what it
// lists and read only what is new -- and read again whole where its blobs are
// asked for. A full verification reads every one again.
func TestWhatAProcessReadIsNotReadAgain(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	n := &atomic.Int64{}
	s, p := chained(t, 5, opening{flob.NewMemStores().Use("checkpoints"), n})

	// The newest, which the last pass wrote and nothing read since, alone.
	n.Store(0)
	_, err := p.readChain(ctx)
	x.NoError(err)
	x.EqualValues(1, n.Load(), "checkpoints the process read before were read again")
	n.Store(0)
	_, err = p.readChain(ctx)
	x.NoError(err)
	x.Zero(n.Load(), "checkpoints the process read before were read again")

	x.NoError(oneMore(t, s, p))
	n.Store(0)
	_, err = p.readChain(ctx)
	x.NoError(err)
	x.EqualValues(1, n.Load(), "more than what was new was read")

	cs := []Checkpoint{}
	x.NoError(p.ReadCheckpoints(ctx, func(c Checkpoint) error { cs = append(cs, c); return nil }))
	x.Len(cs, 6)
	for _, c := range cs {
		x.Len(c.Blobs, c.Count, "checkpoint %d was answered without its blobs", c.K)
	}

	n.Store(0)
	v, err := p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.GreaterOrEqual(n.Load(), int64(6), "a full verification did not read every checkpoint again")
}
