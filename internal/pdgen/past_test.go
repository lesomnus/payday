package pdgen_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	// The past's Event holds an Any, and the compiler resolves its import out
	// of the global registry.
	_ "google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
)

// copies is payday's Event and Fact as `pd gen` writes them into an app:
// **read from the files payday ships**, with the header the synthetic file in
// `readAs` supplies taken off, and each numbered where its placeholder is.
//
// From the source of truth for the reason [ownAll] is: a stub would not
// survive [pdgen.CheckOverlay], which holds the copies to what ships.
func copies(t *testing.T, event, fact int) string {
	t.Helper()

	drop := regexp.MustCompile(`(?m)^(edition|package|import|option (features|go_package))\b.*$`)
	zero := regexp.MustCompile(`(?m)^(\s*)domain: 0$`)

	b := &strings.Builder{}
	b.WriteString("import \"google/protobuf/any.proto\";\n")

	// In the one order, Event and then Fact, as `pd gen` lists them: a refusal
	// names the first of the two it meets, and a map's order is a coin toss.
	for _, v := range []struct {
		name string
		n    int
	}{{"event", event}, {"fact", fact}} {
		name, n := v.name, v.n
		v, err := os.ReadFile(filepath.Join("..", "..", "schema", "past", name+".proto"))
		require.NoError(t, err)

		src := drop.ReplaceAllString(string(v), "")
		require.Len(t, zero.FindAllString(src, -1), 1, "%s.proto has one placeholder", name)
		b.WriteString(zero.ReplaceAllString(src, fmt.Sprintf("${1}domain: %d", n)))
		b.WriteString("\n")
	}

	return b.String()
}

// declare is the line an app declares its past with.
func declare(event, fact int) string {
	return fmt.Sprintf("option (payday.app) = {past: {event: %d, fact: %d}};\n", event, fact)
}

// TestAPastIsReadOffTheDeclaration: an app that declares a past has payday's
// Event and Fact at the numbers it declared, found by their markers in a
// package that is not payday's -- and one that declares none has neither, and
// is missing nothing.
func TestAPastIsReadOffTheDeclaration(t *testing.T) {
	x := require.New(t)

	s, err := readAs(t, "fleet", declare(20, 21)+ownAll(t)+copies(t, 20, 21))
	x.NoError(err)
	x.Equal(&pdgen.Past{Event: 20, Fact: 21, At: "test/schema.proto"}, s.Past)

	for own, want := range map[pdpb.Own]struct {
		name   string
		domain uint8
	}{
		pdpb.Own_OWN_EVENT: {"fleet.Event", 20},
		pdpb.Own_OWN_FACT:  {"fleet.Fact", 21},
	} {
		v := s.Own(own)
		x.NotNil(v, "%s was not found", own)
		x.Equal(want.name, string(v.FullName()))
		x.Equal(want.domain, v.Domain)
	}
	x.NoError(pdgen.CheckOwn(s), "the past is payday's and is all there")

	t.Run("and with none declared, neither is wanted", func(t *testing.T) {
		x := require.New(t)

		s, err := readAs(t, "fleet", ownAll(t))
		x.NoError(err)
		x.Nil(s.Past)
		x.NoError(pdgen.CheckOwn(s), "an app with no past was told it is missing one")
	})

	t.Run("and declared, both are", func(t *testing.T) {
		x := require.New(t)

		s, err := readAs(t, "fleet", declare(20, 21)+ownAll(t))
		x.NoError(err)

		err = pdgen.CheckOwn(s)
		x.Error(err, "a past declared and not there generated quietly")
		for _, want := range []string{"OWN_EVENT", "OWN_FACT", "test/schema.proto declares a past"} {
			x.Contains(err.Error(), want)
		}
	})
}

// TestAPastIsNumberedLikeAnAppsOwn: each of the two in 7..247, the two
// different. payday numbers what every app has up from 1 and down from 255,
// so the ends are its own, and one byte is all an identifier has.
func TestAPastIsNumberedLikeAnAppsOwn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event int
		fact  int
		want  string
	}{
		{"none for the event", 0, 21, "past: says no `event:`"},
		{"none for the fact", 20, 0, "past: says no `fact:`"},
		{"one of payday's low ones", 3, 21, "past: event: 3 is payday's"},
		{"one of payday's high ones", 20, 250, "past: fact: 250 is payday's"},
		{"more than a byte", 20, 300, "does not fit in the one byte"},
		{"the same for both", 20, 20, "says 20 for both"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readAs(t, "fleet", declare(tc.event, tc.fact)+ownAll(t))
			require.Error(t, err, "a past numbered %d and %d was read", tc.event, tc.fact)
			require.Contains(t, err.Error(), tc.want)
			require.Contains(t, err.Error(), "test/schema.proto", "the refusal does not say where")
		})
	}
}

// TestAPastsNumberAnotherEntityHoldsIsRefused, as the declaration's: what is
// wrong is the number the app gave its past, and that is where it is changed.
func TestAPastsNumberAnotherEntityHoldsIsRefused(t *testing.T) {
	x := require.New(t)

	_, err := readAs(t, "fleet", declare(20, 21)+ownAll(t)+copies(t, 20, 21)+
		entity("Robot", `domain: 21`, `Tenant tenant = 2 [(orm.edge) = {}];`))
	x.Error(err, "two kinds of row were given one domain")
	x.Contains(err.Error(), "fleet.Robot declares domain 21")
	x.Contains(err.Error(), "test/schema.proto gives it to the past's Fact")
}

// TestACopyTheDeclarationDoesNotStandBehindIsRefused: an Event or a Fact in a
// schema that declares no past, and one numbered otherwise than its
// declaration says. `pd gen` writes the copies from the declaration and takes
// them out when it goes, so neither comes out of it -- they are what a buf run
// by hand over a tree whose declaration moved leaves, or an edited copy.
func TestACopyTheDeclarationDoesNotStandBehindIsRefused(t *testing.T) {
	t.Run("no declaration", func(t *testing.T) {
		x := require.New(t)

		_, err := readAs(t, "fleet", ownAll(t)+copies(t, 20, 21))
		x.Error(err, "a past nothing declares was generated")
		x.Contains(err.Error(), "fleet.Event is payday's OWN_EVENT")
		x.Contains(err.Error(), "declares none")
		x.Contains(err.Error(), "past: {event: 20, fact: 21}", "the declaration to write again is the copies' own")
	})

	t.Run("another number", func(t *testing.T) {
		x := require.New(t)

		_, err := readAs(t, "fleet", declare(20, 21)+ownAll(t)+copies(t, 20, 22))
		x.Error(err, "a copy numbered otherwise than its declaration was generated")
		x.Contains(err.Error(), "fleet.Fact says domain 22, and test/schema.proto declares 21 for it")
	})
}

// TestAPastIsDeclaredOnce: two files saying it are two lines to keep agreeing,
// and the copies can only be numbered by one of them.
func TestAPastIsDeclaredOnce(t *testing.T) {
	x := require.New(t)

	_, err := readFiles(t, map[string]string{
		"test/schema.proto": inPkg("fleet", declare(20, 21)+ownAll(t)+copies(t, 20, 21)),
		"test/again.proto":  inPkg("fleet", declare(20, 21)),
	})
	x.Error(err, "a past declared twice was read")
	x.Contains(err.Error(), "both declare a past")
	x.Contains(err.Error(), "test/again.proto")
	x.Contains(err.Error(), "test/schema.proto")
}

// TestAnOverlayAddsToThePastAndDoesNotChangeIt: what an app adopting its own
// events into payday's table adds -- the sentence it kept, at the number the
// header holds for one, and a payload -- is an overlay's to add. A number the
// template declares, a header field of another shape, and anything in its
// declaration are not.
func TestAnOverlayAddsToThePastAndDoesNotChangeIt(t *testing.T) {
	with := func(t *testing.T, fields string) string {
		t.Helper()

		src := copies(t, 20, 21)
		head := "message Event {"
		require.Contains(t, src, head)

		return declare(20, 21) + ownAll(t) + strings.Replace(src, head, head+"\n  "+fields, 1)
	}

	t.Run("what an adopting app adds", func(t *testing.T) {
		x := require.New(t)

		s, err := readAs(t, "fleet", with(t, "string desc = 6;\n  string payload = 17;"))
		x.NoError(err, "rove's fields were refused")

		var names []string
		for f := range s.Own(pdpb.Own_OWN_EVENT).Fields() {
			names = append(names, f.Name())
		}
		x.Contains(names, "desc")
		x.Contains(names, "payload")
	})

	t.Run("a header field of another shape", func(t *testing.T) {
		x := require.New(t)

		_, err := readAs(t, "fleet", with(t, "int32 desc = 6;"))
		x.Error(err, "an int desc was generated")
		x.Contains(err.Error(), "fleet.Event: desc: is a TYPE_INT32")
	})

	t.Run("a number the template declares", func(t *testing.T) {
		x := require.New(t)

		src := strings.Replace(with(t, ""), "string kind = 8", "int64 kind = 8", 1)
		_, err := readAs(t, "fleet", src)
		x.Error(err, "the event's kind was redeclared quietly")
		x.Contains(err.Error(), `fleet.Event: 8 is payday's "kind" (string) and was redeclared as "kind" (int64)`)
		x.Contains(err.Error(), "Of its Event and Fact, payday keeps every number they declare")
	})

	t.Run("its declaration", func(t *testing.T) {
		x := require.New(t)

		// The trail's word, which an entity with server writes never says:
		// every one of them is on the trail already.
		src := strings.Replace(with(t, ""), "own: OWN_EVENT", "own: OWN_EVENT\n    trail: {recorded: {}}", 1)
		_, err := readAs(t, "fleet", src)
		x.Error(err, "a trail was said of the past")
		x.Contains(err.Error(), "fleet.Event: trail: a server writes it")
	})
}

// TestOnlyThePastMayAddMany: payday's rules for its own entities are written
// for an Add of one row, and none of them has an AddMany -- but its Event and
// Fact, which are written the way an app's own are: an operation over many
// things writes a child for each, and a resource a fact for each field.
func TestOnlyThePastMayAddMany(t *testing.T) {
	x := require.New(t)

	s, err := readAs(t, "fleet", declare(20, 21)+ownAll(t)+copies(t, 20, 21))
	x.NoError(err)
	x.True(s.Own(pdpb.Own_OWN_EVENT).Rpcs().HasAddMany())
	x.True(s.Own(pdpb.Own_OWN_FACT).Rpcs().HasAddMany())

	// And every other of payday's is held to the rule still: the Holder, here,
	// since the test beside this one asks it of the Tenant.
	src := ownAll(t)
	before, after, ok := strings.Cut(src, "message Holder {")
	x.True(ok)
	src = before + "message Holder {" + strings.Replace(after, "rpc: {crud: true}", "rpc: {crud: true, add: {many: {}}}", 1)

	_, err = readAs(t, "fleet", declare(20, 21)+src+copies(t, 20, 21))
	x.Error(err, "payday's holder was given an AddMany")
	x.Contains(err.Error(), "fleet.Holder asks for AddMany, and it is payday's")
}
