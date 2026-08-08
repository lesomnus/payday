package slug

import (
	"context"
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
	// Whatever it answers with is **not** checked again. A namer that folds
	// differently or allows something [Validate] does not is a namer that
	// meant to, and a second pass would only make it impossible.
	Name(ctx context.Context, entity string, given string) (string, error)
}

// NamerFunc is a [Namer] written as a function, which is what a policy that is
// neither [Names] nor [Required] looks like:
//
//	slug.NamerFunc(func(ctx context.Context, entity string, given string) (string, error) {
//		if entity == "app.Project" && given == "" {
//			return "", errors.New("a project has to be named")
//		}
//
//		return slug.Names().Name(ctx, entity, given)
//	})
type NamerFunc func(ctx context.Context, entity string, given string) (string, error)

func (f NamerFunc) Name(ctx context.Context, entity string, given string) (string, error) {
	return f(ctx, entity, given)
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

func (names) Name(_ context.Context, _ string, given string) (string, error) {
	if given == "" {
		return RandomAlias(), nil
	}

	return ParseAlias(given)
}

// Required answers with a namer that refuses a row nobody named, rather than
// making a name up for it.
//
// It is for an entity whose name is the point: a tenant, a project, anything a
// person writes in a URL or says out loud. The thing it buys is not safety --
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

func (required) Name(_ context.Context, _ string, given string) (string, error) {
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

	return NamerFunc(func(ctx context.Context, entity string, given string) (string, error) {
		if must[entity] {
			return req.Name(ctx, entity, given)
		}

		return next.Name(ctx, entity, given)
	})
}

// NameWith is what the generated servers call, so that the answer to a nil
// [Namer] is written once rather than at every site.
func NameWith(ctx context.Context, n Namer, entity string, given string) (string, error) {
	if n == nil {
		n = Names()
	}

	return n.Name(ctx, entity, given)
}
