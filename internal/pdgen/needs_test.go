package pdgen_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
)

// TestEveryVerbPaydayCallsOfAHolderOrATenantIsListed: an overlay may switch off
// a verb of payday's Holder or Tenant that payday's own code does not call, and
// which verbs those are is a list (`paydayCalls`). A list is what goes stale:
// a caller added without it is a verb an app may switch off and payday then
// calls, which is a failure at run time, in a place the overlay is not.
//
// So the code is read for every call of one: what this package writes, as it
// wrote it for the test app (`server/pd/pd.g.go`, which has every feature
// payday writes for); what `pd new` and `pd sandbox init` write; and payday's
// own runtime packages. A verb called and not listed fails, and so does one
// listed that nothing calls.
func TestEveryVerbPaydayCallsOfAHolderOrATenantIsListed(t *testing.T) {
	x := require.New(t)

	calls := []*regexp.Regexp{
		// A server's, in Go: `s.Ungated.Tenant().Add(`.
		regexp.MustCompile(`\b(Tenant|Holder)\(\)\.(Add|AddMany|Get|Patch|Apply|Erase)\(`),
		// A request built for one, however the server is reached: the
		// gate's `s.Gate.Next().Tenant().Get(ctx, TenantGetRequest_builder`,
		// or a Watch's `s.Get(ctx, HolderGetRequest_builder`.
		regexp.MustCompile(`\b(Tenant|Holder)(Add|AddMany|Get|Patch|Apply)Request_builder\b`),
		// A client's, in Go.
		regexp.MustCompile(`\bNew(Tenant|Holder)ServiceClient\([^)]*\)\.(Add|AddMany|Get|Patch|Apply|Erase)\(`),
		// A page's.
		regexp.MustCompile(`\b(Tenant|Holder)Service\.method\.(add|addMany|get|patch|apply|erase)\b`),
	}

	found := map[string]map[string][]string{"Tenant": {}, "Holder": {}}
	read := func(p string) {
		b, err := os.ReadFile(p)
		x.NoError(err)
		for _, re := range calls {
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				verb := strings.ToLower(m[2])
				if verb == "addmany" {
					verb = "add"
				}
				found[m[1]][verb] = append(found[m[1]][verb], p)
			}
		}
	}

	read(filepath.Join("..", "apptest", "server", "pd", "pd.g.go"))
	for _, root := range []string{
		filepath.Join("..", "pdcli", "template"),
		filepath.Join("..", "pdcli", "template-sandbox"),
		filepath.Join("..", ".."),
	} {
		x.NoError(filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", "gen", ".git":
					return fs.SkipDir
				case "internal":
					// The generator and the test app, which are read as what
					// they write above, and templates, read on their own.
					if root == filepath.Join("..", "..") {
						return fs.SkipDir
					}
				}
				return nil
			}
			switch {
			case strings.HasSuffix(p, "_test.go"), strings.HasSuffix(p, ".test.ts"), strings.HasSuffix(p, ".test.tsx"):
			case strings.HasSuffix(p, ".pb.go"), strings.HasSuffix(p, ".g.go"):
			case strings.HasSuffix(p, ".go"), strings.HasSuffix(p, ".tmpl"), strings.HasSuffix(p, ".ts"), strings.HasSuffix(p, ".tsx"):
				read(p)
			}
			return nil
		}))
	}

	for entity, own := range map[string]pdpb.Own{"Tenant": pdpb.Own_OWN_TENANT, "Holder": pdpb.Own_OWN_HOLDER} {
		listed := pdgen.PaydayCalls[own]
		called := []string{}
		for verb := range found[entity] {
			called = append(called, verb)
		}
		sort.Strings(called)
		for _, verb := range called {
			x.Contains(listed, verb, "payday calls %s's %s (%s), and paydayCalls does not say so: an overlay may switch it off",
				entity, verb, strings.Join(found[entity][verb], ", "))
		}
		for verb := range listed {
			x.Contains(called, verb, "paydayCalls says payday calls %s's %s, and nothing does", entity, verb)
		}
	}
}
