package config_test

import (
	"testing"

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

	for _, c := range []config.DialConfig{
		{Enabled: true},
		{CAFile: "ca.pem"},
		{CertFile: "c.pem"},
		{KeyFile: "k.pem"},
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
// line that was left out.
func TestHalfAKeyPairIsRefused(t *testing.T) {
	x := require.New(t)

	_, err := config.DialConfig{CertFile: "c.pem"}.Credentials()
	x.ErrorContains(err, "key_file")

	_, err = config.DialConfig{KeyFile: "k.pem"}.Credentials()
	x.ErrorContains(err, "cert_file")
}

// TestACaThatIsNotThereIsAnError rather than a quiet fall back to the system
// roots, which would verify against the wrong thing and connect anyway.
func TestACaThatIsNotThereIsAnError(t *testing.T) {
	_, err := config.DialConfig{CAFile: "/nowhere/ca.pem"}.Credentials()
	require.Error(t, err)
}
