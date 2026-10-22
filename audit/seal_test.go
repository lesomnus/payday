package audit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
)

// TestASealIsTheRowItWasMadeOf: a seal covers every field it lists -- the
// personal ones a digest leaves out among them -- of one thing, and is the
// seal of the write whose line it was made with (payday#115).
func TestASealIsTheRowItWasMadeOf(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	// Field 2 is personal: the digest covers 1, and the seal both.
	digest, seal, err := d.OfSealed(object, row(10, 20), []uint32{1}, []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.SealVersion, seal[0])

	v, err := d.CheckSeal(seal, object, row(10, 20), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v)

	v, err = d.Check(digest, object, row(10, 21), []uint32{1}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v, "the digest leaves the personal field out")
	v, err = d.CheckSeal(seal, object, row(10, 21), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Differs, v, "and the seal does not")

	v, err = d.CheckSeal(seal, object, row(10, 20), []uint32{1, 2}, line(2, 0))
	x.NoError(err)
	x.Equal(audit.Forged, v, "a seal of another write: kept from an earlier one, say, and put back with what the row then was")

	v, err = d.CheckSeal(seal, object, row(10, 20), []uint32{1}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Differs, v, "the list it covers is bound into it")

	other := object
	other[15]++
	v, err = d.CheckSeal(seal, other, row(10, 20), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.NotEqual(audit.Matches, v, "a seal is of one thing")

	for _, bad := range [][]byte{seal[:41], append([]byte{9}, seal[1:]...), {}} {
		v, err := d.CheckSeal(bad, object, row(10, 20), []uint32{1, 2}, line(1, 0))
		x.ErrorIs(err, audit.ErrSeal)
		x.Equal(audit.Forged, v)
	}
}

// TestAForgottenRowsSealSaysSo: the seal a forgetting leaves is of the thing
// alone, and says so -- and it is of that thing, so that it cannot be moved to
// another row to make that row's personal fields unchecked.
func TestAForgottenRowsSealSaysSo(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	seal := d.Forgotten(object)
	v, err := d.CheckSeal(seal, object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Forgotten, v)
	v, err = d.CheckSeal(seal, object, row(3, 4), []uint32{1, 2}, line(9, 0))
	x.NoError(err)
	x.Equal(audit.Forgotten, v, "of nothing the row holds, and of no line")

	other := object
	other[15]++
	v, err = d.CheckSeal(seal, other, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Forged, v)
}

// TestACarriedSealIsTheNewLines: a pass that carries a line forward carries
// its seal, copying what it says of the row and binding it to the new line --
// under the key the deployment has now, when it has rotated since -- so the
// row still matches, and a row changed since still differs.
func TestACarriedSealIsTheNewLines(t *testing.T) {
	x := require.New(t)
	before, err := audit.NewDigest(keyA)
	x.NoError(err)
	_, seal, err := before.OfSealed(object, row(1, 2), []uint32{1}, []uint32{1, 2}, line(1, 365*24*time.Hour))
	x.NoError(err)

	d, err := audit.NewDigest(keyB, keyA)
	x.NoError(err)
	carried, err := d.CarrySeal(seal, object, line(2, 0))
	x.NoError(err)

	v, err := d.CheckSeal(carried, object, row(1, 2), []uint32{1, 2}, line(2, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v)
	v, err = d.CheckSeal(carried, object, row(1, 3), []uint32{1, 2}, line(2, 0))
	x.NoError(err)
	x.Equal(audit.Differs, v)
	v, err = d.CheckSeal(carried, object, row(1, 2), []uint32{1, 2}, line(1, 365*24*time.Hour))
	x.NoError(err)
	x.Equal(audit.Forged, v, "it is the new line's, not the old one's")

	forgot, err := d.CarrySeal(before.Forgotten(object), object, line(3, 0))
	x.NoError(err)
	v, err = d.CheckSeal(forgot, object, row(1, 2), []uint32{1, 2}, line(3, 0))
	x.NoError(err)
	x.Equal(audit.Forgotten, v)

	_, err = d.CarrySeal([]byte{1, 0, 9}, object, line(4, 0))
	x.ErrorIs(err, audit.ErrSeal)
}

// TestASealSaysWhichKeyItWasMadeUnder: by the digest key's fingerprint, which
// it is derived from -- so a seal made before a rotation is checked with the
// key it was made under, one under a key let go is counted, and one under any
// other key is not held.
func TestASealSaysWhichKeyItWasMadeUnder(t *testing.T) {
	x := require.New(t)
	a, err := audit.NewDigest(keyA)
	x.NoError(err)
	_, seal, err := a.OfSealed(object, row(1, 2), []uint32{1}, []uint32{1, 2}, line(1, 0))
	x.NoError(err)

	rotated, err := audit.NewDigest(keyB, keyA)
	x.NoError(err)
	v, err := rotated.CheckSeal(seal, object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Matches, v)

	b, err := audit.NewDigest(keyB)
	x.NoError(err)
	v, err = b.CheckSeal(seal, object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.KeyNotHeld, v)
	v, err = b.CheckSeal(a.Forgotten(object), object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.KeyNotHeld, v)

	retired, err := b.Retire(audit.FingerprintOf(keyA))
	x.NoError(err)
	v, err = retired.CheckSeal(seal, object, row(1, 2), []uint32{1, 2}, line(1, 0))
	x.NoError(err)
	x.Equal(audit.Retired, v)
}
