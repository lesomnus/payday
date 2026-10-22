package witnessstore_test

import (
	"strings"
	"testing"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/trail/witnessstore"
)

func statement(k int, says string) trail.Signed {
	return trail.Signed{K: k, Digest: flob.DigestFromBytes([]byte(says)), Statement: []byte(says), Key: []byte("key"), Sig: []byte("sig")}
}

// TestWhatIsHeardIsKept (payday#115): every statement is kept as it was
// heard, and the newest of a chain is the highest number -- of two of one
// number the first heard, since the second is the number taken again after
// the count was wound back. A tenant's chain is answered apart. A blob that
// is no statement is passed over, and one relabelled is read for what it
// holds.
func TestWhatIsHeardIsKept(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s := flob.NewMemStores().Use(witnessstore.Namespace)
	w := witnessstore.New(s)

	h, err := w.Latest(ctx, "")
	x.NoError(err)
	x.Zero(h.K, "a witness that heard nothing answered something")

	one := statement(1, "one")
	r, err := w.Hear(ctx, one)
	x.NoError(err)
	x.Contains(string(r.Receipt), "sha256:", "a receipt that says nothing of where it is kept")

	two := statement(2, "two")
	two.Prev = one.Digest
	_, err = w.Hear(ctx, two)
	x.NoError(err)

	h, err = w.Latest(ctx, "")
	x.NoError(err)
	x.Equal(two.Digest, h.Digest)
	x.Equal(one.Digest, h.Prev)
	x.False(h.At.IsZero())

	_, err = w.Hear(ctx, statement(2, "two, taken again"))
	x.NoError(err)
	_, err = w.Hear(ctx, statement(1, "one, taken again"))
	x.NoError(err)
	h, err = w.Latest(ctx, "")
	x.NoError(err)
	x.Equal(two.Digest, h.Digest, "the number taken again was answered, and not the first")

	theirs := statement(7, "a tenant's")
	theirs.Chain = "a tenant"
	_, err = w.Hear(ctx, theirs)
	x.NoError(err)
	h, err = w.Latest(ctx, "")
	x.NoError(err)
	x.Equal(2, h.K, "a tenant's chain was answered for the deployment's")
	h, err = w.Latest(ctx, "a tenant")
	x.NoError(err)
	x.Equal(7, h.K)

	_, err = s.Add(ctx, flob.Meta{}, strings.NewReader("no statement"))
	x.NoError(err)
	walker, ok := flob.AsWalker(s)
	x.True(ok)
	for v, err := range walker.Walk(ctx) {
		x.NoError(err)
		x.NoError(s.Label(ctx, v.Digest(), flob.Labels{"K": {"1000"}}))
	}
	h, err = w.Latest(ctx, "")
	x.NoError(err)
	x.Equal(two.Digest, h.Digest)
}

// TestAStoreIsADirectory: `store:` and a directory, kept on the disk.
func TestAStoreIsADirectory(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	_, err := witnessstore.Open("store:")
	x.Error(err)
	_, err = witnessstore.Open("log:")
	x.Error(err)

	dir := t.TempDir()
	w, err := witnessstore.Open("store:" + dir)
	x.NoError(err)
	_, err = w.Hear(ctx, statement(3, "three"))
	x.NoError(err)

	again, err := witnessstore.Open("store:" + dir)
	x.NoError(err)
	h, err := again.Latest(ctx, "")
	x.NoError(err)
	x.Equal(3, h.K, "what was heard is not on the disk")
}
