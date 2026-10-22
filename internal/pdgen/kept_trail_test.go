package pdgen_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/internal/pdgen"
)

// TestTheRecorderKeepsOnlyATrailItCanWriteWhole: the recorder writes a row of
// the trail itself to keep it for the commit, and can only when every column is
// one it knows -- payday's own. A column an overlay added is one only the server
// generated with it can fill, so that trail is written a row at a time, as it
// was, rather than kept and written without it.
func TestTheRecorderKeepsOnlyATrailItCanWriteWhole(t *testing.T) {
	emit := func(t *testing.T, src string) string {
		t.Helper()

		s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("fleet", src)})
		if err != nil {
			t.Fatal(err)
		}

		g := p.NewGeneratedFile("audit.go", "example.com/app/server/pd")
		g.P("package pd")
		g.P()
		pdgen.EmitAudit(g, s, pdgen.Paths{
			Ent:  "example.com/app/internal/ent",
			Bare: "example.com/app/server/bare",
		}, protogen.GoImportPath("example.com/app"))

		b, err := g.Content()
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	own := ownAll(t)
	if out := emit(t, own); !strings.Contains(out, "keptTrailOf(k, s.Store.Db).add(ctx, q)") {
		t.Error("payday's own trail is written a statement a row")
	}

	more := strings.Replace(own, "message Audit {", "message Audit {\n  string note = 30;", 1)
	if more == own {
		t.Fatal("no Audit to add a column to")
	}
	if out := emit(t, more); strings.Contains(out, "keptTrailOf") {
		t.Error("a trail with a column the recorder cannot fill is kept for the commit, and written without it")
	}
}
