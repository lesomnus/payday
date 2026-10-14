package pdgen

import (
	"strconv"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/compiler/protogen"
)

const pkgResource = protogen.GoImportPath("github.com/lesomnus/payday/resource")
const pkgResourceSQL = protogen.GoImportPath("github.com/lesomnus/payday/resource/resourcesql")

// EmitResources connects the shared service to this application's types, read
// predicates and secret declarations. Historical reads use the same Scope as
// ordinary reads, evaluated against the recorded root row.
func EmitResources(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	g.P("// Resources serves the common Batch and Watch API. project is required so custom")
	g.P("// read layers cannot silently disappear from historical reads. ResourceProjector")
	g.P("// handles bare.Scope and declared secrets; compose any additional read rules.")
	g.P("func Resources(s ", root.Ident("Server"), ", drv ", pkgDialect.Ident("Driver"), ", guard ", pkgBatch.Ident("Guard"), ", w *", pkgWatch.Ident("Watch"), ", project ", pkgResource.Ident("Project"), ") (*", pkgResource.Ident("Server"), ",error){")
	g.P("b,err:=Batch(s,drv,guard);if err!=nil{return nil,err}")
	g.P("return ", pkgResource.Ident("New"), "(b,w,project,")
	for _, e := range s.Sorted() {
		g.P("&", root.Ident(e.GoName()), "{},")
	}
	g.P(")\n}")
	g.P("var resourceSecrets = map[", pkgReflect.Ident("FullName"), "][]", pkgReflect.Ident("FieldNumber"), "{")
	for _, e := range s.Sorted() {
		if len(e.SecretNumbers) == 0 {
			continue
		}
		g.P(strconv.Quote(string(e.FullName())), ": {")
		for _, n := range e.SecretNumbers {
			g.P(n, ",")
		}
		g.P("},")
	}
	g.P("}")
	g.P("func publicResource(v ", pkgProto.Ident("Message"), ") ", pkgProto.Ident("Message"), " {return ", pkgResource.Ident("Redact"), "(v,resourceSecrets)}")
	g.P("// ResourceProjector applies current Get authorization and scope to the historical")
	g.P("// root row. Relations are checked against current rows, failing closed if gone.")
	g.P("// Scope must be the one installed on the serving stack. Custom Get layers must")
	g.P("// wrap this projector with their historical equivalent before serving Resources.")
	g.P("func ResourceProjector(db *", p.Ent.Ident("Client"), ", scope ", p.Bare.Ident("Scope"), ", guard ", pkgBatch.Ident("Guard"), ") ", pkgResource.Ident("Project"), " {")
	g.P("return func(ctx ", pkgCtx.Ident("Context"), ", message ", pkgProto.Ident("Message"), ") (", pkgProto.Ident("Message"), ",error) {")
	g.P("if guard.IsZero() {return nil,", pkgBatch.Ident("ErrNoGuard"), "}")
	g.P("read:=guard; read.Limiter=nil; read.By=nil")
	g.P("switch v:=message.(type) {")
	for _, e := range s.Sorted() {
		if !e.Rpcs().HasGet() {
			continue
		}
		name := e.GoName()
		ep := p.Ent + "/" + protogen.GoImportPath(e.EntPkg())
		g.P("case *", root.Ident(name), ":")
		g.P("ctx,err:=read.Allow(ctx,", root.Ident(name+"Service_Get_FullMethodName"), ");if err!=nil {")
		g.P("switch ", pkgStatus.Ident("Code"), "(err) {case ", pkgCodes.Ident("PermissionDenied"), ",", pkgCodes.Ident("Unauthenticated"), ",", pkgCodes.Ident("Unimplemented"), ":return nil,nil};return nil,err}")
		g.P("pred,err:=", p.Bare.Ident(name+"Narrow"), "(ctx,scope,nil);if err!=nil{return nil,err}")
		g.P("row,err:=", pkgResourceSQL.Ident("Row"), "(db.Dialect(),", ep.Ident("Table"), ",[]", pkgResourceSQL.Ident("Column"), "{")
		for prop := range e.Props() {
			secret := false
			for _, n := range e.SecretNumbers {
				if n == uint32(prop.Number()) {
					secret = true
				}
			}
			if secret {
				continue
			}
			// Protobuf does not preserve SQL NULL versus an empty collection,
			// or unknown JSON properties. Never authorize a SQL predicate using
			// an invented representation of those columns.
			if _, edge := prop.(graph.Edge); !edge && (graph.IsCollection(prop) || prop.Type() == ormpb.Type_TYPE_JSON || prop.Type() == ormpb.Type_TYPE_MESSAGE || (prop.IsNullable() && !prop.Descriptor().HasPresence())) {
				continue
			}
			col := ep.Ident("Field" + pascal(prop.Name()))
			typ := prop.Type()
			key := int32(0)
			if edge, ok := prop.(graph.Edge); ok {
				// Foreign-key edges are root-row columns. Collection/inverse relations are
				// read from current related rows by the existing scope predicate.
				if edge.IsList() || edge.Inverse() != nil {
					continue
				}
				col = ep.Ident(pascal(prop.Name()) + "Column")
				typ = edge.Target().Key().Type()
				key = int32(edge.Target().Key().Number())
			}
			g.P("{Number:", prop.Number(), ",Name:", col, ",Type:", int32(typ), ",Nullable:", prop.IsNullable(), ",Key:", key, "},")
		}
		g.P("},v);if err!=nil{return nil,err}")
		g.P("query:=db.", name, ".Query();if pred!=nil{query.Where(pred)}")
		g.P("ok,err:=query.Modify(func(q *", pkgEntsql.Ident("Selector"), "){q.From(row)}).Exist(ctx)")
		g.P("if err!=nil || !ok {return nil,err};return publicResource(v),nil")
	}
	g.P("default:return nil,nil\n}\n}\n}")
	emitResourceRecorder(g, s, p)
}

func emitResourceRecorder(g *protogen.GeneratedFile, s *Schema, p Paths) {
	g.P("// ResourceRecorder selects and constructs one envelope for both publication paths.")
	g.P("// Install it in place of WatchRecorder + OutboxRecorder. Audit stays separate.")
	g.P("func ResourceRecorder(w *", pkgWatch.Ident("Watch"), ", outbox bool, selectEvent ", pkgResource.Ident("Select"), ") ", p.Bare.Ident("Recorder"), " {return resourceRecorder{w,outbox,selectEvent}}")
	g.P("type resourceRecorder struct{w *", pkgWatch.Ident("Watch"), "; outbox bool; selectEvent ", pkgResource.Ident("Select"), "}")
	g.P("func(r resourceRecorder) Record(ctx ", pkgCtx.Ident("Context"), ",s ", p.Bare.Ident("Server"), ",c ", p.Bare.Ident("Change"), ") error {")
	g.P("image:=c.Value;if image==nil {image=c.Previous};if image==nil{return nil}")
	g.P("switch image.ProtoReflect().Descriptor().FullName(){")
	var supported []string
	for _, e := range s.Sorted() {
		if e.Rpcs().HasGet() {
			supported = append(supported, strconv.Quote(string(e.FullName())))
		}
	}
	g.P("case ", strings.Join(supported, ","), ":\ndefault:return nil\n}")
	g.P("k,err:=", pkgPdid.Ident("From"), "(keyBytes(c.Key));if err!=nil{return err}")
	g.P("id:=", pkgPdid.Ident("New"), "(OutboxDomain);at:=", pkgTime.Ident("Now"), "().UTC()")
	g.P("event,err:=", pkgResource.Ident("Event"), "(id[:],at,c.Method,", pkgWatch.Ident("CallMethod"), "(ctx,c.Method),publicResource(c.Previous),publicResource(c.Value));if err!=nil{return err}")
	g.P("if r.selectEvent!=nil {ok,err:=r.selectEvent(ctx,", pkgProto.Ident("Clone"), "(event).(*", pkgPdpb.Ident("WatchEvent"), "));if err!=nil || !ok{return err}}")
	g.P("doc:=[]byte{};if v:=hidden(k,c.Patch);v!=nil{doc,err=", pkgProto.Ident("Marshal"), "(v);if err!=nil{return err}}")
	g.P("if r.outbox {")
	g.P("data,err:=", pkgProto.Ident("Marshal"), "(event);if err!=nil{return err}")
	g.P("var actor,tenant ", pkgPdid.Ident("Id"), ";if f,ok:=", pkgFrame.Ident("From"), "(ctx);ok{actor=f.Actor;tenant=f.Tenant}")
	g.P("if err:=s.Db.Outbox.Create().SetId(id.Uuid()).SetDateCreated(at).SetActorId(actor.Uuid()).SetTenantId(tenant.Uuid()).SetMethod(c.Method).SetBy(c.By).SetObjectId(k.Uuid()).SetPatch(doc).SetEvent(data).Exec(ctx);err!=nil{return err}")
	g.P("}")
	g.P("if r.w!=nil{r.w.Note(ctx,", pkgWatch.Ident("Change"), "{Method:c.Method,By:c.By,Key:k,Patch:doc,Resource:event})};return nil\n}")
}
