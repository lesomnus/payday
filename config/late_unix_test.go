//go:build unix

package config_test

import (
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// port is a port on the loopback held for a late address by a socket that is
// bound and does not listen. A connection to it is refused, as one to a port
// nothing listens on is; and no other socket can bind it, since this one does
// not say SO_REUSEADDR, which two sockets binding one port both need. So from
// `reserve` to `listen` the port is the test's, with no moment in which it is
// anybody's.
//
// That moment is what a listener closed to be listened on again left: another
// test's listener took the port in it, the first dial was answered rather than
// refused, and the test that waited for the refusal never heard it.
type port struct {
	fd   int
	addr string
}

func reserve(t *testing.T) *port {
	t.Helper()

	// Under ForkLock, so that no process started meanwhile inherits it before
	// it is marked close-on-exec, as net does for its own.
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	require.NoError(t, err)

	var sa syscall.Sockaddr
	err = syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}})
	if err == nil {
		sa, err = syscall.Getsockname(fd)
	}
	if err != nil {
		syscall.Close(fd)
		require.NoError(t, err)
	}

	return &port{fd: fd, addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(sa.(*syscall.SockaddrInet4).Port))}
}

// listen makes the socket listen and hands it to net: the port is the
// listener's from then on, and closing the listener lets it go.
func (p *port) listen() (net.Listener, error) {
	if err := syscall.Listen(p.fd, syscall.SOMAXCONN); err != nil {
		return nil, err
	}

	// net takes a copy of its own, so this one is closed either way: left
	// open, it would keep the socket listening after the listener closed.
	f := os.NewFile(uintptr(p.fd), p.addr)
	p.fd = -1
	defer f.Close()

	return net.FileListener(f)
}

// close lets the port go, if it was never listened on.
func (p *port) close() {
	if p.fd < 0 {
		return
	}
	syscall.Close(p.fd)
	p.fd = -1
}

// TestALateAddressIsNobodyElsesUntilItOpens: refused until it is opened, and
// no other listener may take its port meanwhile. A late address that let one
// was a refusal that sometimes never came: another test listened on the port
// in the moment it was free, and the first dial was answered.
func TestALateAddressIsNobodyElsesUntilItOpens(t *testing.T) {
	x := require.New(t)

	at := late(t, "")
	l, err := net.Listen("tcp", at.addr)
	if err == nil {
		l.Close()
	}
	x.ErrorIs(err, syscall.EADDRINUSE, "another listener took a late address before it opened")
	x.ErrorIs(dialed(at.addr), syscall.ECONNREFUSED)

	at.open()
	x.NoError(dialed(at.addr))
}
