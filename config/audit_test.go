package config_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

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

// TestCheckpointsGoWhereTheyAreToldTo.
func TestCheckpointsGoWhereTheyAreToldTo(t *testing.T) {
	x := require.New(t)

	p, err := config.AuditConfig{Archive: t.TempDir(), Retain: 1, Checkpoints: config.AuditCheckpointsConfig{Dir: t.TempDir()}}.Policy()
	x.NoError(err)
	x.NotNil(p.Checkpoints, "a directory of their own was not taken")

	p, err = config.AuditConfig{Archive: t.TempDir(), Retain: 1}.Policy()
	x.NoError(err)
	x.Nil(p.Checkpoints, "with no directory, they are the archive's")
}
