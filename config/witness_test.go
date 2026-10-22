package config_test

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
)

// listener is a witness, as far as the configuration can tell.
type listener struct{ told string }

func (*listener) Hear(context.Context, trail.Signed) (trail.Witnessed, error) {
	return trail.Witnessed{}, nil
}

func (*listener) Latest(context.Context, string) (trail.Heard, error) { return trail.Heard{}, nil }

// TestAWitnessIsALineOfConfiguration (payday#115): what
// [config.RegisterWitness] records is opened from the whole of what was
// written, by a scheme compared without case, and named by it up to a `?` --
// which is where what is not to be printed would be. Several may be named,
// none twice; one with no scheme, or one this binary does not open, is
// refused where the process comes up, with what to import for a scheme payday
// has; and there is nothing to hear without an archive.
func TestAWitnessIsALineOfConfiguration(t *testing.T) {
	x := require.New(t)

	config.RegisterWitness("test-ear", func(v string) (*listener, error) { return &listener{told: v}, nil })
	config.RegisterWitness("test-nothing", func(string) (*listener, error) { return nil, nil })
	config.RegisterWitness("test-failing", func(string) (*listener, error) { return nil, errors.New("nobody is listening") })
	x.Contains(config.Witnesses(), "test-ear")

	audit := func(ws ...string) config.AuditConfig {
		return config.AuditConfig{Archive: t.TempDir(), Retain: 1, Checkpoints: config.AuditCheckpointsConfig{Key: pdtest.Key(t), Witnesses: ws}}
	}

	p, err := audit("TEST-EAR:one?token=secret", "test-ear:two").Policy()
	x.NoError(err)
	x.Len(p.Witnesses, 2)
	x.Equal("TEST-EAR:one?token=secret", p.Witnesses["TEST-EAR:one"].(*listener).told)
	x.Contains(p.Witnesses, "test-ear:two")
	x.NotContains(p.String(), "secret")

	for v, says := range map[string]string{
		"test-ear:one":     "named twice",
		"no scheme at all": "no scheme",
		"test-failing:x":   "nobody is listening",
		"test-nothing:x":   "opened nothing",
		"store:/mnt/x":     "config/witnessstore",
		"rekor:x":          "config/witnessrekor",
		"elsewhere:x":      "config.RegisterWitness",
	} {
		ws := []string{v}
		if v == "test-ear:one" {
			ws = []string{v, v + "?again"}
		}

		_, err := audit(ws...).Policy()
		x.ErrorContains(err, "audit.checkpoints.witnesses", v)
		x.ErrorContains(err, says, v)
	}

	_, err = config.AuditConfig{Retain: 1, Discard: true, Checkpoints: config.AuditCheckpointsConfig{Witnesses: []string{"test-ear:x"}}}.Policy()
	x.ErrorContains(err, "no archive", "a witness was named for a deployment that takes no checkpoint")

	c := audit("test-ear:x")
	c.Checkpoints.HearWithin = 3 * time.Hour
	p, err = c.Policy()
	x.NoError(err)
	x.Equal(3*time.Hour, p.HearWithin)
}

// digests is a witness that keeps a statement's digest, as far as the
// configuration can tell.
type digests struct{ listener }

func (*digests) Prehash(crypto.PublicKey) crypto.Hash { return crypto.SHA512 }

// messageOnly is a key in a device that signs Ed25519 over the message alone.
type messageOnly struct{ crypto.Signer }

func (k messageOnly) Sign(r io.Reader, b []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts.HashFunc() != 0 {
		return nil, errors.New("an Ed25519 key here signs a message itself")
	}
	return k.Signer.Sign(r, b, opts)
}

// TestAKeyThatCannotSignADigestIsRefusedForAWitnessThatKeepsOne (payday#115):
// a witness that keeps a statement's digest is told the key's signature over
// one, and a key in a device that signs Ed25519 over the message alone cannot
// make it: refused where the process comes up, with what to name instead --
// and only where such a witness is named.
func TestAKeyThatCannotSignADigestIsRefusedForAWitnessThatKeepsOne(t *testing.T) {
	x := require.New(t)

	_, ed, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)
	config.RegisterSigner("test-message-only", func(string) (messageOnly, error) { return messageOnly{ed}, nil })
	config.RegisterWitness("test-digests", func(string) (*digests, error) { return &digests{}, nil })
	config.RegisterWitness("test-keeps", func(string) (*listener, error) { return &listener{}, nil })

	audit := func(key string, ws ...string) config.AuditConfig {
		return config.AuditConfig{Archive: t.TempDir(), Retain: 1, Checkpoints: config.AuditCheckpointsConfig{Key: key, Witnesses: ws}}
	}

	_, err = audit("test-message-only:x", "test-digests:x").Policy()
	x.ErrorContains(err, "test-digests:x keeps a statement's digest")
	x.ErrorContains(err, "an ECDSA or RSA key")
	x.ErrorContains(err, "store: and log:")

	_, err = audit("test-message-only:x", "test-keeps:x").Policy()
	x.NoError(err, "a key was refused for a witness that keeps statements")
	_, err = audit(pdtest.Key(t), "test-digests:x").Policy()
	x.NoError(err, "an Ed25519 key that signs Ed25519ph was refused")
}
