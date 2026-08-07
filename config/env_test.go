package config_test

import (
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
)

// The tests below are about the reading, not about what any app happens to be
// configured with, so they use a struct of their own. It holds every shape a
// configuration is made of, and there is no app for it to follow.

type Root struct {
	Name string `yaml:"name"`
	Note *string

	// A value that is either given or not, which is how a switch that is on
	// unless it is turned off is spelled.
	On    *bool `yaml:"on"`
	Level *int  `yaml:"level"`

	Nested Nested `yaml:"nested"`
	Absent *Nested

	Shared `yaml:",inline"`

	Whole Whole `yaml:"whole"`

	Renamed string `yaml:"other_name"`
	Hidden  string `yaml:"-"`
	secret  string //nolint:unused // it is not read, which is the point.
}

type Nested struct {
	Count   int  `yaml:"count"`
	Enabled bool `yaml:"enabled"`

	Deep struct {
		Values []string `yaml:"values"`
	} `yaml:"deep"`
}

type Shared struct {
	Inlined string `yaml:"inlined"`
}

// Whole reads itself, so it is read as one value rather than walked into.
type Whole struct {
	Raw string
}

func (w *Whole) UnmarshalYAML(b []byte) error {
	w.Raw = string(b)
	return nil
}

// acme is the app these tests are read on behalf of. It is a made-up name, and
// that it is made up is the point: nothing below would read differently for an
// app called something else.
var acme = config.For("acme")

func TestEnvNames(t *testing.T) {
	t.Run("a name for every field a value fits in", func(t *testing.T) {
		x := require.New(t)

		x.Equal([]string{
			"ACME_NAME",
			"ACME_NOTE",
			"ACME_ON",
			"ACME_LEVEL",
			"ACME_NESTED_COUNT",
			"ACME_NESTED_ENABLED",
			"ACME_NESTED_DEEP_VALUES",
			"ACME_ABSENT_COUNT",
			"ACME_ABSENT_ENABLED",
			"ACME_ABSENT_DEEP_VALUES",
			// Inlined, so it is named as if it were written here.
			"ACME_INLINED",
			// Reads itself, so it is one name rather than many.
			"ACME_WHOLE",
			"ACME_OTHER_NAME",
		}, acme.EnvNames(&Root{}))
	})
	t.Run("nothing for what is not a struct", func(t *testing.T) {
		x := require.New(t)

		x.Empty(acme.EnvNames(Root{}))
		x.Empty(acme.EnvNames(nil))
		x.Empty(acme.EnvNames((*Root)(nil)))
	})

	// Every field of every piece payday offers has a name, since a deployment
	// that cannot be told something in the environment is one that has to be
	// handed a file to say it.
	t.Run("the pieces an app embeds are read like anything else", func(t *testing.T) {
		x := require.New(t)

		type Config struct {
			Server config.ServerConfig `yaml:"server"`
			Db     config.DbConfig     `yaml:"db"`
		}

		vs := acme.EnvNames(&Config{})
		x.Contains(vs, "ACME_SERVER_ADDR")
		x.Contains(vs, "ACME_SERVER_TLS_CERT_FILE")
		x.Contains(vs, "ACME_SERVER_KEEPALIVE_MAX_CONNECTION_AGE")
		x.Contains(vs, "ACME_DB_DSN")
	})
}

func TestOverrideFromEnv(t *testing.T) {
	t.Run("a value is read into the field it is named after", func(t *testing.T) {
		x := require.New(t)

		var v Root
		unknown, err := acme.OverrideFromEnv(&v, []string{
			"ACME_NAME=alice",
			"ACME_NESTED_COUNT=12",
			"ACME_NESTED_ENABLED=true",
			"ACME_NESTED_DEEP_VALUES=[a, b]",
			"ACME_INLINED=here",
			"ACME_OTHER_NAME=renamed",
		})
		x.NoError(err)
		x.Empty(unknown)

		x.Equal("alice", v.Name)
		x.Equal(12, v.Nested.Count)
		x.True(v.Nested.Enabled)
		x.Equal([]string{"a", "b"}, v.Nested.Deep.Values)
		x.Equal("here", v.Inlined)
		x.Equal("renamed", v.Renamed)
	})
	t.Run("a string keeps its punctuation", func(t *testing.T) {
		x := require.New(t)

		// None of this is YAML; it is a data source name and a greeting.
		const (
			dsn    = "postgres://u:p@h:5432/db?sslmode=disable"
			format = "{Hello}, %s! # and a hash"
		)

		var v Root
		_, err := acme.OverrideFromEnv(&v, []string{
			"ACME_NAME=" + dsn,
			"ACME_OTHER_NAME=" + format,
		})
		x.NoError(err)
		x.Equal(dsn, v.Name)
		x.Equal(format, v.Renamed)
	})
	t.Run("a value is made where there was none", func(t *testing.T) {
		x := require.New(t)

		var v Root
		_, err := acme.OverrideFromEnv(&v, []string{
			"ACME_NOTE=a note",
			"ACME_ON=false",
			"ACME_LEVEL=3",
			"ACME_ABSENT_COUNT=3",
		})
		x.NoError(err)

		x.NotNil(v.Note)
		x.Equal("a note", *v.Note)
		// Not a string, so it is read rather than taken; the decoder cannot do
		// this one on its own.
		x.NotNil(v.On)
		x.False(*v.On)
		x.NotNil(v.Level)
		x.Equal(3, *v.Level)
		x.NotNil(v.Absent)
		x.Equal(3, v.Absent.Count)
	})
	t.Run("nothing is made where nothing was said", func(t *testing.T) {
		x := require.New(t)

		var v Root
		unknown, err := acme.OverrideFromEnv(&v, []string{"ACME_NAME=alice"})
		x.NoError(err)
		x.Empty(unknown)

		x.Nil(v.Note)
		x.Nil(v.On)
		x.Nil(v.Level)
		x.Nil(v.Absent)
	})
	t.Run("a value that reads itself is read as one", func(t *testing.T) {
		x := require.New(t)

		var v Root
		_, err := acme.OverrideFromEnv(&v, []string{"ACME_WHOLE=raw: yes"})
		x.NoError(err)
		x.Equal("raw: yes", v.Whole.Raw)
	})
	t.Run("what was already there is left alone", func(t *testing.T) {
		x := require.New(t)

		v := Root{Name: "alice", Renamed: "kept"}
		_, err := acme.OverrideFromEnv(&v, []string{"ACME_NAME=bob"})
		x.NoError(err)

		x.Equal("bob", v.Name)
		x.Equal("kept", v.Renamed)
	})
	t.Run("what is not read from is not named", func(t *testing.T) {
		x := require.New(t)

		v := Root{}
		unknown, err := acme.OverrideFromEnv(&v, []string{
			"ACME_HIDDEN=no",
			"ACME_SECRET=no",
		})
		x.NoError(err)
		x.Equal([]string{"ACME_HIDDEN", "ACME_SECRET"}, unknown)
		x.Empty(v.Hidden)
	})
	t.Run("a name nothing answers to is reported", func(t *testing.T) {
		x := require.New(t)

		var v Root
		unknown, err := acme.OverrideFromEnv(&v, []string{
			"ACME_NAM=alice",   // name, not nam
			"ACME_VERSION=1.0", // the build puts this one here
			"PATH=/usr/bin",    // not ours to look at
			"ACME_NAME=alice",
		})
		x.NoError(err)
		x.Equal([]string{"ACME_NAM", "ACME_VERSION"}, unknown)
		x.Equal("alice", v.Name)
	})
	t.Run("another app's variables are not ours", func(t *testing.T) {
		x := require.New(t)

		// The prefix is the whole of what keeps two payday apps on one machine
		// out of each other's configuration, so it is worth one line saying
		// that it does.
		var v Root
		unknown, err := config.For("other").OverrideFromEnv(&v, []string{"ACME_NAME=alice"})
		x.NoError(err)
		x.Empty(unknown)
		x.Empty(v.Name)
	})
	t.Run("a value that does not fit is refused", func(t *testing.T) {
		x := require.New(t)

		var v Root
		_, err := acme.OverrideFromEnv(&v, []string{"ACME_NESTED_COUNT=lots"})
		x.ErrorContains(err, "ACME_NESTED_COUNT")
	})
	t.Run("nothing to read into is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := acme.OverrideFromEnv(Root{}, []string{"ACME_NAME=alice"})
		x.ErrorContains(err, "pointer to a struct")
	})
	t.Run("nothing is read when nothing is said", func(t *testing.T) {
		x := require.New(t)

		var v Root
		unknown, err := acme.OverrideFromEnv(&v, []string{"PATH=/usr/bin"})
		x.NoError(err)
		x.Empty(unknown)
		x.Equal(Root{}, v)
	})
}

// The template this came from wrote its defaults in afterwards, and had a test
// holding the two in the right order. Nothing is written in afterwards here, so
// there is no order to hold: the default is what the value answers when nobody
// gave it one, and the environment is read before anybody asks.
func TestADefaultCannotUndoTheEnvironment(t *testing.T) {
	x := require.New(t)

	type Config struct {
		Server config.ServerConfig `yaml:"server"`
	}

	x.Equal(config.DefaultAddr, Config{}.Server.ListenAddr())

	var c Config
	_, err := acme.OverrideFromEnv(&c, []string{"ACME_SERVER_ADDR=:1234"})
	x.NoError(err)
	x.Equal(":1234", c.Server.ListenAddr())
}

var _ yaml.BytesUnmarshaler = (*Whole)(nil)
