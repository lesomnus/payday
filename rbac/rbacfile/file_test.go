package rbacfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lesomnus/payday/rbac"
	"github.com/lesomnus/payday/rbac/rbacfile"
	"github.com/stretchr/testify/require"
)

func TestFileChangesAreAuthoritativeAndFailuresKeepLastPolicy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "policy.yaml")
	write := func(v string) { require.NoError(t, os.WriteFile(p, []byte(v), 0600)) }
	write("roles:\n  '@acme/reader':\n    methods: [/app.ManifestService/Get]\nbindings:\n  - role: '@acme/reader'\n    holder: '@acme/kamino'\n")
	f, err := rbacfile.Open(rbacfile.Config{File: p})
	require.NoError(t, err)
	gs, err := f.Grants(t.Context(), "@acme/kamino")
	require.NoError(t, err)
	require.True(t, rbac.Allows(gs, "/app.ManifestService/Get"))
	require.Equal(t, p, gs[0].Source)
	for _, bad := range []string{"roles: [", "rolse: {}", "bindings:\n - role: '@acme/typo'\n   holder: '@acme/kamino'\n"} {
		write(bad)
		// cfg waits for two identical reads before accepting a replacement.
		_ = f.Refresh()
		require.Error(t, f.Refresh())
		gs, err = f.Grants(t.Context(), "@acme/kamino")
		require.NoError(t, err)
		require.NotEmpty(t, gs)
	}
	write("roles: {}\ngroups: {}\nbindings: []\n")
	require.NoError(t, f.Refresh())
	require.NoError(t, f.Refresh())
	gs, err = f.Grants(t.Context(), "@acme/kamino")
	require.NoError(t, err)
	require.Empty(t, gs)
	require.NoError(t, os.Remove(p))
	require.Error(t, f.Refresh())
}
