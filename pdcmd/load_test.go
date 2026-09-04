package pdcmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
)

// TestANameTheAppReadsItselfIsNotATypo: what [Reads] claims is left out of
// the warning, and everything else under the prefix is still in it.
func TestANameTheAppReadsItselfIsNotATypo(t *testing.T) {
	x := require.New(t)
	l := config.For("roster")

	unknown := []string{"ROSTER_ACCOUNT_KEY_CONTOSO", "ROSTER_ACCOUNT_KEY_FABRIKAM", "ROSTER_VERSION", "ROSTER_DB_DNS"}

	x.Equal(unknown, unread(l, unknown, nil), "with nothing claimed, everything is a warning")
	x.Equal([]string{"ROSTER_DB_DNS"}, unread(l, unknown, []string{"ACCOUNT_KEY_", "VERSION"}),
		"a typo was hidden by a claim, or a claim was warned about")

	// The prefix is the app's plus what was said: a claim is not a substring.
	x.Equal(unknown, unread(l, unknown, []string{"KEY_"}))
}
