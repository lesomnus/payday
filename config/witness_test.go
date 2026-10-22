package config_test

import (
	"context"
	"errors"
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
		"rekor:x":          "config.RegisterWitness",
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
