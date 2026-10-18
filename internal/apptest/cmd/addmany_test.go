package cmd_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"

	app "github.com/lesomnus/payday/internal/apptest"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	entreading "github.com/lesomnus/payday/internal/apptest/internal/ent/reading"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/robot"
)

// An AddMany writes the rows an Add of each of its items would have written,
// and these are the rules an Add is held to, asked of each item. payday's
// layers say them as the rule `bare.<E>WithAddRule` puts on the server behind
// them, so an entity that asks for AddMany is held to them without a layer
// having changed -- and an AddMany written past one of them is what these are
// here to catch.

// robots is an AddMany of a robot in `tenant` for every alias, in order.
func robots(tenant pdid.Id, aliases ...string) *app.RobotAddManyRequest {
	items := make([]*app.RobotAddRequest, len(aliases))
	for i, v := range aliases {
		items[i] = app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
			Alias:  v,
		}.Build()
	}

	return app.RobotAddManyRequest_builder{Items: items}.Build()
}

// idOf is the identifier a row was answered with.
func idOf(t *testing.T, v []byte) pdid.Id {
	t.Helper()

	k, err := pdid.From(v)
	require.NoError(t, err)

	return k
}

// field is where a refusal says it was about.
func field(t *testing.T, err error) string {
	t.Helper()

	vs := pderr.Violations(err)
	require.Len(t, vs, 1, "%v", err)

	return vs[0].Field
}

// TestAnAddManyIsAskedWhatAnAddIs: the gate, of every item.
//
// The gate is what refuses a row put into a tenant the caller cannot see, and
// the Add it said that in was the only place it was said. An AddMany inherited
// from the server behind would have planted every one of these.
func TestAnAddManyIsAskedWhatAnAddIs(t *testing.T) {
	b, ctx := build(t)

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	require.NoError(t, err)

	t.Run("a row into a tenant the caller cannot see, wherever it is in the call", func(t *testing.T) {
		x := require.New(t)

		// The last of three, so the two before it were asked and let through:
		// the refusal is of the call, and nothing of it is written.
		req := robots(b.Tenant, "arm-01", "arm-02", "planted")
		req.GetItems()[2].SetTenant(app.TenantRef_builder{Id: other.GetId()}.Build())

		_, err := b.Walled.Robot().AddMany(b.as(ctx), req)
		x.Equal(codes.NotFound, status.Code(err))
		x.True(strings.HasPrefix(status.Convert(err).Message(), "items[2]: "), "the refusal does not name the item: %v", err)

		n, err := b.Ent.Robot.Query().Where(robot.AliasIn("arm-01", "arm-02", "planted")).Count(ctx)
		x.NoError(err)
		x.Zero(n, "a call refused for one of its items wrote the others")
	})

	t.Run("a stamp an item asserts", func(t *testing.T) {
		x := require.New(t)

		req := robots(b.Tenant, "arm-03", "arm-04")
		req.GetItems()[1].SetDateAttested(timestamppb.Now())

		_, err := b.Walled.Robot().AddMany(b.as(ctx), req)
		x.Equal(codes.InvalidArgument, status.Code(err))
		x.Equal("items[1].date_attested", field(t, err))
	})

	t.Run("and every item let through is written, in the order asked", func(t *testing.T) {
		x := require.New(t)

		res, err := b.Walled.Robot().AddMany(b.as(ctx), robots(b.Tenant, "arm-05", "arm-06", "arm-07"))
		x.NoError(err)

		got := []string{}
		for _, v := range res.GetItems() {
			got = append(got, v.GetAlias())

			_, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
				Ref: app.RobotRef_builder{Id: v.GetId()}.Build(),
			}.Build())
			x.NoError(err, "a row it answered with is not there to read")
		}
		x.Equal([]string{"arm-05", "arm-06", "arm-07"}, got)
	})

	t.Run("and the deployment's own path is not asked", func(t *testing.T) {
		x := require.New(t)

		// A different server, not a privilege: the rule is the gate's, and
		// Ungated is a stack the gate was never put on.
		req := robots(b.Tenant, "theirs")
		req.GetItems()[0].SetTenant(app.TenantRef_builder{Id: other.GetId()}.Build())

		_, err := b.Ungated.Robot().AddMany(ctx, req)
		x.NoError(err)
	})
}

// TestAnAddManyNamesAndStampsEveryItem: the Sink, of every item.
//
// On the Sink because both stacks are built on it, so these go for the path a
// deployment writes its own rows through as well -- which is the one asked
// here.
func TestAnAddManyNamesAndStampsEveryItem(t *testing.T) {
	b, ctx := build(t)

	t.Run("a name is folded, and one is made up for an item nobody named", func(t *testing.T) {
		x := require.New(t)

		res, err := b.Ungated.Robot().AddMany(ctx, robots(b.Tenant, "  Arm-01 ", "", ""))
		x.NoError(err)

		vs := res.GetItems()
		x.Len(vs, 3)
		x.Equal("arm-01", vs[0].GetAlias())
		for _, v := range vs[1:] {
			x.NoError(slug.Validate(v.GetAlias()), "a made-up name is not one")
		}
		x.NotEqual(vs[1].GetAlias(), vs[2].GetAlias(), "two items were made up the same name")
	})

	t.Run("a name that cannot be one is refused, naming the item", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Robot().AddMany(ctx, robots(b.Tenant, "arm-02", "Not An Alias!!"))
		x.Equal(codes.InvalidArgument, status.Code(err))
		x.Equal("items[1].alias", field(t, err))

		n, err := b.Ent.Robot.Query().Where(robot.Alias("arm-02")).Count(ctx)
		x.NoError(err)
		x.Zero(n)
	})

	t.Run("and every reading is stamped with its robot's tenant", func(t *testing.T) {
		x := require.New(t)

		other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
		x.NoError(err)

		mine := b.robot(t, ctx, b.Tenant, "arm-03")
		theirs := b.robot(t, ctx, idOf(t, other.GetId()), "arm-04")

		// Each item's own robot, in two tenants, and one item asserting a
		// tenant of its own -- which a stamp takes no word for.
		items := []*app.ReadingAddRequest{reading(mine, 1), reading(theirs, 2), reading(mine, 3)}
		items[2].SetTenantId(other.GetId())

		res, err := b.Ungated.Reading().AddMany(ctx, app.ReadingAddManyRequest_builder{Items: items}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 3)

		for i, want := range []pdid.Id{b.Tenant, idOf(t, other.GetId()), b.Tenant} {
			id := idOf(t, res.GetItems()[i].GetId())
			v, err := b.Ent.Reading.Query().Where(entreading.Id(id.Uuid())).Only(ctx)
			x.NoError(err)
			x.Equal(want.Uuid(), v.TenantId, "item %d is stamped with another tenant than its robot's", i)
		}
	})
}

// TestAnAddManyTriesAgainForANameItMadeUp, as an Add does -- the whole call,
// since the refusal of a statement does not say which of its rows collided.
func TestAnAddManyTriesAgainForANameItMadeUp(t *testing.T) {
	t.Run("with every name it made up made up again", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		// The first name made up is taken, and every one after it is free.
		var n int
		s := b.sink(t, slug.NamerFunc(func(_ context.Context, _ string, given string, _ proto.Message) (string, error) {
			if given != "" {
				return slug.ParseAlias(given)
			}

			n++
			if n == 1 {
				return "taken", nil
			}

			return fmt.Sprintf("free-%d", n), nil
		}))

		_, err := s.Robot().AddMany(ctx, robots(b.Tenant, "taken"))
		x.NoError(err)

		res, err := s.Robot().AddMany(ctx, robots(b.Tenant, "", "given", ""))
		x.NoError(err)

		got := []string{}
		for _, v := range res.GetItems() {
			got = append(got, v.GetAlias())
		}
		x.Equal([]string{"free-3", "given", "free-4"}, got)
		x.Equal(4, n, "the second try was not made from fresh names")
	})

	t.Run("and gives up, passing the refusal on", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		var n int
		only := fixed("only-one")
		s := b.sink(t, slug.NamerFunc(func(ctx context.Context, entity string, given string, m proto.Message) (string, error) {
			if given == "" {
				n++
			}

			return only.Name(ctx, entity, given, m)
		}))

		_, err := s.Robot().AddMany(ctx, robots(b.Tenant, ""))
		x.NoError(err)

		n = 0
		_, err = s.Robot().AddMany(ctx, robots(b.Tenant, "arm-01", ""))
		x.Equal(codes.AlreadyExists, status.Code(err))
		x.Equal(slug.Tries, n)
	})

	t.Run("but not for a call whose every name was given", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		var n int
		s := b.sink(t, slug.NamerFunc(func(_ context.Context, _ string, given string, _ proto.Message) (string, error) {
			n++
			return slug.ParseAlias(given)
		}))

		_, err := s.Robot().AddMany(ctx, robots(b.Tenant, "arm-01"))
		x.NoError(err)

		n = 0
		_, err = s.Robot().AddMany(ctx, robots(b.Tenant, "arm-02", "arm-01"))
		x.Equal(codes.AlreadyExists, status.Code(err))
		x.Equal(2, n, "a call that named every row was tried again")
	})
}

// TestAnAddManyAnswersWithNoSecret: the Secret layer, of every row it answers
// with -- and the trail, which is a second copy nothing erases.
func TestAnAddManyAnswersWithNoSecret(t *testing.T) {
	x := require.New(t)
	// At the content level that keeps the row (#92): what is under test is
	// what the trail holds of one.
	b, ctx := keeping(t, "value")

	secret := []byte("hunter2-and-something-long-enough-to-find")

	req := robots(b.Tenant, "arm-01", "arm-02")
	for _, v := range req.GetItems() {
		v.SetSecret(secret)
	}

	res, err := b.Walled.Robot().AddMany(b.as(ctx), req)
	x.NoError(err)
	x.Len(res.GetItems(), 2)
	for _, v := range res.GetItems() {
		x.Empty(v.GetSecret(), "an AddMany answered with the secret")
	}

	// It was stored, or the above passes because nothing was written.
	vs, err := b.Ent.Robot.Query().Where(robot.AliasIn("arm-01", "arm-02")).All(ctx)
	x.NoError(err)
	x.Len(vs, 2)
	for _, v := range vs {
		x.Equal(secret, v.Secret)
	}

	rows, err := b.Ent.Audit.Query().Where(entaudit.Action(app.RobotService_AddMany_FullMethodName)).All(ctx)
	x.NoError(err)
	x.Len(rows, 2)
	for _, w := range rows {
		x.NotEmpty(w.Value, "no row carried a value, so this never looked at one")
		x.NotContains(string(w.Value), string(secret), "the trail holds the secret")
	}
}

// TestAnAddManyIsOnTheTrail a row at a time, as the Adds it stands for would
// have been: under the tenant each row is in, naming the row.
func TestAnAddManyIsOnTheTrail(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	res, err := b.Walled.Robot().AddMany(b.as(ctx), robots(b.Tenant, "arm-01", "arm-02", "arm-03"))
	x.NoError(err)

	rows, err := b.Ent.Audit.Query().Where(entaudit.Action(app.RobotService_AddMany_FullMethodName)).All(ctx)
	x.NoError(err)
	x.Len(rows, 3)

	want := map[string]bool{}
	for _, v := range res.GetItems() {
		want[string(v.GetId())] = true
	}
	for _, w := range rows {
		x.True(want[string(w.ObjectId[:])], "a line of the trail names no row the call wrote")
		delete(want, string(w.ObjectId[:]))

		x.Equal(b.Holder.Uuid(), w.ActorId)
		x.Equal(b.Tenant.Uuid(), w.TenantId)
	}
	x.Empty(want, "a row the call wrote is not on the trail")
}

// TestAnAddManyAsksAboutARowOnceInATransaction, which is the transaction's and
// not the call's: alone, an AddMany asks each item's questions, as that many
// Adds would; in a transaction begun with `dialect.BeginTx` -- an app's
// operation, or a Batch -- it asks each row once. And either way its rows are
// one statement.
func TestAnAddManyAsksAboutARowOnceInATransaction(t *testing.T) {
	for _, tc := range []struct {
		what  string
		begun bool
		want  int
	}{
		{"in a transaction begun with dialect.BeginTx", true, 1},
		{"and in none, every item", false, 10},
	} {
		t.Run(tc.what, func(t *testing.T) {
			x := require.New(t)
			b, ctx := build(t)
			robot := b.robot(t, ctx, b.Tenant, "arm")

			a := &asked{}
			inserts := 0
			drv := dialect.DebugWithContext(a.on(b.Drv), func(_ context.Context, vs ...any) {
				if q := fmt.Sprint(vs...); strings.Contains(q, "INSERT INTO") && strings.Contains(q, "reading") {
					inserts++
				}
			})
			if tc.begun {
				v, tx, err := dialect.BeginTx(ctx, drv)
				x.NoError(err)
				defer tx.Commit()
				drv = v
			}

			s, err := enttx.Rebind(b.Walled, drv)
			x.NoError(err)

			items := make([]*app.ReadingAddRequest, 10)
			for i := range items {
				items[i] = reading(robot, float64(i))
			}
			res, err := s.Reading().AddMany(b.as(ctx), app.ReadingAddManyRequest_builder{Items: items}.Build())
			x.NoError(err)
			x.Len(res.GetItems(), 10)

			x.Equal(tc.want, a.walled, "the gate's read through the wall")
			x.Equal(tc.want, a.tenancy, "the sink's read of the robot's tenant")
			x.Equal(1, inserts, "the rows were not written in one statement")
		})
	}
}
