package witnessstore_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	_ "github.com/lesomnus/payday/config/witnessstore"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail/witnessstore"
)

// TestAStoreWitnessIsALineOfConfiguration: imported, `store:` and a directory
// is a witness the policy tells every checkpoint.
func TestAStoreWitnessIsALineOfConfiguration(t *testing.T) {
	x := require.New(t)
	x.Contains(config.Witnesses(), witnessstore.Scheme)

	v := "store:" + t.TempDir()
	p, err := config.AuditConfig{Archive: t.TempDir(), Retain: 1, Checkpoints: config.AuditCheckpointsConfig{
		Key: pdtest.Key(t), Witnesses: []string{v},
	}}.Policy()
	x.NoError(err)
	x.IsType(&witnessstore.Witness{}, p.Witnesses[v])
}
