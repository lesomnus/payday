package pdgen

import (
	"bytes"
	"text/template"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
)

const pkgNotify = protogen.GoImportPath("github.com/lesomnus/payday/notify")

// EmitNotify connects the app's public notification contract to its private,
// indexed records. The runtime owns the rules; this bridge only converts values
// and writes through the same generated servers as the rest of the app.
func EmitNotify(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	n, r := s.Own(pdpb.Own_OWN_NOTIFICATION), s.Own(pdpb.Own_OWN_NOTIFICATION_RECEIPT)
	if n == nil || r == nil {
		return
	}
	q := func(pkg protogen.GoImportPath) func(string) string {
		return func(name string) string { return g.QualifiedGoIdent(pkg.Ident(name)) }
	}
	functions := template.FuncMap{
		"a": q(root), "n": q(pkgNotify), "e": q(p.Ent),
		"rp": q(p.EntPkg(n)), "cp": q(p.EntPkg(r)),
		"ctx": q(pkgCtx), "drv": q(pkgDialect), "tx": q(pkgEnttx),
		"w": q(pkgWatch), "sql": q(pkgEntsql), "status": q(pkgStatus),
		"codes": q(pkgCodes), "id": q(pkgPdid), "pb": q(pkgAnypb),
		"timepb": q(protogen.GoImportPath("google.golang.org/protobuf/types/known/timestamppb")),
		"bytes":  q(protogen.GoImportPath("bytes")), "uuid": q(protogen.GoImportPath("uuid")),
		"time": q(pkgTime), "frame": q(pkgFrame), "audit": q(pkgAudit),
		// A backtick, which the template is a raw string of and so cannot hold.
		"bt":      func() string { return "`" },
		"record":  func(suffix string) string { return g.QualifiedGoIdent(root.Ident(n.GoName() + suffix)) },
		"receipt": func(suffix string) string { return g.QualifiedGoIdent(root.Ident(r.GoName() + suffix)) },
	}
	t := template.Must(template.New("notify").Funcs(functions).Parse(notifyGlue))
	var code bytes.Buffer
	stream := g.QualifiedGoIdent(pkgGrpc.Ident("ServerStreamingServer")) + "[" + g.QualifiedGoIdent(root.Ident("NotificationListenResponse")) + "]"
	if err := t.Execute(&code, map[string]any{"Record": n.GoName(), "Receipt": r.GoName(), "Stream": stream, "Global": s.Notify.Global, "Namespace": string(root),
		"Keep": s.Notify.KeepDays, "Forever": s.Notify.Forever, "At": s.Notify.At, "Payloads": payloadNames(s), "Accounts": s.Notify.Accounts}); err != nil {
		panic(err)
	}
	g.P(code.String())
}

// payloadNames are the full names of the app's notification payloads, which
// are the messages Add takes a payload of (payday#193).
func payloadNames(s *Schema) []string {
	vs := make([]string, len(s.Payloads))
	for i, v := range s.Payloads {
		vs[i] = string(v.FullName())
	}

	return vs
}

// emitAuditNotify closes the internal records even when the public service was
// not installed. A missing policy must not turn a generated endpoint into a
// second, unchecked publisher or a way to enumerate other readers' receipts.
func emitAuditNotify(g *protogen.GeneratedFile, s *Schema, root protogen.GoImportPath) {
	if s.Own(pdpb.Own_OWN_NOTIFICATION) == nil {
		return
	}
	g.P("type notifyWrite struct{}")
	g.P("func withNotifyWrite(ctx ", pkgCtx.Ident("Context"), ") ", pkgCtx.Ident("Context"), " { return ", pkgCtx.Ident("WithValue"), "(ctx, notifyWrite{}, notifyWrite{}) }")
	g.P("func isNotifyWrite(ctx ", pkgCtx.Ident("Context"), ") bool { _, ok := ctx.Value(notifyWrite{}).(notifyWrite); return ok }")
	for _, own := range []pdpb.Own{pdpb.Own_OWN_NOTIFICATION, pdpb.Own_OWN_NOTIFICATION_RECEIPT} {
		e := s.Own(own)
		if e == nil {
			continue
		}
		r := e.Rpcs()
		if !r.HasAdd() && !r.HasGet() {
			// Nothing to seal. A receipt has no RPC at all: notify writes it
			// past the servers, and reads it the same way (payday#197).
			continue
		}
		name := e.GoName()
		g.P("// The base Sink seals these paths too, so forgetting an outer layer")
		g.P("// cannot expose notification contents or allow forged receipt owners.")
		g.P("func (s Sink) ", name, "() ", root.Ident(name+"ServiceServer"), " { return notify", name, "Guard{s.Server.", name, "()} }")
		g.P("func (s Audit) ", name, "() ", root.Ident(name+"ServiceServer"), " { return notify", name, "Guard{s.Next().", name, "()} }")
		g.P("type notify", name, "Guard struct { ", root.Ident(name+"ServiceServer"), " }")
		if r.HasAdd() {
			g.P("func (s notify", name, "Guard) Add(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(name+"AddRequest"), ") (*", root.Ident(name), ", error) {")
			g.P("if !isNotifyWrite(ctx) { return nil,", pkgStatus.Ident("Error"), "(", pkgCodes.Ident("PermissionDenied"), ",\"notification records are written through NotificationService\") }; return s.", name, "ServiceServer.Add(ctx,req) }")
		}
		if r.HasGet() {
			g.P("func (s notify", name, "Guard) Get(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(name+"GetRequest"), ") (*", root.Ident(name), ", error) { return nil,", pkgStatus.Ident("Error"), "(", pkgCodes.Ident("PermissionDenied"), ",\"notification records are read through NotificationService\") }")
		}
	}
}

const notifyGlue = `
// NotificationServer serves the app's notification RPCs. Register it explicitly
// with RegisterNotificationServiceServer; private records stay in the ordinary
// generated stack and are sealed by the base Sink and Audit layers.
type NotificationServer struct {
    {{a "UnimplementedNotificationServiceServer"}}
    service *{{n "Service"}}
    stack {{a "Server"}}
    watch *{{w "Watch"}}
    policy {{n "Policy"}}
    options {{n "Options"}}
}

// Notifications binds this app's records and public wire messages to notify.
// Reusing the app driver makes a domain operation and its publication one
// transaction. Every layer of stack must support rebinding.
func Notifications(stack {{a "Server"}}, driver {{drv "Driver"}}, changes *{{w "Watch"}}, policy {{n "Policy"}}, options {{n "Options"}}) (*NotificationServer, error) {
    // A tenant's namespace, or the global one: never the account namespace,
    // which is NotificationAccountService's alone (payday#195).
    options.Accounts=false
    return notifications(stack,driver,changes,policy,options)
}

func notifications(stack {{a "Server"}}, driver {{drv "Driver"}}, changes *{{w "Watch"}}, policy {{n "Policy"}}, options {{n "Options"}}) (*NotificationServer, error) {
    options.Global={{.Global}}
    options.Namespace={{printf "%q" .Namespace}}
    // The messages a notification may carry, which the schema declares
    // (payday#193): what Add takes a payload of, and nothing else.
    options.Payloads=[]string{ {{range .Payloads}}{{printf "%q" .}},{{end}} }
    rebound, err := {{tx "Rebind"}}(stack, driver)
    if err != nil { return nil, err }
    store := notificationStore{stack:rebound, driver:driver, db:{{e "NewClient"}}({{e "Driver"}}(driver))}
    service, err := {{n "New"}}(store, changes, policy, options)
    if err != nil { return nil, err }
    return &NotificationServer{service:service, stack:rebound, watch:changes, policy:policy, options:options},nil
}

// WithDriver joins a business transaction without dropping a layer's rules.
func (s *NotificationServer) WithDriver(driver {{drv "Driver"}}) (*NotificationServer,error) {
    return notifications(s.stack,driver,s.watch,s.policy,s.options)
}
{{if .Accounts}}
// AccountNotificationServer serves NotificationAccountService: the account
// namespace, the one no tenant holds, to an account calling as itself
// (payday#195). Register it with RegisterNotificationAccountServiceServer
// beside the tenant one. Publishing into it is the app's own code, through
// [AccountNotificationServer.Add], and no RPC.
type AccountNotificationServer struct {
    {{a "UnimplementedNotificationAccountServiceServer"}}
    server *NotificationServer
}

// AccountNotifications binds the account namespace to the app's records and
// to a policy of its own, asked with pdid.Nil for the tenant: a policy written
// for tenants that ignores its tenant argument would answer for accounts too.
// The stack, driver and Watch are the ones the tenant service is given.
func AccountNotifications(stack {{a "Server"}}, driver {{drv "Driver"}}, changes *{{w "Watch"}}, policy {{n "Policy"}}, options {{n "Options"}}) (*AccountNotificationServer, error) {
    options.Accounts=true
    s,err:=notifications(stack,driver,changes,policy,options);if err!=nil{return nil,err}
    return &AccountNotificationServer{server:s},nil
}

// WithDriver joins a business transaction, as the tenant server's does.
func (s *AccountNotificationServer) WithDriver(driver {{drv "Driver"}}) (*AccountNotificationServer,error) {
    v,err:=s.server.WithDriver(driver);if err!=nil{return nil,err}
    return &AccountNotificationServer{server:v},nil
}

// Add publishes into the account namespace: the app's own code, under any
// frame its publisher policy allows. The request names no tenant.
func (s *AccountNotificationServer) Add(ctx {{ctx "Context"}}, req *{{a "NotificationAddRequest"}}) (*{{a "Notification"}},error) {
    return s.server.Add(ctx,req)
}

// Invalidate is [NotificationServer.Invalidate] of the account namespace.
func (s *AccountNotificationServer) Invalidate(ctx {{ctx "Context"}}, topics []{{n "Topic"}}) error {
    return s.server.Invalidate(ctx,{{id "Nil"}},topics)
}

func (s *AccountNotificationServer) Get(ctx {{ctx "Context"}}, req *{{a "NotificationGetRequest"}}) (*{{a "Notification"}},error) { return s.server.Get(ctx,req) }
func (s *AccountNotificationServer) List(ctx {{ctx "Context"}}, req *{{a "NotificationListRequest"}}) (*{{a "NotificationListResponse"}},error) { return s.server.List(ctx,req) }
func (s *AccountNotificationServer) Read(ctx {{ctx "Context"}}, req *{{a "NotificationReadRequest"}}) (*{{a "NotificationReadResponse"}},error) { return s.server.Read(ctx,req) }
func (s *AccountNotificationServer) Count(ctx {{ctx "Context"}}, req *{{a "NotificationCountRequest"}}) (*{{a "NotificationCountResponse"}},error) { return s.server.Count(ctx,req) }
func (s *AccountNotificationServer) Mark(ctx {{ctx "Context"}}, req *{{a "NotificationMarkRequest"}}) (*{{a "NotificationMarkResponse"}},error) { return s.server.Mark(ctx,req) }
func (s *AccountNotificationServer) Listen(req *{{a "NotificationListenRequest"}}, stream {{.Stream}}) error { return s.server.Listen(req,stream) }
{{end}}

// Invalidate is an app operation after a committed policy dependency changed.
// It is deliberately absent from the public gRPC service.
func (s *NotificationServer) Invalidate(ctx {{ctx "Context"}}, tenant {{id "Id"}}, topics []{{n "Topic"}}) error {
    return s.service.Invalidate(ctx,tenant,topics)
}

func (s *NotificationServer) Add(ctx {{ctx "Context"}}, req *{{a "NotificationAddRequest"}}) (*{{a "Notification"}},error) {
    tenant,err:=notificationID(req.GetTenantId());if err!=nil{return nil,notifyArgument(err)}
    topic,err:=notificationTopic(req.GetTopic());if err!=nil{return nil,err}
    source,err:=notificationID(req.GetSourceId());if err!=nil{return nil,notifyArgument(err)}
    v,err:=s.service.Add(ctx,{{n "AddRequest"}}{Tenant:tenant,Topic:topic,Kind:req.GetKind(),Source:source,Payload:req.GetPayload(),IdempotencyKey:req.GetIdempotencyKey()})
    if err!=nil{return nil,err};return notificationValue(v),nil
}
func (s *NotificationServer) Get(ctx {{ctx "Context"}}, req *{{a "NotificationGetRequest"}}) (*{{a "Notification"}},error) {
    key,err:={{id "From"}}(req.GetRef().GetId());if err!=nil{return nil,notifyArgument(err)}
    v,err:=s.service.Get(ctx,key);if err!=nil{return nil,err};return notificationValue(v),nil
}
func (s *NotificationServer) List(ctx {{ctx "Context"}}, req *{{a "NotificationListRequest"}}) (*{{a "NotificationListResponse"}},error) {
    q,err:=notificationQuery(req.GetTenantId(),req.GetTopics(),req.GetSize(),req.GetCursor());if err!=nil{return nil,err}
    page,err:=s.service.List(ctx,q);if err!=nil{return nil,err}
    vs:=make([]*{{a "Notification"}},len(page.Items));for i,v:=range page.Items{vs[i]=notificationValue(v)}
    return {{a "NotificationListResponse_builder"}}{Items:vs,Next:page.Next}.Build(),nil
}
func (s *NotificationServer) Read(ctx {{ctx "Context"}}, req *{{a "NotificationReadRequest"}}) (*{{a "NotificationReadResponse"}},error) {
    q,err:=notificationQuery(req.GetTenantId(),req.GetTopics(),req.GetSize(),req.GetCursor());if err!=nil{return nil,err}
    q.Filter,err=notificationFilter(req.GetFilter());if err!=nil{return nil,err}
    page,err:=s.service.Read(ctx,q);if err!=nil{return nil,err}
    vs:=make([]*{{a "NotificationReadItem"}},len(page.Items));for i,v:=range page.Items{
        b:={{a "NotificationReadItem_builder"}}{Notification:notificationValue(v.Notification)}
        if v.Receipt!=nil{b.Receipt=notificationReceiptValue(*v.Receipt)};vs[i]=b.Build()
    }
    return {{a "NotificationReadResponse_builder"}}{Items:vs,Next:page.Next,ReaderId:page.Reader.Bytes()}.Build(),nil
}
func (s *NotificationServer) Count(ctx {{ctx "Context"}}, req *{{a "NotificationCountRequest"}})(*{{a "NotificationCountResponse"}},error){
    q,err:=notificationQuery(req.GetTenantId(),req.GetTopics(),0,"");if err!=nil{return nil,err}
    filter,err:=notificationFilter(req.GetFilter());if err!=nil{return nil,err}
    v,err:=s.service.Count(ctx,{{n "CountRequest"}}{Tenant:q.Tenant,Topics:q.Topics,Filter:filter});if err!=nil{return nil,err}
    topics:=make([]*{{a "NotificationTopicCount"}},len(v.Topics))
    for i,t:=range v.Topics{topics[i]={{a "NotificationTopicCount_builder"}}{Topic:notificationTopicValue(t.Topic),Count:t.Count,SourceCount:t.SourceCount}.Build()}
    return {{a "NotificationCountResponse_builder"}}{Count:v.Count,SourceCount:v.SourceCount,Topics:topics,ReaderId:v.Reader.Bytes()}.Build(),nil
}
func (s *NotificationServer) Mark(ctx {{ctx "Context"}}, req *{{a "NotificationMarkRequest"}}) (*{{a "NotificationMarkResponse"}},error) {
    tenant,err:=notificationID(req.GetTenantId());if err!=nil{return nil,notifyArgument(err)}
    refs:=make([]{{id "Id"}},len(req.GetRefs()))
    for i,r:=range req.GetRefs(){key,err:={{id "From"}}(r.GetId());if err!=nil{return nil,notifyArgument(err)};refs[i]=key}
    v,err:=s.service.Mark(ctx,{{n "MarkRequest"}}{Tenant:tenant,Refs:refs});if err!=nil{return nil,err}
    items:=make([]*{{a "NotificationReceipt"}},len(v.Items));for i,r:=range v.Items{items[i]=notificationReceiptValue(r)}
    return {{a "NotificationMarkResponse_builder"}}{Items:items,ReaderId:v.Reader.Bytes()}.Build(),nil
}
func notificationFilter(v *{{a "NotificationReadFilter"}})({{n "ReadFilter"}},error){
    out:={{n "ReadFilter"}}{Kinds:v.GetKinds(),UnreadOnly:v.GetUnreadOnly()}
    for _,b:=range v.GetSourceIds(){id,err:={{id "From"}}(b);if err!=nil{return out,notifyArgument(err)};out.Sources=append(out.Sources,id)}
    return out,nil
}
func (s *NotificationServer) Listen(req *{{a "NotificationListenRequest"}}, stream {{.Stream}}) error {
    tenant,err:=notificationID(req.GetTenantId());if err!=nil{return notifyArgument(err)}
    topics,err:=notificationTopics(req.GetTopics());if err!=nil{return err}
    return s.service.Listen(stream.Context(),{{n "ListenRequest"}}{Tenant:tenant,Topics:topics},func(event {{n "Event"}})error{
        response:={{a "NotificationListenResponse_builder"}}{}
        if event.Ready {response.Ready={{a "NotificationListenReady_builder"}}{ReaderId:event.Reader.Bytes()}.Build()} else {
            values:=make([]*{{a "NotificationTopic"}},len(event.Topics));for i,v:=range event.Topics{values[i]=notificationTopicValue(v)}
            response.Invalidated={{a "NotificationInvalidation_builder"}}{Topics:values}.Build()
        }
        return stream.Send(response.Build())
    })
}

func notifyArgument(err error)error{return {{status "Error"}}({{codes "InvalidArgument"}},err.Error())}
func notificationBytes(v {{id "Id"}})[]byte{if v.IsZero(){return nil};return v.Bytes()}
func notificationID(v []byte)({{id "Id"}},error){if len(v)==0{return {{id "Nil"}},nil};return {{id "From"}}(v)}
func notificationTopic(v *{{a "NotificationTopic"}})({{n "Topic"}},error){
    if v==nil{return {{n "Topic"}}{},{{status "Error"}}({{codes "InvalidArgument"}},"topic is required")}
    if v.HasResourceId(){key,err:={{id "From"}}(v.GetResourceId());if err!=nil{return {{n "Topic"}}{},notifyArgument(err)};return {{n "Topic"}}{Resource:key},nil}
    if v.HasName(){return {{n "Topic"}}{Name:v.GetName()},nil}
    return {{n "Topic"}}{},{{status "Error"}}({{codes "InvalidArgument"}},"topic needs a resource or name")
}
func notificationTopics(vs []*{{a "NotificationTopic"}})([]{{n "Topic"}},error){
    values:=make([]{{n "Topic"}},len(vs));for i,v:=range vs{topic,err:=notificationTopic(v);if err!=nil{return nil,err};values[i]=topic};return values,nil
}
func notificationTopicValue(v {{n "Topic"}})*{{a "NotificationTopic"}}{
    if !v.Resource.IsZero(){return {{a "NotificationTopic_builder"}}{ResourceId:v.Resource.Bytes()}.Build()}
    return {{a "NotificationTopic_builder"}}{Name:&v.Name}.Build()
}
func notificationQuery(tenant []byte,topics []*{{a "NotificationTopic"}},size int32,cursor string)({{n "Query"}},error){
    key,err:=notificationID(tenant);if err!=nil{return {{n "Query"}}{},notifyArgument(err)}
    values,err:=notificationTopics(topics);if err!=nil{return {{n "Query"}}{},err}
    return {{n "Query"}}{Tenant:key,Topics:values,Size:int(size),Cursor:cursor},nil
}
func notificationValue(v {{n "Notification"}})*{{a "Notification"}}{
    b:={{a "Notification_builder"}}{Id:v.ID.Bytes(),TenantId:notificationBytes(v.Tenant),Topic:notificationTopicValue(v.Topic),Kind:v.Kind,Payload:v.Payload,DateCreated:{{timepb "New"}}(v.DateCreated),DateUpdated:{{timepb "New"}}(v.DateUpdated)}
    if !v.Source.IsZero(){b.SourceId=v.Source.Bytes()};return b.Build()
}
func notificationReceiptValue(v {{n "Receipt"}})*{{a "NotificationReceipt"}}{
    return {{a "NotificationReceipt_builder"}}{Id:v.ID.Bytes(),TenantId:notificationBytes(v.Tenant),NotificationId:v.Notification.Bytes(),ReaderId:v.Reader.Bytes(),DateRead:{{timepb "New"}}(v.DateRead),DateCreated:{{timepb "New"}}(v.DateCreated),DateUpdated:{{timepb "New"}}(v.DateUpdated)}.Build()
}

type notificationStore struct{stack {{a "Server"}}; driver {{drv "Driver"}}; db *{{e "Client"}}}
var _ {{n "Store"}}=notificationStore{}
func(s notificationStore) Driver(){{drv "Driver"}}{return s.driver}
func(s notificationStore) Rebind(driver {{drv "Driver"}})({{n "Store"}},error){
    stack,err:={{tx "Rebind"}}(s.stack,driver);if err!=nil{return nil,err}
    return notificationStore{stack:stack,driver:driver,db:{{e "NewClient"}}({{e "Driver"}}(driver))},nil
}
func(s notificationStore) Insert(ctx {{ctx "Context"}},v {{n "Notification"}})({{n "Notification"}},error){
    b:={{record "AddRequest_builder"}}{ {{if not .Global}}TenantId:v.Tenant.Bytes(),{{end}}Kind:v.Kind,TopicKey:v.Topic.Key(),Producer:v.Producer,IdempotencyKey:v.IdempotencyKey,DateCreated:{{timepb "New"}}(v.DateCreated),PayloadValue:[]byte{}}
    if !v.ID.IsZero(){b.Id=v.ID.Bytes()};if !v.Source.IsZero(){b.SourceId=v.Source.Bytes()}
    if v.Payload!=nil{b.PayloadPresent=true;b.PayloadType=v.Payload.TypeUrl;b.PayloadValue=append([]byte{},v.Payload.Value...)}
    row,err:=s.stack.{{.Record}}().Add(withNotifyWrite(ctx),b.Build());if err!=nil{return {{n "Notification"}}{},err}
    key,err:={{id "From"}}(row.GetId());if err!=nil{return {{n "Notification"}}{},err}
    // Read the stored value: secret response projection may hide the payload,
    // and PostgreSQL normalizes timestamp precision before storing it.
    return s.Find(ctx,key)
}
func(s notificationStore) Find(ctx {{ctx "Context"}},key {{id "Id"}})({{n "Notification"}},error){
    row,err:=s.db.{{.Record}}.Query().Where({{rp "IdEQ"}}(key.Uuid())).Only(ctx)
    if {{e "IsNotFound"}}(err){return {{n "Notification"}}{},{{status "Error"}}({{codes "NotFound"}},"notification not found")}
    if err!=nil{return {{n "Notification"}}{},err};return notificationRow(row.Proto())
}
func(s notificationStore) FindPublication(ctx {{ctx "Context"}},tenant {{id "Id"}},producer,key string)({{n "Notification"}},bool,error){
    row,err:=s.db.{{.Record}}.Query().Where({{if not .Global}}{{rp "TenantIdEQ"}}(tenant.Uuid()),{{end}}{{rp "ProducerEQ"}}(producer),{{rp "IdempotencyKeyEQ"}}(key)).Only(ctx)
    if {{e "IsNotFound"}}(err){return {{n "Notification"}}{},false,nil};if err!=nil{return {{n "Notification"}}{},false,err}
    v,err:=notificationRow(row.Proto());return v,true,err
}
func(s notificationStore) Select(ctx {{ctx "Context"}},q {{n "Selection"}})([]{{n "Notification"}},error){
    query:=s.db.{{.Record}}.Query().Where(notificationSelection(q))
    if q.Position!=nil{query.Where({{rp "Or"}}({{rp "DateCreatedLT"}}(q.Position.DateCreated),{{rp "And"}}({{rp "DateCreatedEQ"}}(q.Position.DateCreated),{{rp "IdLT"}}(q.Position.ID.Uuid()))))}
    rows,err:=query.Order({{e "Desc"}}({{rp "FieldDateCreated"}}),{{e "Desc"}}({{rp "FieldId"}})).Limit(q.Limit).All(ctx);if err!=nil{return nil,err}
    values:=make([]{{n "Notification"}},len(rows));for i,row:=range rows{v,err:=notificationRow(row.Proto());if err!=nil{return nil,err};values[i]=v};return values,nil
}
func notificationSelection(q {{n "Selection"}})func(*{{sql "Selector"}}){return func(s *{{sql "Selector"}}){
    keys:=make([]any,len(q.Topics));for i,t:=range q.Topics{keys[i]=t.Key()}
    s.Where({{sql "In"}}(s.C({{rp "FieldTopicKey"}}),keys...))
    {{if not .Global}}s.Where({{sql "EQ"}}(s.C({{rp "FieldTenantId"}}),q.Tenant.Uuid())){{end}}
    if len(q.Filter.Kinds)>0{values:=make([]any,len(q.Filter.Kinds));for i,v:=range q.Filter.Kinds{values[i]=v};s.Where({{sql "In"}}(s.C({{rp "FieldKind"}}),values...))}
    if len(q.Filter.Sources)>0{values:=make([]any,len(q.Filter.Sources));for i,v:=range q.Filter.Sources{values[i]=v.Uuid()};s.Where({{sql "In"}}(s.C({{rp "FieldSourceId"}}),values...))}
    if q.Filter.UnreadOnly{
        r:={{sql "Table"}}({{cp "Table"}}).As("receipt")
        seen:={{sql "Select"}}(r.C({{cp "FieldNotificationId"}})).From(r).Where({{sql "And"}}({{sql "ColumnsEQ"}}(r.C({{cp "FieldNotificationId"}}),s.C({{rp "FieldId"}})),{{sql "EQ"}}(r.C({{cp "FieldReaderId"}}),q.Reader.Uuid())))
        {{if not .Global}}seen.Where({{sql "EQ"}}(r.C({{cp "FieldTenantId"}}),q.Tenant.Uuid())){{end}}
        s.Where({{sql "Not"}}({{sql "Exists"}}(seen)))
    }
}}
func(s notificationStore) Count(ctx {{ctx "Context"}},q {{n "Selection"}})({{n "Counts"}},error){
    base:={{sql "Dialect"}}(s.driver.Dialect()).Select({{rp "FieldTopicKey"}},{{rp "FieldSourceId"}}).From({{sql "Table"}}({{rp "Table"}}))
    notificationSelection(q)(base)
    query,args:=base.Query()
    query="WITH matches AS ("+query+") SELECT topic_key,COUNT(*),COUNT(DISTINCT source_id) FROM matches GROUP BY topic_key UNION ALL SELECT NULL,COUNT(*),COUNT(DISTINCT source_id) FROM matches"
    var rows {{sql "Rows"}}
    if err:=s.driver.Query(ctx,query,args,&rows);err!=nil{return {{n "Counts"}}{},err};defer rows.Close()
    result:={{n "Counts"}}{}
    for rows.Next(){
        var key *string;var count,sources uint64
        if err:=rows.Scan(&key,&count,&sources);err!=nil{return result,err}
        if key==nil{result.Count=count;result.SourceCount=sources;continue}
        topic,err:={{n "ParseTopicKey"}}(*key);if err!=nil{return result,err}
        result.Topics=append(result.Topics,{{n "TopicCount"}}{Topic:topic,Count:count,SourceCount:sources})
    }
    return result,rows.Err()
}
func(s notificationStore) Receipts(ctx {{ctx "Context"}},tenant,reader {{id "Id"}},ids []{{id "Id"}})([]{{n "Receipt"}},error){
    if len(ids)==0{return nil,nil};keys:=make([]{{uuid "UUID"}},len(ids));for i,v:=range ids{keys[i]=v.Uuid()}
    rows,err:=s.db.{{.Receipt}}.Query().Where({{if not .Global}}{{cp "TenantIdEQ"}}(tenant.Uuid()),{{end}}{{cp "ReaderIdEQ"}}(reader.Uuid()),{{cp "NotificationIdIn"}}(keys...)).All(ctx);if err!=nil{return nil,err}
    values:=make([]{{n "Receipt"}},len(rows));for i,row:=range rows{v,err:=notificationReceiptRow(row.Proto());if err!=nil{return nil,err};values[i]=v};return values,nil
}
// InsertReceipt writes the receipt itself, through the client and past the
// servers: it has no RPC, and is kept off the trail (payday#197). Mark holds
// what the servers would have -- the transaction it is in, the locks that
// keep one receipt per reader, the reader's tenant -- and notes the hint its
// reader's other sessions are owed.
func(s notificationStore) InsertReceipt(ctx {{ctx "Context"}},v {{n "Receipt"}})({{n "Receipt"}},error){
    key:=v.ID;if key.IsZero(){key={{id "New"}}({{.Receipt}}Domain)}
    _,err:=s.db.{{.Receipt}}.Create().SetId(key.Uuid()){{if not .Global}}.SetTenantId(v.Tenant.Uuid()){{end}}.SetNotificationId(v.Notification.Uuid()).SetReaderId(v.Reader.Uuid()).SetDateRead(v.DateRead).SetDateUpdated(v.DateUpdated).SetDateCreated(v.DateCreated).Save(ctx)
    if err!=nil{return {{n "Receipt"}}{},err}
    // Read the stored value, as Insert does: PostgreSQL keeps the precision it
    // keeps, and the first Mark answers with what every later Read will.
    saved,err:=s.db.{{.Receipt}}.Query().Where({{cp "IdEQ"}}(key.Uuid())).Only(ctx);if err!=nil{return {{n "Receipt"}}{},err}
    return notificationReceiptRow(saved.Proto())
}
// Hold takes a share lock of each notification of ids that is there, on
// PostgreSQL and in the order of their identifiers: what Mark asks before it
// reads them, so that an expiry erasing one waits for the receipt being
// written of it (notify.Store.Hold).
func(s notificationStore) Hold(ctx {{ctx "Context"}},ids []{{id "Id"}})error{
    keys:=make([]{{uuid "UUID"}},len(ids));for i,v:=range ids{keys[i]=v.Uuid()}
    _,err:=notificationsLocked(ctx,s.driver,keys,false);return err
}
// notificationsLocked answers which of keys are notifications that are there,
// having locked each -- to share, or to update -- in the order of their
// identifiers, which is the order Mark and an expiry both lock in, so neither
// waits on the other while it holds what the other wants. On SQLite, which
// has one writer at a time, nothing is locked and every key is answered.
func notificationsLocked(ctx {{ctx "Context"}},driver {{drv "Driver"}},keys []{{uuid "UUID"}},update bool)(map[{{uuid "UUID"}}]bool,error){
    out:=make(map[{{uuid "UUID"}}]bool,len(keys))
    if driver.Dialect()!={{drv "Postgres"}}{for _,k:=range keys{out[k]=true};return out,nil}
    if len(keys)==0{return out,nil}
    args:=make([]any,len(keys));for i,k:=range keys{args[i]=k}
    q:={{sql "Dialect"}}(driver.Dialect()).Select({{rp "FieldId"}}).From({{sql "Table"}}({{rp "Table"}})).Where({{sql "In"}}({{rp "FieldId"}},args...)).OrderBy({{rp "FieldId"}})
    if update{q.ForUpdate()}else{q.ForShare()}
    query,qargs:=q.Query()
    var rows {{sql "Rows"}}
    if err:=driver.Query(ctx,query,qargs,&rows);err!=nil{return nil,err};defer rows.Close()
    for rows.Next(){var k {{uuid "UUID"}};if err:=rows.Scan(&k);err!=nil{return nil,err};out[k]=true}
    return out,rows.Err()
}
func(s notificationStore) Change(ctx {{ctx "Context"}},change {{w "Change"}})({{n "Hint"}},bool,error){
    switch change.Key.Domain(){
    case {{.Record}}Domain:
        row,err:=s.Find(ctx,change.Key);if {{status "Code"}}(err)=={{codes "NotFound"}}{return {{n "Hint"}}{},false,nil};if err!=nil{return {{n "Hint"}}{},false,err}
        return {{n "Hint"}}{Tenant:row.Tenant,Topics:[]{{n "Topic"}}{row.Topic}},true,nil
    default:
        // A receipt is no server's write, and its hint is the Signal Mark
        // noted, which Service reads without coming here (payday#197).
        return {{n "Hint"}}{},false,nil
    }
}
{{if .Keep}}
// NotificationKeep is how long a notification is kept from its publication,
// as {{.At}} declares: {{.Keep}} days. [NotificationExpirer] erases what is
// older, with every receipt of it and its idempotency key.
const NotificationKeep = {{.Keep}} * 24 * {{time "Hour"}}

// NotificationExpirer is the loop that erases the notifications older than
// [NotificationKeep], with every receipt of each (payday#194): a
// {{bt}}spin.Spinner{{bt}} called {{bt}}payday.notify.expire{{bt}}, for the spin list.
//
//	s.Spin = append(s.Spin, pd.NotificationExpirer(drv, w, content))
//
// {{bt}}driver{{bt}} is the deployment's own: an expiry is the deployment acting on
// itself, and nobody's in a tenant. {{bt}}content{{bt}} is the trail's, as the recorder
// is given it: each notification erased is a line of the trail saying it went
// ([RecordErased]), filed under the tenant it was in. {{bt}}changes{{bt}} is told which
// topics a batch took from, once it commits, so that a stream showing one
// drops it.
func NotificationExpirer(driver {{drv "Driver"}}, changes *{{w "Watch"}}, content {{audit "Contents"}}) {{n "Expirer"}} {
    return {{n "Expirer"}}{Keep:NotificationKeep,Watch:changes,Erase:func(ctx {{ctx "Context"}},before {{time "Time"}},limit int)([]{{n "Notification"}},error){
        return notificationErase(ctx,driver,content,before,limit)
    }}
}

// notificationErase is one batch of an expiry, in a transaction of its own:
// the oldest {{bt}}limit{{bt}} notifications published before {{bt}}before{{bt}}, every receipt
// of them, and a line of the trail for each notification.
func notificationErase(ctx {{ctx "Context"}},driver {{drv "Driver"}},content {{audit "Contents"}},before {{time "Time"}},limit int)([]{{n "Notification"}},error){
    tdrv,tx,err:={{drv "BeginTx"}}(ctx,driver);if err!=nil{return nil,err}
    defer tx.Rollback()
    db:={{e "NewClient"}}({{e "Driver"}}(tdrv))
    rows,err:=db.{{.Record}}.Query().Where({{rp "DateCreatedLT"}}(before)).Order({{e "Asc"}}({{rp "FieldDateCreated"}}),{{e "Asc"}}({{rp "FieldId"}})).Limit(limit).All(ctx)
    if err!=nil||len(rows)==0{return nil,err}
    // Locked to update before anything goes, in the order Mark holds what it
    // reads: so this waits for a Mark writing a receipt of one, and the
    // receipts' delete below finds what it wrote. An expiry run beside this
    // one has taken what this finds is no longer there.
    keys:=make([]{{uuid "UUID"}},len(rows));for i,row:=range rows{keys[i]=row.Id}
    held,err:=notificationsLocked(ctx,tdrv,keys,true);if err!=nil{return nil,err}
    var out []{{n "Notification"}};var gone []{{uuid "UUID"}};var tenants []{{id "Id"}};erased:=map[{{id "Id"}}][]{{id "Id"}}{}
    for _,row:=range rows{
        if !held[row.Id]{continue}
        v,err:=notificationRow(row.Proto());if err!=nil{return nil,err}
        out=append(out,v);gone=append(gone,row.Id)
        if _,ok:=erased[v.Tenant];!ok{tenants=append(tenants,v.Tenant)}
        erased[v.Tenant]=append(erased[v.Tenant],v.ID)
    }
    if len(gone)==0{return nil,nil}
    if _,err:=db.{{.Record}}.Delete().Where({{rp "IdIn"}}(gone...)).Exec(ctx);err!=nil{return nil,err}
    if _,err:=db.{{.Receipt}}.Delete().Where({{cp "NotificationIdIn"}}(gone...)).Exec(ctx);err!=nil{return nil,err}
    for _,tenant:=range tenants{
        // The deployment's work in the namespace the notifications were in,
        // which is what the trail files an erasure of no row under.
        in:={{frame "Into"}}(ctx,{{frame "New"}}({{id "Nil"}},tenant,{{frame "Grant"}}{}).WithScope({{frame "Only"}}(tenant)))
        if _,err:=RecordErased(in,db,content,{{n "ExpireName"}},erased[tenant]);err!=nil{return nil,err}
    }
    return out,tx.Commit()
}
{{end}}
{{if not .Global}}
// notificationTenant is the namespace a record's tenant_id says: a tenant, or
// the Nil identifier of the account namespace, which is no tenant's
// (payday#195) -- as payday's own records say no tenant.
func notificationTenant(v []byte)({{id "Id"}},error){
    if len(v)==16&&{{bytes "Equal"}}(v,make([]byte,16)){return {{id "Nil"}},nil}
    return {{id "From"}}(v)
}
{{end}}
func notificationRow(v *{{record ""}})({{n "Notification"}},error){
    key,err:={{id "From"}}(v.GetId());if err!=nil{return {{n "Notification"}}{},err}
    {{if .Global}}tenant:={{id "Nil"}}{{else}}tenant,err:=notificationTenant(v.GetTenantId());if err!=nil{return {{n "Notification"}}{},err}{{end}}
    topic,err:={{n "ParseTopicKey"}}(v.GetTopicKey());if err!=nil{return {{n "Notification"}}{},err}
    source,err:=notificationID(v.GetSourceId());if err!=nil{return {{n "Notification"}}{},err}
    row:={{n "Notification"}}{ID:key,Tenant:tenant,Topic:topic,Source:source,Kind:v.GetKind(),Producer:v.GetProducer(),IdempotencyKey:v.GetIdempotencyKey(),DateCreated:v.GetDateCreated().AsTime(),DateUpdated:v.GetDateUpdated().AsTime()}
    if v.GetPayloadPresent(){row.Payload=&{{pb "Any"}}{TypeUrl:v.GetPayloadType(),Value:{{bytes "Clone"}}(v.GetPayloadValue())}}
    return row,nil
}
func notificationReceiptRow(v *{{receipt ""}})({{n "Receipt"}},error){
    key,err:={{id "From"}}(v.GetId());if err!=nil{return {{n "Receipt"}}{},err}
    {{if .Global}}tenant:={{id "Nil"}}{{else}}tenant,err:=notificationTenant(v.GetTenantId());if err!=nil{return {{n "Receipt"}}{},err}{{end}}
    notification,err:={{id "From"}}(v.GetNotificationId());if err!=nil{return {{n "Receipt"}}{},err}
    reader,err:={{id "From"}}(v.GetReaderId());if err!=nil{return {{n "Receipt"}}{},err}
    return {{n "Receipt"}}{ID:key,Tenant:tenant,Notification:notification,Reader:reader,DateRead:v.GetDateRead().AsTime(),DateCreated:v.GetDateCreated().AsTime(),DateUpdated:v.GetDateUpdated().AsTime()},nil
}
`
