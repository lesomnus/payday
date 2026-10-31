package cmd_test

import (
	"context"
	"errors"
	"net"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/entitle"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cli"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/lesomnus/payday/internal/apptest/internal/ent"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/entitlement"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/robot"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// The copy of what each tenant is entitled to (payday#185), on this app's
// database: kept by `entitle.Follower` from a source of grants, and read by
// `entitle.Has` and `entitle.Limit` in the transaction of the write it decides
// -- on SQLite, and on PostgreSQL where PDTEST_POSTGRES names one.

// grants is a `payday.GrantService` in memory: what a billing service would
// serve, with its grants held here and every stream told of what `say` is
// given.
type grants struct {
	pdpb.UnimplementedGrantServiceServer

	mu      sync.Mutex
	held    map[string]*pdpb.EntitlementGrant
	streams []chan *pdpb.EntitlementGrant
}

func newGrants() *grants { return &grants{held: map[string]*pdpb.EntitlementGrant{}} }

func (s *grants) hold(gs ...*pdpb.EntitlementGrant) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, g := range gs {
		s.held[string(g.GetId())] = g
	}
}

func (s *grants) forget(ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		delete(s.held, id)
	}
}

func (s *grants) say(g *pdpb.EntitlementGrant) {
	s.hold(g)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.streams {
		c <- g
	}
}

func (s *grants) Watch(_ *pdpb.GrantWatchRequest, out grpc.ServerStreamingServer[pdpb.GrantWatchResponse]) error {
	c := make(chan *pdpb.EntitlementGrant, 16)
	s.mu.Lock()
	s.streams = append(s.streams, c)
	s.mu.Unlock()

	if err := out.Send(pdpb.GrantWatchResponse_builder{Ready: &pdpb.GrantWatchReady{}}.Build()); err != nil {
		return err
	}
	for {
		select {
		case <-out.Context().Done():
			return nil
		case g := <-c:
			if err := out.Send(pdpb.GrantWatchResponse_builder{Grant: g}.Build()); err != nil {
				return err
			}
		}
	}
}

func (s *grants) List(_ context.Context, req *pdpb.GrantListRequest) (*pdpb.GrantListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res := &pdpb.GrantListResponse{}
	for _, g := range s.held {
		if len(req.GetTenantId()) == 0 || string(req.GetTenantId()) == string(g.GetTenantId()) {
			res.SetItems(append(res.GetItems(), proto.Clone(g).(*pdpb.EntitlementGrant)))
		}
	}

	return res, nil
}

// source is `s` served, as a client of it.
func source(t *testing.T, s *grants) pdpb.GrantServiceClient {
	t.Helper()

	g := grpc.NewServer()
	pdpb.RegisterGrantServiceServer(g, s)
	t.Cleanup(g.Stop)

	return pdpb.NewGrantServiceClient(pdtest.Serve(t, g))
}

// grantOf is a grant of `key` to `tenant`, in force from an hour ago to a day
// from now unless `window` says otherwise.
func grantOf(id string, tenant pdid.Id, key string, version uint64, v entitle.Value, window ...time.Time) *pdpb.EntitlementGrant {
	from, until := time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)
	if len(window) == 2 {
		from, until = window[0], window[1]
	}

	b := pdpb.EntitlementGrant_builder{
		Id:         []byte(id),
		TenantId:   tenant.Bytes(),
		Key:        key,
		Version:    version,
		ValidFrom:  timestamppb.New(from),
		ValidUntil: timestamppb.New(until),
	}
	switch v.Kind() {
	case entitle.KindOn, entitle.KindOff:
		b.On = proto.Bool(v == entitle.On)
	case entitle.KindLimit:
		n, _ := v.Number()
		b.Limit = proto.Int64(n)
	case entitle.KindUnlimited:
		b.Unlimited = &pdpb.EntitlementGrant_Unlimited{}
	}

	return b.Build()
}

// inTx runs `f` in a transaction of its own on the app's database, as an
// operation of the domain does, and commits when it answers nil.
func (b *built) inTx(ctx context.Context, f func(ctx context.Context, drv dialect.Driver) error) error {
	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := f(ctx, drv); err != nil {
		return err
	}

	return tx.Commit()
}

// copied is the rows of the copy `tenant` holds, by the grant each copies.
func (b *built) copied(t *testing.T, ctx context.Context, tenant pdid.Id) map[string]*ent.Entitlement {
	t.Helper()

	vs, err := b.Ent.Entitlement.Query().Where(entitlement.TenantId(tenant.Uuid())).All(ctx)
	require.NoError(t, err)

	out := map[string]*ent.Entitlement{}
	for _, v := range vs {
		out[string(v.GrantId)] = v
	}

	return out
}

// TestTheCopyIsWhatTheSourceSays: a follower applies the source's grants to
// the copy -- a newer version over an older, an ended grant forgotten once
// the source lists it no more -- each write on the trail as the act it is,
// under the grant's tenant, and the reads of the `entitle` package answer
// from it inside a transaction.
func TestTheCopyIsWhatTheSourceSays(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	gone := pdid.New(pd.TenantDomain)
	src := newGrants()
	src.hold(
		grantOf("export", b.Tenant, "apptest.export", 1, entitle.On),
		grantOf("robots", b.Tenant, "apptest.robots", 1, entitle.Limited(2)),
		grantOf("seats", b.Tenant, "apptest.seats", 1, entitle.Unlimited),
		grantOf("theirs", gone, "apptest.robots", 1, entitle.Limited(9)),
	)
	f := entitle.Follower{Source: source(t, src), Copy: b.Entitlements}

	got, err := f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(3, got.Applied)
	x.Equal(1, got.Unknown, "a tenant this app does not have")

	as := b.as(ctx)
	x.NoError(b.inTx(as, func(ctx context.Context, drv dialect.Driver) error {
		r := pd.Entitlements(drv)

		has, err := entitle.Has(ctx, r, "apptest.export")
		x.NoError(err)
		x.True(has)

		has, err = entitle.Has(ctx, r, "apptest.reports")
		x.NoError(err)
		x.False(has, "a key nobody granted")

		limit, err := entitle.Limit(ctx, r, "apptest.robots")
		x.NoError(err)
		x.Equal(entitle.Limited(2), limit)

		seats, err := entitle.Get(ctx, r, "apptest.seats")
		x.NoError(err)
		x.Equal(entitle.Unlimited, seats.Value)
		x.Equal("seats", string(seats.Grant.Grant))

		all, err := entitle.All(ctx, r)
		x.NoError(err)
		x.Len(all, 3)

		return nil
	}))

	// A limit outside a transaction is a lock let go at once.
	_, err = entitle.Limit(as, pd.Entitlements(b.Drv), "apptest.robots")
	x.ErrorIs(err, entitle.ErrNotInTx)

	// Each write is on the trail, by the deployment and under the tenant, and
	// a check of the rows finds every row accounted for.
	rows := b.copied(t, ctx, b.Tenant)
	for _, v := range rows {
		lines := b.trailOf(t, ctx, v.Id[:])
		x.Len(lines, 1)
		x.Equal(entitle.Applied, lines[0].Action)
		x.Equal(b.Tenant.Uuid(), lines[0].TenantId)
		x.Equal(pdid.Nil.Uuid(), lines[0].ActorId, "the deployment's work, and nobody's")
	}
	checked, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(checked.Ok(), "%+v", checked)

	// A newer version is written over the row, and an older one is not.
	src.hold(grantOf("robots", b.Tenant, "apptest.robots", 3, entitle.Limited(5)))
	_, err = f.Reconcile(ctx)
	x.NoError(err)
	src.hold(grantOf("robots", b.Tenant, "apptest.robots", 2, entitle.Unlimited))
	_, err = f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(int64(5), b.copied(t, ctx, b.Tenant)["robots"].Amount)
	x.Equal(uint64(3), b.copied(t, ctx, b.Tenant)["robots"].GrantVersion)
	x.Equal(rows["robots"].Id, b.copied(t, ctx, b.Tenant)["robots"].Id)

	// Ended, the grant is in force no longer; listed no more once ended, the
	// row goes, and the trail says it went.
	src.hold(grantOf("export", b.Tenant, "apptest.export", 2, entitle.On, time.Now().Add(-time.Hour), time.Now().Add(-time.Second)))
	_, err = f.Reconcile(ctx)
	x.NoError(err)
	has, err := entitle.Has(as, pd.Entitlements(b.Drv), "apptest.export")
	x.NoError(err)
	x.False(has)

	src.forget("export")
	got, err = f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(1, got.Forgotten)
	x.NotContains(b.copied(t, ctx, b.Tenant), "export")
	lines := b.trailOf(t, ctx, rows["export"].Id[:])
	x.Equal(entitle.Forgot, lines[len(lines)-1].Action)

	checked, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(checked.Ok(), "%+v", checked)

	// And one the source lists no more that has not ended is kept until it
	// does: the copy takes nothing away on a guess.
	src.forget("seats")
	got, err = f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(1, got.Kept)
	x.Contains(b.copied(t, ctx, b.Tenant), "seats")
}

// TestTwoWritesCannotBothPassALimitOfOne: each of two writes checks the limit
// in its own transaction, counts, and adds a robot; the second waits on the
// first's lock -- PostgreSQL's row lock, SQLite's one writer -- and then counts
// what the first wrote, and is refused.
func TestTwoWritesCannotBothPassALimitOfOne(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	x.NoError(b.Entitlements.In(ctx, b.Tenant, func(ctx context.Context, s entitle.Store) error {
		_, err := s.Put(ctx, entitle.Row{
			Tenant: b.Tenant, Key: "apptest.robots", Value: entitle.Limited(1),
			Until: time.Now().Add(time.Hour), Grant: []byte("one"), Version: 1,
		})
		return err
	}))

	refused := errors.New("the plan holds no more robots")
	add := func(ctx context.Context, alias string, checked chan<- struct{}, proceed <-chan struct{}) error {
		return b.inTx(b.as(ctx), func(ctx context.Context, drv dialect.Driver) error {
			limit, err := entitle.Limit(ctx, pd.Entitlements(drv), "apptest.robots")
			if err != nil {
				return err
			}
			if checked != nil {
				checked <- struct{}{}
			}
			if proceed != nil {
				<-proceed
			}

			// Counted after the lock, so that a write that won the race is
			// counted.
			n, err := ent.NewClient(ent.Driver(drv)).Robot.Query().
				Where(robot.TenantId(b.Tenant.Uuid()), robot.DateErasedIsNil()).
				Count(ctx)
			if err != nil {
				return err
			}
			if !limit.Allows(int64(n) + 1) {
				return refused
			}

			next, err := enttx.Rebind(b.Ungated, drv)
			if err != nil {
				return err
			}
			_, err = next.Robot().Add(ctx, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  alias,
			}.Build())

			return err
		})
	}

	checked, proceed := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() { first <- add(ctx, "first", checked, proceed) }()
	<-checked

	// The second begins while the first holds its check, and waits for it.
	second := make(chan error, 1)
	go func() { second <- add(ctx, "second", nil, nil) }()
	select {
	case err := <-second:
		x.Failf("the second write did not wait for the first", "it answered %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(proceed)
	x.NoError(<-first)
	x.ErrorIs(<-second, refused)

	n, err := b.Ent.Robot.Query().Where(robot.TenantId(b.Tenant.Uuid())).Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "two writes passed a limit of one")
}

// TestAReadThatDoesNotLockLetsBothThrough is why [entitle.Limit] locks: the
// same two writes deciding by `entitle.Get`, which does not, each read that
// there is room and each take it. PostgreSQL's, since SQLite begins a write
// one at a time whatever is read in it.
func TestAReadThatDoesNotLockLetsBothThrough(t *testing.T) {
	if os.Getenv(pdtest.Postgres) == "" {
		t.Skipf("%s is not set; SQLite writes one transaction at a time, so nothing races there", pdtest.Postgres)
	}

	x := require.New(t)
	b, ctx := build(t)

	x.NoError(b.Entitlements.In(ctx, b.Tenant, func(ctx context.Context, s entitle.Store) error {
		_, err := s.Put(ctx, entitle.Row{
			Tenant: b.Tenant, Key: "apptest.robots", Value: entitle.Limited(1),
			Until: time.Now().Add(time.Hour), Grant: []byte("one"), Version: 1,
		})
		return err
	}))

	var both sync.WaitGroup
	both.Add(2)
	add := func(alias string) error {
		return b.inTx(b.as(ctx), func(ctx context.Context, drv dialect.Driver) error {
			got, err := entitle.Get(ctx, pd.Entitlements(drv), "apptest.robots")
			if err != nil {
				return err
			}
			n, err := ent.NewClient(ent.Driver(drv)).Robot.Query().Where(robot.TenantId(b.Tenant.Uuid())).Count(ctx)
			if err != nil {
				return err
			}

			// Both have read before either writes.
			both.Done()
			both.Wait()

			if !got.Value.Allows(int64(n) + 1) {
				return errors.New("refused")
			}
			next, err := enttx.Rebind(b.Ungated, drv)
			if err != nil {
				return err
			}
			_, err = next.Robot().Add(ctx, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  alias,
			}.Build())

			return err
		})
	}

	errs := make(chan error, 2)
	go func() { errs <- add("first") }()
	go func() { errs <- add("second") }()
	x.NoError(<-errs)
	x.NoError(<-errs)

	n, err := b.Ent.Robot.Query().Where(robot.TenantId(b.Tenant.Uuid())).Count(ctx)
	x.NoError(err)
	x.Equal(2, n)
}

// TestTheCopyGoesWithItsTenant: its rows hold the tenant by an edge, so the
// database keeps none past the tenant, and a grant the source still says of a
// tenant that is gone is applied to nothing.
func TestTheCopyGoesWithItsTenant(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "leaving"}.Build())
	x.NoError(err)
	leaving := must(pdid.From(v.GetId()))

	src := newGrants()
	src.hold(grantOf("theirs", leaving, "apptest.robots", 1, entitle.Limited(3)))
	f := entitle.Follower{Source: source(t, src), Copy: b.Entitlements}
	_, err = f.Reconcile(ctx)
	x.NoError(err)
	x.Len(b.copied(t, ctx, leaving), 1)

	// The tenant cannot go while its copy is there...
	x.Error(b.Ent.Tenant.DeleteOneId(leaving.Uuid()).Exec(ctx))

	// ...and goes with it, as an offboarding takes a tenant's rows.
	_, err = b.Ent.Entitlement.Delete().Where(entitlement.TenantId(leaving.Uuid())).Exec(ctx)
	x.NoError(err)
	x.NoError(b.Ent.Tenant.DeleteOneId(leaving.Uuid()).Exec(ctx))

	// The source still says it, and nothing comes back.
	got, err := f.Reconcile(ctx)
	x.NoError(err)
	x.Equal(1, got.Unknown)
	x.Empty(b.copied(t, ctx, leaving))
}

// TestTheServerFollowsTheSourceItIsNamed: `grants.addr` is a follower in the
// spin list, on the copy the server reads, which applies what the source's
// stream says as it says it.
func TestTheServerFollowsTheSourceItIsNamed(t *testing.T) {
	x := require.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	src := newGrants()
	g := grpc.NewServer()
	pdpb.RegisterGrantServiceServer(g, src)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go g.Serve(l)
	t.Cleanup(g.Stop)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:     dbOf(t),
		Watch:  config.WatchConfig{Broker: config.BrokerMemory},
		Grants: cmd.GrantsConfig{Addr: l.Addr().String()},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(ctx, s))
	x.NotNil(s.Follower)
	x.True(slices.ContainsFunc(s.Spin, func(v any) bool { return v == any(s.Follower) }), "the follower is not in the spin list")

	v, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)
	tenant := must(pdid.From(v.GetId()))

	done := make(chan error, 1)
	go func() { done <- s.Follower.Spin(ctx) }()

	b := &built{Server: s, Tenant: tenant}
	x.Eventually(func() bool {
		src.mu.Lock()
		defer src.mu.Unlock()
		return len(src.streams) > 0
	}, 5*time.Second, 10*time.Millisecond)
	src.say(grantOf("export", tenant, "apptest.export", 1, entitle.On))
	x.Eventually(func() bool { return len(b.copied(t, ctx, tenant)) == 1 }, 5*time.Second, 10*time.Millisecond)

	cancel()
	x.NoError(<-done)
}
