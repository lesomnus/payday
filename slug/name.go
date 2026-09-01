package slug

import (
	"context"

	"google.golang.org/protobuf/proto"
)

// Namer decides the alias of a row that is being created.
//
// Unset is [Names]: fold what was given, and make a name up when nothing was.
// [Required] is the other way, per entity with [RequiredFor].
//
// # Why one method rather than one per entity
//
// The same reason `bare.Minter` has one: an alias is a string whatever entity
// it belongs to, so which entity it is can be an argument. A `Scope` needs a
// method per entity because a predicate is typed per entity and there is no
// way around it; this has no such problem, and a hook with one method is a hook
// an app implements once.
//
// # Why there is no "was it given"
//
// `Minter` is told, because a zero identifier is a legal-looking value and
// "made one up" has to be told apart from "was handed the zero one". An alias
// cannot be: the field has implicit presence, so an empty string **is** the
// absence and there is nothing else to report.
//
// That is worth knowing before writing one. A namer cannot answer differently
// for "the caller left it out" and "the caller sent an empty string", because
// on the wire those are the same request.
//
// # It is asked on Add and not on Patch
//
// Again for the reason a minter is: it decides the name of a row being made. A
// patch that cleared the alias is a caller asking for something invalid, and
// answering that with an invented name would be handing them a row they did not
// ask for. A patch that mentions the alias is folded and checked and nothing
// else.
type Namer interface {
	// Name answers with the alias to store, or with why the given one cannot
	// be a name.
	//
	// `entity` is the full protobuf name -- "app.Robot" -- which is what makes
	// a per-entity policy something a caller writes rather than something this
	// interface has to have a shape for.
	//
	// `req` is the whole Add request, so that a name can be derived from the
	// row rather than invented beside it. Without it the only policies
	// expressible are "refuse" and "make one up at random": an app that wants
	// `Acme Corporation` to become `acme-corporation` has to see the name, and
	// `header.Of(req)` is how -- an Add request carries the same header fields
	// its entity does. It may be nil where there is no request.
	//
	// Whatever it answers with is **not** checked again. A namer that folds
	// differently or allows something [Validate] does not is a namer that
	// meant to, and a second pass would only make it impossible.
	Name(ctx context.Context, entity string, given string, req proto.Message) (string, error)
}

// NamerFunc is a [Namer] written as a function, which is what a policy that is
// neither [Names] nor [Required] looks like:
//
//	slug.NamerFunc(func(ctx context.Context, entity string, given string, req proto.Message) (string, error) {
//		if given == "" {
//			// A name from the row rather than beside it.
//			if v := header.Of(req).Name; v != "" {
//				return slug.Slugify(v), nil
//			}
//		}
//
//		return slug.Names().Name(ctx, entity, given, req)
//	})
type NamerFunc func(ctx context.Context, entity string, given string, req proto.Message) (string, error)

func (f NamerFunc) Name(ctx context.Context, entity string, given string, req proto.Message) (string, error) {
	return f(ctx, entity, given, req)
}

// Names is what a server with no namer does: fold the name, and make one up
// when the request carried none.
//
// # Why making one up is the default
//
// The same reason `bare.Minter` makes a key up. A row needs an identity the
// caller may not have an opinion about, and the framework already supplies one
// of the two -- an Add with no `id` gets a fresh uuid and nobody calls that
// dangerous. A name is that decision one field over.
//
// The argument against it does not survive payday's own test for what is worth
// refusing, which is **does it go quietly wrong.** A row called `qxbmrtz` is not
// quiet: it is on the first screen that draws a name, in the first log line that
// mentions it. Compare the things generation does refuse -- an entity with no
// domain, one that never said which side of the wall it is on, a watch with
// nothing to order two answers by. Every one of those is invisible until much
// later and somewhere else.
//
// So an entity nobody names -- a joint of a robot, a cell of a fleet -- costs
// nothing, and the alternative was every client carrying the same
// `if alias == ""`.
//
// # What it does not do
//
// Folding is the other half and is the one that matters most: "  Acme " and
// "acme" are one row to a person and two to a unique index, and the only place
// that difference can be closed once is on the way in.
//
// And a name that is **given** and is not a name is still refused. Making one
// up is what happens when nothing was said, not a repair of what was.
func Names() Namer { return names{} }

type names struct{}

func (names) Name(_ context.Context, _ string, given string, _ proto.Message) (string, error) {
	if given == "" {
		return RandomAlias(), nil
	}

	return ParseAlias(given)
}

// Required answers with a namer that refuses a row nobody named, rather than
// making a name up for it.
//
// It is for an entity whose name is the point: a tenant, a project, anything a
// person writes in a Url or says out loud. The thing it buys is not safety --
// see [Names] -- it is **feedback**: a client that dropped the field is told
// so, instead of writing a row that has to be found and renamed.
//
// It also changes what a repeated mistake looks like. With a name made up, a
// client that drops the field writes a new row every time, since a made-up name
// never collides; with this, the second attempt is the same refusal as the
// first.
//
//	sink.WithNamer(slug.Required())
func Required() Namer { return required{} }

type required struct{}

func (required) Name(_ context.Context, _ string, given string, _ proto.Message) (string, error) {
	return ParseAlias(given)
}

// RequiredFor is [Required] for some entities and `next` for the rest, which is
// the shape a schema with a mixture has.
//
// `next` may be nil, which is [Names].
//
// It is here rather than left to a switch because the switch is the thing
// people get wrong: a name in the list that is not an entity is a policy that
// silently never applies, and this at least keeps the list in one place where
// it can be read against the schema.
//
//	sink.WithNamer(slug.RequiredFor(nil, "payday.Tenant", "app.Project"))
func RequiredFor(next Namer, entities ...string) Namer {
	if next == nil {
		next = Names()
	}

	must := make(map[string]bool, len(entities))
	for _, v := range entities {
		must[v] = true
	}

	req := Required()

	return NamerFunc(func(ctx context.Context, entity string, given string, m proto.Message) (string, error) {
		if must[entity] {
			return req.Name(ctx, entity, given, m)
		}

		return next.Name(ctx, entity, given, m)
	})
}

// NameWith is what the generated servers call, so that the answer to a nil
// [Namer] is written once rather than at every site.
func NameWith(ctx context.Context, n Namer, entity string, given string, req proto.Message) (string, error) {
	if n == nil {
		n = Names()
	}

	return n.Name(ctx, entity, given, req)
}

// Tries is how many names a server makes up before it gives up.
//
// # Why there is a retry at all
//
// [Alphabet] to the seventh is about 3.4e9, and the index a name is unique in
// is one tenant's rows of one entity. A tenant holding a million auto-named
// rows collides on about one insert in 3,400 -- which arrives as
// `AlreadyExists` on a call where the caller never mentioned a name, and is
// exactly as confusing as that sounds.
//
// Three, because each try is independent: at one in 3,400 the second leaves one
// in 1e7 and the third one in 4e10. What it does **not** fix is a name space
// that is actually full -- that is not chance, it is exhaustion, and the answer
// to it is a longer name rather than more tries.
//
// # Why it does not need to know what collided
//
// It cannot: a collision on the name and a collision on the key are the same
// gRpc code, and telling them apart means reading the driver's own words --
// text on SQLite, a struct field on PostgreSql. A table of those is a table
// that is wrong on the next driver.
//
// It does not have to. **A retry changes only the name**, so it can only ever
// succeed if the name was what collided; a duplicate key fails all three times
// and the last refusal is what the caller gets, unaltered. Nothing real is
// masked, and the cost of guessing wrong is two wasted attempts.
//
// That is also why the refusal is passed through rather than rewritten. Saying
// "no free name after 3 tries" would be asserting the one thing this cannot
// know, and it would say it loudest exactly when it is wrong.
const Tries = 3
