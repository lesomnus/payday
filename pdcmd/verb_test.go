package pdcmd

import (
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestAStreamThatEndedCleanlyIsStillAGap.
//
// The failure this guards is the quiet one, and it is quiet in both directions:
// a server that closed the subscription hands back [io.EOF], which reads as "no
// error", and a `RecvMsg` loop that stopped on nil would return as though the
// command had finished. A watch never finishes -- it is a gap or it is still
// running -- so the transport's own answer is the one thing that cannot be
// passed along.
//
// Written against the function rather than a server because the case is one a
// test cannot easily arrange: what ends a payday Watch in practice is a
// connection going, which arrives as a status and not as EOF.
func TestAStreamThatEndedCleanlyIsStillAGap(t *testing.T) {
	x := require.New(t)

	for _, tc := range []struct {
		what string
		err  error
	}{
		{"the server closed it", io.EOF},
		{"the loop stopped without saying why", nil},
	} {
		t.Run(tc.what, func(t *testing.T) {
			err := ended(tc.err)
			x.Error(err)

			// And it says what to do about it, because there is something to do
			// and nothing else is going to say so.
			x.Contains(err.Error(), "backlog")
			x.Contains(err.Error(), "--retry")
		})
	}

	// Anything that already said what happened says it. A status carries a code
	// somebody may be switching on, and wrapping it in a sentence about
	// backlogs would take that away to add nothing.
	s := status.Error(codes.Unavailable, "the connection went")
	err := ended(s)
	x.Equal(codes.Unavailable, status.Code(err))
	x.NotContains(err.Error(), "backlog")

	// And an EOF that arrived wrapped is the same ending as a bare one. It is
	// what a decoder in the middle does to it, and a stream that ended is a gap
	// whichever layer noticed first.
	x.Contains(ended(fmt.Errorf("decode: %w", io.EOF)).Error(), "backlog")
}
