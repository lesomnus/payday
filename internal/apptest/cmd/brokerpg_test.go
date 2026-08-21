package cmd_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/config/brokerpg"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
)

// The case `broker: memory` gets wrong, run end to end.
//
// Two servers on one database is two replicas for every purpose this is about:
// each has its own broker, its own listener, and its own subscribers. A client
// watching against one, a write landing on the other -- with `memory` the
// stream stays open, looks healthy, and never hears anything again.

// TestAWatchHearsAWriteOnTheOtherReplica.
func TestAWatchHearsAWriteOnTheOtherReplica(t *testing.T) {
	x := require.New(t)

	dsn := os.Getenv(pdtest.Postgres)
	if dsn == "" {
		t.Skipf("%s is not set; a broker that crosses processes needs the database that carries it", pdtest.Postgres)
	}

	// One database, named the way a deployment does: `postgres`, which
	// `config/brokerpg` registered, with no address of its own -- the rows'
	// own database is the answer for this broker rather than a fallback.
	w := config.WatchConfig{Broker: brokerpg.Name}
	db := dbOf(t)

	one, ctx := buildOn(t, db, w)
	two, _ := buildOn(t, db, w)

	// `LISTEN` is a round trip and `Build` answers before it has necessarily
	// landed. A publish that beats it is lost, which is the guarantee and not
	// a defect.
	time.Sleep(500 * time.Millisecond)

	vs := one.sow(ctx, x, one.Tenant, 1, "arm-")

	_, out, stop := one.watching(t, app.RobotWatchRequest_builder{
		Filters: []*app.RobotFilter{
			app.RobotFilter_builder{Ref: app.RobotRef_builder{Id: vs[0].GetId()}.Build()}.Build(),
		},
	}.Build())
	defer stop()

	next(t, out) // the snapshot

	// The write, on the **other** server, and over its own connection: what
	// publishes is the interceptor, so a direct call into a server writes the
	// row and tells nobody. Nothing connects the two but the database.
	elsewhere := app.NewClient(pdtest.Serve(t, two.grpc(t, pdtest.Logging(t))))

	_, err := elsewhere.Robot().Patch(two.travels(ctx), app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: vs[0].GetId()}.Build(),
		Alias:       z.Ptr("renamed-elsewhere"),
		DateUpdated: vs[0].GetDateUpdated(),
	}.Build())
	x.NoError(err)

	res := next(t, out)
	x.Len(res.GetItems(), 1)
	x.Equal("renamed-elsewhere", res.GetItems()[0].GetValue().GetAlias(),
		"a write on the other replica did not reach this stream")
}
