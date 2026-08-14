package pdcmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdid"
)

// Ref is how a person names one row on a command line.
//
// Either an identifier, or an alias with the tenant it is unique inside. Both
// are written the way they are written everywhere else in payday:
//
//	019ff7c9-8a1e-7c3d-9f00-2b6c1f0a4d51
//	@acme/alice
//	@alice
//
// The tenant is part of the name and not a flag, because an alias without one
// names somebody in every tenant and nobody in particular. Where it may be left
// off is where the entity is not inside a tenant -- a Tenant itself -- and
// where the server can supply it, which is what an anchored credential does.
type Ref struct {
	Id pdid.Id

	Tenant string
	Alias  string
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

	if !strings.HasPrefix(s, "@") {
		// Not an alias, so it has to be an identifier -- and it is parsed here
		// rather than passed along as bytes so that a mistyped uuid is refused
		// by the command instead of by the server, which cannot say which
		// argument it was.
		id, err := pdid.Parse(s)
		if err != nil {
			return Ref{}, fmt.Errorf("not an identifier and not an @alias: %w", err)
		}

		return Ref{Id: id}, nil
	}

	name := s[1:]
	tenant, alias, ok := strings.Cut(name, "/")
	if !ok {
		return Ref{Alias: tenant}, nil
	}
	if tenant == "" || alias == "" {
		return Ref{}, errors.New("expected @TENANT/ALIAS")
	}

	return Ref{Tenant: tenant, Alias: alias}, nil
}

func (RefParser) ToString(v Ref) string {
	if !v.Id.IsZero() {
		return v.Id.String()
	}
	if v.Tenant != "" {
		return fmt.Sprintf("@%s/%s", v.Tenant, v.Alias)
	}

	return "@" + v.Alias
}

func (RefParser) String() string {
	return "ID|@[TENANT/]ALIAS"
}
