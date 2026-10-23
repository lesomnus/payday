package pdcli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// past writes one of an app's own files, saying `option` beside what every
// file of it says, and answers with the app.
func past(t *testing.T, root string, name string, option string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(root, "proto", "app", name), []byte(`edition = "2023";

package app;

import "payday.proto";

option go_package = "github.com/acme/thing";
`+option+"\n"), 0o644))
}

// TestThePastIsReadBeforeAnythingIsCompiled: what an app declares of its past
// decides what `pd gen` writes for buf to compile, so it is read off the file
// as written -- in each of the forms an option can be written in, which a
// compile would read as one.
func TestThePastIsReadBeforeAnythingIsCompiled(t *testing.T) {
	for name, option := range map[string]string{
		"the literal": `option (payday.app) = {past: {event: 17, fact: 16}};`,
		"over lines, with comments": `option (payday.app) = {
  // The past, numbered after the app's own.
  past: {
    event: 17 // its operations
    fact: 0x10
  }
};`,
		"by the past's name": `option (payday.app).past = {event: 17, fact: 16};`,
		"field by field":     "option (payday.app).past.event = 17;\noption (payday.app).past.fact = 16;",
		"with a leading dot": `option (.payday.app) = {past <event: 17 fact: 16>};`,
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			root := app(t, "github.com/acme/thing")
			past(t, root, "widget.proto", option)

			l, err := pdcli.Discover(root)
			x.NoError(err)
			x.Equal(&pdcli.Past{Event: 17, Fact: 16, At: "proto/app/widget.proto"}, l.Past)
		})
	}

	t.Run("and an app that says nothing of one has none", func(t *testing.T) {
		x := require.New(t)

		for _, option := range []string{"", "option (payday.app) = {};"} {
			root := app(t, "github.com/acme/thing")
			past(t, root, "widget.proto", option)

			l, err := pdcli.Discover(root)
			x.NoError(err)
			x.Nil(l.Past, "%q declared a past", option)
		}
	})
}

// TestAPastIsRefusedBeforeAnythingIsCompiled: the numbers go into the copies
// `pd gen` writes, so a wrong one is refused before it is written into a file
// -- in the words `pdgen.Read` uses of the compiled schema, since it is the
// same rule -- and so is a declaration written twice, or misspelt.
func TestAPastIsRefusedBeforeAnythingIsCompiled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		option string
		want   string
	}{
		{"one of payday's numbers", `option (payday.app) = {past: {event: 3, fact: 16}};`, "past: event: 3 is payday's"},
		{"one number for both", `option (payday.app) = {past: {event: 17, fact: 17}};`, "says 17 for both"},
		{"a number left out", `option (payday.app) = {past: {event: 17}};`, "past: says no `fact:`"},
		{"a field misspelt", `option (payday.app) = {past: {evnt: 17, fact: 16}};`, "unknown field: evnt"},
		{"an option it is no field of", `option (payday.app).(payday.entity) = {};`, "names (payday.entity)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			root := app(t, "github.com/acme/thing")
			past(t, root, "widget.proto", tc.option)

			_, err := pdcli.Discover(root)
			x.Error(err, "%s was read", tc.option)
			x.Contains(err.Error(), tc.want)
			x.Contains(err.Error(), "proto/app/widget.proto", "the refusal does not say where")
		})
	}

	t.Run("twice", func(t *testing.T) {
		x := require.New(t)

		root := app(t, "github.com/acme/thing")
		past(t, root, "gadget.proto", `option (payday.app) = {past: {event: 17, fact: 16}};`)
		past(t, root, "widget.proto", `option (payday.app) = {past: {event: 17, fact: 16}};`)

		_, err := pdcli.Discover(root)
		x.Error(err, "a past declared in two files was read")
		x.Contains(err.Error(), "proto/app/gadget.proto and proto/app/widget.proto both declare a past")
	})
}

// TestWhatAPastWouldBeWrittenOverIsRefusedBeforeBuf: an app's own `Event` and
// a file of its own called `event.proto` both collide with the copy `pd gen`
// is about to write -- a symbol defined twice, and two files both written as
// `event.pb.go` -- and buf names neither in words that point at the past. So
// they are refused first, and nothing is written.
func TestWhatAPastWouldBeWrittenOverIsRefusedBeforeBuf(t *testing.T) {
	declared := `option (payday.app) = {past: {event: 17, fact: 16}};`

	for name, tc := range map[string]struct {
		file string
		src  string
		want []string
	}{
		"a message of the app's": {
			"calendar.proto",
			"message Event {\n  bytes id = 1;\n}",
			[]string{"proto/app/calendar.proto declares a message Event", "proto/app/widget.proto declares a past", "proto/ext/payday/event.ext.proto"},
		},
		"a file of the app's": {
			"fact.proto",
			"message Reading {\n  bytes id = 1;\n}",
			[]string{"proto/app/fact.proto is called what payday's copy of its past is", "fact.pb.go"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			root := app(t, "github.com/acme/thing")
			past(t, root, "widget.proto", declared)
			past(t, root, tc.file, tc.src)

			l, err := pdcli.Discover(root)
			x.NoError(err)

			err = pdcli.Gen{Layout: l}.Run(t.Context())
			x.Error(err, "what the past is written over was let through")
			for _, want := range tc.want {
				x.Contains(err.Error(), want)
			}

			_, err = os.Stat(l.Path(l.DirPd()))
			x.ErrorIs(err, os.ErrNotExist, "something was written before the refusal")
		})
	}
}

// TestTheCopiesAreTheAppsAndGoWithTheDeclaration: `pd gen` writes payday's
// Event and Fact into an app that declares a past -- in the app's package,
// importing the copies beside them, numbered as the app declared and with its
// overlay merged -- numbers them again when the declaration changes, and takes
// them out when it goes, with everything that was generated from them.
func TestTheCopiesAreTheAppsAndGoWithTheDeclaration(t *testing.T) {
	x := require.New(t)

	l := genApp(t)
	event := filepath.Join(l.Root, l.DirPd(), "event.proto")
	fact := filepath.Join(l.Root, l.DirPd(), "fact.proto")
	read := func(p string) string {
		b, err := os.ReadFile(p)
		x.NoError(err)

		return string(b)
	}

	// From nothing: the committed copies are not what is being tested.
	x.NoError(os.Remove(event))
	x.NoError(os.Remove(fact))
	x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))

	v := read(event)
	for _, want := range []string{
		"package app;",
		`import "app/payday/tenant.proto";`,
		`option go_package = "github.com/lesomnus/payday/internal/apptest";`,
		"own: OWN_EVENT",
		"domain: 17",
		"string desc = 6",
	} {
		x.Contains(v, want)
	}
	x.NotContains(v, "domain: 0")
	x.NotContains(v, "package payday;")
	x.Contains(read(fact), "domain: 18")

	// Numbered from the declaration, every run.
	robot := l.Path(pdcli.DirProto, "app", "robot.proto")
	src := read(robot)
	x.Contains(src, "past: {event: 17, fact: 18}")
	x.NoError(os.WriteFile(robot, []byte(strings.Replace(src, "past: {event: 17, fact: 18}", "past: {event: 19, fact: 20}", 1)), 0o644))

	l, err := pdcli.Discover(l.Root)
	x.NoError(err)
	x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))
	x.Contains(read(event), "domain: 19")
	x.Contains(read(fact), "domain: 20")

	// And gone with it, with what was generated from them.
	x.NoError(os.WriteFile(robot, []byte(strings.Replace(src, "past: {event: 17, fact: 18}", "", 1)), 0o644))
	x.NoError(os.Remove(l.Path(pdcli.DirExt, "payday", "event.ext.proto")))

	l, err = pdcli.Discover(l.Root)
	x.NoError(err)
	x.Nil(l.Past)
	x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))

	for _, p := range []string{
		event,
		fact,
		filepath.Join(l.Root, l.DirPd(), "event_svc.g.proto"),
		l.Path("event.pb.go"),
		l.Path("fact_svc.pb.go"),
		l.Path(pdcli.DirEnt, "schema", "event.go"),
		l.Path(pdcli.DirEnt, "event.go"),
		l.Path(pdcli.DirEnt, "fact"),
		l.Path(pdcli.DirBare, "fact.g.go"),
	} {
		_, err := os.Stat(p)
		x.ErrorIs(err, os.ErrNotExist, "%s outlived the declaration", p)
	}

	// Which a second run agrees with.
	vs, err := pdcli.Gen{Layout: l}.Check(t.Context())
	x.NoError(err)
	x.Empty(vs)
}

// TestAnEntityIsNotGivenThePastsNumbers: the declaration holds two domains
// before any file does -- the copies are written by the next generation -- and
// in an app that declares a past, `Event` and `Fact` are payday's names.
func TestAnEntityIsNotGivenThePastsNumbers(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	past(t, root, "widget.proto", `option (payday.app) = {past: {event: 7, fact: 8}};`)

	l, err := pdcli.Discover(root)
	x.NoError(err)

	_, err = (pdcli.Entity{Layout: l, Name: "Robot", Tenanted: true}).Add()
	x.NoError(err)

	vs, err := pdcli.Domains(l)
	x.NoError(err)
	x.Equal(map[int]string{9: "Robot"}, vs, "an entity was given a number the past holds")

	_, err = (pdcli.Entity{Layout: l, Name: "Event", Tenanted: true}).Add()
	x.Error(err, "an app's own Event was written beside the past's")
	x.Contains(err.Error(), "payday writes a message Event into this app, for the past proto/app/widget.proto declares")

	t.Run("and one that declares none may have an Event of its own", func(t *testing.T) {
		x := require.New(t)

		root := app(t, "github.com/acme/thing")
		l, err := pdcli.Discover(root)
		x.NoError(err)

		_, err = (pdcli.Entity{Layout: l, Name: "Event", Tenanted: true}).Add()
		x.NoError(err, "a calendar's Event was refused")
	})
}

// TestAnOverlayOfThePastIsMergedOnlyWithOne: an overlay of payday's Event is an
// overlay of something an app has when it declares a past, and of nothing when
// it does not -- a file never merged, the same as a typo, with another fix.
func TestAnOverlayOfThePastIsMergedOnlyWithOne(t *testing.T) {
	doctor := func(t *testing.T, option string) string {
		t.Helper()
		x := require.New(t)

		root := app(t, "github.com/acme/thing")
		past(t, root, "widget.proto", option)

		dir := filepath.Join(root, pdcli.DirExt, "payday")
		x.NoError(os.MkdirAll(dir, 0o755))
		x.NoError(os.WriteFile(filepath.Join(dir, "event.ext.proto"), []byte("message Event {}\n"), 0o644))

		l, err := pdcli.Discover(root)
		x.NoError(err)

		var said string
		for _, v := range pdcli.Doctor(t.Context(), l) {
			said += v.String() + "\n"
		}

		return said
	}

	x := require.New(t)

	said := doctor(t, "")
	x.Contains(said, "event.ext.proto extends payday's Event, which is written into an app that declares a past")
	x.Contains(said, "option (payday.app) = {past:")

	x.NotContains(doctor(t, `option (payday.app) = {past: {event: 17, fact: 16}};`), "event.ext.proto")
}
