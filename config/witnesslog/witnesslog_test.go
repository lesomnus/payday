package witnesslog_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	_ "github.com/lesomnus/payday/config/witnesslog"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail/witnesslog"
)

// TestALogWitnessIsALineOfConfiguration: imported, `log:` is a witness the
// policy tells every checkpoint.
func TestALogWitnessIsALineOfConfiguration(t *testing.T) {
	x := require.New(t)
	x.Contains(config.Witnesses(), witnesslog.Scheme)

	p, err := config.AuditConfig{Archive: t.TempDir(), Retain: 1, Checkpoints: config.AuditCheckpointsConfig{
		Key: pdtest.Key(t), Witnesses: []string{"log:"},
	}}.Policy()
	x.NoError(err)
	x.IsType(witnesslog.Witness{}, p.Witnesses["log:"])
}
