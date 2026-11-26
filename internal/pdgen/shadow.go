package pdgen

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/internal/pdgen/shadow"
	"github.com/lesomnus/payday/pdpb"
)

// An RPC an app declares is a method of its service, and a layer writes, for
// every entity it says something about, a type that embeds the layer's struct
// beside that service:
//
//	type gateRelay struct {
//		Gate
//		api.RelayServiceServer
//	}
//
// Both are at one depth, so a name the two share is promoted from neither: the
// type has no method of that name and is not the service it is returned as.
// The layer's struct has a method for every entity -- the accessor -- so an
// RPC called what an entity is called is that collision. An app found it with
// `ProducerService/Relay` beside its entity `Relay`, and could not compile: the
// method it would have written itself is in `server/pd`, which `pd gen` owns.
//
// So where a name is shadowed the generated type says the RPC itself, as a
// method that calls the service's. That is exactly what promotion would have
// been, so the layer does for that RPC what it does for every RPC of the
// service it does not override: nothing of its own, and whatever wraps it
// (the gate's method rule is `guarded<E>`, outside this type) still wraps it.

func (s *Schema) layerNames(layer string) map[string]bool {
	es := make([]string, 0, len(s.Entities))
	for _, v := range s.Entities {
		es = append(es, v.GoName())
	}

	names := map[string]bool{}
	for _, v := range shadow.Names(layer, es) {
		names[v] = true
	}

	return names
}

// shadowed writes, on `recv` -- the type `layer` writes for entity `name`,
// embedding the layer's struct beside the entity's service -- each RPC of that
// service whose name the layer's struct also answers to, as a method that
// calls the service's.
//
// `declared` is what the type already writes itself, which is the layer saying
// something of that RPC and not a collision.
func shadowed(g *protogen.GeneratedFile, s *Schema, root protogen.GoImportPath, layer, recv, name string, declared ...string) {
	names := s.layerNames(layer)
	svc := name + "ServiceServer"
	for _, v := range s.rpcs {
		if v.Entity != name || !names[v.Rpc] || slices.Contains(declared, v.Rpc) {
			continue
		}

		g.P("// ", v.Rpc, " is the service's: ", layer, " answers to the name as well, and")
		g.P("// where the two are at one depth neither is promoted. This is what")
		g.P("// promotion would have been.")
		switch {
		case v.Client:
			kind := "ClientStreamingServer"
			if v.Stream {
				kind = "BidiStreamingServer"
			}
			g.P("func (s ", recv, ") ", v.Rpc, "(out ", pkgGrpc.Ident(kind), "[", root.Ident(v.In), ", ", root.Ident(v.Out), "]) error {")
			g.P("	return s.", svc, ".", v.Rpc, "(out)")
		case v.Stream:
			g.P("func (s ", recv, ") ", v.Rpc, "(req *", root.Ident(v.In), ", out ", pkgGrpc.Ident("ServerStreamingServer"), "[", root.Ident(v.Out), "]) error {")
			g.P("	return s.", svc, ".", v.Rpc, "(req, out)")
		default:
			g.P("func (s ", recv, ") ", v.Rpc, "(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(v.In), ") (*", root.Ident(v.Out), ", error) {")
			g.P("	return s.", svc, ".", v.Rpc, "(ctx, req)")
		}
		g.P("}")
		g.P("")
	}
}

// checkShadowed refuses an RPC called what a type the generators write for
// its service holds as a field: that cannot be forwarded, since a type cannot
// have a method and a field of one name, and the field is what the type's own
// code reads.
//
// What can be forwarded is, by [shadowed], and is not refused. What is left is
// a short list of names nobody is likely to mean -- but an app that means one
// is told which, by name, rather than handed a server that does not compile.
func (s *Schema) checkShadowed() error {
	var errs []string
	for _, v := range s.rpcs {
		e := s.byGoName(v.Entity)
		if e == nil {
			continue
		}

		for _, w := range s.fieldsBeside(e) {
			if v.Rpc != w.name {
				continue
			}

			errs = append(errs, fmt.Sprintf("%sService/%s is called what %s holds as a field, and a method of that name cannot be written beside it; name the RPC otherwise",
				v.Entity, v.Rpc, w.of))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}

	return nil
}

type besideField struct {
	name string
	of   string
}

// fieldsBeside is every exported field of every type the generators write
// around `e`'s service, with what holds it.
func (s *Schema) fieldsBeside(e *Entity) []besideField {
	name := e.GoName()
	svc := name + "ServiceServer"

	vs := []besideField{
		// Every type around a service holds it as a field: the bare server's
		// is the unimplemented one, and the layers' is the service itself.
		{svc, "every layer's type around the service"},
		{"Store", "bare." + svc},
		{"Unimplemented" + svc, "bare." + svc},
	}
	for _, f := range shadow.BareFields {
		vs = append(vs, besideField{f, "bare." + svc + " (from bare.Store)"})
	}
	for _, layer := range s.layersAround(e) {
		vs = append(vs, besideField{layer, strings.ToLower(layer[:1]) + layer[1:] + name})
	}

	return vs
}

// layersAround is every layer that writes a type embedding its struct beside
// `e`'s service, read off the same conditions the layers are written under.
func (s *Schema) layersAround(e *Entity) []string {
	var vs []string

	if s.Has(pdpb.Own_OWN_TENANT) && s.Has(pdpb.Own_OWN_HOLDER) {
		if e.IsTenant || e.Own == pdpb.Own_OWN_HOLDER {
			vs = append(vs, "Gate")
		} else if _, _, _, ok := s.admitted(e); ok {
			vs = append(vs, "Gate")
		}
	}

	if s.Has(pdpb.Own_OWN_AUDIT) {
		ev, fact := pastGlued(s)
		if e.Own == pdpb.Own_OWN_AUDIT || (ev != nil && (e == ev || e == fact)) {
			vs = append(vs, "Audit")
		}
	}

	// The intercept writes a type for every entity with an RPC it wraps, and
	// it wraps every one but a client stream.
	if slices.ContainsFunc(s.rpcs, func(v interceptRpc) bool { return v.Entity == e.GoName() && !v.Client }) {
		vs = append(vs, "Intercept")
	}

	if s.secretWraps(e) {
		vs = append(vs, "Secret")
	}

	return vs
}

func (s *Schema) byGoName(name string) *Entity {
	for _, v := range s.Entities {
		if v.GoName() == name {
			return v
		}
	}

	return nil
}
