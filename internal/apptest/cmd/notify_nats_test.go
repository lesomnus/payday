//go:build !js

package cmd_test

import (
	"testing"
	"time"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/config/brokernats"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"
)

// The same RPC/policy contract runs over both transports: creation, private
// receipts, and revocation of an existing stream after a remote role change.
func TestNotifyNATSCrossReplicaListen(t *testing.T) {
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, NoSigs: true, NoLog: true})
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	notificationCrossReplica(t, config.WatchConfig{Broker: brokernats.Name, Dsn: s.ClientURL(), Namespace: "apptest"})
}

func TestBuiltServerReleasesNATSOnCloseAndConstructionFailure(t *testing.T) {
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, NoSigs: true, NoLog: true})
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	c := cmd.Config{Db: dbOf(t), Watch: config.WatchConfig{Broker: brokernats.Name, Dsn: s.ClientURL(), Namespace: "lifecycle"}}
	built, err := cmd.Build(t.Context(), c)
	require.NoError(t, err)
	require.Positive(t, s.NumClients())
	require.NoError(t, built.Close())
	require.Eventually(t, func() bool { return s.NumClients() == 0 }, time.Second, time.Millisecond)
	c.Audit.Profile = "invalid-profile"
	_, err = cmd.Build(t.Context(), c)
	require.Error(t, err)
	require.Eventually(t, func() bool { return s.NumClients() == 0 }, time.Second, time.Millisecond)
}
