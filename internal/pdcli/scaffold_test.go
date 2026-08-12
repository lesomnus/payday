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

	// And it is one of the three rather than several.
	_, err = (pdcli.Entity{Layout: l, Name: "Robot", Tenanted: true, Global: true}).Add()
	x.ErrorContains(err, "behind the wall or it is not")
}

// TestWhatIsWrittenIsWhatTheGeneratorWillTake.
//
// The scaffold's own output has to pass every refusal the generator has, or the
// first thing somebody does with this command is read a generated error. The
// pairs that matter are `watch:` needing a version field and needing `ref`
// among the filters -- both of which were found by scaffolding one and running
// `pd gen`.
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

	x.Contains(src, `date_updated = 13 [(orm.field) = {version: {}}]`, "a watch has nothing to order two answers by")
	x.Contains(src, `by: [{name: "ref"}]`, "a watch has no way to name the rows it is about")
	// Nothing about tenancy, which is the declaration -- behind the wall. What
	// the scaffold writes out instead is why, since a reader who finds no
	// tenancy line has to be able to tell "assumed" from "forgotten".
	x.NotContains(src, `tenanted:`)
	x.Contains(src, "Nothing about tenancy, which is the declaration")
	x.Contains(src, `name: "page"`, "a list with no index scans the table")
	x.Contains(src, `option go_package = "github.com/acme/thing"`)

	// The tenant, where a generation puts it. payday's entities are copied
	// **into** the app's proto package, so this is `app/payday/` even for an app
	// that kept every default -- and `proto/payday/` is a path nothing writes.
	x.Contains(src, `import "app/payday/tenant.proto";`)
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

	root := app(t, "hday.io/kamino")

	// An app at none of the defaults: a package of its own, and its messages
	// under `api/` rather than at the module root.
	dir := filepath.Join(root, "proto", "hday.oasys")
	x.NoError(os.MkdirAll(dir, 0o755))
	x.NoError(os.WriteFile(filepath.Join(dir, "robot.proto"), []byte(
		"edition = \"2023\";\n\npackage hday.oasys;\n\noption go_package = \"hday.io/kamino/api\";\n"), 0o644))

	l, err := pdcli.Discover(root)
	x.NoError(err)
	x.Equal("hday.oasys", l.ProtoPkg)

	p, err := (pdcli.Entity{Layout: l, Name: "Fleet", Tenanted: true}).Add()
	x.NoError(err)

	rel, err := filepath.Rel(root, p)
	x.NoError(err)
	x.Equal(filepath.Join("proto", "hday.oasys", "fleet.proto"), rel,
		"it went into a package this app does not have")

	b, err := os.ReadFile(p)
	x.NoError(err)
	src := string(b)

	x.Contains(src, "package hday.oasys;")
	x.NotContains(src, "package app;")
	x.Contains(src, `option go_package = "hday.io/kamino/api"`)
	x.Contains(src, `import "hday.oasys/payday/tenant.proto";`)

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
