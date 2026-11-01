package cmd_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/notify"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	entnotificationreceiptrecord "github.com/lesomnus/payday/internal/apptest/internal/ent/notificationreceiptrecord"
	entnotificationrecord "github.com/lesomnus/payday/internal/apptest/internal/ent/notificationrecord"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// The account namespace (payday#195): notifications no tenant holds, which an
// account reads as itself through NotificationAccountService -- the one an
// invitation is addressed to before it is anybody's member among them.

// accountNotificationPolicy is the account namespace's own: an account reads
// the topic of its own identifier and the notice every account reads, and the
// deployment's code publishes.
type accountNotificationPolicy struct{}

func (accountNotificationPolicy) Publisher(_ context.Context, tenant pdid.Id, _ notify.Topic) (string, error) {
	if !tenant.IsZero() {
		return "", status.Error(codes.PermissionDenied, "the account namespace is no tenant's")
	}
	return "deployment", nil
}

func (accountNotificationPolicy) Reader(ctx context.Context, _ pdid.Id) (pdid.Id, error) {
	a, ok := frame.AccountOf(ctx)
	if !ok {
		return pdid.Nil, status.Error(codes.FailedPrecondition, "an account reads these as itself")
	}
	return a, nil
}

func (accountNotificationPolicy) Read(_ context.Context, _, reader pdid.Id, topic notify.Topic) error {
	if topic.Name == "everyone" || (!topic.Resource.IsZero() && topic.Resource == reader) {
		return nil
	}
	return status.Error(codes.PermissionDenied, "not this account's")
}

type accountNotifications struct {
	*accounts

	server  *pd.AccountNotificationServer
	tenants *pd.NotificationServer
	client  app.NotificationAccountServiceClient
	tenant  app.NotificationServiceClient

	// The deployment's own code, which publishes into the account namespace.
	deployment context.Context
}

func withAccountNotifications(t *testing.T) (*accountNotifications, context.Context) {
	t.Helper()
	x := require.New(t)
	w, ctx := withAccounts(t)

	s, err := pd.AccountNotifications(w.Walled, w.Drv, w.Watch, accountNotificationPolicy{}, notify.Options{CursorKey: bytes.Repeat([]byte{29}, 32)})
	x.NoError(err)
	tenants := notificationServer(t, w.built)

	g := w.grpc(t)
	app.RegisterNotificationAccountServiceServer(g, s)
	app.RegisterNotificationServiceServer(g, tenants)
	conn := pdtest.Serve(t, g)

	return &accountNotifications{
		accounts: w, server: s, tenants: tenants,
		client: app.NewNotificationAccountServiceClient(conn), tenant: app.NewNotificationServiceClient(conn),
		deployment: frame.Into(ctx, frame.New(pdid.Nil, pdid.Nil, frame.Whole())),
	}, ctx
}

func (n *accountNotifications) publish(t *testing.T, s *pd.AccountNotificationServer, topic *app.NotificationTopic, key string) *app.Notification {
	t.Helper()

	payload, err := anypb.New(app.JobDone_builder{Outcome: "invited"}.Build())
	require.NoError(t, err)
	v, err := s.Add(n.deployment, app.NotificationAddRequest_builder{Topic: topic, Kind: "invitation.received", Payload: payload, IdempotencyKey: key}.Build())
	require.NoError(t, err)

	return v
}

func ofAccount(a pdid.Id) *app.NotificationTopic {
	return app.NotificationTopic_builder{ResourceId: a.Bytes()}.Build()
}

var everyone = app.NotificationTopic_builder{Name: z.Ptr("everyone")}.Build()

func TestNotifyAnAccountReadsItsOwnAsItself(t *testing.T) {
	x := require.New(t)
	n, ctx := withAccountNotifications(t)

	invited := n.publish(t, n.server, ofAccount(n.none), "invited")
	notice := n.publish(t, n.server, everyone, "notice")
	x.Empty(invited.GetTenantId())

	// Stored in the namespace no tenant holds, which is a Nil tenant here.
	row := n.Ent.NotificationRecord.Query().Where(entnotificationrecord.IdEQ(mustId(x, invited.GetId()).Uuid())).OnlyX(t.Context())
	x.Equal(pdid.Nil.Uuid(), row.TenantId)

	// The account in no tenant reads what is addressed to it, as itself.
	none := n.as(ctx, n.none)
	topics := []*app.NotificationTopic{ofAccount(n.none), everyone}
	page, err := n.client.Read(none, app.NotificationReadRequest_builder{Topics: topics}.Build())
	x.NoError(err)
	x.Equal(n.none.Bytes(), page.GetReaderId())
	x.Len(page.GetItems(), 2)
	counts, err := n.client.Count(none, app.NotificationCountRequest_builder{Topics: topics, Filter: app.NotificationReadFilter_builder{UnreadOnly: true}.Build()}.Build())
	x.NoError(err)
	x.EqualValues(2, counts.GetCount())
	got, err := n.client.Get(none, app.NotificationGetRequest_builder{Ref: app.NotificationRef_builder{Id: invited.GetId()}.Build()}.Build())
	x.NoError(err)
	x.True(proto.Equal(invited, got))

	listening, cancel := context.WithTimeout(none, 10*time.Second)
	defer cancel()
	stream, err := n.client.Listen(listening, app.NotificationListenRequest_builder{Topics: topics}.Build())
	x.NoError(err)
	ready, err := stream.Recv()
	x.NoError(err)
	x.Equal(n.none.Bytes(), ready.GetReady().GetReaderId())

	marked, err := n.client.Mark(none, app.NotificationMarkRequest_builder{Refs: []*app.NotificationRef{app.NotificationRef_builder{Id: invited.GetId()}.Build()}}.Build())
	x.NoError(err)
	x.Len(marked.GetItems(), 1)
	receipt := marked.GetItems()[0]
	x.Equal(n.none.Bytes(), receipt.GetReaderId())
	x.Empty(receipt.GetTenantId())
	stored := n.Ent.NotificationReceiptRecord.Query().Where(entnotificationreceiptrecord.IdEQ(mustId(x, receipt.GetId()).Uuid())).OnlyX(t.Context())
	x.Equal(pdid.Nil.Uuid(), stored.TenantId)
	hint, err := stream.Recv()
	x.NoError(err)
	x.True(proto.Equal(ofAccount(n.none), hint.GetInvalidated().GetTopics()[0]), "the account's other sessions learn of its receipt")

	// Another account reads the notice every account reads, and not what is
	// addressed to somebody else; and its read state is its own.
	one := n.as(ctx, n.one)
	_, err = n.client.Read(one, app.NotificationReadRequest_builder{Topics: []*app.NotificationTopic{ofAccount(n.none)}}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
	_, err = n.client.Get(one, app.NotificationGetRequest_builder{Ref: app.NotificationRef_builder{Id: invited.GetId()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
	page, err = n.client.Read(one, app.NotificationReadRequest_builder{Topics: []*app.NotificationTopic{everyone}}.Build())
	x.NoError(err)
	x.Len(page.GetItems(), 1)
	x.True(proto.Equal(notice, page.GetItems()[0].GetNotification()))
	x.False(page.GetItems()[0].HasReceipt())

	// The app's own code tells the account's streams of a change it made.
	x.NoError(n.server.Invalidate(n.deployment, []notify.Topic{notify.Named("everyone")}))
	hint, err = stream.Recv()
	x.NoError(err)
	x.True(proto.Equal(everyone, hint.GetInvalidated().GetTopics()[0]))

	// A publication into the account namespace is on the trail, filed under
	// no tenant: the deployment's own record.
	line := n.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(mustId(x, invited.GetId()).Uuid())).OnlyX(t.Context())
	x.Equal(pdid.Nil.Uuid(), line.TenantId)
}

// TestNotifyTheAccountNamespaceAndATenantsAreApart: an account reads no
// tenant's notifications through its service, no tenant reads the account
// namespace through its own, and a caller that is not an account calling as
// itself reads nothing of it.
func TestNotifyTheAccountNamespaceAndATenantsAreApart(t *testing.T) {
	x := require.New(t)
	n, ctx := withAccountNotifications(t)

	invited := n.publish(t, n.server, ofAccount(n.both), "invited")
	maintenance := app.NotificationTopic_builder{Name: z.Ptr("maintenance")}.Build()
	ofAcme, err := n.tenants.Add(notificationAs(ctx, n.acme, n.bothAcme.Bytes()), app.NotificationAddRequest_builder{
		TenantId: n.acme.Bytes(), Topic: maintenance, Kind: "maintenance.scheduled", IdempotencyKey: "acme",
	}.Build())
	x.NoError(err)

	// The account, calling as itself: a tenant named is no namespace of its
	// service, and a tenant's notification is not found there.
	both := n.as(ctx, n.both)
	_, err = n.client.Read(both, app.NotificationReadRequest_builder{TenantId: n.acme.Bytes(), Topics: []*app.NotificationTopic{maintenance}}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	_, err = n.client.Get(both, app.NotificationGetRequest_builder{Ref: app.NotificationRef_builder{Id: ofAcme.GetId()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
	_, err = n.client.Mark(both, app.NotificationMarkRequest_builder{Refs: []*app.NotificationRef{app.NotificationRef_builder{Id: ofAcme.GetId()}.Build()}}.Build())
	x.Equal(codes.NotFound, status.Code(err))

	// The account as its membership of acme: the tenant's, and not the
	// account namespace's.
	member := n.as(ctx, n.both, "acme")
	page, err := n.tenant.Read(member, app.NotificationReadRequest_builder{TenantId: n.acme.Bytes(), Topics: []*app.NotificationTopic{maintenance}}.Build())
	x.NoError(err)
	x.Len(page.GetItems(), 1)
	x.Equal(n.bothAcme.Bytes(), page.GetReaderId(), "a membership reads as the membership")
	_, err = n.tenant.Get(member, app.NotificationGetRequest_builder{Ref: app.NotificationRef_builder{Id: invited.GetId()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
	_, err = n.tenant.Read(member, app.NotificationReadRequest_builder{Topics: []*app.NotificationTopic{ofAccount(n.both)}}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "the tenant service has no namespace of no tenant")

	// Anybody but an account calling as itself reads nothing of it: a holder's
	// credential, and the deployment's own frame.
	holder := auth.PlainProvider("@acme/admin").Provide(ctx)
	_, err = n.client.Read(holder, app.NotificationReadRequest_builder{Topics: []*app.NotificationTopic{everyone}}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
	_, err = n.server.Read(n.deployment, app.NotificationReadRequest_builder{Topics: []*app.NotificationTopic{everyone}}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))

	// And a tenant's notification server is never the account namespace's,
	// whatever its options say.
	s, err := pd.Notifications(n.Walled, n.Drv, n.Watch, accountNotificationPolicy{}, notify.Options{CursorKey: bytes.Repeat([]byte{29}, 32), Accounts: true})
	x.NoError(err)
	_, err = s.Add(n.deployment, app.NotificationAddRequest_builder{Topic: everyone, Kind: "notice", IdempotencyKey: "forced"}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
}

// TestNotifyTheAccountNamespaceExpiresAsTheRestDoes: its notifications are kept
// as long as the app keeps every other, and go with their receipts.
func TestNotifyTheAccountNamespaceExpiresAsTheRestDoes(t *testing.T) {
	x := require.New(t)
	n, ctx := withAccountNotifications(t)

	old, err := pd.AccountNotifications(n.Walled, n.Drv, n.Watch, accountNotificationPolicy{}, notify.Options{
		CursorKey: bytes.Repeat([]byte{29}, 32),
		Clock:     func() time.Time { return time.Now().Add(-pd.NotificationKeep - time.Hour) },
	})
	x.NoError(err)
	stale := n.publish(t, old, ofAccount(n.none), "stale")
	fresh := n.publish(t, n.server, ofAccount(n.none), "fresh")
	_, err = n.client.Mark(n.as(ctx, n.none), app.NotificationMarkRequest_builder{Refs: []*app.NotificationRef{app.NotificationRef_builder{Id: stale.GetId()}.Build()}}.Build())
	x.NoError(err)

	erased, err := pd.NotificationExpirer(n.Drv, n.Watch, n.Contents).Pass(t.Context())
	x.NoError(err)
	x.Equal(1, erased)
	x.False(n.Ent.NotificationRecord.Query().Where(entnotificationrecord.IdEQ(mustId(x, stale.GetId()).Uuid())).ExistX(t.Context()))
	x.True(n.Ent.NotificationRecord.Query().Where(entnotificationrecord.IdEQ(mustId(x, fresh.GetId()).Uuid())).ExistX(t.Context()))
	x.Zero(n.Ent.NotificationReceiptRecord.Query().CountX(t.Context()))
	line := n.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(mustId(x, stale.GetId()).Uuid()), entaudit.ActionEQ(notify.ExpireName)).OnlyX(t.Context())
	x.Equal(pdid.Nil.Uuid(), line.TenantId)
}
