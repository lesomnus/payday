package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
)

type Loadable struct {
	Name   string `yaml:"name"`
	Secret string `yaml:"secret"`

	Nested struct {
		Count int `yaml:"count"`
	} `yaml:"nested"`
}

func write(t *testing.T, dir string, name string, body string) string {
	t.Helper()

	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

func TestFor(t *testing.T) {
	t.Run("the app's name is where its file and its variables come from", func(t *testing.T) {
		x := require.New(t)

		l := config.For("go-app")
		x.Equal("go-app", l.Name())
		x.Equal("GO_APP_", l.Prefix())
		x.Equal([]string{"go-app.yaml", "go-app.yml"}, l.Paths())
	})
	t.Run("a name is spelled the way a variable may be", func(t *testing.T) {
		x := require.New(t)

		// Whatever the app is called, the prefix has to be a name a shell can
		// set, so anything that is not a letter or a digit is an underscore.
		x.Equal("MY_APP_", config.For("my.app").Prefix())
		x.Equal("MY_APP_", config.For("My App").Prefix())
		x.Equal("V2_", config.For("v2").Prefix())
	})
	t.Run("an app with no name is refused", func(t *testing.T) {
		x := require.New(t)

		// Not an error: an empty name reads `.yaml` and `_*`, and both of those
		// would be found sooner or later.
		x.Panics(func() { config.For("") })
	})
}

func TestLoad(t *testing.T) {
	t.Run("a file is read, and then the environment over it", func(t *testing.T) {
		x := require.New(t)

		p := write(t, t.TempDir(), "acme.yaml", "name: from the file\nnested:\n  count: 1\n")

		var c Loadable
		from, err := acme.Load(&c, p, []string{"ACME_NESTED_COUNT=2"})
		x.NoError(err)
		x.Equal(p, from.Path)
		x.Empty(from.Unknown)

		x.Equal("from the file", c.Name)
		// The order is the whole of it: a deployment that sets a variable
		// expects the variable, and a file that won would say so nowhere.
		x.Equal(2, c.Nested.Count)
	})
	t.Run("a secret is named in the file rather than written in it", func(t *testing.T) {
		x := require.New(t)

		t.Setenv("ACME_TEST_SECRET", "a secret nobody else knows")
		p := write(t, t.TempDir(), "acme.yaml", "secret: ${env:ACME_TEST_SECRET}\nname: ${env:ACME_TEST_ABSENT:-a default}\n")

		var c Loadable
		_, err := acme.Load(&c, p, nil)
		x.NoError(err)
		x.Equal("a secret nobody else knows", c.Secret)
		x.Equal("a default", c.Name)
	})
	t.Run("a name the file asks for and nothing set is refused", func(t *testing.T) {
		x := require.New(t)

		// Rather than an empty string, which is a database with no password
		// and a server that starts anyway.
		p := write(t, t.TempDir(), "acme.yaml", "secret: ${env:ACME_TEST_NOBODY_SET_THIS}\n")

		var c Loadable
		_, err := acme.Load(&c, p, nil)
		x.ErrorContains(err, "ACME_TEST_NOBODY_SET_THIS")
	})
	t.Run("the app's own file is what is read when none was named", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		write(t, dir, "acme.yml", "name: the second spelling\n")
		t.Chdir(dir)

		var c Loadable
		from, err := acme.Load(&c, "", nil)
		x.NoError(err)
		x.Equal("acme.yml", from.Path)
		x.Equal("the second spelling", c.Name)
	})
	t.Run("having no file at all is a way to be configured", func(t *testing.T) {
		x := require.New(t)

		t.Chdir(t.TempDir())

		var c Loadable
		from, err := acme.Load(&c, "", []string{"ACME_NAME=alice"})
		x.NoError(err)
		x.Empty(from.Path, "and the empty path is how the app can say so")
		x.Equal("alice", c.Name)
	})
	t.Run("a file that was named and is not there is refused", func(t *testing.T) {
		x := require.New(t)

		// The one place a missing file is an error. A misspelt path would
		// otherwise be a server running on defaults, serving happily, with
		// nothing anywhere saying which file it did not find.
		var c Loadable
		_, err := acme.Load(&c, filepath.Join(t.TempDir(), "nope.yaml"), nil)
		x.ErrorIs(err, os.ErrNotExist)
		x.ErrorContains(err, "nope.yaml")
	})
	t.Run("a file that is not YAML is refused", func(t *testing.T) {
		x := require.New(t)

		p := write(t, t.TempDir(), "acme.yaml", "name: [unclosed\n")

		var c Loadable
		_, err := acme.Load(&c, p, nil)
		x.Error(err)
	})
	t.Run("a name nothing answers to comes back rather than refusing the load", func(t *testing.T) {
		x := require.New(t)

		t.Chdir(t.TempDir())

		var c Loadable
		from, err := acme.Load(&c, "", []string{"ACME_NAM=alice", "PATH=/usr/bin"})
		x.NoError(err)
		x.Equal([]string{"ACME_NAM"}, from.Unknown)
	})
}
