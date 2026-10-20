package config_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/trail"
)

// TestTheCheckpointsKeyIsAFileOrTheDocumentItself.
//
// A file is how a secret is mounted, and the document itself is how one comes
// through the environment; a key that does not read is refused where the
// process comes up, not discovered when checkpoints stop being signed.
func TestTheCheckpointsKeyIsAFileOrTheDocumentItself(t *testing.T) {
	x := require.New(t)

	ed, edPub, err := trail.GenerateKey()
	x.NoError(err)

	// And one of the kind a TPM signs with, as `openssl genpkey -algorithm EC
	// -pkeyopt ec_paramgen_curve:P-256` writes it.
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	x.NoError(err)
	der, err := x509.MarshalPKCS8PrivateKey(k)
	x.NoError(err)
	ec := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	ecPub, err := trail.PublicKeyString(k.Public())
	x.NoError(err)

	for doc, pub := range map[string]string{string(ed): edPub, string(ec): ecPub} {
		path := filepath.Join(t.TempDir(), "checkpoints.pem")
		x.NoError(os.WriteFile(path, []byte(doc), 0o600))

		for _, v := range []string{path, doc} {
			p, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Key: v}}.Policy()
			x.NoError(err)
			x.NotNil(p.Signer)
			got, err := trail.PublicKeyString(p.Signer.Public())
			x.NoError(err)
			x.Equal(pub, got)
		}
	}

	p, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Trust: []string{edPub, ecPub}}}.Policy()
	x.NoError(err)
	x.Nil(p.Signer)
	x.Len(p.Trust, 2)

	_, err = config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Key: filepath.Join(t.TempDir(), "nothing.pem")}}.Policy()
	x.ErrorContains(err, "audit.checkpoints.key")
	_, err = config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Trust: []string{"short"}}}.Policy()
	x.ErrorContains(err, "audit.checkpoints.trust[0]")
}

// TestTheDigestKeyIsAFileOrTheKeyItself: base64 of at least 32 bytes, in the
// file a secret is mounted as or after `base64:` for one that comes through the
// environment -- and anything else refused where the process comes up, not
// found when the rows are checked.
func TestTheDigestKeyIsAFileOrTheKeyItself(t *testing.T) {
	x := require.New(t)

	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	path := filepath.Join(t.TempDir(), "digest")
	x.NoError(os.WriteFile(path, []byte(key+"\n"), 0o600))

	for _, v := range []string{path, "base64:" + key, "base64:" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))} {
		cs, err := config.AuditConfig{Digest: config.AuditDigestConfig{Key: v}}.Contents()
		x.NoError(err, v)
		x.NotNil(cs.Digest)
	}

	cs, err := config.AuditConfig{}.Contents()
	x.NoError(err)
	x.Nil(cs.Digest, "no key digests nothing")

	for _, v := range []string{
		"base64:" + base64.StdEncoding.EncodeToString([]byte("short")),
		"base64:not base64!",
		filepath.Join(t.TempDir(), "nothing"),
	} {
		_, err := config.AuditConfig{Digest: config.AuditDigestConfig{Key: v}}.Contents()
		x.ErrorContains(err, "digest: key", v)
	}

	_, err = config.AuditConfig{Digest: config.AuditDigestConfig{Previous: []string{"base64:" + key}}}.Contents()
	x.ErrorContains(err, "previous", "keys to check with and none to make a digest")
}

// TestCheckpointsGoWhereTheyAreToldTo.
func TestCheckpointsGoWhereTheyAreToldTo(t *testing.T) {
	x := require.New(t)

	p, err := config.AuditConfig{Archive: t.TempDir(), Retain: 1, Checkpoints: config.AuditCheckpointsConfig{Dir: t.TempDir()}}.Policy()
	x.NoError(err)
	x.NotNil(p.Checkpoints, "a directory of their own was not taken")

	p, err = config.AuditConfig{Archive: t.TempDir(), Retain: 1}.Policy()
	x.NoError(err)
	x.Nil(p.Checkpoints, "with no directory, they are the archive's")

	// On a disk that says it keeps them, kept by the process too: an erase is
	// refused, and the process says so where it comes up.
	p, err = config.AuditConfig{Archive: t.TempDir(), Retain: 1, Checkpoints: config.AuditCheckpointsConfig{Dir: t.TempDir(), WriteOnce: true}}.Policy()
	x.NoError(err)
	x.True(trail.IsWriteOnce(p.Checkpoints))
	x.ErrorIs(p.Checkpoints.Erase(t.Context(), "sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"), trail.ErrWriteOnce)
	x.Contains(p.String(), "write-once")

	_, err = config.AuditConfig{Archive: t.TempDir(), Retain: 1, Checkpoints: config.AuditCheckpointsConfig{WriteOnce: true}}.Policy()
	x.ErrorContains(err, "audit.checkpoints.write_once", "the archive is where a pass erases from")
}

// TestTheKeysAreWhatTheConfigurationSays.
//
// The master's public half is configuration, and its private half is read only
// when given -- from a file, which is how a secret is mounted, or the key
// itself, which is how one comes through the environment for one command. A key
// store inside the archive is refused: it would be kept as long as the archive
// and backed up with it, which is the one thing a key that can be destroyed
// must not be.
func TestTheKeysAreWhatTheConfigurationSays(t *testing.T) {
	x := require.New(t)

	master, err := age.GenerateX25519Identity()
	x.NoError(err)
	public := master.Recipient().String()
	archive := t.TempDir()

	p, err := config.AuditConfig{Archive: archive, Retain: 1, Keys: config.AuditKeysConfig{Dir: t.TempDir(), Master: []string{public}}}.Policy()
	x.NoError(err)
	x.NotNil(p.Keys)
	x.Len(p.Keys.Master, 1)
	x.Empty(p.Keys.Identities, "a process that serves holds the master's private half")

	path := filepath.Join(t.TempDir(), "master.txt")
	x.NoError(os.WriteFile(path, []byte("# created by age-keygen\n"+master.String()+"\n"), 0o600))
	for _, v := range []string{path, master.String()} {
		p, err := config.AuditConfig{Archive: archive, Retain: 1, Keys: config.AuditKeysConfig{
			Dir: t.TempDir(), Master: []string{public}, Identity: v,
		}}.Policy()
		x.NoError(err)
		x.Len(p.Keys.Identities, 1)
	}

	p, err = config.AuditConfig{Archive: archive, Retain: 1}.Policy()
	x.NoError(err)
	x.Nil(p.Keys, "a configuration that says nothing sealed something")

	for want, c := range map[string]config.AuditConfig{
		"audit.keys.dir: inside audit.archive": {Archive: archive, Keys: config.AuditKeysConfig{Dir: filepath.Join(archive, "keys"), Master: []string{public}}},
		"audit.keys.dir":                       {Archive: archive, Keys: config.AuditKeysConfig{Master: []string{public}}},
		"audit.keys.master:":                   {Archive: archive, Keys: config.AuditKeysConfig{Dir: t.TempDir()}},
		"audit.keys.master[0]":                 {Archive: archive, Keys: config.AuditKeysConfig{Dir: t.TempDir(), Master: []string{master.String()}}},
		"audit.keys.identity":                  {Archive: archive, Keys: config.AuditKeysConfig{Dir: t.TempDir(), Master: []string{public}, Identity: filepath.Join(t.TempDir(), "none")}},
		"audit.archive names no archive":       {Keys: config.AuditKeysConfig{Dir: t.TempDir(), Master: []string{public}}},
	} {
		_, err := c.Policy()
		x.ErrorContains(err, want)
		if err != nil {
			x.NotContains(err.Error(), "AGE-SECRET-KEY", "an error printed a private key")
		}
	}
}
