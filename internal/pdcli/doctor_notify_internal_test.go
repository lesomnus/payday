package pdcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
)

// What `pd doctor` asks of an app that keeps its notifications for a number of
// days (payday#194): that something erases them after -- read off the app's
// own Go, so each case is a few lines of it in a module of its own.

// expiring is what the doctor says of an app of `files` whose schema keeps
// notifications `days` days, or forever when it is zero.
func expiring(t *testing.T, days uint32, files map[string]string) string {
	t.Helper()
	x := require.New(t)

	root := t.TempDir()
	for name, src := range files {
		p := filepath.Join(root, name)
		x.NoError(os.MkdirAll(filepath.Dir(p), 0o755))
		x.NoError(os.WriteFile(p, []byte(src), 0o644))
	}

	s := &pdgen.Schema{Notify: &pdgen.Notify{Notification: 40, Receipt: 41, At: "proto/app/app.proto", KeepDays: days}}
	if days == 0 {
		s.Notify.Forever = "an inbox its people keep"
	}

	l := Layout{Root: root, Work: root, Module: "github.com/acme/thing"}
	said := []string{}
	for _, v := range doctorNotify(l, s) {
		said = append(said, v.String())
	}

	return strings.Join(said, "\n")
}

const spinningExpirer = `package cmd

import (
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/watch"
	"github.com/protobuf-orm/ent/dialect"

	"github.com/acme/thing/server/pd"
)

type Server struct{ Spin []any }

func Build(drv dialect.Driver, w *watch.Watch, content audit.Contents) *Server {
	s := &Server{}
	s.Spin = append(s.Spin, pd.NotificationExpirer(drv, w, content))
	return s
}
`

func TestNotificationsAnExpirerErasesAreWell(t *testing.T) {
	require.Empty(t, expiring(t, 90, map[string]string{"cmd/serve.go": spinningExpirer}))

	// And an app that keeps them forever, or has none, is asked nothing.
	require.Empty(t, expiring(t, 0, map[string]string{"cmd/serve.go": "package cmd\n"}))
	l := Layout{Root: t.TempDir(), Module: "github.com/acme/thing"}
	l.Work = l.Root
	require.Empty(t, doctorNotify(l, &pdgen.Schema{}))
}

// TestNotificationsNothingErasesAreSaid: the declaration says they go after so
// many days, and nothing takes them.
func TestNotificationsNothingErasesAreSaid(t *testing.T) {
	x := require.New(t)

	v := expiring(t, 90, map[string]string{"cmd/serve.go": "package cmd\n"})
	x.Contains(v, "proto/app/app.proto keeps notifications 90 days, and nothing erases them")
	x.Contains(v, "pd.NotificationExpirer(drv, w, content)")

	// A test that builds one says nothing about what the app serves.
	x.NotEmpty(expiring(t, 90, map[string]string{"cmd/serve_test.go": strings.Replace(spinningExpirer, "package cmd", "package cmd_test", 1)}))
}
