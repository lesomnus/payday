package leasepg_test

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
	"github.com/lesomnus/payday/config/leasepg"
	"github.com/lesomnus/payday/pdtest"
)

// TestAPostgresLeaseIsOnlyOnPostgres: `spin.lease: postgres` is a name the
// configuration has once this is imported, it holds on the app's own database,
// and an app on anything else is told it needs another lease.
func TestAPostgresLeaseIsOnlyOnPostgres(t *testing.T) {
	x := require.New(t)
	x.Contains(config.Leases(), leasepg.Name)

	_, err := config.SpinConfig{Lease: leasepg.Name}.Build(t.Context(), config.DbConfig{Dialect: "sqlite3", Dsn: "file::memory:"})
	x.ErrorContains(err, "PostgreSQL")

	v := os.Getenv(pdtest.Postgres)
	if v == "" {
		t.Skipf("%s is not set", pdtest.Postgres)
	}
	l, err := config.SpinConfig{Lease: leasepg.Name}.Build(t.Context(), config.DbConfig{Dialect: "postgres", Dsn: v})
	x.NoError(err)
	held, release, err := l.Hold(t.Context(), t.Name())
	x.NoError(err)
	x.NoError(held.Err())
	release()
}

// TestWhatTheLeaseSaysLaterIsInTheLogItWasBuiltWith: `Build` is given the
// serve command's context, and a connection lost later -- which names went with
// it -- is said in the log that context carries. Built on a context of its own,
// as it was, the lease said it to the OpenTelemetry global, which nothing
// installs and which keeps nothing.
func TestWhatTheLeaseSaysLaterIsInTheLogItWasBuiltWith(t *testing.T) {
	x := require.New(t)

	v := os.Getenv(pdtest.Postgres)
	if v == "" {
		t.Skipf("%s is not set", pdtest.Postgres)
	}
	app := applicationName(t)

	var b syncBuffer
	ctx := log.Into(t.Context(), slog.New(slog.NewTextHandler(&b, nil)))
	l, err := config.SpinConfig{Lease: leasepg.Name, Dsn: withApplication(t, v, app)}.
		Build(ctx, config.DbConfig{Dialect: "postgres", Dsn: v})
	x.NoError(err)

	// Held under a context that carries no log, so that what is said is said
	// where the lease was built.
	quiet := context.WithoutCancel(context.Background())
	held, release, err := l.Hold(quiet, t.Name())
	x.NoError(err)
	defer release()

	terminate(t, v, app)

	// The next question on that connection finds it gone, which is when the
	// lease says so -- sooner than its next ping.
	asked, cancel := context.WithTimeout(quiet, 5*time.Second)
	defer cancel()
	go func() {
		if _, r, err := l.Hold(asked, t.Name()+"/next"); err == nil {
			r()
		}
	}()

	select {
	case <-held.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("the hold was not lost")
	}
	x.Eventually(func() bool { return strings.Contains(b.String(), "connection was lost") },
		5*time.Second, 10*time.Millisecond, "said nowhere it was built: %q", b.String())
	x.Contains(b.String(), t.Name())
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

// syncBuffer is a log the lease's goroutines and the test both read.
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
