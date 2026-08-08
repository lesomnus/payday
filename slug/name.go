package slug

import (
	"context"
)

// Namer decides the alias of a row that is being created.
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

// NamerFunc is a [Namer] written as a function, which is what a per-entity
// policy usually is:
//
//	slug.NamerFunc(func(ctx context.Context, entity string, given string) (string, error) {
//		if entity == "app.Joint" && given == "" {
//			given = slug.RandomAlias()
//		}
//
//		return slug.Names().Name(ctx, entity, given)
//	})
type NamerFunc func(ctx context.Context, entity string, given string) (string, error)

func (f NamerFunc) Name(ctx context.Context, entity string, given string) (string, error) {
	return f(ctx, entity, given)
}

// Names is what a server with no namer does: fold the name, and refuse one that
// is not a name.
//
// Folding is the half that is not a refusal and is the one that matters most:
// "  Acme " and "acme" are one row to a person and two to a unique index, and
// the only place that difference can be closed once is on the way in.
func Names() Namer { return names{} }

type names struct{}

func (names) Name(_ context.Context, _ string, given string) (string, error) {
	return ParseAlias(given)
}

// Generate answers with a namer that makes a name up when the request carried
// none, and otherwise does what `next` does.
//
// `next` may be nil, which is [Names].
//
// # What it costs, said plainly
//
// A caller who **forgot** the field gets a row called `qxbmrtz` and never finds
// out, because there is no way to tell that from a caller who meant it (see
// [Namer]). That is why it is not what a server does by default: it is a
// decision about one entity in one app, and it reads as one where it is wired.
//
// For an entity nobody names -- a joint of a robot, a cell of a fleet -- it is
// the right answer, and the alternative is every client carrying the same
// `if alias == ""`.
func Generate(next Namer) Namer {
	if next == nil {
		next = Names()
	}

	return NamerFunc(func(ctx context.Context, entity string, given string) (string, error) {
		if given == "" {
			given = RandomAlias()
		}

		return next.Name(ctx, entity, given)
	})
}

// GenerateFor is [Generate] for some entities and `next` for the rest.
//
// It is the shape a schema with a mixture has, and it is here rather than left
// to a switch because the switch is the thing people get wrong -- a name in the
// list that is not an entity is a policy that silently never applies.
//
//	pd.WithNamer(slug.GenerateFor(nil, "app.Joint", "app.Cell"))
func GenerateFor(next Namer, entities ...string) Namer {
	if next == nil {
		next = Names()
	}

	auto := make(map[string]bool, len(entities))
	for _, v := range entities {
		auto[v] = true
	}

	gen := Generate(next)

	return NamerFunc(func(ctx context.Context, entity string, given string) (string, error) {
		if auto[entity] {
			return gen.Name(ctx, entity, given)
		}

		return next.Name(ctx, entity, given)
	})
}

// NameWith is what the generated servers call, so that the answer to a nil
// [Namer] is written once rather than at every site.
func NameWith(ctx context.Context, n Namer, entity string, given string) (string, error) {
	if n == nil {
		return ParseAlias(given)
	}

	return n.Name(ctx, entity, given)
}
