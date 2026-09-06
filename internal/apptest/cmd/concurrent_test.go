package cmd_test

import (
	"fmt"
	"sync"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/lesomnus/payday/pdtest"
)

// TestCallsAtOnceAllSayWhoIsCalling.
//
// Every call resolves who is calling by reading the holder the credential
// names, through the server the wall was never installed on. That read is one
// query per call and several calls arrive at once -- a page drawing two lists
// is two, and a devtools panel under React's StrictMode is the same call twice
// -- so what is being asked here is whether the resolver is a thing several
// requests may be in at the same time.
//
// It travels the whole chain rather than calling the resolver directly. What
// would be wrong is not the function; it is the function under the stack it
// runs in.
//
// This passes, and it passed while a browser was refusing every second call
// with the very error it asserts against -- which is the useful part. The
// database a process opens is not the one a page opens, and what was wrong was
// the second one; `ts/sandbox.html` asks the same question where that is true.
func TestCallsAtOnceAllSayWhoIsCalling(t *testing.T) {
	x := pdtest.NewX(t)
	ctx := t.Context()

	c := cmd.Config{Db: dbOf(t), Watch: config.WatchConfig{Broker: config.BrokerMemory}}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))

	// Somebody to be, put there the way a deployment puts the first one there.
	tenant, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)

	_, err = s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.GetId()}.Build(),
		Alias:  "admin",
	}.Build())
	x.NoError(err)

	g, err := s.Grpc(ctx, c, pdtest.Logging(t))
	x.NoError(err)

	client := app.NewClient(pdtest.Serve(t, g))
	as := metadata.AppendToOutgoingContext(ctx, auth.Header, auth.PlainScheme+" @acme/admin")

	for _, n := range []int{2, 4, 16} {
		t.Run(fmt.Sprintf("%d at once", n), func(t *testing.T) {
			x := pdtest.NewX(t)

			errs := make([]error, n)
			wg := sync.WaitGroup{}
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()

					_, errs[i] = client.Robot().List(as, app.RobotListRequest_builder{}.Build())
				}()
			}
			wg.Wait()

			for i, err := range errs {
				x.NoError(err, "the %dth of %d at once", i, n)
			}
		})
	}
}
