package pdcli_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

func TestNotifyIsReadBeforeCompilation(t *testing.T) {
	for _, option := range []string{
		`option (payday.app) = {notify: {notification: 40, receipt: 41}};`,
		`option (payday.app).notify = {notification: 40, receipt: 41};`,
		"option (payday.app).notify.notification = 40;\noption (payday.app).notify.receipt = 41;",
		`option (payday.app) = {past: {event: 17, fact: 16}, notify: {notification: 40, receipt: 41}, domains: {archived: 42}};`,
	} {
		root := app(t, "github.com/acme/thing")
		past(t, root, "widget.proto", option)
		l, err := pdcli.Discover(root)
		require.NoError(t, err)
		require.Equal(t, &pdcli.Notify{Notification: 40, Receipt: 41, At: "proto/app/widget.proto"}, l.Notify)
	}
	root := app(t, "github.com/acme/thing")
	past(t, root, "widget.proto", `option (payday.app) = {past: {event: 17, fact: 16}};`)
	l, err := pdcli.Discover(root)
	require.NoError(t, err)
	require.Nil(t, l.Notify)
}

func TestGlobalNotifyGeneratesAndRunsWithoutTenantRecords(t *testing.T) {
	l := genApp(t)
	at := filepath.Join(l.Root, "proto", "app", "robot.proto")
	b, err := os.ReadFile(at)
	require.NoError(t, err)
	source := strings.Replace(string(b), "notify: {notification: 40, receipt: 41}", "notify: {notification: 40, receipt: 41, global: true}", 1)
	require.NotEqual(t, string(b), source)
	require.NoError(t, os.WriteFile(at, []byte(source), 0o644))
	l, err = pdcli.Discover(l.Root)
	require.NoError(t, err)
	require.True(t, l.Notify.Global)
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	require.NoError(t, (pdcli.Gen{Layout: l, Log: os.Stderr}).Run(ctx))
	require.NoError(t, os.WriteFile(filepath.Join(l.Root, "server", "pd", "notify_global_test.go"), []byte(globalNotificationRuntimeTest), 0o644))
	command := exec.CommandContext(ctx, "go", "test", "./server/pd", "-run", "^TestGeneratedGlobalNotification$", "-count=1")
	command.Dir = l.Root
	command.WaitDelay = 2 * time.Second
	out, err := command.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

const globalNotificationRuntimeTest = `package pd_test

import (
    "context"
    "testing"
    "bytes"
    "sync"
    "time"
    "sync/atomic"
    "github.com/lesomnus/payday/internal/apptest/cli"
    "github.com/lesomnus/payday/config"
    "github.com/lesomnus/payday/config/brokerpg"
    "github.com/lesomnus/payday/frame"
    "github.com/lesomnus/payday/notify"
    "github.com/lesomnus/payday/pdid"
    "github.com/lesomnus/payday/pdtest"
    app "github.com/lesomnus/payday/internal/apptest"
    "github.com/lesomnus/payday/internal/apptest/cmd"
    "github.com/lesomnus/payday/internal/apptest/server/pd"
    "github.com/stretchr/testify/require"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc"
    "google.golang.org/grpc/status"
    "google.golang.org/protobuf/proto"
)

type globalNotifyPolicy struct{revoked *atomic.Bool}
func(globalNotifyPolicy) Publisher(context.Context,pdid.Id,notify.Topic)(string,error){return "local-worker",nil}
func(globalNotifyPolicy) Reader(ctx context.Context,_ pdid.Id)(pdid.Id,error){return frame.Must(ctx).Actor,nil}
func(p globalNotifyPolicy) Read(_ context.Context,scope,reader pdid.Id,topic notify.Topic)error{
    if p.revoked.Load() || !scope.IsZero() || topic.Name!="work" {return status.Error(codes.PermissionDenied,"not allowed")};return nil
}

type globalNotifyStream struct{
    grpc.ServerStream
    ctx context.Context
    events chan *app.NotificationListenResponse
}
func(s *globalNotifyStream)Context()context.Context{return s.ctx}
func(s *globalNotifyStream)Send(v *app.NotificationListenResponse)error{
    select{case s.events<-v:return nil;case <-s.ctx.Done():return s.ctx.Err()}
}

func TestGeneratedGlobalNotification(t *testing.T){
    driver,dsn:=pdtest.DB(t)
    broker:=config.BrokerMemory
    if driver=="postgres"{broker=brokerpg.Name}
    server,err:=cmd.Build(t.Context(),cmd.Config{Db:config.DbConfig{Driver:driver,Dsn:dsn},Watch:config.WatchConfig{Broker:broker}})
    require.NoError(t,err);defer server.Close()
    require.NoError(t,cli.Migrate(t.Context(),server))
    require.Zero(t,server.Ent.Tenant.Query().CountX(t.Context()))
    pdid.Register("global-test.LocalOwner",247,"global-test-owner")
    owner:=pdid.New(247)
    ctx:=frame.Into(t.Context(),frame.New(owner,pdid.Nil,frame.Whole()))
    var revoked atomic.Bool
    service,err:=pd.Notifications(server.Walled,server.Drv,server.Watch,globalNotifyPolicy{&revoked},notify.Options{CursorKey:bytes.Repeat([]byte{3},32)})
    require.NoError(t,err)
    name:="work";topic:=app.NotificationTopic_builder{Name:&name}.Build()
    add:=app.NotificationAddRequest_builder{Topic:topic,Kind:"done",IdempotencyKey:"one"}.Build()
    v,err:=service.Add(ctx,add);require.NoError(t,err);require.Empty(t,v.GetTenantId())
    retry,err:=service.Add(ctx,add);require.NoError(t,err);require.True(t,proto.Equal(v,retry))
    var wg sync.WaitGroup
    failures:=make(chan error,6)
    for range 6{wg.Go(func(){got,err:=service.Add(ctx,add);if err==nil && !proto.Equal(v,got){err=status.Error(codes.Internal,"different publication")};failures<-err})};wg.Wait()
    for range 6{require.NoError(t,<-failures)}
    require.Nil(t,(&app.NotificationRecord{}).ProtoReflect().Descriptor().Fields().ByNumber(2))
    require.Nil(t,(&app.NotificationReceiptRecord{}).ProtoReflect().Descriptor().Fields().ByNumber(2))
    query:=app.NotificationCountRequest_builder{Topics:[]*app.NotificationTopic{topic},Filter:app.NotificationReadFilter_builder{UnreadOnly:true}.Build()}.Build()
    counts,err:=service.Count(ctx,query);require.NoError(t,err);require.EqualValues(t,1,counts.GetCount())
    mark:=app.NotificationMarkRequest_builder{Refs:[]*app.NotificationRef{app.NotificationRef_builder{Id:v.GetId()}.Build()}}.Build()
    result,err:=service.Mark(ctx,mark);require.NoError(t,err);require.Len(t,result.GetItems(),1);require.Empty(t,result.GetItems()[0].GetTenantId())
    for range 6{wg.Go(func(){got,err:=service.Mark(ctx,mark);if err==nil && !proto.Equal(result,got){err=status.Error(codes.Internal,"different receipt")};failures<-err})};wg.Wait()
    for range 6{require.NoError(t,<-failures)}
    counts,err=service.Count(ctx,query);require.NoError(t,err);require.Zero(t,counts.GetCount())
    require.Zero(t,server.Ent.Tenant.Query().CountX(t.Context()))
    add.SetTenantId(pdid.New(pd.TenantDomain).Bytes())
    _,err=service.Add(ctx,add);require.Equal(t,codes.InvalidArgument,status.Code(err))
    _,err=service.Read(t.Context(),app.NotificationReadRequest_builder{Topics:[]*app.NotificationTopic{topic}}.Build());require.Equal(t,codes.Unauthenticated,status.Code(err))

    listenCtx,cancel:=context.WithTimeout(ctx,10*time.Second);defer cancel()
    stream:=&globalNotifyStream{ctx:listenCtx,events:make(chan *app.NotificationListenResponse,4)}
    done:=make(chan error,1)
    go func(){done<-service.Listen(app.NotificationListenRequest_builder{Topics:[]*app.NotificationTopic{topic}}.Build(),stream)}()
    next:=func()*app.NotificationListenResponse{select{case v:=<-stream.events:return v;case <-listenCtx.Done():t.Fatal("global listener stalled");return nil}}
    require.True(t,next().HasReady())
    require.NoError(t,service.Invalidate(ctx,pdid.Nil,[]notify.Topic{notify.Named("work")}))
    require.Len(t,next().GetInvalidated().GetTopics(),1)
    revoked.Store(true)
    require.NoError(t,service.Invalidate(ctx,pdid.Nil,[]notify.Topic{notify.Named("work")}))
    select{case err:=<-done:require.Equal(t,codes.PermissionDenied,status.Code(err));case <-listenCtx.Done():t.Fatal("revocation stalled")}
}
`

func TestNotifyDeclarationsAreRefusedBeforeCompilation(t *testing.T) {
	for _, option := range []string{
		`option (payday.app) = {notify: {notification: 40}};`,
		`option (payday.app) = {notify: {notification: 40, receipt: 40}};`,
		`option (payday.app) = {notify: {notification: 2, receipt: 41}};`,
		`option (payday.app) = {notify: {notification: 40, receipt: 256}};`,
		`option (payday.app) = {past: {event: 40, fact: 16}, notify: {notification: 40, receipt: 41}};`,
		`option (payday.app) = {notify: {notification: 40, receipt: 41}, domains: {access: 41}};`,
		`option (payday.app) = {notify: {notificaton: 40, receipt: 41}};`,
	} {
		root := app(t, "github.com/acme/thing")
		past(t, root, "widget.proto", option)
		_, err := pdcli.Discover(root)
		require.Error(t, err, option)
		require.Contains(t, err.Error(), "proto/app/widget.proto")
	}
}
