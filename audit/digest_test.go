package audit_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
)

var (
	keyA = []byte("the first key, thirty-two bytes!")
	keyB = []byte("and the second, which replaced it")
)

// A row to digest: any message does, and a Timestamp has two fields to cover.
func row(seconds int64, nanos int32) *timestamppb.Timestamp {
	return &timestamppb.Timestamp{Seconds: seconds, Nanos: nanos}
}

var object = pdid.Id{7, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

func TestADigestIsTheRowItWasMadeOf(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	sum, err := d.Of(object, row(10, 20), []uint32{1, 2})
	x.NoError(err)
	x.Len(sum, 1+8+32)

	v, err := d.Check(sum, object, row(10, 20), []uint32{1, 2})
	x.NoError(err)
	x.Equal(audit.Matches, v)

	v, err = d.Check(sum, object, row(10, 21), []uint32{1, 2})
	x.NoError(err)
	x.Equal(audit.Differs, v, "a field it covers changed")

	other := object
	other[15]++
	v, err = d.Check(sum, other, row(10, 20), []uint32{1, 2})
	x.NoError(err)
	x.Equal(audit.Differs, v, "a digest is about one thing, and not another with the same row")
}

// TestWhatADigestDoesNotCoverIsOutsideIt: a field a later migration added is
// in no digest made before, so an old row still matches -- and the list of
// what it covers is bound into it, so that shortening the list does not take a
// changed field out of the comparison.
func TestWhatADigestDoesNotCoverIsOutsideIt(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	sum, err := d.Of(object, row(10, 0), []uint32{1})
	x.NoError(err)

	v, err := d.Check(sum, object, row(10, 999), []uint32{1})
	x.NoError(err)
	x.Equal(audit.Matches, v, "nanos is outside what it covers")

	sum, err = d.Of(object, row(10, 20), []uint32{1, 2})
	x.NoError(err)
	v, err = d.Check(sum, object, row(10, 21), []uint32{1})
	x.NoError(err)
	x.Equal(audit.Differs, v, "the list is part of what was digested")
}

// TestADigestSaysWhichKeyItWasMadeUnder: one made before a rotation is checked
// with the key it was made under, and one under a key the check was not given
// says so rather than differing.
func TestADigestSaysWhichKeyItWasMadeUnder(t *testing.T) {
	x := require.New(t)
	before, err := audit.NewDigest(keyA)
	x.NoError(err)
	sum, err := before.Of(object, row(1, 2), []uint32{1, 2})
	x.NoError(err)

	after, err := audit.NewDigest(keyB, keyA)
	x.NoError(err)
	v, err := after.Check(sum, object, row(1, 2), []uint32{1, 2})
	x.NoError(err)
	x.Equal(audit.Matches, v)

	forgot, err := audit.NewDigest(keyB)
	x.NoError(err)
	v, err = forgot.Check(sum, object, row(1, 2), []uint32{1, 2})
	x.NoError(err)
	x.Equal(audit.KeyNotHeld, v)
}

func TestAKeyIsLongEnough(t *testing.T) {
	_, err := audit.NewDigest([]byte("short"))
	require.Error(t, err)

	_, err = audit.NewDigest(keyA, []byte("short"))
	require.Error(t, err)
}

// TestADigestOfAMapIsTheSameWhateverOrderItWasBuiltIn: marshalled
// deterministically, so the same row is the same bytes.
func TestADigestOfAMapIsTheSameWhateverOrderItWasBuiltIn(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	a, err := structpb.NewStruct(map[string]any{"a": 1, "b": 2, "c": 3})
	x.NoError(err)
	b, err := structpb.NewStruct(map[string]any{"c": 3, "b": 2, "a": 1})
	x.NoError(err)

	sa, err := d.Of(object, a, []uint32{1})
	x.NoError(err)
	v, err := d.Check(sa, object, b, []uint32{1})
	x.NoError(err)
	x.Equal(audit.Matches, v)
}
