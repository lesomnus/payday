package pdcmd

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"
)

func TestTheConfigurationIsPrintedWithoutItsSecrets(t *testing.T) {
	type roster struct {
		Addr  string `yaml:"addr"`
		Token string `yaml:"token"`
	}
	type db struct {
		Driver string `yaml:"driver"`
		Dsn    string `yaml:"dsn"`
	}
	type cfg struct {
		Db       db                `yaml:"db"`
		Pg       db                `yaml:"pg"`
		Roster   roster            `yaml:"roster"`
		Keys     map[string]string `yaml:"keys"`
		Key      string            `yaml:"key"`
		KeyFile  string            `yaml:"key_file"`
		Seed     map[string]string `yaml:"seed"`
		Audience string            `yaml:"audience"`
	}

	v, err := redacted(&cfg{
		Db:       db{Driver: "pgx", Dsn: "postgres://khala:PGSECRET@khala-db:5432/khala?sslmode=disable"},
		Pg:       db{Driver: "pgx", Dsn: "host=db user=khala password=PGSECRET dbname=khala"},
		Roster:   roster{Addr: "roster:8080", Token: "rt_REVIEWSECRETVALUE"},
		Keys:     map[string]string{"contoso": "rt_ALSOSECRET"},
		Key:      "file:/run/roster/directory.key",
		KeyFile:  "/etc/tls/key.pem",
		Seed:     map[string]string{"password": "admin123", "holder": "admin"},
		Audience: "urn:hday:api:kamino",
	})
	require.NoError(t, err)
	b, err := yaml.Marshal(v)
	require.NoError(t, err)
	out := string(b)

	for _, secret := range []string{"PGSECRET", "rt_REVIEWSECRETVALUE", "rt_ALSOSECRET", "admin123"} {
		require.NotContains(t, out, secret)
	}

	// And what says where a secret is, or is not one, is as it was.
	for _, kept := range []string{
		"roster:8080", "file:/run/roster/directory.key", "/etc/tls/key.pem",
		"urn:hday:api:kamino", "khala-db:5432", "user=khala", "holder: admin",
	} {
		require.Contains(t, out, kept)
	}

	// In the order the struct has it, rather than whatever a map iterates in.
	require.Less(t, strings.Index(out, "db:"), strings.Index(out, "roster:"))
}
