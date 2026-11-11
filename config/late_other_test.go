//go:build !unix

package config_test

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// port is, where there is no socket to hold one with that does not listen, a
// port a listener was given and let go of. Another listener may take it before
// it is listened on again, which late_unix_test.go's cannot; payday's CI runs
// the tests on Linux.
type port struct {
	addr string
}

func reserve(t *testing.T) *port {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &port{addr: l.Addr().String()}
	require.NoError(t, l.Close())

	return p
}

func (p *port) listen() (net.Listener, error) {
	return net.Listen("tcp", p.addr)
}

func (p *port) close() {}
