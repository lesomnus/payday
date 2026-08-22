package pdcli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// TestAnEntityIsGivenADomainNothingElseHas is the first of the two things this
// command is for.
//
// Two entities sharing a domain makes an identifier lie about what it names. It
// is caught at generation -- but only after the schema is written and only if
// the generation is run, and by then somebody has typed a number.
func TestAnEntityIsGivenADomainNothingElseHas(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	l, err := pdcli.Discover(root)
	x.NoError(err)

	for _, name := range []string{"Robot", "Joint", "Fleet"} {
		_, err := (pdcli.Entity{Layout: l, Name: name, Tenanted: true}).Add()
		x.NoError(err)
	}

	vs, err := pdcli.Domains(l)
	x.NoError(err)
	x.Len(vs, 3, "two of them were given the same domain")

	// From 7, because payday keeps the low numbers for what it ships -- an app
	// that started at 1 collides with Tenant on its first entity.
	x.Equal("Robot", vs[7])
	x.Equal("Joint", vs[8])
	x.Equal("Fleet", vs[9])
}

// TestADeletedEntitysDomainIsNotOfferedAgain, as far as it can be known.
//
// A domain outlives the row it named -- an identifier in an audit trail says
// what kind of thing it was long after the row is gone -- so handing a deleted
// entity's number to the next one makes those rows say the wrong word, and
// nothing detects it before or after.
//
// It is not refused. The number is written in the app's own schema in plain
// sight, and reusing it is a decision an app may make. What this says is that
// it will not be made by accepting a suggestion.
//
// **And it is bounded, which the second half asserts.** This reads the schema
// as it is now; a schema does not record what it used to say. So a number is
// skipped only while something above it survives to raise the highest, and
// deleting the highest-numbered entity does hand its number back. Closing that
// needs a record of what has ever been allocated, which is a file to keep in
// step -- and the number is in the schema, where a person about to reuse one
// can see it.
func TestADeletedEntitysDomainIsNotOfferedAgain(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	l, err := pdcli.Discover(root)
	x.NoError(err)

	first, err := (pdcli.Entity{Layout: l, Name: "Robot", Tenanted: true}).Add()
	x.NoError(err)
	_, err = (pdcli.Entity{Layout: l, Name: "Joint", Tenanted: true}).Add()
	x.NoError(err)

	// Robot had 7 and is gone; Joint still holds 8.
	x.NoError(os.Remove(first))

	p, err := (pdcli.Entity{Layout: l, Name: "Fleet", Tenanted: true}).Add()
	x.NoError(err)

	b, err := os.ReadFile(p)
	x.NoError(err)
	x.Contains(string(b), "domain: 9", "a number a deleted entity held was handed out again")

	t.Run("and not when the highest is the one that went", func(t *testing.T) {
		x := require.New(t)

		root := app(t, "github.com/acme/other")
		l, err := pdcli.Discover(root)
		x.NoError(err)

		p, err := (pdcli.Entity{Layout: l, Name: "Robot", Tenanted: true}).Add()
		x.NoError(err)
		x.NoError(os.Remove(p))

		q, err := (pdcli.Entity{Layout: l, Name: "Joint", Tenanted: true}).Add()
		x.NoError(err)

		b, err := os.ReadFile(q)
		x.NoError(err)

		// 7 again. Nothing here knows Robot ever existed, and the alternative
		// is a file that has to be kept in step with the schema.
		x.Contains(string(b), "domain: 7")
	})
}

// TestTenancyDefaultsToTheLoudAnswer is the second.
//
// Getting tenancy wrong fails two ways and they are not alike: a wall assumed
// wrongly hides every row, which is noticed within minutes, and no wall assumed
// wrongly shows every row to everybody, which is noticed by whoever it happens
// to. So saying nothing means the first, here and in the schema, and the answer
// that leaks is the one somebody has to write.
func TestTenancyDefaultsToTheLoudAnswer(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	l, err := pdcli.Discover(root)
	x.NoError(err)

	// Saying nothing is behind the wall, which is what saying nothing means in
	// the schema too -- so the scaffold does not ask.
	p, err := (pdcli.Entity{Layout: l, Name: "Robot"}).Add()
	x.NoError(err)

	b, err := os.ReadFile(p)
	x.NoError(err)

	// No tenancy **declaration** -- the comment above it says the word, which is
	// the point of the comment, so the assertion is about the declared line.
	x.NotContains(string(b), "\n    global: {}")
	x.NotContains(string(b), "\n    tenant: {}")
	x.NotContains(string(b), "\n    tenanted:")

	// And it is one of the two rather than both.
	_, err = (pdcli.Entity{Layout: l, Name: "Robot", Tenanted: true, Global: true}).Add()
	x.ErrorContains(err, "behind the wall or it is not")
}

// TestWhatIsWrittenIsWhatTheGeneratorWillTake.
//
// A scaffold whose output is refused is a scaffold whose first user reads a
// generated error about a file they did not write. That has happened twice --
// an entity with no erasure declaration, and one declaring a second tenant --
// so what the file holds is asserted here rather than left to whoever runs
// `pd gen` next.
//
// Each of these is a refusal the generator makes, read as the line that avoids
// it: a `watch:` with nothing to order two answers by, or no way to name the
// rows it is about; a `list:` whose index does not end in the key, so a page
// scans; an entity that says nothing at all about erasure. And two that are
// not refusals but are wrong quietly -- the Go package, and the import that
// reaches the tenant, which is `app/payday/` because generation copies
// payday's entities **into** the app's own proto package.
func TestWhatIsWrittenIsWhatTheGeneratorWillTake(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	l, err := pdcli.Discover(root)
	x.NoError(err)

	p, err := (pdcli.Entity{Layout: l, Name: "Robot", Tenanted: true, Watch: true}).Add()
	x.NoError(err)

	b, err := os.ReadFile(p)
	x.NoError(err)
	src := string(b)

	x.Contains(src, `date_updated = 13 [(orm.field) = {version: {}}]`,
		"a watch has nothing to order two answers by")
	x.Contains(src, `by: [{name: "ref"}]`,
		"a watch has no way to name the rows it is about")
	x.Contains(src, `name: "page"`, "a list with no index scans the table")
	x.Contains(src, `option go_package = "github.com/acme/thing"`)

	// The tenant, where a generation puts it. payday's entities are copied
	// **into** the app's proto package, so this is `app/payday/` even for an
	// app that kept every default -- and `proto/payday/` is a path nothing
	// writes.
	x.Contains(src, `import "app/payday/tenant.proto";`)

	// Nothing about tenancy, which is the declaration -- behind the wall. What
	// the scaffold writes out instead is why, since a reader who finds no
	// tenancy line has to be able to tell "assumed" from "forgotten".
	x.NotContains(src, `tenanted:`)
	x.Contains(src, "Nothing about tenancy, which is the declaration")

	// And an erasure declaration, on every entity rather than only a watched
	// one: generation refuses an entity holding neither an `erased:` field nor
	// `erase: {hard: {}}`, so the scaffold cannot leave the question open.
	x.Contains(src, `date_erased = 14 [(orm.field) = {erased: {}}]`)

	// Which is true of the plainest entity there is, and that is the case the
	// flag used to decide.
	q, err := (pdcli.Entity{Layout: l, Name: "Widget"}).Add()
	x.NoError(err)

	w, err := os.ReadFile(q)
	x.NoError(err)
	x.Contains(string(w), `date_erased = 14 [(orm.field) = {erased: {}}]`,
		"an entity that says nothing about erasure is one generation refuses")
}

// TestTheTenantIsPaydaysAndNotTheAppsToDeclare.
//
// `--tenant` is the third tenancy the schema has and the one no app may
// scaffold. payday ships the tenant and every generation copies it into the
// app's own proto package, so an app declaring a second one writes a schema
// that cannot generate -- and the scaffold offering it was the tool writing
// something its own generator will not take.
//
// It is refused with the reason rather than dropped from the parser, because
// `unknown flag` sends the same person to write `tenant: {}` by hand: the same
// refused schema, arriving from a generator that can only say what is wrong and
// not what was wanted. What was wanted is `--tenanted`, or fields on payday's
// Tenant through an overlay, and both of those are in the refusal.
func TestTheTenantIsPaydaysAndNotTheAppsToDeclare(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	l, err := pdcli.Discover(root)
	x.NoError(err)

	_, err = (pdcli.Entity{Layout: l, Name: "Org", Tenant: true}).Add()
	x.ErrorContains(err, "payday ships the tenant")

	// Where the one it has is, and where a field of the app's own goes on it.
	// A refusal that only says no leaves somebody to guess both.
	x.ErrorContains(err, "proto/app/payday/tenant.proto")
	x.ErrorContains(err, "proto/ext/payday/tenant.ext.proto")
	x.ErrorContains(err, "--tenanted")

	// And nothing was written on the way to refusing.
	_, err = os.Stat(filepath.Join(root, "proto", "app", "org.proto"))
	x.True(os.IsNotExist(err), "a refused entity left a file behind")

	// The fact the refusal rests on, provoked rather than asserted: a schema
	// holding what `--tenant` would have written does not generate, and the
	// generator is the one that says so.
	t.Run("because a second one does not generate", func(t *testing.T) {
		x := require.New(t)

		l := genApp(t)

		p, err := (pdcli.Entity{Layout: l, Name: "Org", Global: true}).Add()
		x.NoError(err)

		b, err := os.ReadFile(p)
		x.NoError(err)
		x.NoError(os.WriteFile(p, []byte(
			strings.Replace(string(b), "\n    global: {}", "\n    tenant: {}", 1)), 0o644))

		err = pdcli.Gen{Layout: l}.Run(t.Context())
		x.ErrorContains(err, "app.Tenant and app.Org both say they are the tenant")
	})
}

// TestANameAnotherFileHoldsIsRefused.
//
// A proto package is one namespace, so what makes a name taken is the schema
// and not the file it is written in. A check that reads only the file about to
// be appended to passes `--file fleet.proto` for a `Robot` that is already in
// `robot.proto`, and what refuses it then is protoc, on a file this wrote.
//
// Three ways a name is taken and three trees, so that none of them rests on
// another having run: the app's own second file, a message that is not an
// entity at all, and one payday ships into every app.
func TestANameAnotherFileHoldsIsRefused(t *testing.T) {
	t.Run("in another file of the same package", func(t *testing.T) {
		x := require.New(t)

		root := app(t, "github.com/acme/thing")
		l, err := pdcli.Discover(root)
		x.NoError(err)

		_, err = (pdcli.Entity{Layout: l, Name: "Robot", Tenanted: true}).Add()
		x.NoError(err)

		_, err = (pdcli.Entity{Layout: l, Name: "Robot", Tenanted: true, File: "fleet.proto"}).Add()
		x.ErrorContains(err, "already holds a message Robot")

		// Named, because "it is taken" without saying where is a thing to go
		// looking for in a schema of thirty files.
		x.ErrorContains(err, "proto/app/robot.proto")

		_, err = os.Stat(filepath.Join(root, "proto", "app", "fleet.proto"))
		x.True(os.IsNotExist(err), "a refused entity left a file behind")
	})

	// Every message and not only every entity: a request message written into a
	// service overlay takes the name just as thoroughly, and a duplicate is a
	// duplicate whether or not either of them is a row.
	t.Run("including a message that is not an entity", func(t *testing.T) {
		x := require.New(t)

		root := app(t, "github.com/acme/thing")
		l, err := pdcli.Discover(root)
		x.NoError(err)

		x.NoError(os.MkdirAll(filepath.Join(root, "proto", "ext", "app"), 0o755))
		x.NoError(os.WriteFile(filepath.Join(root, "proto", "ext", "app", "robot_svc.ext.proto"),
			[]byte("message RobotMoveRequest {\n  RobotRef ref = 1;\n}\n"), 0o644))

		_, err = (pdcli.Entity{Layout: l, Name: "RobotMoveRequest", Tenanted: true}).Add()
		x.ErrorContains(err, "already holds a message RobotMoveRequest")
	})

	// And one payday ships, in an app that has never generated: the copies are
	// not on disk yet, so the only place the name can be seen to be taken is
	// payday's own schema -- which is where every app's copy comes from.
	t.Run("and one payday ships, before a first generation", func(t *testing.T) {
		x := require.New(t)

		root := app(t, "github.com/acme/other")
		l, err := pdcli.Discover(root)
		x.NoError(err)

		_, err = (pdcli.Entity{Layout: l, Name: "Holder", Tenanted: true}).Add()
		x.ErrorContains(err, "payday ships a message Holder")
	})
}

// TestAnEntityIsWrittenInThisAppsPackage, and not in the one `pd new` happens to
// write.
//
// Three things in the head of a new .proto are the app's and all three were the
// template's defaults hard-coded: the proto package, the Go package the messages
// land in, and the import that reaches the tenant. Every one of them is read off
// the layout, which reads them off the schema -- so an app that renamed its
// package says so once and everything follows.
//
// What it cost is worth keeping: run against an app whose package is its own,
// this wrote `proto/app/fleet.proto` declaring `package app` and a `go_package`
// pointing at the module root. `pd gen` then refused the app for declaring two
// of each, which is the right answer arriving after the file is on disk.
func TestAnEntityIsWrittenInThisAppsPackage(t *testing.T) {
	x := require.New(t)

	root := app(t, "example.com/telemetry")

	// An app at none of the defaults: a package of its own, and its messages
	// under `api/` rather than at the module root.
	dir := filepath.Join(root, "proto", "telemetry")
	x.NoError(os.MkdirAll(dir, 0o755))
	x.NoError(os.WriteFile(filepath.Join(dir, "robot.proto"), []byte(
		"edition = \"2023\";\n\npackage telemetry;\n\noption go_package = \"example.com/telemetry/api\";\n"), 0o644))

	l, err := pdcli.Discover(root)
	x.NoError(err)
	x.Equal("telemetry", l.ProtoPkg)

	p, err := (pdcli.Entity{Layout: l, Name: "Fleet", Tenanted: true}).Add()
	x.NoError(err)

	rel, err := filepath.Rel(root, p)
	x.NoError(err)
	x.Equal(filepath.Join("proto", "telemetry", "fleet.proto"), rel,
		"it went into a package this app does not have")

	b, err := os.ReadFile(p)
	x.NoError(err)
	src := string(b)

	x.Contains(src, "package telemetry;")
	x.NotContains(src, "package app;")
	x.Contains(src, `option go_package = "example.com/telemetry/api"`)
	x.Contains(src, `import "telemetry/payday/tenant.proto";`)

	// And the domain is still one past the highest, which is the other half of
	// what this command is for. Nothing here declares one, so it is the first.
	x.Contains(src, "domain: 7")
}

// TestASecondEntityGoesBesideTheFirst rather than replacing the file.
func TestASecondEntityGoesBesideTheFirst(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	l, err := pdcli.Discover(root)
	x.NoError(err)

	one := pdcli.Entity{Layout: l, Name: "Robot", Tenanted: true, File: "fleet.proto"}
	two := pdcli.Entity{Layout: l, Name: "Joint", Tenanted: true, File: "fleet.proto"}

	p, err := one.Add()
	x.NoError(err)
	_, err = two.Add()
	x.NoError(err)

	b, err := os.ReadFile(p)
	x.NoError(err)
	x.Equal(1, strings.Count(string(b), "edition = "), "the second write began a new file inside the first")
	x.Contains(string(b), "message Robot {")
	x.Contains(string(b), "message Joint {")

	// And the same name twice is refused rather than written twice.
	_, err = two.Add()
	x.ErrorContains(err, "already holds a message Joint")
}

// TestTheFileIsNamedAfterTheEntity when nobody said otherwise.
func TestTheFileIsNamedAfterTheEntity(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	l, err := pdcli.Discover(root)
	x.NoError(err)

	p, err := (pdcli.Entity{Layout: l, Name: "RobotArm", Global: true}).Add()
	x.NoError(err)
	x.Equal("robot_arm.proto", filepath.Base(p))
}
