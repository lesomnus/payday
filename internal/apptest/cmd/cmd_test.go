package cmd_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/apptest/cmd"
)

// TestConfigEnvIsReadFromTheStruct is why this command exists and why it could
// not have lived on `pd`.
//
// A documented list of environment variables goes out of date on the commit
// that adds a field, and the way that is found out is somebody setting one that
// does nothing. This is read from the struct, so it cannot.
func TestConfigEnvIsReadFromTheStruct(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	root := cmd.Cmd(&c)

	out := &bytes.Buffer{}
	root.Writer = out
	x.NoError(root.Run(t.Context(), []string{"config", "env"}))

	got := out.String()
	for _, want := range []string{
		"APPTEST_DB_DSN",
		"APPTEST_SERVER_ADDR",
		// The one this app added since the list would have been written, which
		// is the whole point.
		"APPTEST_WATCH_BROKER",
	} {
		x.Contains(got, want)
	}

	// And nothing about the values, since the use of this is to be pasted
	// somewhere.
	x.NotContains(got, "=")
}

// TestConfigEnvSaysWhetherAndNeverWhat.
func TestConfigEnvSaysWhetherAndNeverWhat(t *testing.T) {
	x := require.New(t)

	t.Setenv("APPTEST_DB_DSN", "postgres://someone:hunter2@db/app")

	var c cmd.Config
	root := cmd.Cmd(&c)

	out := &bytes.Buffer{}
	root.Writer = out
	x.NoError(root.Run(t.Context(), []string{"config", "env", "--set"}))

	got := out.String()
	x.Contains(got, "APPTEST_DB_DSN\tset")
	x.NotContains(got, "hunter2")

	// One that is not set says so rather than being left out: what a deployment
	// wants is the whole list with a mark against it.
	x.True(strings.Contains(got, "APPTEST_SERVER_ADDR\t-"))
}
