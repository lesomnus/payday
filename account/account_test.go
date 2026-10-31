package account_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/account"
	"github.com/lesomnus/payday/pdid"
)

const (
	tenantDomain pdid.Domain = 1
	holderDomain pdid.Domain = 2
)

func TestParseSelector(t *testing.T) {
	id := pdid.New(tenantDomain)

	for _, tc := range []struct {
		in   string
		want account.Selector
	}{
		{"", account.Selector{}},
		{"   ", account.Selector{}},
		{"acme", account.Selector{Alias: "acme"}},
		{"@acme", account.Selector{Alias: "acme"}},
		{" @Acme ", account.Selector{Alias: "acme"}},
		{id.String(), account.Selector{Id: id}},
	} {
		t.Run(tc.in, func(t *testing.T) {
			x := require.New(t)

			v, err := account.ParseSelector(tc.in)
			x.NoError(err)
			x.Equal(tc.want, v)
		})
	}

	// What names no tenant at all is refused as that, and not answered as a
	// tenant somebody else is in.
	for _, in := range []string{
		"00000000-0000-0000-0000-000000000000",
		"0a1b2c3d-0000-4000-8000-000000000000",
		"acme, hooli",
		"@",
		"a b",
	} {
		t.Run(in, func(t *testing.T) {
			_, err := account.ParseSelector(in)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestSelectorOf(t *testing.T) {
	const header = "acme-workspace"

	t.Run("names none without the header", func(t *testing.T) {
		x := require.New(t)

		v, err := account.SelectorOf(context.Background(), header)
		x.NoError(err)
		x.True(v.IsZero())

		v, err = account.SelectorOf(metadata.NewIncomingContext(context.Background(), metadata.Pairs("other", "acme")), header)
		x.NoError(err)
		x.True(v.IsZero())
	})

	t.Run("names the one it says", func(t *testing.T) {
		x := require.New(t)

		v, err := account.SelectorOf(metadata.NewIncomingContext(context.Background(), metadata.Pairs(header, "@acme")), header)
		x.NoError(err)
		x.Equal("acme", v.Alias)
		x.Equal("@acme", v.String())
	})

	t.Run("refuses two, rather than taking either", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(header, "acme", header, "hooli"))
		_, err := account.SelectorOf(ctx, header)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}
