package config_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/trail"
)

// held is a key in a device, as far as the configuration can tell: a signer
// that something opened, and that is closed when it is let go.
type held struct {
	crypto.Signer
	pub    crypto.PublicKey
	closed bool
}

func (h *held) Public() crypto.PublicKey {
	if h.pub != nil {
		return h.pub
	}

	return h.Signer.Public()
}

func (h *held) Close() error {
	h.closed = true
	return nil
}

func p256(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()

	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	return k
}

// TestAKeyInADeviceIsNamedByItsScheme: what [config.RegisterSigner] records is
// opened from the whole of what was written, by a scheme compared without
// case, and is the checkpoints' signer.
func TestAKeyInADeviceIsNamedByItsScheme(t *testing.T) {
	x := require.New(t)

	k := p256(t)
	var told string
	config.RegisterSigner("test-device", func(v string) (*held, error) {
		told = v
		return &held{Signer: k}, nil
	})
	x.Contains(config.Signers(), "test-device")

	p, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Key: " Test-Device:slot=1;object=checkpoints?pin-source=/run/pin "}}.Policy()
	x.NoError(err)
	x.Equal("Test-Device:slot=1;object=checkpoints?pin-source=/run/pin", told, "the whole of it, scheme and all")
	got, err := trail.PublicKeyString(p.Signer.Public())
	x.NoError(err)
	want, err := trail.PublicKeyString(k.Public())
	x.NoError(err)
	x.Equal(want, got)
}

// TestAKeyThatSignsWhatDoesNotVerifyIsRefused where the process comes up: a
// device whose public half is not its private half's -- a token holding two
// objects of one label, say -- signs every checkpoint as one [trail.Policy.Verify]
// calls forged, and nothing says so until somebody verifies.
func TestAKeyThatSignsWhatDoesNotVerifyIsRefused(t *testing.T) {
	x := require.New(t)

	h := &held{Signer: p256(t), pub: p256(t).Public()}
	config.RegisterSigner("test-mismatched", func(string) (*held, error) { return h, nil })

	_, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Key: "test-mismatched:x"}}.Policy()
	x.ErrorContains(err, "audit.checkpoints.key: test-mismatched:")
	x.ErrorContains(err, "does not verify")
	x.True(h.closed, "a signer refused and left open")
}

// TestWhatADeviceSaysIsTheRefusal: a signer that cannot open says why, under
// its scheme, and one that opens nothing is refused rather than left to panic
// at the first checkpoint.
func TestWhatADeviceSaysIsTheRefusal(t *testing.T) {
	x := require.New(t)

	config.RegisterSigner("test-failing", func(string) (*held, error) { return nil, errors.New("no token in slot 1") })
	_, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Key: "test-failing:x"}}.Policy()
	x.ErrorContains(err, "audit.checkpoints.key: test-failing: no token in slot 1")

	config.RegisterSigner("test-nothing", func(string) (*held, error) { return nil, nil })
	_, err = config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Key: "test-nothing:x"}}.Policy()
	x.ErrorContains(err, "test-nothing: opened nothing")
}

// TestASchemeThisBinaryDoesNotOpenIsRefused, with the package that would, and
// without what was written: what follows a scheme can be a PIN.
func TestASchemeThisBinaryDoesNotOpenIsRefused(t *testing.T) {
	x := require.New(t)

	_, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{
		Key: "pkcs11:token=payday;object=checkpoints?module-path=/usr/lib/softhsm/libsofthsm2.so&pin-value=8341",
	}}.Policy()
	x.ErrorContains(err, "audit.checkpoints.key: `pkcs11:`")
	x.ErrorContains(err, "config/signerpkcs11")
	x.NotContains(err.Error(), "8341", "a PIN, in an error that goes to a log")

	_, err = config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Key: "keyring:checkpoints"}}.Policy()
	x.ErrorContains(err, "`keyring:`")
	x.ErrorContains(err, "config.RegisterSigner")
}

// TestAPathIsAPathEvenWithAColonInIt: a scheme is two characters at least, so a
// drive letter begins a file, and a file whose name looks like a scheme no
// binary opens is still read.
func TestAPathIsAPathEvenWithAColonInIt(t *testing.T) {
	x := require.New(t)

	doc, pub, err := trail.GenerateKey()
	x.NoError(err)
	t.Chdir(t.TempDir())
	for _, name := range []string{"c:checkpoints.pem", "keys:checkpoints.pem"} {
		x.NoError(os.WriteFile(filepath.Join(".", name), doc, 0o600))

		p, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Key: name}}.Policy()
		x.NoError(err, name)
		got, err := trail.PublicKeyString(p.Signer.Public())
		x.NoError(err)
		x.Equal(pub, got, name)
	}
}

// TestASchemeIsAURIs: [config.RegisterSigner] refuses what could not begin a
// value, so that a registration nothing could ever name fails where it is
// written.
func TestASchemeIsAURIs(t *testing.T) {
	x := require.New(t)

	for _, v := range []string{"c", "1pkcs11", "pkcs#11", "", "tpm:"} {
		x.Panics(func() {
			config.RegisterSigner(v, func(string) (crypto.Signer, error) { return nil, nil })
		}, v)
	}
}
