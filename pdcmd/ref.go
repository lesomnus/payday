package pdcmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"
)

// Ref is how a person names one row on a command line.
//
// Either an identifier, or a [slug]:
//
//	019ff7c9-8a1e-8c3d-9f02-2b6c1f0a4d51
//	@acme/alice
//	@alice
//	@acme/alice#holder
//
// The tenant is part of the name and not a flag, because an alias without one
// names somebody in every tenant and nobody in particular. Where it may be left
// off is where the entity is not inside a tenant -- a Tenant itself -- and
// where the server can supply it, which is what an anchored credential does.
//
// The slug half is read by [slug.Parse] rather than by a second parser here,
// so what a command accepts and what a header accepts are the same text going
// wrong the same way: the parts are normalized the way the database normalized
// them on the way in -- "@ACME / Alice" names what "@acme/alice" names -- and
// the "#kind" is read as the assertion slug says it is. This package is where
// the assertion gets *checked*: every command knows which entity it is about,
// so [Ref.Expect] runs where slug's own readers have nothing to check against.
type Ref struct {
	Id pdid.Id

	Tenant string
	Alias  string

	// Domain is what the writer said this names, and [pdid.Unknown] when they
	// said nothing. It is a claim until [Ref.Expect] has been given something
	// to check it against -- see [slug.Slug.Domain], which is where it came
	// from.
	Domain pdid.Domain
}

// Expect checks this reference against the kind of thing the command is about.
//
// Both written forms carry a kind, and both are claims. A slug says it out
// loud with "#holder"; an identifier says it in its ninth byte, put there by
// the mint. Checking them here is what turns a wrong-kind reference from a
// silent not-found -- the server narrows to rows of its own entity, finds
// nothing, and is right -- into a refusal that names the mistake.
//
// A reference that said nothing passes: `@acme/alice` on `holder get` claims
// nothing, so there is nothing to check. And a `d` of [pdid.Unknown] checks
// nothing, because it means the caller has no expectation to check against --
// which is [Tree.Unary] wiring a method whose entity it could not resolve, not
// an entity without a kind.
func (r Ref) Expect(d pdid.Domain) error {
	if d == pdid.Unknown {
		return nil
	}

	if !r.Id.IsZero() {
		if got := r.Id.Domain(); got != d {
			return &pdid.DomainError{Want: d, Got: got}
		}

		return nil
	}

	if r.Domain != pdid.Unknown && r.Domain != d {
		s, err := slug.New(r.Tenant, r.Alias, r.Domain)
		if err != nil {
			// A Ref built in code from parts that are not a slug. The check is
			// still the answer; only the pretty-printed name is not available.
			return &pdid.DomainError{Want: d, Got: r.Domain}
		}

		return &slug.DomainError{Slug: s, Want: d, Got: r.Domain}
	}

	return nil
}

// Fill writes this reference into a generated `XxxRef`.
//
// # Why by field name rather than by Go type
//
// There are three shapes of `Ref` in a payday app and an app has all three:
//
//	CellRef   { oneof key { bytes id = 1 } }                  -- no alias at all
//	TenantRef { oneof key { bytes id = 1; string alias = 4 } } -- the wall itself
//	RobotRef  { oneof key { bytes id = 1; RobotRefBySlug slug = 4 } }
//
// Which one an entity has is decided by the schema -- whether it carries an
// alias, and whether it sits inside a tenant -- so a command that works for
// every entity cannot name a Go type. It reads the field that is there.
//
// The field numbers are fixed by payday's field-number rule (1 is the key, 2 is
// the tenant, 4 is the alias) but this matches on **names**, because a name is
// what the rule is written in and a number that moved would be a schema change
// that should fail loudly here rather than fill in the wrong field.
func (r Ref) Fill(m protoreflect.Message) error {
	fs := m.Descriptor().Fields()

	if !r.Id.IsZero() {
		fd := fs.ByName("id")
		if fd == nil {
			return fmt.Errorf("%s: cannot be named by identifier", m.Descriptor().FullName())
		}

		m.Set(fd, protoreflect.ValueOfBytes(r.Id.Bytes()))
		return nil
	}

	if r.Alias == "" {
		return errors.New("neither an identifier nor an alias")
	}

	// The wall itself: a tenant's alias is unique on its own, so its `Ref`
	// carries the string rather than a message holding one.
	if fd := fs.ByName("alias"); fd != nil && fd.Kind() == protoreflect.StringKind {
		if r.Tenant != "" {
			return fmt.Errorf("%s: is not inside a tenant, so @%s/ names nothing",
				m.Descriptor().FullName(), r.Tenant)
		}

		m.Set(fd, protoreflect.ValueOfString(r.Alias))
		return nil
	}

	fd := fs.ByName("slug")
	if fd == nil || fd.Kind() != protoreflect.MessageKind {
		return fmt.Errorf("%s: cannot be named by alias", m.Descriptor().FullName())
	}

	slug := m.NewField(fd).Message()
	sfs := slug.Descriptor().Fields()

	afd := sfs.ByName("alias")
	if afd == nil {
		return fmt.Errorf("%s: has no alias", slug.Descriptor().FullName())
	}
	slug.Set(afd, protoreflect.ValueOfString(r.Alias))

	if r.Tenant != "" {
		tfd := sfs.ByName("tenant")
		if tfd == nil || tfd.Kind() != protoreflect.MessageKind {
			return fmt.Errorf("%s: has no tenant to name", slug.Descriptor().FullName())
		}

		// The tenant of a slug is itself a `TenantRef`, so it is named the same
		// two ways -- and here it is always by alias, because that is what was
		// written before the slash. A caller with the tenant's identifier gives
		// the row's identifier instead.
		tenant := slug.NewField(tfd).Message()
		tafd := tenant.Descriptor().Fields().ByName("alias")
		if tafd == nil {
			return fmt.Errorf("%s: cannot be named by alias", tenant.Descriptor().FullName())
		}

		tenant.Set(tafd, protoreflect.ValueOfString(r.Tenant))
		slug.Set(tfd, protoreflect.ValueOfMessage(tenant))
	}

	m.Set(fd, protoreflect.ValueOfMessage(slug))
	return nil
}

// FlgRef and ArgRef are a [Ref] on a command line.
type FlgRef = flg.Base[Ref, RefParser]
type ArgRef = arg.Mono[Ref, RefParser]

type RefParser struct{}

func (RefParser) Parse(s string) (Ref, error) {
	if s == "" {
		return Ref{}, errors.New("empty")
	}

	if !slug.Is(s) {
		// Not a slug, so it has to be an identifier -- and it is parsed here
		// rather than passed along as bytes so that a mistyped uuid is refused
		// by the command instead of by the server, which cannot say which
		// argument it was.
		id, err := pdid.Parse(s)
		if err != nil {
			return Ref{}, fmt.Errorf("not an identifier and not an @alias: %w", err)
		}

		return Ref{Id: id}, nil
	}

	// The "@" is required here where [slug.Parse] leaves it optional, because
	// this is the one field that takes both kinds of reference -- the mark is
	// how the two are told apart, which is the job the slug package gave it.
	v, err := slug.Parse(s)
	if err != nil {
		return Ref{}, err
	}

	return Ref{Tenant: v.Tenant(), Alias: v.Alias(), Domain: v.Domain()}, nil
}

func (RefParser) ToString(v Ref) string {
	if !v.Id.IsZero() {
		return v.Id.String()
	}

	var b strings.Builder
	b.WriteByte('@')
	if v.Tenant != "" {
		b.WriteString(v.Tenant)
		b.WriteByte('/')
	}
	b.WriteString(v.Alias)
	if v.Domain != pdid.Unknown {
		b.WriteByte('#')
		b.WriteString(v.Domain.String())
	}

	return b.String()
}

func (RefParser) String() string {
	return "Id|@[TENANT/]ALIAS[#KIND]"
}
