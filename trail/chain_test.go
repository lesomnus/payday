package trail

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
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

// numbered is the checkpoint the store keeps under a number.
func numbered(t *testing.T, p Policy, k int) stored {
	ch, err := readChain(t.Context(), p.checkpoints())
	require.NoError(t, err)
	vs := ch.numbered(k)
	require.Len(t, vs, 1)

	return vs[0]
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

		ch, err := readChain(ctx, p.checkpoints())
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
		two, three, four := numbered(t, p, 2), numbered(t, p, 3), numbered(t, p, 4)
		label(two.d, 4)
		label(four.d, 7)

		p.Pass(ctx, s)
		x.NotEqual(two.d, numbered(t, p, 5).c.Prev, "the next checkpoint named one by its labels")
		x.Equal(three.d, numbered(t, p, 5).c.Prev)
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
		_, err = p.Accept(ctx, s, "a pass the deployment's restart stopped")
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

		_, err = p.Accept(ctx, s, "the checkpoint store restored from a copy made before checkpoint 3")
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
