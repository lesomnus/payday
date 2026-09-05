package pdcli_test

import (
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// TestTheTemplateIsWhatAPersonWrites, and nothing that a generator does.
//
// A template carrying generated code is a template carrying the output of one
// version of the generators, and the first thing anybody does with it is
// regenerate. So what is checked here is that none of it is there -- and the
// slow test below is what says the rest of it appears when `pd gen` runs.
func TestTheTemplateIsWhatAPersonWrites(t *testing.T) {
	x := require.New(t)

	dir := filepath.Join(t.TempDir(), "app")
	x.NoError((pdcli.New{Dir: dir, Module: "github.com/acme/thing"}).Write())

	var vs []string
	x.NoError(filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, _ := filepath.Rel(dir, p)
		vs = append(vs, filepath.ToSlash(rel))

		return nil
	}))

	for _, v := range vs {
		x.NotContains(v, ".g.go", "%s is generated", v)
		x.NotContains(v, ".pb.go", "%s is generated", v)
		x.NotContains(v, "gen/", "%s is generated", v)
	}

	// And the pieces a person does write.
	for _, want := range []string{
		"go.mod",
		"buf.yaml",
		"cmd/serve.go",
		"cmd/config.go",
		"cmd/auth.go",
		"cmd/thing/main.go",
		"proto/app/thing.proto",
		"thing.yaml",
		"ts/src/client.ts",
	} {
		x.Contains(vs, want)
	}
}

// TestTheTemplateIsGoThatParses, which is less than it sounds and more than
// there was.
//
// Nothing compiles what `pd new` writes. Compiling it means a generation first
// -- half the imports are packages `pd gen` has not written yet -- and that
// means fetching a module graph, so the honest place for it is CI on a real
// scaffold rather than a unit test that pretends to be fast.
//
// Parsing needs none of that and catches the mistake that actually happens:
// somebody edits a `.tmpl` by hand, and the first person to find out is
// whoever ran `pd new` and got a file that will not build. It does not catch a
// wrong identifier or a missing import, so it is a floor and not a check.
func TestTheTemplateIsGoThatParses(t *testing.T) {
	x := require.New(t)

	dir := filepath.Join(t.TempDir(), "app")
	x.NoError((pdcli.New{Dir: dir, Module: "github.com/acme/thing"}).Write())

	n := 0
	x.NoError(filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".go" {
			return err
		}

		n++
		_, err = parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)

		return err
	}))

	// The count is here so that a template that stopped writing Go at all --
	// a rename, a walk that lands somewhere else -- fails rather than passes
	// with nothing to say.
	x.NotZero(n)
}

// TestTheTemplateIsGofmtd, which parsing does not say.
//
// The `.tmpl` suffix is what keeps the template out of the repository's own
// `gofmt -l .`, and what it writes is Go nobody runs a formatter over -- so an
// import in the wrong place travels to every app `pd new` writes and is first
// noticed by the person who ran it. It happened: the move to the ent fork put
// `github.com/protobuf-orm/...` above `github.com/lesomnus/...` in one group,
// and CI's own check for this sat behind a build error for as long as the
// template also imported a package payday had removed.
//
// Formatting is worth a test rather than a shrug because this is the one file
// in the repository whose output is somebody else's starting point.
func TestTheTemplateIsGofmtd(t *testing.T) {
	x := require.New(t)

	dir := filepath.Join(t.TempDir(), "app")
	x.NoError((pdcli.New{Dir: dir, Module: "github.com/acme/thing"}).Write())

	n := 0
	x.NoError(filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".go" {
			return err
		}

		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		want, err := format.Source(src)
		if err != nil {
			return err
		}

		n++
		rel, _ := filepath.Rel(dir, p)
		x.Equal(string(want), string(src), "%s is not gofmt'd", rel)

		return nil
	}))

	x.NotZero(n)
}

// TestTheTemplateImportsWhatGenerationWrites.
//
// The template's TypeScript names generated modules by path, and nothing else
// checks that those paths are the ones `pd gen --ts` produces: neither half is
// compiled here. The Go half is parsed rather than compiled -- see
// [TestTheTemplateIsGoThatParses] for what that does and does not catch -- and
// the TypeScript half is not read by anything, since it needs an `npm install`
// in a tree that has been generated.
//
// So it was wrong, in four lines, from the first commit. `gen/` mirrors
// `proto/`, and payday's entities are copied **into** the app's proto package
// -- `gen/app/payday/tenant_svc_pb.js`. The template asked for
// `gen/payday/tenant_svc_pb.js`, and the reason that is easy to write and hard
// to see is that `gen/payday/` **exists**: it holds `entity_pb.ts`, the
// descriptor for the `(payday.entity)` option. The import resolves to a real
// directory and finds no module in it.
//
// What this derives the answer from is the two things a generation reads -- the
// app's own `.proto` files and payday's schema directory -- rather than a list
// written out here, which would be the same mistake one level up.
func TestTheTemplateImportsWhatGenerationWrites(t *testing.T) {
	x := require.New(t)

	dir := filepath.Join(t.TempDir(), "app")
	x.NoError((pdcli.New{Dir: dir, Module: "github.com/acme/thing"}).Write())

	// payday writes these itself, at the root of `gen/` rather than under any
	// package: they are one declaration per entity for the local store, and
	// there is no `.proto` they correspond to.
	want := []string{"../gen/entities.js", "../gen/domains.js"}

	// One `.proto` becomes a module for its messages and one for the contract
	// generated from it, both named after the file.
	add := func(from string, under string) {
		vs, err := os.ReadDir(from)
		x.NoError(err)

		for _, v := range vs {
			name := v.Name()
			if !strings.HasSuffix(name, ".proto") || strings.HasSuffix(name, ".g.proto") {
				continue
			}

			stem := strings.TrimSuffix(name, ".proto")
			want = append(want, under+stem+"_pb.js", under+stem+"_svc_pb.js")
		}
	}

	pkg := "../gen/" + pdcli.ProtoPkgDefault + "/"
	add(filepath.Join(dir, pdcli.DirProto, pdcli.ProtoPkgDefault), pkg)

	// And payday's, **inside** the app's package rather than beside it. This is
	// the line the bug was in.
	schema, err := pdcli.SchemaDir()
	x.NoError(err)
	add(schema, pkg+"payday/")

	// Every generated module the template names.
	from := regexp.MustCompile(`from '(\.\./gen/[^']+)'`)

	var got []string
	root := filepath.Join(dir, "ts", "src")
	x.NoError(filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(dir, p)
		for _, m := range from.FindAllSubmatch(b, -1) {
			got = append(got, string(m[1])+"\t"+filepath.ToSlash(rel))
		}

		return nil
	}))

	x.NotEmpty(got, "the template names no generated module, so this checks nothing")

	for _, v := range got {
		path, where, _ := strings.Cut(v, "\t")
		x.True(slices.Contains(want, path),
			"%s imports %s, which no generation writes.\n\nWhat there is:\n  %s",
			where, path, strings.Join(want, "\n  "))
	}
}

// TestTheNameIsWrittenOnce, which is what everything about where configuration
// comes from is derived from.
func TestTheNameIsWrittenOnce(t *testing.T) {
	x := require.New(t)

	dir := filepath.Join(t.TempDir(), "app")
	x.NoError((pdcli.New{Dir: dir, Module: "github.com/acme/Thing-Server"}).Write())

	// Folded into something that can be an alias, since that is what it is used
	// as: the prefix of every environment variable and the name of the file.
	b, err := os.ReadFile(filepath.Join(dir, "cmd", "config.go"))
	x.NoError(err)
	x.Contains(string(b), `Name = "thing-server"`)

	_, err = os.Stat(filepath.Join(dir, "thing-server.yaml"))
	x.NoError(err)

	_, err = os.Stat(filepath.Join(dir, "cmd", "thing-server", "main.go"))
	x.NoError(err)

	// And the module goes everywhere an import does.
	b, err = os.ReadFile(filepath.Join(dir, "cmd", "serve.go"))
	x.NoError(err)
	x.Contains(string(b), `"github.com/acme/Thing-Server/server/pd"`)
	x.NotContains(string(b), "apptest")
}

// TestWhereItWentIsAnswerableAfterwards.
//
// [pdcli.New] takes a value, so the defaults `Write` applies are gone when it
// returns -- and every caller that needed to know where the app went filled
// them in again for itself. One of them got it wrong in the direction nobody
// tests: `pd new github.com/acme/widget`, with no DIR, wrote `widget/` and then
// said the app was in "", because it read the empty fields rather than the
// rule. So the rule is a method, and this is what says the method answers for
// a `New` nobody filled in.
func TestWhereItWentIsAnswerableAfterwards(t *testing.T) {
	for _, tc := range []struct {
		desc   string
		n      pdcli.New
		where  string
		called string
	}{
		{
			desc:   "nothing but a module",
			n:      pdcli.New{Module: "github.com/acme/widget"},
			where:  "widget",
			called: "widget",
		},
		{
			desc:   "a name of its own",
			n:      pdcli.New{Module: "github.com/acme/thing", Name: "gizmo"},
			where:  "gizmo",
			called: "gizmo",
		},
		{
			desc:   "a directory of its own",
			n:      pdcli.New{Module: "github.com/acme/thing", Dir: "somewhere/else"},
			where:  "somewhere/else",
			called: "thing",
		},
		{
			desc:   "folded, the way the name is everywhere else",
			n:      pdcli.New{Module: "github.com/acme/Thing-Server"},
			where:  "thing-server",
			called: "thing-server",
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)
			x.Equal(tc.where, tc.n.Where())
			x.Equal(tc.called, tc.n.Called())
		})
	}

	// And it is the directory `Write` really used, not a second opinion about
	// it: the one thing a caller does with the answer is tell a person where to
	// cd to.
	t.Run("and it is where Write put it", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		t.Chdir(root)

		n := pdcli.New{Module: "github.com/acme/widget"}
		x.NoError(n.Write())

		_, err := os.Stat(filepath.Join(root, n.Where(), "go.mod"))
		x.NoError(err)
	})
}

// TestWritingOverSomebodyElsesWorkIsRefused, which is the one mistake here that
// deleting something does not undo.
func TestWritingOverSomebodyElsesWorkIsRefused(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	x.NoError(os.WriteFile(filepath.Join(dir, "README.md"), []byte("mine"), 0o644))

	err := (pdcli.New{Dir: dir, Module: "github.com/acme/thing"}).Write()
	x.ErrorContains(err, "not empty")
}

// TestTheStepsAreInAnOrderThatWorks.
//
// None of it is guessable, and every one of these was found by typing the
// printed steps into an empty directory and watching one of them fail.
//
//   - `go tool pd` builds payday's command from **this app's** module graph, so
//     the tool directive has to have been fetched like any other;
//   - the generators have to build before anything is generated, and that needs
//     sums -- but a plain `go mod tidy` cannot run until the generated packages
//     exist, since a module that is not there resolves as one to fetch. So:
//     `-e` first, and the real one after;
//   - and `buf dep update` is not a step at all. It compiles the workspace
//     before writing the lock, and the workspace does not compile until
//     `pd gen` has copied payday's entities in -- so as a first step it fails
//     on every new app. `pd gen` owns it; see `run.deps`.
func TestTheStepsAreInAnOrderThatWorks(t *testing.T) {
	x := require.New(t)

	vs := (pdcli.New{Module: "github.com/acme/thing"}).Steps()
	joined := strings.Join(vs, "\n")

	x.Contains(joined, "go get -tool github.com/lesomnus/payday/cmd/pd")

	tools := strings.LastIndex(joined, "go get -tool")
	lenient := strings.Index(joined, "go mod tidy -e")
	gen := strings.Index(joined, "pd gen .")
	tidy := strings.Index(joined, "\ngo mod tidy\n")
	build := strings.Index(joined, "go build ./...")

	x.Less(tools, lenient, "the generators cannot build without their sums")
	x.Less(lenient, gen)
	x.Less(gen, tidy, "tidy before gen fetches this app's own packages from a repository nobody pushed")
	x.Less(tidy, build)

	x.NotContains(joined, "buf dep update", "it cannot run before a generation; pd gen does it")
}
