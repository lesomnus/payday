package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/mkot"
	"github.com/lesomnus/xli/cfg"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
)

// App is every block payday offers, embedded the way an app embeds them.
type App struct {
	Server config.ServerConfig `yaml:"server"`
	Db     config.DbConfig     `yaml:"db"`
	Otel   config.OtelConfig   `yaml:"otel"`
	Watch  config.WatchConfig  `yaml:"watch"`
	Audit  config.AuditConfig  `yaml:"audit"`
}

// TestTheBlocksAreReadByCfg is that the pieces an app embeds are read by what
// reads an app's configuration. That is xli's `cfg` and not this package, so
// nothing else here would notice a block it cannot read.
func TestTheBlocksAreReadByCfg(t *testing.T) {
	t.Run("every value has a name of its own", func(t *testing.T) {
		x := require.New(t)

		// cfg.New refuses two fields with one name, or one variable, which is
		// what embedding blocks side by side could otherwise do quietly.
		var c App
		vs := cfg.New("acme", &c).EnvNames()

		// Every field of every piece has a name, since a deployment that cannot
		// be told something in the environment is one that has to be handed a
		// file to say it.
		x.Contains(vs, "ACME_SERVER_ADDR")
		x.Contains(vs, "ACME_SERVER_TLS_CERT_FILE")
		x.Contains(vs, "ACME_SERVER_KEEPALIVE_MAX_CONNECTION_AGE")
		x.Contains(vs, "ACME_DB_DSN")
		x.Contains(vs, "ACME_WATCH_BROKER")
		x.Contains(vs, "ACME_AUDIT_PROFILE")
	})
	t.Run("a file, and the environment over it", func(t *testing.T) {
		x := require.New(t)

		p := write(t, t.TempDir(), "acme.yaml", `
server:
  addr: ":50051"
  keepalive:
    max_connection_age: 30m
db:
  driver: pgx
  dsn: postgres://app:${env:PW}@db/app
otel:
  processors:
    resource:
      attributes:
        - key: deploy
          value: ${env:DEPLOY}
  providers:
    tracer:
      processors: [resource]
watch:
  broker: memory
audit:
  profile: pci
`)
		var c App
		_, err := cfg.New("acme", &c).Load(p, []string{"PW=hunter2", "DEPLOY=prod", "ACME_SERVER_ADDR=:6000"})
		x.NoError(err)

		x.Equal(":6000", c.Server.ListenAddr())
		x.Equal(30*time.Minute, c.Server.Keepalive.MaxConnectionAge)
		x.Equal("postgres://app:hunter2@db/app", c.Db.Dsn)
		x.Equal("memory", c.Watch.Broker)
		x.Equal("pci", c.Audit.Profile)
		// OtelConfig reads itself, through mkot, and a reference in it is
		// resolved all the same.
		x.Contains(c.Otel.Processors, mkot.Id("resource"))
	})
	t.Run("config prints no password", func(t *testing.T) {
		x := require.New(t)

		var c App
		s, err := cfg.New("acme", &c, cfg.WithPaths()).Load("", []string{
			"ACME_DB_DSN=postgres://app:hunter2@db/app",
			// The database `migrate plan` works migrations out on has a
			// password like any other, under a name that is not `dsn`.
			"ACME_DB_DEV_DSN=postgres://app:devsecret@db/dev",
		})
		x.NoError(err)

		b := &strings.Builder{}
		x.NoError(s.Print(b))
		x.NotContains(b.String(), "hunter2")
		x.NotContains(b.String(), "devsecret")
		x.Contains(b.String(), "postgres://app:<redacted>@db/app")
		x.Contains(b.String(), "postgres://app:<redacted>@db/dev")
	})
	t.Run("config leaves out a block nothing in it was said about", func(t *testing.T) {
		x := require.New(t)

		var c App
		s, err := cfg.New("acme", &c).Load("", []string{"ACME_DB_DSN=postgres://app@db/app"})
		x.NoError(err)

		b := &strings.Builder{}
		x.NoError(s.Print(b))
		x.Contains(b.String(), "db:")
		// `server:` with nothing under it is a key whose value is null, and
		// what `config` prints is meant to be a file that reads back to the
		// same configuration.
		x.NotContains(b.String(), "server:")
	})
}

// write writes body to the file name in dir and answers with its path.
func write(t *testing.T, dir string, name string, body string) string {
	t.Helper()

	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}
