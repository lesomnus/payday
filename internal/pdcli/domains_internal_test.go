package pdcli

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/schema"
)

// TestTheFilesNumberedAreTheOnesThatShip: `pd gen` numbers payday's files by
// their names, and what may be numbered is said by marker (`pdgen.Numbered`).
// A file renamed, or a marker moved to another file, would be a declaration
// written into nothing -- which generation then refuses, in a file the app did
// not write.
func TestTheFilesNumberedAreTheOnesThatShip(t *testing.T) {
	x := require.New(t)

	owns := []pdpb.Own{}
	for _, v := range pdgen.Numbered {
		owns = append(owns, v.Own)
	}
	names := []pdpb.Own{}
	for _, v := range numberedFiles {
		names = append(names, v.Own)

		b, err := fs.ReadFile(schema.FS(), schema.DirOwn+"/"+v.Name)
		x.NoError(err, "payday ships no %s", v.Name)
		x.Contains(string(b), "message "+v.Message+" {")
		x.Contains(string(b), "own: "+v.Own.String()+"\n")

		was, err := pdgen.Shipped(v.Own)
		x.NoError(err)
		_, err = numbered(b, was, 240)
		x.NoError(err, "%s is not numbered as it ships", v.Name)
	}
	x.ElementsMatch(owns, names)
}

// TestOnlyTheOneLineIsNumbered: a file of payday's is numbered by writing over
// the one `domain:` it says, and only when it says the one this expects --
// anything else is a template this was not written for.
func TestOnlyTheOneLineIsNumbered(t *testing.T) {
	x := require.New(t)

	v, err := numbered([]byte("message M {\n  option (payday.entity) = {\n    own: OWN_ARCHIVED\n    domain: 5\n  };\n}\n"), 5, 240)
	x.NoError(err)
	x.Equal("message M {\n  option (payday.entity) = {\n    own: OWN_ARCHIVED\n    domain: 240\n  };\n}\n", string(v))

	for name, src := range map[string]string{
		"none":    "message M {}\n",
		"two":     "  domain: 5\n  domain: 5\n",
		"another": "  domain: 6\n",
	} {
		_, err := numbered([]byte(src), 5, 240)
		x.Error(err, "%s was numbered", name)
	}
}
