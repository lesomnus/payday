package pdgen

import (
	"strconv"

	"github.com/lesomnus/payday/pdpb"
	"google.golang.org/protobuf/compiler/protogen"
)

const pkgAdmission = protogen.GoImportPath("github.com/lesomnus/payday/admission")

// EmitAdmission binds the process policy to the native write seam. Installation
// belongs on the sink, so neither layer order nor Patch's inner Apply bypass it.
func EmitAdmission(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	g.P("// Admission protects the entities options names, including direct ent mutations.")
	g.P("// Install once on the serving sink. Payday's recording tables are excluded;")
	g.P("// their writes are consequences of admitted operations and cannot recurse.")
	g.P("func Admission(policy ", pkgAdmission.Ident("Policy"), ", options ", pkgAdmission.Ident("Options"), ") ", p.Bare.Ident("Option"), " {return func(s *", p.Bare.Ident("Server"), ")error {")
	g.P("names,err:=", pkgAdmission.Ident("Select"), "(options.Entities,[]string{")
	for _, e := range s.Sorted() {
		switch e.Own {
		case pdpb.Own_OWN_UNSPECIFIED, pdpb.Own_OWN_TENANT, pdpb.Own_OWN_HOLDER:
			g.P(strconv.Quote(string(e.FullName())), ",")
		}
	}
	g.P("});if err!=nil{return err}")
	g.P("check,err:=", pkgAdmission.Ident("New"), "(policy,options,[]", pkgAdmission.Ident("Entity"), "{")
	for _, e := range s.Sorted() {
		name := e.GoName()
		g.P("{Row:&", root.Ident(name), "{},Requests:[]", pkgProto.Ident("Message"), "{")
		if e.Rpcs().HasGet() || e.Rpcs().HasPatch() || e.Rpcs().HasErase() {
			g.P("&", root.Ident(name+"Ref"), "{},")
		}
		if e.Rpcs().HasAdd() || e.Rpcs().HasAddMany() {
			g.P("&", root.Ident(name+"AddRequest"), "{},")
		}
		if e.Rpcs().HasPatch() {
			g.P("&", root.Ident(name+"PatchRequest"), "{},")
		}
		g.P("}},")
	}
	g.P("},resourceSecrets);if err!=nil{return err};return ", p.Bare.Ident("WithAdmission"), "(admitter{check},names...)(s) }")
	g.P("}")
	g.P("type admitter struct{ check *", pkgAdmission.Ident("Checker"), " }")
	g.P("func(a admitter) Admit(ctx ", pkgCtx.Ident("Context"), ", c ", p.Bare.Ident("Change"), ")error {")
	g.P("id,err:=", pkgAudit.Ident("Identifier"), "(c.Key);if err!=nil{return err}")
	g.P("return a.check.Check(ctx,", pkgAdmission.Ident("Call"), "{Entity:c.Entity,Object:id,Method:c.Method,By:c.By,Request:c.Request,Previous:c.Previous,Value:c.Value}) }")
	g.P("// AdmissionUnary captures the outer request before an app layer changes it or")
	g.P("// turns it into several writes. Put it at the transport boundary. In-process")
	g.P("// custom calls and individual stream messages can use bare.Origin explicitly.")
	g.P("func AdmissionUnary(ctx ", pkgCtx.Ident("Context"), ", req any, info *", pkgGrpc.Ident("UnaryServerInfo"), ", next ", pkgGrpc.Ident("UnaryHandler"), ") (any,error) {")
	g.P("v,ok:=req.(", pkgProto.Ident("Message"), ");if !ok{return nil,", pkgStatus.Ident("Error"), "(", pkgCodes.Ident("Internal"), ",\"admission: RPC request is not a protobuf message\")};return next(", p.Bare.Ident("Origin"), "(ctx,info.FullMethod,v),req) }")
}
