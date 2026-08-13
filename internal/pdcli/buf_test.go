package pdcli_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// TestABufIsNamedRatherThanFound.
//
// The environment wins outright and nothing checks it against a digest: naming
// a buf is saying which one, and a payday that refused it would be refusing the
// answer it asked for. It is the escape hatch for the machine with no route to
// GitHub and for somebody bisecting buf itself.
func TestABufIsNamedRatherThanFound(t *testing.T) {
	x := require.New(t)

	t.Setenv(pdcli.BufEnv, "/somewhere/of/my/own/buf")

	v, err := pdcli.Buf(t.Context())
	x.NoError(err)
	x.Equal("/somewhere/of/my/own/buf", v)
}

// TestTheBufThisPaydayPinsIsTheBufThatRuns.
//
// The version is not a thing an app is asked, so this is the assertion that the
// answer is the pinned one and that it is the real thing: it is fetched,
// verified against the digest published beside the release, and then run.
//
// It touches the network on a cold cache, which is the honest shape of the
// thing being tested -- there is no version to check without fetching one.
func TestTheBufThisPaydayPinsIsTheBufThatRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("fetches a release")
	}

	x := require.New(t)

	at, err := pdcli.Buf(t.Context())
	x.NoError(err)
	x.Contains(at, pdcli.BufVersion, "the path says which buf ran")

	b, err := exec.CommandContext(t.Context(), at, "--version").CombinedOutput()
	x.NoError(err)
	x.Equal(pdcli.BufVersion, strings.TrimSpace(string(b)))

	// And again, which is the cached path: nothing is fetched twice, and the
	// answer is the same file.
	again, err := pdcli.Buf(t.Context())
	x.NoError(err)
	x.Equal(at, again)
}
