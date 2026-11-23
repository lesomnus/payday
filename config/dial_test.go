package config_test

import (
	"strings"
	"testing"

	"github.com/lesomnus/xli/cfg/ref"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
)

// TestNothingConfiguredIsPlaintext, which is what a checkout wants and what a
// deployment must not have. It is the default because an app that cannot run
// until certificates exist is an app nobody runs, and it warns for the same
// reason `auth.Plain` does.
func TestNothingConfiguredIsPlaintext(t *testing.T) {
	x := require.New(t)

	c := config.DialConfig{}
	x.False(c.Active())

	v, err := c.Credentials()
	x.NoError(err)
	x.Equal("insecure", v.Info().SecurityProtocol)
}

// TestAnythingWrittenDownTurnsItOn, so that naming a CA is not a setting
// somebody also has to remember to enable.
func TestAnythingWrittenDownTurnsItOn(t *testing.T) {
	x := require.New(t)

	cert, key := keypair(t)
	for _, c := range []config.DialConfig{
		{Enabled: true},
		{Ca: fileRef(t, cert)},
		{Cert: fileRef(t, cert)},
		{Key: fileRef(t, key)},
	} {
		x.True(c.Active())
	}
}

// TestSystemRootsAreEnough, which is all a public certificate authority needs.
func TestSystemRootsAreEnough(t *testing.T) {
	x := require.New(t)

	v, err := config.DialConfig{Enabled: true}.Credentials()
	x.NoError(err)
	x.Equal("tls", v.Info().SecurityProtocol)
}

// TestHalfAKeyPairIsRefused, because the other outcome is a handshake failing
// against a server that asked for a certificate, which says nothing about the
// line that was left out. Both keys are named, as the file writes them.
func TestHalfAKeyPairIsRefused(t *testing.T) {
	x := require.New(t)

	cert, key := keypair(t)
	for _, c := range []config.DialConfig{
		{Cert: fileRef(t, cert)},
		{Key: literal(t, read(t, key))},
	} {
		_, err := c.Credentials()
		x.EqualError(err, "tls: both cert and key are needed to present a certificate")
	}
}

// TestACaThatIsNotThereIsAnError rather than a quiet fall back to the system
// roots, which would verify against the wrong thing and connect anyway.
func TestACaThatIsNotThereIsAnError(t *testing.T) {
	_, err := config.DialConfig{Ca: fileRef(t, "/nowhere/ca.pem")}.Credentials()
	require.ErrorContains(t, err, "tls.ca")
}

// TestACaWithNoCertificateInItIsRefused by its key, and not by what it holds,
// for the reason the server's client_ca is.
func TestACaWithNoCertificateInItIsRefused(t *testing.T) {
	x := require.New(t)

	const junk = "this is not a certificate"
	for _, ca := range []ref.String{
		literal(t, junk),
		fileRef(t, write(t, t.TempDir(), "ca.pem", junk+"\n")),
	} {
		_, err := config.DialConfig{Ca: ca}.Credentials()
		x.EqualError(err, "tls.ca: no certificate in it")
	}
}

// TestASystemsWholeBundleIsRead, which is some hundreds of KiB: a value of a
// file is read up to 1 MiB, where a secret's 64 KiB would refuse it.
func TestASystemsWholeBundleIsRead(t *testing.T) {
	x := require.New(t)

	cert, _ := keypair(t)
	one := read(t, cert)
	bundle := strings.Repeat(one, (256<<10)/len(one)+1)
	p := write(t, t.TempDir(), "bundle.pem", bundle)

	_, err := config.DialConfig{Ca: fileRef(t, p)}.Credentials()
	x.NoError(err)
}
