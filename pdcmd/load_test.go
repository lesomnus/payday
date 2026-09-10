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

// TestWhatAnOrchestratorInjectedIsNotATypo.
//
// Kubernetes sets one group of these per service in the namespace, named after
// the service and uppercased -- so a deployment whose services are named after
// the app buries the warning under its own plumbing. The first `tenant add` in
// a roster cluster printed fifty-seven of them before its answer, which is not
// noise but the check defeated: the line it exists for would have been entry
// fifty-eight.
func TestWhatAnOrchestratorInjectedIsNotATypo(t *testing.T) {
	x := require.New(t)
	l := config.For("roster")

	// Verbatim from a namespace holding `roster`, `roster-data`, `roster-hydra`
	// and `roster-login`.
	injected := []string{
		"ROSTER_PORT",
		"ROSTER_PORT_80_TCP",
		"ROSTER_PORT_80_TCP_ADDR",
		"ROSTER_PORT_80_TCP_PORT",
		"ROSTER_PORT_80_TCP_PROTO",
		"ROSTER_SERVICE_HOST",
		"ROSTER_SERVICE_PORT",
		"ROSTER_SERVICE_PORT_HTTP",
		"ROSTER_DATA_PORT_8080_TCP_ADDR",
		"ROSTER_DATA_SERVICE_PORT_GRPC",
		"ROSTER_HYDRA_PORT_4445_TCP_PROTO",
		"ROSTER_LOGIN_SERVICE_HOST",
	}
	x.Empty(unread(l, injected, nil), "an orchestrator's own variables were called typos")

	// And the one it is for is still said, standing beside all of them.
	beside := append(append([]string{}, injected...), "ROSTER_LOGIN_ADDRR")
	x.Equal([]string{"ROSTER_LOGIN_ADDRR"}, unread(l, beside, nil))

	// Nothing that is not that shape is swallowed. `_PORT` at the end is the
	// one this trades away, and these are what must not go with it.
	said := []string{
		"ROSTER_DB_DNS",
		"ROSTER_PORTAL",
		"ROSTER_SERVICE_PORTAL",
		"ROSTER_PORT_EIGHTY",
		"ROSTER_PORT_80_SCTP_ADDR",
		"ROSTER_SERVICEHOST",
	}
	x.Equal(said, unread(l, said, nil))
}
