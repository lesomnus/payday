package pdcli_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// TestAnEntityMayBeNamedWhatTheGeneratedServerNamesItsOwn.
//
// ent puts an entity's predicates in a package named after it, folded down,
// and the generated server imports that package. Its name was the one name in
// `server/pd` an app chose, beside every name payday's generator writes there
// -- and an app named an entity `Image`, which is what `left` called the row a
// write handed over. `image.IdEQ` was then the parameter's, and the app did not
// compile, on a line it could not edit (hday.io/workday, 2026-10-25). Each
// name here met one of payday's in a different place:
//
//   - `Image`, a parameter of a function that names every kind's package;
//   - `Wall`, a type of the file's own, so the two meet at file scope;
//   - `Page`, a local of the switches over every kind -- a check of the rows,
//     the trail carried forward, the past's tenants;
//   - `Read`, a local of a function written for each kind, which meets the
//     package of only the kind it is written for.
//
// The import is named `ent` and the entity's own name now, which no ent
// package is and no name payday writes is; see [pdgen.Paths.EntPkg]. A
// generation is not enough to show it: the generated code was valid Go to every
// tool but the compiler, so this builds what it generates.
func TestAnEntityMayBeNamedWhatTheGeneratedServerNamesItsOwn(t *testing.T) {
	x := require.New(t)

	l := genApp(t)

	var b strings.Builder
	b.WriteString(`edition = "2023";

package app;

import "app/payday/tenant.proto";
import "google/protobuf/timestamp.proto";
import "orm.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;
option go_package = "github.com/lesomnus/payday/internal/apptest";
`)
	// Behind the wall, listed and watched, erased softly, and with a name about
	// a person, so that each is in every part of the file that is written for
	// an entity like that: the wall, the list, the trail's check of the rows
	// and its seals, the past's names.
	for i, name := range []string{"Image", "Wall", "Page", "Read"} {
		fmt.Fprintf(&b, `
message %s {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string name = 5 [(payday.field) = {personal: true}];
  google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];
  google.protobuf.Timestamp date_erased = 14 [(orm.field) = {erased: {}}];
  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];

  option (orm.message) = {
    rpc: {crud: true}
    indexes: [{name: "page", refs: [{name: "tenant", number: 2}, {name: "date_created", number: 15}, {name: "id", number: 1}]}]
  };
  option (payday.entity) = {
    domain: %d
    list: {
      order: [{field: {name: "date_created"}}, {field: {name: "id"}}]
      with: [{name: "tenant"}]
      by: [{name: "ref"}, {name: "tenant"}]
      size: 50
      max: 100
    }
    watch: {}
  };
}
`, name, 100+i)
	}

	x.NoError(os.WriteFile(filepath.Join(l.Root, "proto", "app", "named.proto"), []byte(b.String()), 0o644))
	x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))

	build := exec.Command("go", "build", "./...")
	build.Dir = l.Root
	out, err := build.CombinedOutput()
	x.NoError(err, "%s", out)
}

// TestEveryEntPackageIsImportedUnderPaydaysName.
//
// What makes the test above hold for every name and not only its four: no
// package of an entity is imported under the name ent gave it. One path built
// by hand -- the outbox's drain and the walk of a `via` each built one -- is
// that name back, and an entity named like a local of the function it is in
// does not compile again. Read off the app's committed file, which every
// entity's package is in.
func TestEveryEntPackageIsImportedUnderPaydaysName(t *testing.T) {
	x := require.New(t)

	at := filepath.Join("..", "apptest", "server", "pd", "pd.g.go")
	f, err := parser.ParseFile(token.NewFileSet(), at, nil, parser.ImportsOnly)
	x.NoError(err)

	const ent = "github.com/lesomnus/payday/internal/apptest/internal/ent/"
	n := 0
	for _, v := range f.Imports {
		path, err := strconv.Unquote(v.Path.Value)
		x.NoError(err)

		pkg, ok := strings.CutPrefix(path, ent)
		if !ok || strings.Contains(pkg, "/") || pkg == "predicate" {
			continue
		}

		n++
		x.NotNil(v.Name, "%s is imported under the name ent gave it", path)
		x.True(strings.HasPrefix(v.Name.Name, "ent") && strings.ContainsFunc(v.Name.Name, unicode.IsUpper),
			"%s is imported as %s, a name an entity's package may have", path, v.Name.Name)
	}
	x.Positive(n, "no package of an entity: the test reads the wrong file")
}
