package grpcx

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// pgError is what pgx's PgError is to [unreachable]: an error with an SQLSTATE.
type pgError string

func (e pgError) Error() string    { return "FATAL: (SQLSTATE " + string(e) + ")" }
func (e pgError) SQLState() string { return string(e) }

type netSyscallError struct{ errno syscall.Errno }

func (e *netSyscallError) Error() string { return "connect: " + e.errno.Error() }
func (e *netSyscallError) Unwrap() error { return e.errno }

// TestWhatIsUnreachable is the line between Unavailable and Internal. The
// errors of a real driver are TestAStoreThatCannotBeReachedIsUnavailable's, in
// the test app; these are the shapes it reads.
func TestWhatIsUnreachable(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: &netSyscallError{syscall.ECONNREFUSED}}

	for _, v := range []struct {
		what string
		err  error
		is   bool
	}{
		{"a dial refused", refused, true},
		{"a dial refused, as pgx wraps it", fmt.Errorf("failed to connect to `user=app database=app`: %w", refused), true},
		{"too many clients already (53300)", fmt.Errorf("failed to connect: %w", pgError("53300")), true},
		{"a connection lost (08006)", pgError("08006"), true},
		{"a server shutting down (57P01)", pgError("57P01"), true},
		{"a server starting (57P03)", pgError("57P03"), true},
		{"several addresses, one full", errors.Join(errors.New("tls refused"), pgError("53300")), true},
		{"a connection database/sql threw away", fmt.Errorf("query: %w", driver.ErrBadConn), true},
		{"a reset, bare", fmt.Errorf("read: %w", syscall.ECONNRESET), true},

		{"a wrong password (28P01)", pgError("28P01"), false},
		{"a unique index (23505)", pgError("23505"), false},
		{"a file that is not there", syscall.ENOENT, false},
		{"anything else", errors.New("no"), false},
	} {
		t.Run(v.what, func(t *testing.T) {
			require.Equal(t, v.is, unreachable(v.err), "%v", v.err)
		})
	}
}
