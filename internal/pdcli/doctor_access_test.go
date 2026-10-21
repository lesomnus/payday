package pdcli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// TestAnAppThatDoesNotWireTheAccessRecordIsTold: nothing fails without it,
// which is why it is asked about (#111). Each part is found by what the app's
// own code names, and what a test wires does not count.
func TestAnAppThatDoesNotWireTheAccessRecordIsTold(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	x.NoError(os.MkdirAll(filepath.Join(root, "cmd"), 0o755))
	write := func(name string, body string) {
		x.NoError(os.WriteFile(filepath.Join(root, "cmd", name), []byte(body), 0o644))
	}

	said := found(t, must(pdcli.Discover(root)))
	x.Contains(said, "the access record is not wired")
	x.Contains(said, "pd.AccessRecorder()", "the fix is what to type")

	// A test that builds one is not the app serving one.
	write("serve_test.go", `package cmd

import "github.com/lesomnus/payday/access"

var _ = access.Config{}
`)
	x.Contains(found(t, must(pdcli.Discover(root))), "no interceptor from an `access.Config`")

	// Two of the three, under a name of the app's own.
	write("serve.go", `package cmd

import (
	acc "github.com/lesomnus/payday/access"

	"github.com/acme/thing/server/pd"
)

func wire() {
	w := &acc.Writer{Sink: pd.AccessSink(nil)}
	_ = acc.Config{Writer: w}.Interceptor()
}
`)
	said = found(t, must(pdcli.Discover(root)))
	x.Contains(said, "no `AccessRecorder` among the servers' recorders")
	x.NotContains(said, "no interceptor")
	x.NotContains(said, "no `access.Writer`")

	write("serve.go", `package cmd

import (
	acc "github.com/lesomnus/payday/access"

	"github.com/acme/thing/server/pd"
)

func wire() {
	_ = pd.AccessRecorder()
	w := &acc.Writer{Sink: pd.AccessSink(nil)}
	_ = acc.Config{Writer: w}.Interceptor()
}
`)
	x.NotContains(found(t, must(pdcli.Discover(root))), "the access record is not wired")
}

// TestTheReferenceAppWiresTheAccessRecord is the other half: the app payday is
// tried against is not told.
func TestTheReferenceAppWiresTheAccessRecord(t *testing.T) {
	x := require.New(t)

	x.NotContains(found(t, apptest(t, x)), "the access record is not wired")
}
