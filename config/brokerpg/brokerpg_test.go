package brokerpg_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/config/brokerpg"
	"github.com/lesomnus/payday/pdtest"
)

// TestWhatTheBrokerSaysLaterIsInTheLogItWasBuiltWith: `Build` is given the
// context the server is built under, and a listener that stopped is said in the
// log that context carries -- where an operator reads it, rather than into the
// OpenTelemetry global the broker logged to when it was built on a context of
// its own, which nothing installs and which keeps nothing.
func TestWhatTheBrokerSaysLaterIsInTheLogItWasBuiltWith(t *testing.T) {
	x := require.New(t)

	v := os.Getenv(pdtest.Postgres)
	if v == "" {
		t.Skipf("%s is not set", pdtest.Postgres)
	}
	app := applicationName(t)

	var b syncBuffer
	ctx := log.Into(t.Context(), slog.New(slog.NewTextHandler(&b, nil)))
	_, err := config.WatchConfig{Broker: brokerpg.Name, Dsn: withApplication(t, v, app)}.
		Build(ctx, config.DbConfig{Dialect: "postgres", Dsn: v})
	x.NoError(err)

	terminate(t, v, app)

	x.Eventually(func() bool { return strings.Contains(b.String(), "listening stopped") },
		5*time.Second, 10*time.Millisecond, "said nowhere it was built: %q", b.String())
}

// applicationName is a name for this test's sessions and nobody else's, so
// that what it ends on a shared server is its own.
func applicationName(t *testing.T) string {
	t.Helper()

	k := make([]byte, 6)
	_, err := rand.Read(k)
	require.NoError(t, err)

	return fmt.Sprintf("pdtest-%x", k)
}

// withApplication is `dsn` with its sessions called `app`.
func withApplication(t *testing.T, dsn string, app string) string {
	t.Helper()

	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("application_name", app)
	u.RawQuery = q.Encode()

	return u.String()
}

// terminate ends every session called `app`, as a database restarting or a
// connection cut does.
func terminate(t *testing.T, dsn string, app string) {
	t.Helper()

	admin, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	defer admin.Close(context.Background())

	var n int
	require.NoError(t, admin.QueryRow(t.Context(), `
		SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
		WHERE application_name = $1 AND pid <> pg_backend_pid()`, app).Scan(&n))
	require.NotZero(t, n, "no session called %s", app)
}

// syncBuffer is a log the broker's goroutines and the test both read.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
