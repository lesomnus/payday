package audit

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
)

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
