package config_test

import (
	"crypto/ed25519"
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

	doc, pub, err := trail.GenerateKey()
	x.NoError(err)
	path := filepath.Join(t.TempDir(), "checkpoints.pem")
	x.NoError(os.WriteFile(path, doc, 0o600))

	for _, v := range []string{path, string(doc)} {
		p, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Key: v}}.Policy()
		x.NoError(err)
		x.NotNil(p.Key)
		x.Equal(pub, trail.PublicKeyString(p.Key.Public().(ed25519.PublicKey)))
	}

	p, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Trust: []string{pub}}}.Policy()
	x.NoError(err)
	x.Nil(p.Key)
	x.Len(p.Trust, 1)

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
