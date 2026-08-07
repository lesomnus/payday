package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
)

func TestOtelConfig(t *testing.T) {
	t.Run("telemetry nobody can find is refused", func(t *testing.T) {
		x := require.New(t)

		// The template this came from wrote its own name in here. A framework
		// cannot, so the app says it -- and an app that says nothing would
		// emit spans attributed to "", which looks like working.
		var c config.OtelConfig
		_, _, err := c.Build(t.Context(), config.Service{})
		x.ErrorContains(err, "named")
	})
	t.Run("a configuration that says nothing still has every signal", func(t *testing.T) {
		x := require.New(t)

		var c config.OtelConfig
		ctx, o, err := c.Build(t.Context(), config.Service{
			Name:    "acme",
			Version: "1.2.3",
			Scope:   "github.com/lesomnus/payday/config",
		})
		x.NoError(err)
		x.NotNil(o)
		x.NotNil(ctx)
	})
}
