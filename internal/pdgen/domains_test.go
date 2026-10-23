package pdgen_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
)

// number is the line an app gives payday's Archived and Access domains with,
// saying what `fields` says.
func number(fields string) string {
	return "option (payday.app) = {domains: {" + fields + "}};\n"
}

// numberWithPast is [number] in an app that declares a past as well: one
// option, since a file says `(payday.app)` once.
func numberWithPast(event, fact int, fields string) string {
	return fmt.Sprintf("option (payday.app) = {past: {event: %d, fact: %d}, domains: {%s}};\n", event, fact, fields)
}

// renumbered is `src` with the `domain:` payday ships the entity of marker
// `own` at written over with `to`, as `pd gen` writes it into an app that
// numbers it: one line, in payday's own file.
func renumbered(t *testing.T, src string, own pdpb.Own, to int) string {
	t.Helper()

	from, err := pdgen.Shipped(own)
	require.NoError(t, err)

	at := regexp.MustCompile(fmt.Sprintf(`(?m)^(\s*)domain: %d$`, from))
	require.Len(t, at.FindAllString(src, -1), 1, "payday's %s says its domain once", own)

	return at.ReplaceAllString(src, fmt.Sprintf("${1}domain: %d", to))
}

// numberedAs is payday's own entities as `pd gen` writes them into an app that
// gives its Archived and Access these domains.
func numberedAs(t *testing.T, archived, access int) string {
	t.Helper()

	return renumbered(t, renumbered(t, ownAll(t), pdpb.Own_OWN_ARCHIVED, archived), pdpb.Own_OWN_ACCESS, access)
}

// TestPaydaysArchivedAndAccessAreFiveAndSixUnlessTheAppSays: what every app
// that never spent either number has, and what it has without saying anything.
func TestPaydaysArchivedAndAccessAreFiveAndSixUnlessTheAppSays(t *testing.T) {
	x := require.New(t)

	s, err := readAs(t, "fleet", ownAll(t))
	x.NoError(err)
	x.Nil(s.Domains)
	x.EqualValues(5, s.Own(pdpb.Own_OWN_ARCHIVED).Domain)
	x.EqualValues(6, s.Own(pdpb.Own_OWN_ACCESS).Domain)

	for _, own := range []pdpb.Own{pdpb.Own_OWN_ARCHIVED, pdpb.Own_OWN_ACCESS} {
		_, ok := s.Domains.Domain(own)
		x.False(ok, "%s is numbered by a declaration nobody wrote", own)
	}
}

// TestAnAppNumbersPaydaysArchivedAndAccess: the two are found by their markers
// at the numbers the app gave them, wherever those are in its range -- one of
// them, or both, beside a past.
func TestAnAppNumbersPaydaysArchivedAndAccess(t *testing.T) {
	x := require.New(t)

	s, err := readAs(t, "fleet", number("archived: 240, access: 241")+numberedAs(t, 240, 241))
	x.NoError(err)
	x.Equal(&pdgen.AppDomains{Archived: 240, Access: 241, At: "test/schema.proto"}, s.Domains)
	x.EqualValues(240, s.Own(pdpb.Own_OWN_ARCHIVED).Domain)
	x.EqualValues(241, s.Own(pdpb.Own_OWN_ACCESS).Domain)
	x.NoError(pdgen.CheckOwn(s))

	t.Run("one of them", func(t *testing.T) {
		x := require.New(t)

		s, err := readAs(t, "fleet", number("archived: 9")+renumbered(t, ownAll(t), pdpb.Own_OWN_ARCHIVED, 9))
		x.NoError(err)
		x.EqualValues(9, s.Own(pdpb.Own_OWN_ARCHIVED).Domain)
		x.EqualValues(6, s.Own(pdpb.Own_OWN_ACCESS).Domain, "the one it says nothing of moved")
	})

	t.Run("beside a past", func(t *testing.T) {
		x := require.New(t)

		s, err := readAs(t, "fleet", numberWithPast(20, 21, "archived: 22, access: 23")+numberedAs(t, 22, 23)+copies(t, 20, 21))
		x.NoError(err)
		x.EqualValues(22, s.Own(pdpb.Own_OWN_ARCHIVED).Domain)
		x.EqualValues(20, s.Own(pdpb.Own_OWN_EVENT).Domain)
	})

	t.Run("and an app's own entity at payday's numbers", func(t *testing.T) {
		x := require.New(t)

		// kamino: a robot and a model at 5 and 6 since before payday took
		// them, in every identifier and every certificate issued.
		s, err := readAs(t, "fleet", number("archived: 240, access: 241")+numberedAs(t, 240, 241)+
			entity("Robot", `domain: 5`, `Tenant tenant = 2 [(orm.edge) = {}];`)+
			entity("Model", `domain: 6`, `Tenant tenant = 2 [(orm.edge) = {}];`))
		x.NoError(err)

		got := map[string]uint8{}
		for _, v := range s.Entities {
			got[v.GoName()] = v.Domain
		}
		x.EqualValues(5, got["Robot"])
		x.EqualValues(6, got["Model"])
	})
}

// TestTheNumbersAnAppGivesPaydaysAreRefused unless they are numbers of its
// own: each in 7..247, the two different, and neither the past's. Payday's
// own number is not one -- saying nothing keeps it.
func TestTheNumbersAnAppGivesPaydaysAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []string
	}{
		{"one of payday's low ones", number("archived: 3"), []string{"domains: archived: 3 is payday's"}},
		{
			"the one payday gives it",
			number("archived: 5"),
			[]string{"domains: archived: 5 is payday's", "Its Archived is 5 where nothing numbers it -- say nothing to keep it there"},
		},
		{"one of payday's high ones", number("access: 250"), []string{"domains: access: 250 is payday's", "Its Access is 6"}},
		{"more than a byte", number("access: 300"), []string{"domains: access: 300 does not fit in the one byte"}},
		{"the same for both", number("archived: 240, access: 240"), []string{"domains: says 240 for both"}},
		{
			"one the past takes",
			numberWithPast(20, 21, "archived: 21"),
			[]string{"domains: archived: 21 is what `past:` gives the past's Fact", "Give Archived a number nothing holds"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readAs(t, "fleet", tc.src+ownAll(t))
			require.Error(t, err, "%q was read", tc.src)
			for _, want := range tc.want {
				require.Contains(t, err.Error(), want)
			}
			require.Contains(t, err.Error(), "test/schema.proto", "the refusal does not say where")
		})
	}
}

// TestPaydaysDomainsAreDeclaredOnce, for the reason a past is: the copies can
// only be numbered by one of them.
func TestPaydaysDomainsAreDeclaredOnce(t *testing.T) {
	x := require.New(t)

	_, err := readFiles(t, map[string]string{
		"test/schema.proto": inPkg("fleet", number("archived: 240")+renumbered(t, ownAll(t), pdpb.Own_OWN_ARCHIVED, 240)),
		"test/again.proto":  inPkg("fleet", number("archived: 240")),
	})
	x.Error(err, "payday's domains declared twice were read")
	x.Contains(err.Error(), "both give payday's entities domains")
	x.Contains(err.Error(), "test/again.proto")
	x.Contains(err.Error(), "test/schema.proto")
}

// TestANumberPaydaysAndAnAppsBothHoldIsSaidWhereItIsMended: an app's entity at
// the number payday gives its Archived or Access -- what payday#163 is about --
// is told the line that moves payday's, and one whose declaration gave a number
// another entity holds is told the declaration is what to change.
func TestANumberPaydaysAndAnAppsBothHoldIsSaidWhereItIsMended(t *testing.T) {
	robot := func(n int) string {
		return entity("Robot", fmt.Sprintf(`domain: %d`, n), `Tenant tenant = 2 [(orm.edge) = {}];`)
	}
	model := func(n int) string {
		return entity("Model", fmt.Sprintf(`domain: %d`, n), `Tenant tenant = 2 [(orm.edge) = {}];`)
	}

	for _, tc := range []struct {
		name string
		src  string
		want []string
	}{
		{
			"an app's own at payday's",
			ownAll(t) + robot(5),
			[]string{
				"fleet.Robot declares domain 5, which payday gives its Archived where an app gives it no other. A domain is one entity's",
				"option (payday.app) = {domains: {archived: 7}};",
			},
		},
		{
			"two of an app's own at payday's two",
			ownAll(t) + robot(5) + model(6),
			[]string{
				"fleet.Robot declares domain 5, which payday gives its Archived, and fleet.Model declares domain 6, which payday gives its Access, where an app gives them no other",
				"option (payday.app) = {domains: {archived: 7, access: 8}};",
			},
		},
		{
			"beside the one it numbers",
			number("access: 241") + renumbered(t, ownAll(t), pdpb.Own_OWN_ACCESS, 241) + robot(5) + model(7),
			[]string{
				"fleet.Robot declares domain 5, which payday gives its Archived",
				"option (payday.app) = {domains: {archived: 8, access: 241}};",
			},
		},
		{
			"a number the app gave payday's",
			number("archived: 240") + renumbered(t, ownAll(t), pdpb.Own_OWN_ARCHIVED, 240) + robot(240),
			[]string{
				"fleet.Robot declares domain 240, and test/schema.proto gives it to payday's Archived",
				"Give Archived a number nothing holds",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readAs(t, "fleet", tc.src)
			require.Error(t, err, "two kinds of row were given one domain")
			for _, want := range tc.want {
				require.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestACopyNoDeclarationStandsBehindIsRefused: payday's Archived or Access at
// a number other than the one declared, or than payday's where none is. `pd
// gen` writes the copies from the declaration on every run, so neither comes
// out of it.
func TestACopyNoDeclarationStandsBehindIsRefused(t *testing.T) {
	t.Run("another number than declared", func(t *testing.T) {
		x := require.New(t)

		_, err := readAs(t, "fleet", number("archived: 240")+ownAll(t))
		x.Error(err, "a copy at payday's number was generated for a declaration of another")
		x.Contains(err.Error(), "fleet.Archived says domain 5, and test/schema.proto declares 240 for it")
	})

	t.Run("a number nothing declares", func(t *testing.T) {
		x := require.New(t)

		_, err := readAs(t, "fleet", renumbered(t, ownAll(t), pdpb.Own_OWN_ACCESS, 241))
		x.Error(err, "a copy numbered by a declaration that went was generated")
		x.Contains(err.Error(), "fleet.Access says domain 241, and nothing declares a number for it, which leaves it at payday's 6")
		x.Contains(err.Error(), "option (payday.app) = {domains: {access: 241}};", "the line to say again is the copy's own number")
	})
}

// TestOnlyArchivedAndAccessMove: what an overlay may not do to the rest of
// payday's entities it may not do to these two either, and the rest keep the
// numbers payday gives them.
func TestOnlyArchivedAndAccessMove(t *testing.T) {
	t.Run("another of payday's", func(t *testing.T) {
		x := require.New(t)

		src := strings.Replace(ownAll(t), "domain: 4\n", "domain: 30\n", 1)
		x.NotEqual(ownAll(t), src, "payday's outbox is not at 4 any more")

		_, err := readAs(t, "fleet", src)
		x.Error(err, "payday's outbox was moved")
		x.Contains(err.Error(), "fleet.Outbox: (payday.entity) domain is not payday's")
	})

	t.Run("the rest of a numbered one's declaration", func(t *testing.T) {
		x := require.New(t)

		src := strings.Replace(numberedAs(t, 240, 241), "own: OWN_ARCHIVED\n", "own: OWN_ARCHIVED\n    trail: {recorded: {}}\n", 1)
		_, err := readAs(t, "fleet", number("archived: 240, access: 241")+src)
		x.Error(err, "a numbered entity's declaration was changed")
		x.Contains(err.Error(), "fleet.Archived")
	})
}
