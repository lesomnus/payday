package pdcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
)

// What `pd doctor` asks of an app's meters (payday#186): that what the schema
// declares is read, that a reader has a sink, and that it runs -- read off the
// app's own Go, so each case is a few lines of it in a module of its own.

// wired is an app of `files`, Go written into its module, and what the doctor
// says of its meters when the schema declares `keys`.
func wired(t *testing.T, keys []string, files map[string]string) string {
	t.Helper()
	x := require.New(t)

	root := t.TempDir()
	for name, src := range files {
		p := filepath.Join(root, name)
		x.NoError(os.MkdirAll(filepath.Dir(p), 0o755))
		x.NoError(os.WriteFile(p, []byte(src), 0o644))
	}

	s := &pdgen.Schema{}
	for _, k := range keys {
		s.Meters = append(s.Meters, &pdgen.Meter{Key: k})
	}

	l := Layout{Root: root, Work: root, Module: "github.com/acme/thing"}
	said := []string{}
	for _, v := range doctorMeters(l, s) {
		said = append(said, v.String())
	}

	return strings.Join(said, "\n")
}

const serving = `package cmd

import (
	"github.com/lesomnus/payday/meter"

	"github.com/acme/thing/server/pd"
)

type Server struct {
	Meters *meter.Reader
	Spin   []any
}

func Build(sink meter.Sink) *Server {
	s := &Server{}
	s.Meters = &meter.Reader{
		Tenants: pd.MeterTenants(nil),
		Meters:  pd.Meters(nil),
		Sink:    sink,
	}
	s.Spin = append(s.Spin, s.Meters)

	return s
}
`

// TestTheWiringTheGuideGivesIsWell, which is the half that fails if this
// learns to complain about something ordinary.
func TestTheWiringTheGuideGivesIsWell(t *testing.T) {
	require.Empty(t, wired(t, []string{"acme.robots"}, map[string]string{"cmd/serve.go": serving}))

	// And an app that declares none and builds no reader is asked nothing.
	require.Empty(t, wired(t, nil, map[string]string{"cmd/serve.go": "package cmd\n"}))
}

// TestDeclaredMetersNothingReadsAreSaid: the schema's meters, and no
// `pd.Meters` to read them -- an app that serves, and bills nobody.
func TestDeclaredMetersNothingReadsAreSaid(t *testing.T) {
	x := require.New(t)

	said := wired(t, []string{"acme.robots", "acme.bytes"}, map[string]string{"cmd/serve.go": "package cmd\n"})
	x.Contains(said, "the schema declares 2 meters -- acme.robots, acme.bytes -- and nothing reads them")
	x.Contains(said, "Meters:  pd.Meters(client)")

	// A reader of the app's own meters alone leaves the schema's unread.
	said = wired(t, []string{"acme.robots"}, map[string]string{"cmd/serve.go": strings.Replace(serving,
		"pd.Meters(nil)", "[]meter.Meter{}", 1)})
	x.Contains(said, "nothing reads them")

	// And a test that reads them is not the app reading them.
	said = wired(t, []string{"acme.robots"}, map[string]string{
		"cmd/serve.go":      "package cmd\n",
		"cmd/serve_test.go": serving,
	})
	x.Contains(said, "nothing reads them")
}

// TestAReaderWithNoSinkIsSaid, and one whose sink is set after it is built is
// not.
func TestAReaderWithNoSinkIsSaid(t *testing.T) {
	x := require.New(t)

	src := strings.Replace(serving, "\t\tSink:    sink,\n", "", 1)
	x.Contains(wired(t, []string{"acme.robots"}, map[string]string{"cmd/serve.go": src}),
		"cmd/serve.go:16 builds a meter.Reader with no Sink")

	nilled := strings.Replace(serving, "Sink:    sink,", "Sink:    nil,", 1)
	x.Contains(wired(t, []string{"acme.robots"}, map[string]string{"cmd/serve.go": nilled}), "with no Sink")

	later := strings.Replace(src, "\ts.Spin = append(s.Spin, s.Meters)", "\ts.Meters.Sink = sink\n\ts.Spin = append(s.Spin, s.Meters)", 1)
	x.Empty(wired(t, []string{"acme.robots"}, map[string]string{"cmd/serve.go": later}))
}

// TestAReaderThatDoesNotSpinIsSaid: built, and never in a spin list -- which
// measures a tenant when the app asks for one, and on no schedule.
func TestAReaderThatDoesNotSpinIsSaid(t *testing.T) {
	x := require.New(t)

	src := strings.Replace(serving, "\ts.Spin = append(s.Spin, s.Meters)\n", "", 1)
	said := wired(t, []string{"acme.robots"}, map[string]string{"cmd/serve.go": src})
	x.Contains(said, "cmd/serve.go:16 builds a meter.Reader that is never handed to spin.Run")
	x.Contains(said, "s.Spin = append(s.Spin, s.Meters)")

	// Handed over however an app hands it: in place, as what a function
	// answers with, and in a slice of its own.
	for name, v := range map[string]string{
		"in place": `package cmd

import "github.com/lesomnus/payday/meter"

func Build(sink meter.Sink, spin []any) []any {
	return append(spin, &meter.Reader{Sink: sink})
}
`,
		"from a function": `package cmd

import "github.com/lesomnus/payday/meter"

func meters(sink meter.Sink) meter.Reader {
	return meter.Reader{Sink: sink}
}

func Build(sink meter.Sink, spin []any) []any {
	return append(spin, meters(sink))
}
`,
		"in a slice": `package cmd

import "github.com/lesomnus/payday/meter"

func Build(sink meter.Sink) []any {
	r := meter.Reader{Sink: sink}
	return []any{r}
}
`,
		"under another name": `package cmd

import m "github.com/lesomnus/payday/meter"

func Build(sink m.Sink, spin []any) []any {
	var r = &m.Reader{Sink: sink}
	return append(spin, r)
}
`,
	} {
		x.Empty(wired(t, nil, map[string]string{"cmd/serve.go": v}), name)
	}
}
