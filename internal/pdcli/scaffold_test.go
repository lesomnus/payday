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

// TestTenancyIsAskedForAndNotGuessed is the second.
//
// Generation refuses an entity that says nothing, which is the point -- so a
// scaffold that emitted the option without it would be a scaffold whose first
// output does not build.
func TestTenancyIsAskedForAndNotGuessed(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	l, err := pdcli.Discover(root)
	x.NoError(err)

	_, err = (pdcli.Entity{Layout: l, Name: "Robot"}).Add()
	x.ErrorContains(err, "--tenanted")
	x.ErrorContains(err, "only the first of those is ever noticed")

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
	x.Contains(src, `by: ["ref"]`, "a watch has no way to name the rows it is about")
	x.Contains(src, `tenanted: {via: "tenant"}`)
	x.Contains(src, `name: "page"`, "a list with no index scans the table")
	x.Contains(src, `option go_package = "github.com/acme/thing"`)
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
