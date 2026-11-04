package pdgen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/internal/pdgen"
)

// TestAFormerNameIsRead: what an entity was called is its message's full name
// then, the word it was written as -- the message's in kebab-case, or what the
// schema says it declared -- and the table its rows were in.
func TestAFormerNameIsRead(t *testing.T) {
	x := require.New(t)

	s, err := read(t, tenant+
		entity("Domain", `domain: 24, global: {}, formerly: [{message: "TenantDomain"}]`)+
		entity("Key", `domain: 25, global: {}, formerly: [{message: "SshKey", name: "key"}, {message: "AccessKey"}]`))
	x.NoError(err)

	by := map[string]*pdgen.Entity{}
	for _, v := range s.Entities {
		by[v.GoName()] = v
	}
	x.Equal([]pdgen.Former{{FullName: "test.TenantDomain", Name: "tenant-domain", Table: "tenantdomain"}}, by["Domain"].Formerly)
	x.Equal([]pdgen.Former{
		{FullName: "test.SshKey", Name: "key", Table: "sshkey"},
		{FullName: "test.AccessKey", Name: "access-key", Table: "accesskey"},
	}, by["Key"].Formerly)
	x.Empty(by["Tenant"].Formerly)
}

// TestAFormerNameIsWhatIsGenerated: each name is registered with the domain,
// and each table another name was in is moved onto the one the entity is in
// now -- and nothing for a name of the same letters in another case, whose
// table is the one it has.
func TestAFormerNameIsWhatIsGenerated(t *testing.T) {
	x := require.New(t)

	s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("test", tenant+
		entity("Domain", `domain: 24, global: {}, formerly: [{message: "TenantDomain"}, {message: "DOMAIN"}]`))})
	x.NoError(err)

	g := p.NewGeneratedFile("pd.go", "example.com/app/server/pd")
	g.P("package pd")
	g.P()
	pdgen.EmitDomains(g, s)
	pdgen.EmitTableRenames(g, s)
	b, err := g.Content()
	x.NoError(err)
	out := string(b)

	x.Contains(out, `pdid.Register("test.Domain", DomainDomain, "domain")`)
	x.Contains(out, `pdid.Formerly("test.TenantDomain", DomainDomain, "tenant-domain")`)
	x.Contains(out, `pdid.Formerly("test.DOMAIN", DomainDomain, "d-o-m-a-i-n")`)
	x.Contains(out, `{From: "tenantdomain", To: "domain"},`)
	x.Equal(1, strings.Count(out, "{From: "), "a table of the same name moves nowhere")

	x.Equal([]pdgen.TableRename{{From: "tenantdomain", To: "domain"}}, pdgen.TableRenames(s))

	t.Run("and an app that renamed nothing has none", func(t *testing.T) {
		x := require.New(t)

		s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("test", tenant+entity("Domain", `domain: 24, global: {}`))})
		x.NoError(err)
		g := p.NewGeneratedFile("pd.go", protogen.GoImportPath("example.com/app/server/pd"))
		g.P("package pd")
		pdgen.EmitTableRenames(g, s)
		b, err := g.Content()
		x.NoError(err)
		x.Contains(string(b), "var TableRenames = []rename.Table{}")
		x.NotContains(string(b), "pdid.Formerly")
	})
}

// TestAFormerNameNobodyElseHasIsAll: a name an entity had is never given to
// another, which a domain number is not either -- an earlier build wrote it
// down meaning the one that had it, and it would be read back as the other.
// A full name, a word or a table: whoever has it now, or had it.
func TestAFormerNameNobodyElseHasIsAll(t *testing.T) {
	for name, c := range map[string]struct {
		src  string
		want string
	}{
		"said without a message": {
			entity("Domain", `domain: 24, global: {}, formerly: [{name: "tenant-domain"}]`),
			"says no `message`",
		},
		"a full name where its name goes": {
			entity("Domain", `domain: 24, global: {}, formerly: [{message: "test.TenantDomain"}]`),
			"is no message's name",
		},
		"what it is called now": {
			entity("Domain", `domain: 24, global: {}, formerly: [{message: "Domain"}]`),
			"is what it is called now",
		},
		"said twice": {
			entity("Domain", `domain: 24, global: {}, formerly: [{message: "TenantDomain"}, {message: "TenantDomain"}]`),
			"said twice",
		},
		"what another is called now": {
			entity("Domain", `domain: 24, global: {}, formerly: [{message: "Cell"}]`) +
				entity("Cell", `domain: 25, global: {}`),
			"test.Cell, which test.Cell is too",
		},
		"the word another is written as now": {
			entity("Domain", `domain: 24, global: {}, formerly: [{message: "Place", name: "cell"}]`) +
				entity("Cell", `domain: 25, global: {}`),
			`written "cell", which test.Cell is too`,
		},
		"the table another is in now": {
			entity("Domain", `domain: 24, global: {}, formerly: [{message: "WORKCELL"}]`) +
				entity("WorkCell", `domain: 25, global: {}`),
			`in the table "workcell", which test.WorkCell is too`,
		},
		"what another was": {
			entity("Domain", `domain: 24, global: {}, formerly: [{message: "Place"}]`) +
				entity("Cell", `domain: 25, global: {}, formerly: [{message: "Place"}]`),
			"which test.Domain was too",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := read(t, tenant+c.src)
			require.ErrorContains(t, err, c.want)
		})
	}
}
