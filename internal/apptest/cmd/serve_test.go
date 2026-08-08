package cmd_test

import (
	"net/url"
	"testing"

	"github.com/ncruces/go-sqlite3/vfs/memdb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// TestTheAppIsWhatTheAppWrote is the CP2 checkpoint: the whole of this app's
// own wiring is cmd/config.go and cmd/serve.go, and this is it running.
//
// The question it answers is not "does it work" but "did anything actually
// move". An extraction that only turned code into configuration would show up
// here as a test that has to say as much as the code it replaced.
func TestTheAppIsWhatTheAppWrote(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{
		Db: config.DbConfig{
			Driver: "sqlite3",
			Dsn:    memdb.TestDB(t, url.Values{"_pragma": {"foreign_keys(1)"}}),
		},
		// Named rather than defaulted, which is the one thing this app's
		// configuration is refused for saying nothing about; see
		// `config.WatchConfig`.
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	x.NoError(s.Ent.Schema.Create(ctx))

	// The deployment puts the first tenant there, through the server the wall
	// was never installed on -- there is nobody to be inside a tenant yet.
	tenant, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)

	k, err := pdid.From(tenant.GetId())
	x.NoError(err)
	x.Equal(pd.TenantDomain, k.Domain(),
		"the minter came from the schema and nothing in this app wrote it")
}

// TestServes travels the whole path a request travels: the chain payday
// assembles, the codec, the status code turned into an error and back.
func TestServes(t *testing.T) {
	x := pdtest.NewX(t)
	ctx := t.Context()

	c := cmd.Config{
		Db: config.DbConfig{
			Driver: "sqlite3",
			Dsn:    memdb.TestDB(t, url.Values{"_pragma": {"foreign_keys(1)"}}),
		},
		// Named rather than defaulted, which is the one thing this app's
		// configuration is refused for saying nothing about; see
		// `config.WatchConfig`.
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))

	conn := pdtest.Serve(t, s.Grpc(ctx, c, pdtest.Logging(t)))
	client := app.NewClient(conn)

	// Nobody vouched for this call, so the wall refuses it rather than serving
	// it as everybody. That it comes back through a real connection is the
	// point: an interceptor chain is not something a direct call travels.
	_, err = client.Tenant().Get(ctx, app.TenantGetByAlias("acme"))
	x.ErrCode(codes.Unauthenticated, err)
}
