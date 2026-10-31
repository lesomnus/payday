package pdcli

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/schema"
)

// TestTheCopyThatIsWrittenIsTheCopyThatShips: `pd gen` copies the file it
// names, and payday ships what is in `schema/entitle` -- numbered at the
// app's domain, and taken out when the declaration goes.
func TestTheCopyThatIsWrittenIsTheCopyThatShips(t *testing.T) {
	x := require.New(t)

	ships, err := fs.ReadDir(schema.FS(), schema.DirEntitle)
	x.NoError(err)
	files := []string{}
	for _, v := range ships {
		files = append(files, v.Name())
	}
	names := []string{}
	features, err := schema.Features()
	x.NoError(err)
	for _, v := range entitleFiles {
		names = append(names, v.Name)
		x.Equal(schema.DirEntitle, features[v.Own])
	}
	x.ElementsMatch(files, names)

	root, dst := t.TempDir(), t.TempDir()
	l := Layout{Root: root, ProtoPkg: "app", Module: "example.com/app", Pkg: "example.com/app",
		Entitlements: &Entitlements{Domain: 43, At: "proto/app/app.proto"}}
	x.NoError(os.MkdirAll(l.Path(DirExt, "payday"), 0o755))

	g := Gen{Layout: l}
	x.NoError(g.entitlements(context.Background(), dst))
	b, err := os.ReadFile(filepath.Join(dst, "entitlement.proto"))
	x.NoError(err)
	x.Contains(string(b), "domain: 43")
	x.Contains(string(b), "package app;")
	x.Contains(string(b), `import "app/payday/tenant.proto";`)
	x.Contains(string(b), "own: OWN_ENTITLEMENT")

	g.Layout.Entitlements = nil
	x.NoError(g.entitlements(context.Background(), dst))
	_, err = os.Stat(filepath.Join(dst, "entitlement.proto"))
	x.True(os.IsNotExist(err), "the copy survived its declaration")
}

// TestAnAppWithEntitlementsHasTheCopysNamesTaken: a message, an enum or a
// service of the copy's names, or a file of its name, is refused once the app
// declares entitlements, and is the app's own business until then.
func TestAnAppWithEntitlementsHasTheCopysNamesTaken(t *testing.T) {
	x := require.New(t)

	root := t.TempDir()
	x.NoError(os.MkdirAll(filepath.Join(root, DirProto, "app"), 0o755))
	p := filepath.Join(root, DirProto, "app", "plan.proto")
	l := Layout{Root: root, ProtoPkg: "app"}

	for _, src := range []string{
		"message Entitlement {}",
		"enum EntitlementValue { ENTITLEMENT_VALUE_UNSPECIFIED = 0; }",
		"service EntitlementService {}",
	} {
		x.NoError(os.WriteFile(p, []byte("edition = \"2023\"; package app; "+src), 0o644))

		l.Entitlements = nil
		x.NoError(checkEntitleNames(l), src)

		l.Entitlements = &Entitlements{Domain: 43, At: "proto/app/app.proto"}
		x.ErrorContains(checkEntitleNames(l), "declares entitlements", src)
	}

	// Nested is named after what it is in.
	x.NoError(os.WriteFile(p, []byte("edition = \"2023\"; package app; message Plan { message Entitlement {} }"), 0o644))
	x.NoError(checkEntitleNames(l))

	// And a file of the copy's name is one Go file twice.
	x.NoError(os.Remove(p))
	x.NoError(os.WriteFile(filepath.Join(root, DirProto, "app", "entitlement.proto"), []byte("edition = \"2023\"; package app;"), 0o644))
	x.ErrorContains(checkEntitleNames(l), "entitlement.pb.go")
}

// TestEntitlementsAreDeclaredOnceAtANumberOfTheirOwn: read off the file that
// says `option (payday.app)`, held to what the same app numbers, and said in
// one file.
func TestEntitlementsAreDeclaredOnceAtANumberOfTheirOwn(t *testing.T) {
	x := require.New(t)

	declared := func(t *testing.T, files map[string]string) (Layout, error) {
		t.Helper()

		root := t.TempDir()
		for name, src := range files {
			p := filepath.Join(root, DirProto, "app", name)
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte(src), 0o644))
		}

		l := Layout{Root: root, ProtoPkg: "app"}
		var err error
		if l.Past, err = readPast(l); err != nil {
			return l, err
		}
		if l.Notify, err = readNotify(l); err != nil {
			return l, err
		}
		if l.Domains, err = readDomains(l); err != nil {
			return l, err
		}
		l.Entitlements, err = readEntitlements(l)

		return l, err
	}
	head := "edition = \"2023\";\npackage app;\nimport \"payday.proto\";\n"

	l, err := declared(t, map[string]string{"app.proto": head + "option (payday.app) = {entitlements: {domain: 43}};\n"})
	x.NoError(err)
	x.Equal(uint32(43), l.Entitlements.Domain)
	x.Equal("proto/app/app.proto", l.Entitlements.At)

	l, err = declared(t, map[string]string{"app.proto": head + "option (payday.app) = {past: {event: 17, fact: 16}};\n"})
	x.NoError(err)
	x.Nil(l.Entitlements)

	for src, says := range map[string]string{
		"option (payday.app) = {entitlements: {}};":                                                    "says no `domain:`",
		"option (payday.app) = {entitlements: {domain: 3}};":                                           "is payday's",
		"option (payday.app) = {past: {event: 17, fact: 43}, entitlements: {domain: 43}};":             "past.fact",
		"option (payday.app) = {notify: {notification: 43, receipt: 41}, entitlements: {domain: 43}};": "notify.notification",
		"option (payday.app) = {domains: {archived: 43}, entitlements: {domain: 43}};":                 "domains.archived",
	} {
		_, err := declared(t, map[string]string{"app.proto": head + src + "\n"})
		x.ErrorContains(err, says, src)
	}

	_, err = declared(t, map[string]string{
		"app.proto":   head + "option (payday.app) = {entitlements: {domain: 43}};\n",
		"other.proto": head + "option (payday.app) = {entitlements: {domain: 44}};\n",
	})
	x.ErrorContains(err, "both declare entitlements")
}
