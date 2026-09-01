package auth

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"
)

// ParseName reads whoever a credential names, written one of the two ways
// anything else names a row.
//
//	@acme/admin                            the alias, within the alias of its tenant
//	0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a   the identifier
//	0199c3f42a108abc8a039f2e1c4d5b6a       the identifier, as its bytes
//
// It fills the part of an [Identity] that says who and nothing more:
// [Identity.Method] and [Identity.Grant] belong to whichever handler read the
// credential, since only it knows where the claim came from and whether that
// place had anywhere to carry an attenuation.
//
// Every handler that carries a name reads it with this -- what a caller writes
// in a header and what a certificate says are the same name, so they are read
// by the same code and go wrong in the same way.
//
// # Why the header form changed
//
// The form this came from wrote `acme/admin`, with no mark, and told the two
// apart by looking at them: a "/" meant a name, and anything else was tried as
// a Uuid and then as hex. That is guessing from the shape of what was written,
// and [slug] holds the reason the shape cannot be trusted -- an alias is
// lowercase letters, digits and hyphens, and so is a Uuid, so
// "abcd1234-2a10-8abc-8a03-9f2e1c4d5b6a" is a legal alias. That the
// identifiers this app makes happen to begin with a digit today is an accident
// of the clock and not a rule anybody wrote down.
//
// The "@" is the rule, and [slug.Is] is the one question this asks. It costs a
// character in a header and in a certificate, and it buys a field that takes
// both kinds of reference without either side guessing which it was handed.
func ParseName(v string) (Identity, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return Identity{}, errors.New("says nothing")
	}

	if slug.Is(v) {
		return parseSlug(v)
	}

	// An identifier, written either as a Uuid or as the bytes of one. Both go
	// through [pdid.Parse], which refuses a Uuid that is not one of ours --
	// and a caller naming one of those is naming a row this app never wrote.
	if id, err := pdid.Parse(v); err == nil {
		return Identity{Id: id.String()}, nil
	}
	if b, err := hex.DecodeString(v); err == nil {
		if id, err := pdid.From(b); err == nil {
			return Identity{Id: id.String()}, nil
		}
	}

	return Identity{}, fmt.Errorf(`%q is neither an identifier nor a name; a name is written "@tenant/alias"`, v)
}

func parseSlug(v string) (Identity, error) {
	s, err := slug.Parse(v)
	if err != nil {
		// The reason in words rather than the error itself. A slug refuses
		// with InvalidArgument, which is the right answer where a slug is a
		// field of a request and the wrong one here: a header that is not a
		// name is a credential that is not a credential, and the caller has to
		// go and get one that is. See [statusOf].
		return Identity{}, errors.New(err.Error())
	}
	if !s.HasTenant() {
		// A slug may leave the tenant to whoever reads it, and here there is
		// nobody to read it from -- working out which tenant the caller is of
		// is what this is for, and it is the first thing a request has. An
		// alias on its own names one row in every tenant there is.
		return Identity{}, fmt.Errorf("%s: names an alias and no tenant", s)
	}
	if s.HasDomain() {
		// A slug may say what kind of thing it names, and there is nothing
		// here to check that against: an [Identity] carries the tenant and the
		// alias and not the assertion, so a "#robot" written in a credential
		// would be dropped rather than disagreed with -- and a dropped
		// assertion is exactly the silence [slug.Slug.Expect] exists to
		// prevent. A credential says who, and only ever who.
		//
		// An identifier that names the wrong kind of thing is a different
		// matter and is let through: the domain is one of its bytes and
		// travels with it, so whoever looks it up still has it to disagree
		// with.
		return Identity{}, fmt.Errorf("%s: a credential says who is calling, not what kind of thing they are", s)
	}

	return Identity{Tenant: s.Tenant(), Alias: s.Alias()}, nil
}

// Name spells an identity the way [ParseName] reads it, which is what a
// [Provider] writes and what a test keys a table by.
//
// An identity that names both spells the identifier: that is the one that
// names a row, where an alias is a name a row was given and may be given to
// another after it is gone.
//
// An identity that names nobody spells nothing, rather than something that
// would read back as somebody else.
func (v Identity) Name() string {
	if v.Id != "" {
		return v.Id
	}
	if v.Tenant == "" || v.Alias == "" {
		return ""
	}

	return "@" + v.Tenant + "/" + v.Alias
}
