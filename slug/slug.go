// Package slug is the human-readable way to name a row.
//
//	@[TENANT/]ALIAS[#DOMAIN]
//
//	@acme/arm-01#robot   the robot "arm-01", held by the tenant "acme"
//	@acme/arm-01         what kind of thing it is comes from where it is written
//	@acme#tenant         a tenant, which nothing is above
//	arm-01               the tenant comes from who is asking
//
// A slug is what a person writes: an authorization header, a key in a config
// file, a URI in a certificate, a line in a log, an argument to the CLI. The
// wire does not carry one -- a reference there is a structured message that the
// server already holds as a type, and reading text back into it on every
// request would be parsing what nothing had to serialize.
//
// # Why the "@"
//
// It is not decoration. A field that has to take "an identifier or a name" can
// be handed both, and the two written forms overlap: an alias is lowercase
// letters, digits and hyphens, and so is a UUID.
// "abcd1234-2a10-8abc-8a03-9f2e1c4d5b6a" breaks none of the rules in
// [Validate]. That the identifiers this app makes happen to begin with a zero
// digit today is an accident of the clock -- a millisecond timestamp will not
// reach a hex letter for thousands of years -- and not a rule anybody wrote
// down. The "@" is the rule, and [Is] is how a field asks which of the two it
// was given.
//
// # Why the domain is an assertion
//
// The word after the "#" is the same domain an identifier carries in its ninth
// byte, and it comes out of the same declaration in the schema; see
// [pdid.Domain]. It is optional, because most places that hold a slug already
// know what kind of thing belongs there, and when it is written it is *checked*
// ([Slug.Expect]) rather than obeyed.
//
// Obeying it would be the cheaper thing and it costs a class of silence: a slug
// carrying the wrong word would name a different kind of row, and the
// disagreement that could have caught it is exactly what was thrown away.
package slug

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
)

// ErrNoSuchDomain is a "#word" this deployment has no domain for, so there is
// nothing to check the assertion against.
var ErrNoSuchDomain = errors.New("no domain goes by that name")

// Slug is a name for one row.
//
// It holds the three parts rather than the text they were written as. A string
// type -- which is what this was before -- is a name anything can be cast into,
// so a function taking one has been told nothing; and every accessor has to
// split the text again, which is a parser running wherever a caller happened to
// ask a question. Here, holding one is proof that it parsed, and the only way
// to make one is to say what the parts are.
//
// The zero value names nothing. It is comparable, so two slugs naming the same
// row are equal whatever they were typed as -- see [ParseAlias].
type Slug struct {
	// tenant is the alias of who holds this, or empty when the writer left it
	// to the reader.
	tenant string
	// alias is the name itself, and the one part that is never optional.
	alias string
	// domain is what the writer said this names, and [pdid.Unknown] when they
	// said nothing. Nothing may register as Unknown -- see [pdid.Register] --
	// so "no domain" cannot be confused with a real one.
	domain pdid.Domain
}

// New answers with the slug naming `alias`, held by `tenant`, of domain `d`.
//
// An empty tenant and a domain of [pdid.Unknown] are how a caller says that
// part is left to whoever reads it. Both parts are normalized as [ParseAlias]
// normalizes, so a slug built from what a person typed is the same value as one
// built from what the database holds.
func New(tenant, alias string, d pdid.Domain) (Slug, error) {
	v := Slug{domain: d}
	if tenant != "" {
		t, err := parseAlias(partTenant, tenant)
		if err != nil {
			return Slug{}, err
		}
		v.tenant = t
	}

	a, err := parseAlias(partAlias, alias)
	if err != nil {
		return Slug{}, err
	}
	v.alias = a

	return v, nil
}

// Parse reads a slug out of its written form.
//
// The leading "@" is optional here and certain in [Slug.String]: a slug arrives
// from places that have already decided the field is a name -- a config key, a
// CLI argument -- and demanding the mark there would be demanding it of
// somebody who has nothing to disambiguate. It is written back because whatever
// reads the output next may not be so sure; see [Is].
//
// Each part is normalized on its own, so "@ACME / Arm-01" and "@acme/arm-01"
// are the same slug for the same reason "  Acme " and "acme" are the same
// alias. What is refused is a part that is missing rather than untidy: "/admin"
// and "acme/" and "@admin#" are somebody's typo, and reading them as though the
// missing part had been left out on purpose would name a row nobody asked for.
func Parse(s string) (Slug, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "@")

	var v Slug
	body, name, hasDomain := strings.Cut(s, "#")
	if hasDomain {
		name = strings.ToLower(strings.TrimSpace(name))

		d, ok := pdid.DomainOf(name)
		if !ok {
			return Slug{}, &NoSuchDomainError{Name: name}
		}
		v.domain = d
	}

	alias := body
	if tenant, rest, ok := strings.Cut(body, "/"); ok {
		t, err := parseAlias(partTenant, tenant)
		if err != nil {
			return Slug{}, err
		}

		v.tenant = t
		alias = rest
	}

	a, err := parseAlias(partAlias, alias)
	if err != nil {
		return Slug{}, err
	}
	v.alias = a

	return v, nil
}

// MustParse is [Parse] for a literal written in the source, where a bad one is
// a mistake rather than a request.
func MustParse(s string) Slug {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}

	return v
}

// Is reports whether `s` is written as a slug rather than as an identifier.
//
// This is the whole job of the "@": one field, two kinds of reference, and a
// mark that says which without either side guessing from the shape of what
// follows. See the package comment for why the shape cannot be trusted.
func Is(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "@")
}

// Tenant is the alias of who holds this, and empty when the slug left that to
// whoever reads it.
func (v Slug) Tenant() string { return v.tenant }

// Alias is the name itself, without the tenant it is under or the kind of thing
// it is.
func (v Slug) Alias() string { return v.alias }

// Domain is what this slug says it names, and [pdid.Unknown] when it says
// nothing. It is a claim until [Slug.Expect] has been given something to check
// it against.
func (v Slug) Domain() pdid.Domain { return v.domain }

func (v Slug) HasTenant() bool { return v.tenant != "" }

func (v Slug) HasDomain() bool { return v.domain != pdid.Unknown }

// IsZero reports whether this slug names nothing, which is what a field nobody
// filled in holds.
func (v Slug) IsZero() bool { return v == Slug{} }

// String writes this slug out, with the "@" that [Parse] does not insist on.
//
// Reading the output back answers with an equal slug. That was not true of the
// form this came from, where parsing dropped the "@" and printing never put it
// back, so a value that went through both came out as a different string than
// it went in as -- which is a difference that shows up as a cache key that
// misses or a config file that stops matching itself.
//
// A domain this deployment never registered prints as its number, which is what
// [pdid.Domain.String] answers, and such a slug does not read back. That is the
// honest answer rather than a lossy one: this deployment cannot say what kind
// of thing "domain(200)" is, so it cannot check anything written about it.
func (v Slug) String() string {
	if v.alias == "" {
		return ""
	}

	var b strings.Builder
	b.WriteByte('@')
	if v.tenant != "" {
		b.WriteString(v.tenant)
		b.WriteByte('/')
	}
	b.WriteString(v.alias)
	if v.domain != pdid.Unknown {
		b.WriteByte('#')
		b.WriteString(v.domain.String())
	}

	return b.String()
}

// WithTenant answers with this slug held by `tenant`, or with no tenant at all
// when `tenant` is empty.
//
// It takes the alias of the tenant and not another slug. Taking a slug is what
// the form this came from did, and it read the *tenant* of what it was handed,
// so `MustParse("admin").WithTenant(MustParse("acme"))` answered with "admin" --
// unchanged, and quietly, since "acme" names a tenant without being under one.
func (v Slug) WithTenant(tenant string) (Slug, error) {
	return New(tenant, v.alias, v.domain)
}

// WithDomain answers with this slug saying it names a `d`.
//
// It is a claim being written, not a row being renamed: what it is for is
// putting the domain a reader knows from context onto a slug that arrived
// without one, so that the whole thing can be printed. Use [Slug.Expect] to go
// the other way and check a claim that was already made.
func (v Slug) WithDomain(d pdid.Domain) Slug {
	v.domain = d
	return v
}

// Expect answers with this slug read where a `d` was expected.
//
// A slug that said nothing takes `d` from where it was read, which is why the
// domain is optional at all. A slug that said something else is **refused**,
// with the same error a reference of the wrong kind is refused with -- it
// matches [pdid.ErrDomain] and answers InvalidArgument -- because the two are
// the same mistake written two ways, and a caller should not have to learn
// which one they made.
func (v Slug) Expect(d pdid.Domain) (Slug, error) {
	if v.domain == pdid.Unknown {
		v.domain = d
		return v, nil
	}
	if v.domain != d {
		return Slug{}, &DomainError{Slug: v, Want: d, Got: v.domain}
	}

	return v, nil
}

// MarshalText makes a slug readable by anything that reads text: a YAML key, a
// JSON field, a flag. Those are where slugs live, so the alternative is every
// one of those places knowing to call [Parse] -- and the one that forgets holds
// a string that was never checked.
func (v Slug) MarshalText() ([]byte, error) {
	return []byte(v.String()), nil
}

// UnmarshalText reads a slug that was written into a document.
//
// An empty field answers with the zero slug rather than an error, which is the
// one place empty is not a mistake: a field nobody filled in is absent, and
// absent is what the zero value means. [Parse] still refuses it, because a
// caller who reached for Parse had a string they believed was a name.
func (v *Slug) UnmarshalText(b []byte) error {
	if strings.TrimSpace(string(b)) == "" {
		*v = Slug{}
		return nil
	}

	u, err := Parse(string(b))
	if err != nil {
		return err
	}

	*v = u
	return nil
}

// DomainError is a slug that says it names one kind of thing where another was
// expected.
//
// It matches [pdid.ErrDomain] rather than carrying a sentinel of its own: a
// handler that wants to know "was I handed the wrong kind of thing" is asking
// one question, and whether the caller wrote it as text or as sixteen bytes is
// not part of it.
type DomainError struct {
	Slug      Slug
	Want, Got pdid.Domain
}

func (e *DomainError) Error() string {
	return fmt.Sprintf("slug: %s names a %s, and this is a %s", e.Slug, e.Got, e.Want)
}

func (e *DomainError) Is(target error) bool { return target == pdid.ErrDomain }

func (e *DomainError) GRPCStatus() *status.Status {
	return status.New(codes.InvalidArgument, e.Error())
}

// NoSuchDomainError is a "#word" naming a kind of thing this deployment has no
// domain for.
//
// It is refused rather than read as "no domain was said", which is what the
// form this came from did -- its lookup answered with the unknown domain for
// anything it did not recognize, so "@acme/admin#robto" was a slug with no
// assertion in it and the check that would have caught the typo never ran.
//
// The message says what this app does have, since a caller who wrote the wrong
// word usually wants the list of right ones.
type NoSuchDomainError struct {
	Name string
}

func (e *NoSuchDomainError) Error() string {
	if e.Name == "" {
		return `slug: the "#" says a kind of thing follows, and none does`
	}

	vs := slices.Sorted(maps.Values(pdid.Domains()))
	if len(vs) == 0 {
		return fmt.Sprintf("slug: nothing here is a %q; this app has declared no kinds at all", e.Name)
	}

	return fmt.Sprintf("slug: nothing here is a %q; it has %s", e.Name, strings.Join(vs, ", "))
}

func (e *NoSuchDomainError) Is(target error) bool { return target == ErrNoSuchDomain }

func (e *NoSuchDomainError) GRPCStatus() *status.Status {
	return status.New(codes.InvalidArgument, e.Error())
}
