package audit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
)

// TestADigestOfVersionTwoIsStillChecked: a line's digest as the builds before
// seals made it -- the line's MAC with no flags, under a label of its own --
// is checked as it was, says its write sealed nothing, and is carried as
// version 3 saying the same (payday#115). The version a build no longer makes
// is made here by hand, which is why this is the package's own test.
func TestADigestOfVersionTwoIsStillChecked(t *testing.T) {
	x := require.New(t)
	d, err := NewDigest([]byte("the first key, thirty-two bytes!"))
	x.NoError(err)

	object := pdid.Id{7, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	at := time.Date(2026, 10, 22, 9, 30, 0, 123456000, time.UTC)
	kept := Line{Id: [16]byte{0xa0, 1}, Created: at, Action: "/app.RobotService/Patch"}
	row := &timestamppb.Timestamp{Seconds: 1, Nanos: 2}

	v2 := func(r []byte) []byte {
		out := append([]byte{2}, d.print[:]...)
		out = append(out, lineMAC(d.key, object, kept, r)...)
		return append(out, r...)
	}
	r, err := d.rowPart(object, row, []uint32{1, 2})
	x.NoError(err)
	sum := v2(r)
	x.Len(sum, lineAndRow)

	v, err := d.Check(sum, object, row, []uint32{1, 2}, kept)
	x.NoError(err)
	x.Equal(Matches, v)
	x.False(Sealing(sum), "a build before seals sealed nothing")

	v, err = d.Check(sum, object, &timestamppb.Timestamp{Seconds: 1, Nanos: 3}, []uint32{1, 2}, kept)
	x.NoError(err)
	x.Equal(Differs, v)

	other := kept
	other.Id[1]++
	v, err = d.Check(sum, object, row, []uint32{1, 2}, other)
	x.NoError(err)
	x.Equal(Forged, v, "bound to its line, as it always was")

	// Read as version 3 it is no digest of the line at all: the label and the
	// flags are what tell the two apart.
	as3 := append([]byte{DigestVersion, 0}, sum[1:]...)
	v, err = d.Check(as3, object, row, []uint32{1, 2}, kept)
	x.NoError(err)
	x.Equal(Forged, v)

	carried, err := d.Carry(sum, object, other)
	x.NoError(err)
	x.Equal(DigestVersion, carried[0])
	x.False(Sealing(carried))
	v, err = d.Check(carried, object, row, []uint32{1, 2}, other)
	x.NoError(err)
	x.Equal(Matches, v)

	gone := v2(nil)
	x.Len(gone, onlyLine)
	v, err = d.CheckGone(gone, object, kept)
	x.NoError(err)
	x.Equal(Matches, v)
}

// TestASealIsMadeUnderAKeyOfItsOwn: the key a seal is made under is derived
// from the digest's, and is not it -- so a seal and a digest of the same row,
// over the same fields, are not one MAC, and nothing a line keeps (which leaves
// the database with the archive) can be held up against a seal.
func TestASealIsMadeUnderAKeyOfItsOwn(t *testing.T) {
	x := require.New(t)
	key := []byte("the first key, thirty-two bytes!")

	x.NotEqual(key, sealKey(key))
	x.Len(sealKey(key), 32)
	x.NotEqual(sealKey(key), sealKey([]byte("and the second, which replaced it")))

	object := pdid.Id{7, 1}
	row := &timestamppb.Timestamp{Seconds: 1, Nanos: 2}
	digest, err := rowMAC(key, object, row, []uint32{1, 2})
	x.NoError(err)
	seal, err := macOfRow(sealKey(key), "payday.trail.seal.row\x00", object, row, []uint32{1, 2})
	x.NoError(err)
	x.NotEqual(digest, seal)
}
