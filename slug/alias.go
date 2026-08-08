package slug

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pderr"
)

// AliasMaxLen is the longest an alias can be.
//
// Sixty-three is the length of a DNS label, and that is the point: an alias
// that fits one can be a hostname, a subdomain, a Kubernetes name, a certificate
// SAN. None of that is needed today and all of it is cheap to keep, whereas a
// limit raised later cannot be lowered -- by then rows are named.
const AliasMaxLen = 63

// Alphabet is what a name this package makes up is spelled with: the lowercase
// letters, less i, l and o. It is not what an alias may hold -- that is
// [Validate], and it is wider -- but what is chosen when nobody is choosing.
//
// Those three are gone because these strings are read aloud, written on paper
// and typed back in, and "l" for "1" is a row that is not found -- or worse, a
// row that is. Digits are gone with them: keeping "0" beside "o" or "1" beside
// "l" is the same confusion from the other side, and the letters alone are
// already 23^7 for a name of seven, which is more names than anything here will
// need.
//
// It being letters only has a second effect worth having. Every character in it
// is legal as the *first* character of an alias, so a generator never has a
// special case at position zero -- which is exactly where the form this came
// from went wrong, drawing the first character from a different modulus than
// the rest and never once producing a "z".
const Alphabet = "abcdefghjkmnpqrstuvwxyz"

// ErrAlias is a string that cannot be an alias.
var ErrAlias = errors.New("not an alias")

// aliasPattern is the merge of the two forms this came from: begins with a
// lowercase letter, and is groups of lowercase alphanumerics joined by single
// hyphens.
//
// Beginning with a letter is so that a name a person reads does not look like a
// number they can count with. The hyphen is the only joiner -- an underscore
// was allowed by one of the two, and allowing it closes the door on
// [AliasMaxLen]'s reason, since no DNS label may hold one.
var aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// Validate reports whether `v` is an alias, and which rule it broke if not.
//
// This is the bare grammar and nothing else: what a database column holds and
// what a [Slug] is made of. A string a person typed goes through [ParseAlias]
// first, which folds the difference between "  Acme " and "acme" before there
// is anything to judge.
func Validate(v string) error { return validate(partAlias, v) }

func validate(part string, v string) error {
	switch {
	case v == "":
		return &AliasError{Part: part, Why: "must not be empty"}
	case len(v) > AliasMaxLen:
		return &AliasError{Part: part, Why: fmt.Sprintf("must be at most %d characters", AliasMaxLen)}
	case !aliasPattern.MatchString(v):
		return &AliasError{Part: part, Why: `must begin with a lowercase letter and hold only lowercase letters, digits and single hyphens, such as "arm-01"`}
	}

	return nil
}

// ParseAlias normalizes `v` into an alias, or reports why it cannot be one.
//
// Surrounding spaces are dropped and the case is folded, so "  Acme " and
// "acme" name the same row. The folding is what makes it safe to compare
// aliases with "=" -- both in Go and in the database, where the column is
// whatever was stored -- and normalizing on the way in is the only place it can
// be done once.
func ParseAlias(v string) (string, error) {
	return parseAlias(partAlias, v)
}

// RandomAlias answers with an alias nobody chose, for a row that needs a name
// before anybody has an opinion about it.
//
// Seven characters of [Alphabet] is about 31 bits, which is enough that these
// do not collide in a table a person will ever read, and short enough to say
// over a phone.
func RandomAlias() string { return RandomAliasN(7) }

// RandomAliasN is [RandomAlias] of a given length.
//
// It panics on a length that cannot be an alias rather than answering with
// something that is not one. The length is written in the code and never comes
// from a request, so there is nobody to hand an error to and nothing a caller
// could do about it if there were.
func RandomAliasN(n int) string {
	if n < 1 || n > AliasMaxLen {
		panic(fmt.Sprintf("slug: an alias of %d characters is not an alias", n))
	}

	// 256 is not a multiple of 23, so folding a whole byte into the alphabet
	// would make the first three letters land slightly more often than the
	// rest. The skew is small enough not to matter for collisions and the fix
	// is to throw away the bytes that cause it, which costs one comparison and
	// leaves nothing to explain later.
	const limit = 256 - 256%len(Alphabet)

	vs := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(vs) < n {
		// Since Go 1.24 this cannot fail -- it fills the buffer or the process
		// stops -- so there is no error worth carrying up from here.
		_, _ = rand.Read(buf)

		for _, b := range buf {
			if int(b) >= limit {
				continue
			}

			vs = append(vs, Alphabet[int(b)%len(Alphabet)])
			if len(vs) == n {
				break
			}
		}
	}

	return string(vs)
}

// EncodeAlias spells bytes as an alias: one character of [Alphabet] per byte.
//
// It is a way of naming, not an encoding -- nothing reads it back, and
// [EncodeAliasN] throws away the tail, so being reversible was never on offer.
// What it has to be is stable, spellable, and an alias whatever it is handed.
// Sixteen bytes come out as sixteen characters carrying some 72 bits, which is
// far past the point where two rows are named the same thing.
//
// One character per byte is the whole of it, deliberately. The form this came
// from wrote base32 and then trimmed the leading digits off, which left a
// string whose length depended on its input, which is why it needed a second
// encoding for when the trimming left too little -- and the two encodings
// disagreed about which letters were allowed, so what a name looked like
// depended on how many digits its bytes happened to start with.
//
// It panics on no bytes at all, for the reason [RandomAliasN] does.
func EncodeAlias(b []byte) string {
	if len(b) == 0 {
		panic("slug: no bytes cannot be named")
	}

	vs := make([]byte, len(b))
	for i, v := range b {
		vs[i] = Alphabet[int(v)%len(Alphabet)]
	}

	return string(vs)
}

// EncodeAliasN is [EncodeAlias] cut to `n` characters, for a name that has to
// be short rather than distinct.
func EncodeAliasN(b []byte, n int) string {
	if n < 1 {
		panic(fmt.Sprintf("slug: an alias of %d characters is not an alias", n))
	}

	v := EncodeAlias(b)
	if len(v) > n {
		v = v[:n]
	}

	return v
}

// The two places an alias can sit in a slug. They are carried in the error so
// that a caller who wrote "@acme /admin" is told which half of it was the
// problem, which one message about "an alias" cannot say.
const (
	partAlias  = "alias"
	partTenant = "tenant"
)

func parseAlias(part string, v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if err := validate(part, v); err != nil {
		return "", err
	}

	return v, nil
}

// AliasError is a string that cannot be an alias, and which rule it broke.
//
// It answers InvalidArgument, since an alias arrives from a request far more
// often than from the code, and it does not echo what it was given back: the
// rule and an example of a name that keeps it are what a caller can act on.
//
// It carries the rule as a [pderr.Violation] as well as in the message, which
// is what lets a page put the line under the box rather than a sentence at the
// top of the form.
//
// The violation names **no field**, and that is not an omission. Nothing here
// has seen a request: a slug is a string, and which field of which message that
// string arrived in is known only to whoever had the message. [Part] says which
// half of the slug was wrong and goes in the words; where the slug itself was
// is said by that caller, with [pderr.At]. Guessing here would put the line
// under a box called "alias" every time a name went wrong, including the times
// the box was called something else.
type AliasError struct {
	// Part is which half of a slug this was -- see [Slug].
	Part string
	// Why is the rule that was broken, in the words a caller needs.
	Why string
}

func (e *AliasError) Error() string { return fmt.Sprintf("%s: %s", e.Part, e.Why) }

func (e *AliasError) Is(target error) bool { return target == ErrAlias }

func (e *AliasError) GRPCStatus() *status.Status {
	s, _ := status.FromError(pderr.Invalid(pderr.Violation{Why: e.Error()}))
	return s
}
