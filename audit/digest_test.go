package audit_test

import (
	"testing"
	"time"

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

// at is when the lines of these tests were written: to the microsecond, as a
// writer stores one.
var at = time.Date(2026, 10, 22, 9, 30, 0, 123456000, time.UTC)

// line is the n-th line of the trail, written `ago` before [at].
func line(n byte, ago time.Duration) audit.Line {
	return audit.Line{Id: [16]byte{0xa0, n}, Created: at.Add(-ago), Action: "/app.RobotService/Patch"}
}

func TestADigestIsTheRowItWasMadeOf(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	sum, err := d.Of(object, row(10, 20), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Len(sum, 1+2*(8+32))
	x.Equal(audit.DigestVersion, sum[0])

	v, err := d.Check(sum, object, row(10, 20), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v)

	v, err = d.Check(sum, object, row(10, 21), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Differs, v, "a field it covers changed")

	other := object
	other[15]++
	v, err = d.Check(sum, other, row(10, 20), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.NotEqual(audit.Matches, v, "a digest is about one thing, and not another with the same row")
}

// TestADigestIsTheLinesThatKeepsIt: copied onto another line -- a newer one,
// one moved in time, one that says another act -- it is not that line's, and
// that is a finding of its own whatever the row is (payday#115).
func TestADigestIsTheLinesThatKeepsIt(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	kept := line(1, time.Hour)
	sum, err := d.Of(object, row(10, 20), []uint32{1, 2}, kept)
	x.NoError(err)

	newer := line(2, 0)
	moved := kept
	moved.Created = kept.Created.Add(-24 * time.Hour)
	relabelled := kept
	relabelled.Action = "/app.RobotService/Add"
	for _, l := range []audit.Line{newer, moved, relabelled} {
		v, err := d.Check(sum, object, row(10, 20), []uint32{1, 2}, l)
		x.NoError(err)
		x.Equal(audit.Forged, v, "%+v", l)
	}

	// The moment is the microsecond, which is what a database keeps: the same
	// line read back with less is the same line.
	read := kept
	read.Created = kept.Created.Add(999 * time.Nanosecond)
	v, err := d.Check(sum, object, row(10, 20), []uint32{1, 2}, read)
	x.NoError(err)
	x.Equal(audit.Matches, v)
}

// TestWhatADigestDoesNotCoverIsOutsideIt: a field a later migration added is
// in no digest made before, so an old row still matches -- and the list of
// what it covers is bound into it, so that shortening the list does not take a
// changed field out of the comparison.
func TestWhatADigestDoesNotCoverIsOutsideIt(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	sum, err := d.Of(object, row(10, 0), []uint32{1}, line(1, 0))
	x.NoError(err)

	v, err := d.Check(sum, object, row(10, 999), []uint32{1}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v, "nanos is outside what it covers")

	sum, err = d.Of(object, row(10, 20), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	v, err = d.Check(sum, object, row(10, 21), []uint32{1}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Differs, v, "the list is part of what was digested")
}

// TestADigestSaysWhichKeyItWasMadeUnder: one made before a rotation is checked
// with the key it was made under; one under a key the deployment let go is
// said to be; one under any other key is not held, which a check reports as a
// finding.
func TestADigestSaysWhichKeyItWasMadeUnder(t *testing.T) {
	x := require.New(t)
	before, err := audit.NewDigest(keyA)
	x.NoError(err)
	sum, err := before.Of(object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)

	after, err := audit.NewDigest(keyB, keyA)
	x.NoError(err)
	v, err := after.Check(sum, object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v)

	forgot, err := audit.NewDigest(keyB)
	x.NoError(err)
	v, err = forgot.Check(sum, object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.KeyNotHeld, v)

	letGo, err := forgot.Retire(audit.FingerprintOf(keyA))
	x.NoError(err)
	v, err = letGo.Check(sum, object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Retired, v)

	_, err = after.Retire(audit.FingerprintOf(keyA))
	x.Error(err, "a key it holds is not one it let go")
	_, err = after.Retire(after.Fingerprint())
	x.Error(err)
}

// TestAWriteThatLeftNoRowSaysSo: an erase that took its row keeps a digest of
// its line alone. A thing that is no row matches it; a row that is there
// differs from it -- brought back past the trail -- and a digest of a row that
// was left differs from a thing that is not there.
func TestAWriteThatLeftNoRowSaysSo(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	erased := d.Gone(object, line(1, 0))
	x.Len(erased, 1+8+32)

	v, err := d.CheckGone(erased, object, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v)

	v, err = d.Check(erased, object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Differs, v, "the trail says the row went, and here it is")

	v, err = d.CheckGone(erased, object, line(2, 0))
	x.NoError(err)
	x.Equal(audit.Forged, v)

	left, err := d.Of(object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	v, err = d.CheckGone(left, object, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Differs, v, "the trail says a row was left, and there is none")
}

// TestACarriedDigestSaysWhatItsLineSaid: what an older line said of a row is
// put on a new line with the row's MAC as it was, so the row it vouched for
// still matches, and a row changed since still differs -- the MAC is never
// made again of the row as it is. A version-1 digest is carried as version 2.
func TestACarriedDigestSaysWhatItsLineSaid(t *testing.T) {
	x := require.New(t)
	before, err := audit.NewDigest(keyA)
	x.NoError(err)
	sum, err := before.Of(object, row(1, 2), []uint32{1, 2}, line(1, 365*24*time.Hour))
	x.NoError(err)

	// Carried by a deployment that has rotated its key since.
	d, err := audit.NewDigest(keyB, keyA)
	x.NoError(err)
	carried, err := d.Carry(sum, object, line(2, 0))
	x.NoError(err)

	v, err := d.Check(carried, object, row(1, 2), []uint32{1, 2}, line(2, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v)
	v, err = d.Check(carried, object, row(1, 3), []uint32{1, 2}, line(2, 0))
	x.NoError(err)
	x.Equal(audit.Differs, v)
	v, err = d.Check(carried, object, row(1, 2), []uint32{1, 2}, line(1, 365*24*time.Hour))
	x.NoError(err)
	x.Equal(audit.Forged, v, "it is the new line's, not the old one's")

	gone, err := d.Carry(d.Gone(object, line(3, time.Hour)), object, line(4, 0))
	x.NoError(err)
	v, err = d.CheckGone(gone, object, line(4, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v)

	_, err = d.Carry([]byte{9, 9, 9}, object, line(5, 0))
	x.ErrorIs(err, audit.ErrDigest)
}

// TestADigestOfVersionOneIsStillChecked: bound to no line, it is checked as it
// always was, and carried onto one.
func TestADigestOfVersionOneIsStillChecked(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	// Version 1 is version 2's row part behind a version byte of its own.
	v2, err := d.Of(object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	v1 := append([]byte{1}, v2[1+8+32:]...)

	v, err := d.Check(v1, object, row(1, 2), []uint32{1, 2}, line(9, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v, "no line is bound into it")
	v, err = d.Check(v1, object, row(1, 3), []uint32{1, 2}, line(9, 0))
	x.NoError(err)
	x.Equal(audit.Differs, v)

	carried, err := d.Carry(v1, object, line(2, 0))
	x.NoError(err)
	x.Equal(audit.DigestVersion, carried[0])
	v, err = d.Check(carried, object, row(1, 2), []uint32{1, 2}, line(2, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v)
}

// TestADigestNoVersionMadeIsForged: of a version nothing made or the wrong
// length, it is no digest of this line's.
func TestADigestNoVersionMadeIsForged(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	sum, err := d.Of(object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	for _, v := range [][]byte{sum[:40], append([]byte{3}, sum[1:]...), {1}} {
		got, err := d.Check(v, object, row(1, 2), []uint32{1, 2}, line(1, 0))
		x.ErrorIs(err, audit.ErrDigest)
		x.Equal(audit.Forged, got)
	}
}

func TestAFingerprintIsWrittenAndReadBack(t *testing.T) {
	x := require.New(t)

	p := audit.FingerprintOf(keyA)
	got, err := audit.ParseFingerprint(p.String())
	x.NoError(err)
	x.Equal(p, got)
	x.Len(p.String(), 16)

	_, err = audit.ParseFingerprint("not hex")
	x.Error(err)
	_, err = audit.ParseFingerprint("abcd")
	x.Error(err)
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

	sa, err := d.Of(object, a, []uint32{1}, line(1, 0))
	x.NoError(err)
	v, err := d.Check(sa, object, b, []uint32{1}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v)
}
