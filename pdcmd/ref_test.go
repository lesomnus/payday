package pdcmd

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"
)

// The registry is the process's, and this package's tests link no generated
// app, so these two words are this file's own. High numbers, so a generated
// app arriving in this test binary later collides with nothing.
func init() {
	pdid.Register("reftest.Thing", 201, "thing")
	pdid.Register("reftest.Other", 202, "other")
}

// TestARefIsReadTheWayEveryOtherNameIs.
//
// The slug half of a REF goes through [slug.Parse] rather than a second parser
// here, and this is what that buys: the parts are folded the way the database
// folded them on the way in, and the "#kind" is read rather than swallowed.
// The form this came from read "@acme#tenant" -- the slug package's own
// example -- as the alias "acme#tenant", which the server correctly could not
// find.
func TestARefIsReadTheWayEveryOtherNameIs(t *testing.T) {
	x := require.New(t)

	v, err := RefParser{}.Parse("@ACME / Alice")
	x.NoError(err)
	x.Equal(Ref{Tenant: "acme", Alias: "alice"}, v)

	v, err = RefParser{}.Parse("@acme/alice#thing")
	x.NoError(err)
	x.Equal("acme", v.Tenant)
	x.Equal("alice", v.Alias)
	x.Equal(pdid.Domain(201), v.Domain)

	// And it writes back as it was read, "#" included, so a value that goes
	// through both comes out naming the same row.
	x.Equal("@acme/alice#thing", RefParser{}.ToString(v))

	// A word nothing here goes by is a typo, and reading it as "no assertion"
	// is exactly the silence the assertion exists to end.
	_, err = RefParser{}.Parse("@acme/alice#thign")
	x.ErrorIs(err, slug.ErrNoSuchDomain)

	// A part that is missing rather than untidy is refused; slug says why.
	for _, s := range []string{"@", "@acme/", "@/alice"} {
		_, err := RefParser{}.Parse(s)
		x.Error(err, s)
	}
}

// TestAReferenceOfTheWrongKindIsRefusedByName.
//
// Both written forms carry a kind -- a slug in its "#", an identifier in its
// ninth byte -- and both are claims until something checks them. [Ref.Expect]
// is that check, and both mistakes answer [pdid.ErrDomain]: they are the same
// mistake written two ways, and a caller should not have to learn which one
// they made.
func TestAReferenceOfTheWrongKindIsRefusedByName(t *testing.T) {
	x := require.New(t)

	id := Ref{Id: pdid.New(201)}
	x.NoError(id.Expect(201))
	err := id.Expect(202)
	x.ErrorIs(err, pdid.ErrDomain)
	x.Contains(err.Error(), "thing")
	x.Contains(err.Error(), "other")

	named, err := RefParser{}.Parse("@acme/alice#thing")
	x.NoError(err)
	x.NoError(named.Expect(201))
	err = named.Expect(202)
	x.ErrorIs(err, pdid.ErrDomain)
	x.Contains(err.Error(), "thing")
	x.Contains(err.Error(), "other")

	// A reference that said nothing claims nothing.
	bare, err := RefParser{}.Parse("@acme/alice")
	x.NoError(err)
	x.NoError(bare.Expect(201))
	x.NoError(bare.Expect(202))

	// And an expectation of Unknown checks nothing, because it means the
	// caller has none -- see [Tree.Unary] -- not that the row is of no kind.
	x.NoError(id.Expect(pdid.Unknown))
	x.NoError(named.Expect(pdid.Unknown))
}

// TestExpectDoesNotPanicOnARefBuiltInCode.
//
// A Ref made of parts that were never a slug still gets the check; only the
// pretty-printed name in the error is lost.
func TestExpectDoesNotPanicOnARefBuiltInCode(t *testing.T) {
	x := require.New(t)

	v := Ref{Alias: "NOT AN ALIAS", Domain: 201}
	err := v.Expect(202)
	x.Error(err)
	x.True(errors.Is(err, pdid.ErrDomain))
}
