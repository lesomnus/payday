// Package shadow is what the generator knows of the names a layer's struct
// answers to, apart from the generator so that the test app -- which has the
// structs, built -- can hold it to them without depending on the generator.
//
// A layer writes, for each entity it says something about, a type embedding
// its struct beside the entity's service. A name both answer to at one depth
// is promoted from neither, so an RPC of such a name is written on the type by
// the generator; it can only do that for a name it knows. See `shadowed` in
// the generator.
package shadow

import "slices"

// overlayNames is what every layer's struct is given by embedding the app's
// `Overlay`: the field, the server the field holds, and the method answering
// it. The server's own methods are the entity accessors, which [Names]
// adds from the schema.
var overlayNames = []string{"Overlay", "Server", "Next"}

// layerOwn is, of each layer whose struct is embedded beside a service, every
// exported method the generator writes on the struct that is not an entity's
// accessor. A layer that grows one adds it here, or
// `TestALayerAnswersToNothingTheGeneratorDoesNotKnow` (in the test app) fails:
// a name missing here is an RPC of that name that does not compile.
var layerOwn = map[string][]string{
	"Gate":      {"WithDriver"},
	"Audit":     {"WithDriver"},
	"Intercept": {"WithDriver"},
	"Secret":    {"WithDriver"},
}

// Layers is every layer whose struct the generator embeds beside an entity's
// service, in a stable order.
func Layers() []string {
	vs := make([]string, 0, len(layerOwn))
	for k := range layerOwn {
		vs = append(vs, k)
	}
	slices.Sort(vs)

	return vs
}

// Names is every exported name the struct of `layer` answers to -- its
// fields and its methods, its own and promoted -- in an app whose entities
// are `entities`. It is the whole set and not only the names that collide
// today: a name the struct holds deeper than the service is promoted from the
// service and does not collide, and a forwarding method for it is what
// promotion already did, so knowing the whole set costs nothing and is the
// set a test can hold the generator to.
func Names(layer string, entities []string) []string {
	own, ok := layerOwn[layer]
	if !ok {
		return nil
	}

	vs := slices.Concat(overlayNames, own, entities)
	slices.Sort(vs)

	return slices.Compact(vs)
}

// BareFields is every exported field of the ORM generator's `bare.Store`,
// which its `bare.<E>ServiceServer` embeds beside the service's
// `Unimplemented<E>ServiceServer`. An RPC called one of these is ambiguous
// there, and the bare server is not this generator's to write a method on --
// its own methods read `s.Db` and `s.Scope`, so a method of that name would
// take the field from them. `TestTheBareServerAnswersToNothingTheGeneratorDoesNotKnow`
// (in the test app) holds this to the struct.
var BareFields = []string{"Db", "Mint", "Now", "Rec", "Scope"}
