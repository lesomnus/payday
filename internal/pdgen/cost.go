package pdgen

import (
	"google.golang.org/protobuf/compiler/protogen"
)

// EmitCost writes how many calls a request of this app counts as: one, but an
// AddMany is one for each of its items.
//
// An AddMany of a thousand rows is a thousand Adds' worth of work sent as one
// call, and the limit counted it as one -- a thousand calls' worth for the
// price of one. Which RPCs write many rows is the schema's to say, so the
// answer is generated: an RPC an entity asks for is weighed without anybody
// listing it.
func EmitCost(g *protogen.GeneratedFile, s *Schema, root protogen.GoImportPath) {
	g.P("// Cost is how many calls a request counts as: an AddMany is one for each of")
	g.P("// its items -- the Add each stands for -- and every other call is one.")
	g.P("//")
	g.P("// It is what the limit charges a call; the interceptor is handed it:")
	g.P("//")
	g.P("//	WithUnary(grpcx.LimitUnary(c.Server.Limiter(), gate.ByTenant(), pd.Cost))")
	g.P("//")
	g.P("// An app that weighs RPCs of its own writes a Cost that asks this one for")
	g.P("// the rest.")
	g.P("func Cost(method string, req any) int {")
	many := []*Entity{}
	for _, v := range s.Sorted() {
		if v.Rpcs().HasAddMany() {
			many = append(many, v)
		}
	}
	if len(many) > 0 {
		g.P("	switch method {")
		for _, v := range many {
			name := v.GoName()
			g.P("	case ", root.Ident(name+"Service_AddMany_FullMethodName"), ":")
			g.P("		if v, ok := req.(*", root.Ident(name+"AddManyRequest"), "); ok {")
			g.P("			return max(len(v.GetItems()), 1)")
			g.P("		}")
		}
		g.P("	}")
		g.P("")
	}
	g.P("	return 1")
	g.P("}")
	g.P("")
	g.P("var _ ", pkgGrpcx.Ident("Cost"), " = Cost")
	g.P("")
}
