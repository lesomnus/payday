package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
)

// TestAnUnnamedBrokerIsRefused pins the refusal [config.WatchConfig] is built
// around.
//
// The field has no default on purpose: the in-process broker is silently wrong
// for two replicas, and a default would hand it to whoever said nothing. That
// whole argument rests on the empty string being an error -- and on the error
// naming both ways out, because the person reading it is holding a deployment
// that will not start and should not have to open a manual to learn that
// `memory` and `none` are the words.
func TestAnUnnamedBrokerIsRefused(t *testing.T) {
	x := require.New(t)

	_, err := config.WatchConfig{}.Build(config.DbConfig{})
	x.ErrorContains(err, "watch.broker")
	x.ErrorContains(err, config.BrokerMemory)
	x.ErrorContains(err, config.BrokerNone)

	// The reason, not just the names. An empty string that merely fell through
	// to the not-registered error would name both brokers too, since that one
	// lists everything -- what marks this refusal as its own is that it says
	// what goes wrong, which is the sentence the guide leans on.
	x.ErrorContains(err, "one replica")
}
