package pdcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
)

// What `pd doctor` asks of an app's copy of its tenants' entitlements
// (payday#185): that something keeps it, that what keeps it has a source and
// a copy, and that it runs -- read off the app's own Go, as the meters' are.

// following is an app of `files`, and what the doctor says of its copy when
// the schema declares entitlements, or does not.
func following(t *testing.T, declared bool, files map[string]string) string {
	t.Helper()
	x := require.New(t)

	root := t.TempDir()
	for name, src := range files {
		p := filepath.Join(root, name)
		x.NoError(os.MkdirAll(filepath.Dir(p), 0o755))
		x.NoError(os.WriteFile(p, []byte(src), 0o644))
	}

	s := &pdgen.Schema{}
	if declared {
		s.Entitlements = &pdgen.Entitlements{Domain: 43, At: "proto/thing/thing.proto"}
	}

	l := Layout{Root: root, Work: root, Module: "github.com/acme/thing"}
	said := []string{}
	for _, v := range doctorEntitlements(l, s) {
		said = append(said, v.String())
	}

	return strings.Join(said, "\n")
}

const followed = `package cmd

import (
	"github.com/lesomnus/payday/entitle"
	"github.com/lesomnus/payday/pdpb"

	"github.com/acme/thing/server/pd"
)

type Server struct {
	Follower *entitle.Follower
	Spin     []any
}

func Build(conn any) *Server {
	s := &Server{}
	s.Follower = &entitle.Follower{
		Source: pdpb.NewGrantServiceClient(conn),
		Copy:   pd.EntitlementCopy(nil, nil),
	}
	s.Spin = append(s.Spin, s.Follower)

	return s
}
`

// TestTheFollowingTheGuideGivesIsWell, which is the half that fails if this
// learns to complain about something ordinary.
func TestTheFollowingTheGuideGivesIsWell(t *testing.T) {
	require.Empty(t, following(t, true, map[string]string{"cmd/serve.go": followed}))

	// And an app that declares none and builds no follower is asked nothing.
	require.Empty(t, following(t, false, map[string]string{"cmd/serve.go": "package cmd\n"}))
}

// TestACopyNothingKeepsIsSaid: declared, and no follower anywhere -- so no
// source of grants is configured at all. A follower in a test does not count.
func TestACopyNothingKeepsIsSaid(t *testing.T) {
	x := require.New(t)

	said := following(t, true, map[string]string{"cmd/serve.go": "package cmd\n"})
	x.Contains(said, "the schema declares entitlements and nothing keeps the copy")
	x.Contains(said, "pd.EntitlementCopy(drv, content)")

	said = following(t, true, map[string]string{
		"cmd/serve.go":      "package cmd\n",
		"cmd/serve_test.go": followed,
	})
	x.Contains(said, "nothing keeps the copy")
}

// TestAFollowerWithNoSourceIsSaid, and one with no copy; and one given its
// source after it is built is not.
func TestAFollowerWithNoSourceIsSaid(t *testing.T) {
	x := require.New(t)

	src := strings.Replace(followed, "\t\tSource: pdpb.NewGrantServiceClient(conn),\n", "", 1)
	x.Contains(following(t, true, map[string]string{"cmd/serve.go": src}),
		"cmd/serve.go:17 builds an entitle.Follower with no Source -- no source of grants is configured")

	nilled := strings.Replace(followed, "Source: pdpb.NewGrantServiceClient(conn),", "Source: nil,", 1)
	x.Contains(following(t, true, map[string]string{"cmd/serve.go": nilled}), "with no Source")

	later := strings.Replace(src, "\ts.Spin = append(s.Spin, s.Follower)",
		"\ts.Follower.Source = pdpb.NewGrantServiceClient(conn)\n\ts.Spin = append(s.Spin, s.Follower)", 1)
	x.Empty(following(t, true, map[string]string{"cmd/serve.go": later}))

	uncopied := strings.Replace(followed, "\t\tCopy:   pd.EntitlementCopy(nil, nil),\n", "", 1)
	said := following(t, true, map[string]string{"cmd/serve.go": uncopied})
	x.Contains(said, "builds an entitle.Follower with no Copy")
	x.NotContains(said, "with no Source")
}

// TestAFollowerThatDoesNotSpinIsSaid: built, and never in a spin list, which
// keeps nothing.
func TestAFollowerThatDoesNotSpinIsSaid(t *testing.T) {
	x := require.New(t)

	src := strings.Replace(followed, "\ts.Spin = append(s.Spin, s.Follower)\n", "", 1)
	said := following(t, true, map[string]string{"cmd/serve.go": src})
	x.Contains(said, "cmd/serve.go:17 builds an entitle.Follower that is never handed to spin.Run")
	x.Contains(said, "s.Spin = append(s.Spin, s.Follower)")

	// Handed over however an app hands it.
	for name, v := range map[string]string{
		"in place": `package cmd

import "github.com/lesomnus/payday/entitle"

func Build(src any, c entitle.Copy, spin []any) []any {
	return append(spin, entitle.Follower{Source: src, Copy: c})
}
`,
		"from a function": `package cmd

import "github.com/lesomnus/payday/entitle"

func follower(src any, c entitle.Copy) *entitle.Follower {
	return &entitle.Follower{Source: src, Copy: c}
}

func Build(src any, c entitle.Copy, spin []any) []any {
	return append(spin, follower(src, c))
}
`,
		"in a slice, under another name": `package cmd

import e "github.com/lesomnus/payday/entitle"

func Build(src any, c e.Copy) []any {
	f := e.Follower{Source: src, Copy: c}
	return []any{f}
}
`,
	} {
		x.Empty(following(t, true, map[string]string{"cmd/serve.go": v}), name)
	}
}
