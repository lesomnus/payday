package rbac_test

import (
	"testing"

	"github.com/lesomnus/payday/rbac"
	"github.com/stretchr/testify/require"
)

func TestRoleHoldersExpandBindingsAndOwnTheirResults(t *testing.T) {
	r := rbac.Rules{
		Roles: map[string]rbac.Role{
			"@acme/buyer":  {Methods: []string{"/app.OrderService/Get"}},
			"@acme/empty":  {},
			"@other/buyer": {},
		},
		Groups: map[string]rbac.Group{
			"@acme/staff": {Members: []string{"@acme/zoe", "@acme/amy", "@acme/zoe"}},
			"@acme/idle":  {Members: []string{"@acme/idle"}},
		},
		Bindings: []rbac.Binding{
			{Role: "@acme/buyer", Group: "@acme/staff"},
			{Role: "@acme/buyer", Holder: "@acme/amy"},
			{Role: "@acme/buyer", Holder: "@acme/bob"},
			{Role: "@other/buyer", Holder: "@other/amy"},
		},
	}
	s, err := rbac.New(r)
	require.NoError(t, err)
	r.Groups["@acme/staff"].Members[0] = "@acme/changed"
	r.Bindings[2].Holder = "@acme/changed"
	want := []string{"@acme/amy", "@acme/bob", "@acme/zoe"}
	holders, err := s.Holders(t.Context(), "@acme/buyer")
	require.NoError(t, err)
	require.Equal(t, want, holders)
	for _, name := range holders {
		grants, err := s.Grants(t.Context(), name)
		require.NoError(t, err)
		require.True(t, rbac.Allows(grants, "/app.OrderService/Get"))
	}
	holders[0] = "@acme/changed"
	holders, err = s.Holders(t.Context(), "@acme/buyer")
	require.NoError(t, err)
	require.Equal(t, want, holders)
	for _, role := range []string{"@acme/missing", "@acme/empty"} {
		holders, err := s.Holders(t.Context(), role)
		require.NoError(t, err)
		require.Empty(t, holders)
	}
	holders, err = s.Holders(t.Context(), "@other/buyer")
	require.NoError(t, err)
	require.Equal(t, []string{"@other/amy"}, holders, "role membership does not depend on its method count")
	_, err = s.Holders(t.Context(), "buyer")
	require.Error(t, err)
}

func TestRoleHoldersUseThePinnedGrantRevision(t *testing.T) {
	s, err := rbac.NewStore(rules())
	require.NoError(t, err)
	pinned := s.Pin(t.Context())
	next := rules()
	next.Bindings[0] = rbac.Binding{Role: "@acme/reader", Holder: "@acme/new"}
	require.NoError(t, s.Replace(next))
	holders, err := s.Holders(pinned, "@acme/reader")
	require.NoError(t, err)
	require.Equal(t, []string{"@acme/kamino"}, holders)
	grants, err := s.Grants(pinned, holders[0])
	require.NoError(t, err)
	require.NotEmpty(t, grants)
	holders, err = s.Holders(t.Context(), "@acme/reader")
	require.NoError(t, err)
	require.Equal(t, []string{"@acme/new"}, holders)
	_, err = s.Holders(pinned, "bad")
	require.Error(t, err)
	other, err := rbac.NewStore(rbac.Rules{})
	require.NoError(t, err)
	holders, err = other.Holders(pinned, "@acme/reader")
	require.NoError(t, err)
	require.Empty(t, holders, "a context pinned by another store must not supply this store's policy")
}
