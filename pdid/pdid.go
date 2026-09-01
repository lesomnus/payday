// Package pdid is the identifier every row of a payday app is named by.
//
// It is a UUID, and it carries one byte saying what kind of thing it names.
// That byte is the whole of what this package adds, and it buys three things
// that a plain UUID cannot:
//
//   - **A reference can be checked before the database is.** A Holder's
//     identifier handed to something that wants a Tenant's is refused at the
//     edge, with a message that says which kind it actually was, rather than
//     becoming a query that matches nothing and a NotFound that says nothing.
//   - **A row that is gone can still be named.** An audit trail records what
//     was written to by identifier and not by table -- an identifier is unique
//     across all of them -- and so a row erased later leaves an identifier that
//     answers to nothing. The domain outlives the row.
//   - **Whatever answers to it can be found without asking every table.**
//
// # The layout
//
// It is a UUIDv8, which is the version the standard set aside for exactly this:
// everything but the version and variant is the implementation's to define.
//
//	 0                   1                   2                   3
//	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                          unix_ts_ms                           |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|          unix_ts_ms           |1 0 0 0|          seq          |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|1 0|    rand   |     domain    |             rand              |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                             rand                              |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//
// A millisecond timestamp, a twelve-bit counter that orders what is made
// inside one of them, the domain, and 54 bits of randomness.
//
// The counter is the part worth knowing about, because it is easy to lose.
// [New] builds on `uuid.NewV7`, whose twelve bits of `rand_a` are a sequence
// that makes identifiers made in the same millisecond come out in the order
// they were made. Writing the version as a whole byte would take four of those
// twelve with it, leaving 256 orderable per millisecond instead of 4096 -- so
// the version is written into the high nibble and the rest is left alone.
//
// The domain sits at byte 9, which is the second half of the fourth group when
// a UUID is written out. It reads without counting:
//
//	0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a
//	                    ^^ domain
//
// The fourth group always starts with 8, 9, a or b, since that is what the
// variant makes of it, so the two digits that matter are the ones after it.
//
// # Nothing is a domain until something says so
//
// A [Domain] is a number, and what it means is the app's. [Register] is how
// generated code says it, from what the schema declared, so that the number and
// the name it goes by are said once and in the same place. Reading a domain out
// of an identifier that nothing registered answers with the number and no name,
// which is what an identifier from another deployment looks like.
package pdid

import (
	"fmt"

	"github.com/google/uuid"
)

// Id is a Uuid that says what kind of thing it names.
type Id uuid.UUID

// Nil is the identifier that names nothing, and so is the identifier nothing
// may be stored under. Its domain is [Unknown].
var Nil = Id(uuid.Nil)

// New answers with a fresh identifier of the given domain.
//
// It panics if the source of randomness fails, which is what `uuid.Must` means
// and is the right answer here: a process that cannot make an identifier cannot
// serve a request either.
func New(d Domain) Id {
	v := uuid.Must(uuid.NewV7())

	// The version, and only the version. The low nibble of this byte is the
	// high half of v7's twelve-bit sequence counter, which is what orders
	// identifiers made inside one millisecond; writing the byte whole would
	// throw two thirds of that away. See the package comment.
	v[6] = 0x80 | (v[6] & 0x0F)
	v[9] = byte(d)

	return Id(v)
}

// Uuid answers with this identifier as a plain Uuid, which is what the database
// and the wire hold.
func (v Id) Uuid() uuid.UUID { return uuid.UUID(v) }

func (v Id) String() string { return v.Uuid().String() }

// Bytes answers with the sixteen bytes a request carries.
func (v Id) Bytes() []byte {
	b := make([]byte, 16)
	copy(b, v[:])
	return b
}

// Domain is what this identifier names, and [Unknown] if it is not one of ours.
func (v Id) Domain() Domain {
	d, _ := Of(uuid.UUID(v))
	return d
}

// IsZero reports whether this is [Nil].
func (v Id) IsZero() bool { return v == Nil }

// Of answers with the domain `v` names, and whether `v` is an identifier of
// this shape at all.
//
// The check is the point. Every sixteen bytes can be read as a UUID and every
// Uuid has a ninth byte, so without it any identifier would be taken to be
// making a claim about itself -- a v4 whose ninth byte happens to be 3 would
// pass as whatever 3 was registered as. What the version and the variant say is
// the only reason to believe the rest.
func Of(v uuid.UUID) (Domain, bool) {
	if v.Version() != 8 || v.Variant() != uuid.RFC4122 {
		return Unknown, false
	}

	return Domain(v[9]), true
}

// From reads an identifier out of the sixteen bytes a request carries.
//
// It refuses bytes that are not an identifier of this shape, since a caller
// that sends something else is naming a row this app never wrote.
func From(b []byte) (Id, error) {
	v, err := uuid.FromBytes(b)
	if err != nil {
		return Nil, err
	}
	if _, ok := Of(v); !ok {
		return Nil, fmt.Errorf("%w: %s", ErrNotAnId, v)
	}

	return Id(v), nil
}

// Parse reads an identifier out of its written form.
func Parse(s string) (Id, error) {
	v, err := uuid.Parse(s)
	if err != nil {
		return Nil, err
	}
	if _, ok := Of(v); !ok {
		return Nil, fmt.Errorf("%w: %s", ErrNotAnId, v)
	}

	return Id(v), nil
}

// MustParse is [Parse] for a literal written in the source, where a bad one is
// a mistake rather than a request.
func MustParse(s string) Id {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}

	return v
}

// WithDomain answers with this identifier as one of `d`.
//
// It is for tests and for whatever has to build an identifier a piece at a
// time. An identifier that names a row already has a domain, and changing it
// makes one that names nothing.
func WithDomain(v Id, d Domain) Id {
	v[9] = byte(d)
	return v
}
