package pdgen

import (
	"strconv"

	"github.com/lesomnus/payday/pdpb"
	"google.golang.org/protobuf/compiler/protogen"
)

const pkgJSON = protogen.GoImportPath("encoding/json")

// Generic watches already know the generated service and immutable row ID.
// Extra images are needed only for notification topics, not for every write.
func emitRouting(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	n := s.Own(pdpb.Own_OWN_NOTIFICATION)
	g.P("// routingSnapshots captures notification metadata in the write transaction.")
	g.P("func routingSnapshots(method string) ", p.Bare.Ident("SnapshotOptions"), " {")
	if n != nil {
		g.P("if ", pkgWatch.Ident("ServiceOf"), "(method)==", strconv.Quote("/"+string(n.FullName())+"Service/"), " {return ", p.Bare.Ident("SnapshotOptions"), "{Value:true,Previous:true}}")
	}
	g.P("return ", p.Bare.Ident("SnapshotOptions"), "{} }")
	g.P("// changeRouting never substitutes the caller's tenant for the changed row's.")
	g.P("// A nil result means metadata was unavailable; receivers must overdeliver.")
	g.P("func changeRouting(c ", p.Bare.Ident("Change"), ")([]", pkgWatch.Ident("Route"), ",[]byte,error){")
	if n != nil {
		g.P("key,err:=", pkgPdid.Ident("From"), "(keyBytes(c.Key));if err!=nil{return nil,nil,nil}")
		g.P("if key.Domain()==", n.GoName(), "Domain {")
		g.P("image:=c.Value;if image==nil{image=c.Previous}")
		g.P("v,ok:=image.(*", root.Ident(n.GoName()), ");if !ok||v==nil{return nil,nil,nil}")
		g.P("row,err:=notificationRow(v);if err!=nil{return nil,nil,err}")
		g.P("hint:=", pkgNotify.Ident("Hint"), "{Tenant:row.Tenant,Topics:[]", pkgNotify.Ident("Topic"), "{row.Topic}}")
		g.P("signal,err:=", pkgJSON.Ident("Marshal"), "(hint);if err!=nil{return nil,nil,err}")
		g.P("return ", pkgNotify.Ident("Routes"), "(", strconv.Quote(string(root)), ",hint),signal,nil }")
	}
	g.P("return []", pkgWatch.Ident("Route"), "{},nil,nil }")
}
