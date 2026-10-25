package pdcli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// TestPaydaysDomainsAreReadBeforeAnythingIsCompiled: the numbers an app gives
// payday's Archived and Access decide what `pd gen` writes for buf to compile,
// so they are read off the file as written -- in each form an option can be
// written in, and beside a past, which the same option declares.
func TestPaydaysDomainsAreReadBeforeAnythingIsCompiled(t *testing.T) {
	for name, tc := range map[string]struct {
		option string
		want   pdcli.AppDomains
	}{
		"the literal": {
			`option (payday.app) = {domains: {archived: 240, access: 241}};`,
			pdcli.AppDomains{Archived: 240, Access: 241},
		},
		"by its name": {
			`option (payday.app).domains = {archived: 240, access: 241};`,
			pdcli.AppDomains{Archived: 240, Access: 241},
		},
		"field by field": {
			"option (payday.app).domains.archived = 240;\noption (payday.app).domains.access = 241;",
			pdcli.AppDomains{Archived: 240, Access: 241},
		},
		"one of them": {
			`option (payday.app) = {domains: {access: 241}};`,
			pdcli.AppDomains{Access: 241},
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			root := app(t, "github.com/acme/thing")
			past(t, root, "widget.proto", tc.option)

			l, err := pdcli.Discover(root)
			x.NoError(err)

			tc.want.At = "proto/app/widget.proto"
			x.Equal(&tc.want, l.Domains)
			x.Nil(l.Past)
		})
	}

	t.Run("beside a past", func(t *testing.T) {
		x := require.New(t)

		root := app(t, "github.com/acme/thing")
		past(t, root, "widget.proto", `option (payday.app) = {
  past: {event: 17, fact: 16}
  domains: {archived: 18, access: 19}
};`)

		l, err := pdcli.Discover(root)
		x.NoError(err)
		x.Equal(&pdcli.Past{Event: 17, Fact: 16, At: "proto/app/widget.proto"}, l.Past)
		x.Equal(&pdcli.AppDomains{Archived: 18, Access: 19, At: "proto/app/widget.proto"}, l.Domains)
	})

	t.Run("and an app that says nothing of them has payday's", func(t *testing.T) {
		x := require.New(t)

		for _, option := range []string{"", "option (payday.app) = {};", `option (payday.app) = {past: {event: 17, fact: 16}};`} {
			root := app(t, "github.com/acme/thing")
			past(t, root, "widget.proto", option)

			l, err := pdcli.Discover(root)
			x.NoError(err)
			x.Nil(l.Domains, "%q gave payday's entities domains", option)
		}
	})
}

// TestPaydaysDomainsAreRefusedBeforeAnythingIsCompiled: the numbers go into
// the copies `pd gen` writes, so a wrong one is refused before it is in a file
// -- in the words `pdgen.Read` uses, since it is the same rule -- and so is a
// declaration written twice, or misspelt.
func TestPaydaysDomainsAreRefusedBeforeAnythingIsCompiled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		option string
		want   string
	}{
		{"one of payday's numbers", `option (payday.app) = {domains: {archived: 5}};`, "domains: archived: 5 is payday's"},
		{"one number for both", `option (payday.app) = {domains: {archived: 240, access: 240}};`, "says 240 for both"},
		{
			"one the past takes",
			`option (payday.app) = {past: {event: 17, fact: 16}, domains: {access: 16}};`,
			"domains: access: 16 is what `past:` gives the past's Fact",
		},
		{"a field misspelt", `option (payday.app) = {domains: {archive: 240}};`, "unknown field: archive"},
		{
			"payday's Owed, which keeps its number",
			`option (payday.app) = {domains: {owed: 200}};`,
			"names owed, which is payday's Owed, and it keeps 254 in every app",
		},
		{
			"payday's RowSeal, as its marker is spelt",
			`option (payday.app) = {domains: {archived: 240, row_seal: 200}};`,
			"names row_seal, which is payday's RowSeal, and it keeps 255 in every app",
		},
		{
			"payday's RowSeal, as its message is",
			`option (payday.app).domains.rowseal = 200;`,
			"names rowseal, which is payday's RowSeal",
		},
		{
			"the past's",
			`option (payday.app) = {domains: {event: 17}};`,
			"names event, and the past's Event is numbered by `past:`",
		},
		{
			"one of the two, outside",
			`option (payday.app) = {archived: 240};`,
			"names archived outside `domains:`, which is where payday's Archived is numbered",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			root := app(t, "github.com/acme/thing")
			past(t, root, "widget.proto", tc.option)

			_, err := pdcli.Discover(root)
			x.Error(err, "%s was read", tc.option)
			x.Contains(err.Error(), tc.want)
			x.Contains(err.Error(), "proto/app/widget.proto", "the refusal does not say where")
		})
	}

	t.Run("twice", func(t *testing.T) {
		x := require.New(t)

		root := app(t, "github.com/acme/thing")
		past(t, root, "gadget.proto", `option (payday.app) = {domains: {archived: 240}};`)
		past(t, root, "widget.proto", `option (payday.app) = {domains: {access: 241}};`)

		_, err := pdcli.Discover(root)
		x.Error(err, "payday's domains given in two files were read")
		x.Contains(err.Error(), "proto/app/gadget.proto and proto/app/widget.proto both give payday's entities domains")
	})
}

// TestTheCopiesAreNumberedAsTheAppDeclares: `pd gen` writes payday's Archived
// and Access at the numbers an app gives them, numbers them again when the
// declaration changes, and writes them back at payday's when it goes -- one of
// them, or both -- with what is generated from them.
func TestTheCopiesAreNumberedAsTheAppDeclares(t *testing.T) {
	x := require.New(t)

	l := genApp(t)
	read := func(p string) string {
		b, err := os.ReadFile(p)
		x.NoError(err)

		return string(b)
	}
	archived := filepath.Join(l.Root, l.DirPd(), "archived.proto")
	access := filepath.Join(l.Root, l.DirPd(), "access.proto")
	generated := l.Path(pdcli.DirPd_, "pd.g.go")
	x.Contains(read(archived), "domain: 5\n", "apptest keeps payday's numbers")
	x.Contains(read(access), "domain: 6\n", "apptest keeps payday's numbers")

	robot := l.Path(pdcli.DirProto, "app", "robot.proto")
	src := read(robot)
	declare := func(t *testing.T, fields string) pdcli.Layout {
		t.Helper()

		const past = "past: {event: 17, fact: 18}"
		x.Contains(src, past)
		x.NoError(os.WriteFile(robot, []byte(strings.Replace(src, past, past+"\n  domains: {"+fields+"}", 1)), 0o644))

		l, err := pdcli.Discover(l.Root)
		x.NoError(err)
		x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))

		return l
	}

	l = declare(t, "archived: 240, access: 241")
	for _, want := range []string{"own: OWN_ARCHIVED\n", "\n    domain: 240\n", "package app;", `option go_package = "github.com/lesomnus/payday/internal/apptest";`} {
		x.Contains(read(archived), want)
	}
	x.NotContains(read(archived), "\n    domain: 5\n")
	x.Contains(read(access), "\n    domain: 241\n")
	x.Regexp(`ArchivedDomain\s+pdid\.Domain\s*=\s*240\b`, read(generated))
	x.Regexp(`AccessDomain\s+pdid\.Domain\s*=\s*241\b`, read(generated))

	// Numbered from the declaration, every run -- and one of them alone.
	l = declare(t, "access: 230")
	x.Contains(read(archived), "domain: 5\n", "the one it said nothing of was left where it was moved to")
	x.Contains(read(access), "domain: 230\n")

	// And written back at payday's when it goes.
	x.NoError(os.WriteFile(robot, []byte(src), 0o644))
	l, err := pdcli.Discover(l.Root)
	x.NoError(err)
	x.Nil(l.Domains)
	x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))
	x.Contains(read(archived), "domain: 5\n")
	x.Contains(read(access), "domain: 6\n")
	x.Regexp(`AccessDomain\s+pdid\.Domain\s*=\s*6\b`, read(generated))

	vs, err := pdcli.Gen{Layout: l}.Check(t.Context())
	x.NoError(err)
	x.Empty(vs, "the app with no declaration is not the app as committed")
}

// TestAnEntityIsNotGivenTheNumbersAnAppGavePaydays: the declaration holds the
// two before a generation writes them into the copies, and after -- and they
// are payday's entities wherever the app put them, so one put at 240 is not
// the highest of the app's own.
func TestAnEntityIsNotGivenTheNumbersAnAppGavePaydays(t *testing.T) {
	for name, tc := range map[string]struct {
		option string
		want   int
	}{
		"next to its own":       {`option (payday.app) = {domains: {archived: 7, access: 8}};`, 9},
		"far above them":        {`option (payday.app) = {domains: {archived: 240, access: 241}};`, 7},
		"beside a past":         {`option (payday.app) = {past: {event: 7, fact: 8}, domains: {archived: 9}};`, 10},
		"one of them, the next": {`option (payday.app) = {domains: {access: 7}};`, 8},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			root := app(t, "github.com/acme/thing")
			past(t, root, "widget.proto", tc.option)

			l, err := pdcli.Discover(root)
			x.NoError(err)

			_, err = (pdcli.Entity{Layout: l, Name: "Robot", Tenanted: true}).Add()
			x.NoError(err)

			vs, err := pdcli.Domains(l)
			x.NoError(err)
			x.Equal(map[int]string{tc.want: "Robot"}, vs)
		})
	}

	t.Run("and with the copies written", func(t *testing.T) {
		x := require.New(t)

		root := app(t, "github.com/acme/thing")
		past(t, root, "widget.proto", `option (payday.app) = {domains: {archived: 240, access: 241}};`)

		// What the generation leaves in the app's tree, saying the numbers.
		dir := filepath.Join(root, "proto", "app", "payday")
		x.NoError(os.MkdirAll(dir, 0o755))
		for name, n := range map[string]string{"archived.proto": "240", "access.proto": "241"} {
			x.NoError(os.WriteFile(filepath.Join(dir, name), []byte("message M {\n  option (payday.entity) = {\n    domain: "+n+"\n  };\n}\n"), 0o644))
		}

		l, err := pdcli.Discover(root)
		x.NoError(err)

		_, err = (pdcli.Entity{Layout: l, Name: "Robot", Tenanted: true}).Add()
		x.NoError(err)

		vs, err := pdcli.Domains(l)
		x.NoError(err)
		x.Equal("Robot", vs[7], "the copies' numbers were taken for the app's highest: %v", vs)
	})
}

// renumberedRuns is apptest's tests of the trail's archive, of its manifest
// and of the access record, which [TestARenumberedAppKeepsItsTrailAndItsAccessRecord]
// asks to have seen pass by name: they are what an app that numbers payday's
// Archived and Access is relying on, and a test that skipped is one that
// passed.
var renumberedRuns = []string{
	// That every identifier of the two carries the app's number, and the
	// archive files the access record under its kind's name.
	"TestTheManifestAndTheAccessRecordAreTheDomainsTheAppGivesThem",

	// The access record, written by what happened and kept as a kind of the
	// trail's.
	"TestAReadIsRecordedOfWhoseDataItCarried",
	"TestAWriteIsRecordedInItsOwnTransaction",
	"TestARefusedCallIsRecordedWithWhatItNamed",
	"TestTheAccessRecordIsKeptAsAKindOfItsOwn",
	"TestAPassReadsTheAccessRecordFromWhereItLeftOff",

	// The trail's archive, and the manifest that is its account.
	"TestTheTrailIsKeptPerKindOfThing",
	"TestTheArchiveIsSplitByKindSoThatTheSecondClockCanBe",
	"TestAnArchivedRowIsForgottenToo",
	"TestTheManifestIsKeptInTheAppsOwnTable",
	"TestTheManifestIsSignedAndChainedInTheAppsOwnTable",
	"TestADatabaseRestoredFromBeforeTheLatestCheckpointIsAcceptedInTheAppsOwnTable",
	"TestTheGeneratedManifestWritesARowWholeWithItsSignature",

	// And the archive read back by kind, for a check of the rows (`Newest`).
	"TestARowWhoseTrailWasArchivedIsCheckedAgainstTheArchive",
	"TestASealedArchiveIsCheckedWithNoTenantsKey",
	"TestARowNobodyWroteSinceItsWindowIsCarriedForward",
}

// TestARenumberedAppKeepsItsTrailAndItsAccessRecord (payday#163): an app that
// gives payday's Archived and Access numbers of its own generates, builds, and
// passes everything apptest demonstrates -- its trail, the archive and its
// manifest, the access record -- every identifier of the two carrying its
// number.
//
// The app is apptest, copied, giving them 240 and 241: far from its own, as
// an app moving them out of the way of its own 5 and 6 would. What runs is the
// copy's whole suite, on the database this run was given -- PostgreSQL when
// `PDTEST_POSTGRES` names one, which the environment hands the run, and SQLite
// otherwise. All of it rather than the tests of the two, because a number
// written down where the schema should have been is found where it is used,
// and that is not always where anybody would have looked.
//
// It is the test that fails when anything knows 5 or 6 that is not the schema:
// a generated `pdid.New(5)`, a document recognised as the access record's by
// `domain == 6`, a kind filed by its number. The apptest committed beside it
// runs the same tests at payday's numbers, so between the two neither number
// passes for the other.
func TestARenumberedAppKeepsItsTrailAndItsAccessRecord(t *testing.T) {
	x := require.New(t)

	l := genApp(t)
	robot := l.Path(pdcli.DirProto, "app", "robot.proto")
	b, err := os.ReadFile(robot)
	x.NoError(err)

	const past = "past: {event: 17, fact: 18}"
	x.Contains(string(b), past)
	x.NoError(os.WriteFile(robot, []byte(strings.Replace(string(b), past, past+"\n  domains: {archived: 240, access: 241}", 1)), 0o644))

	l, err = pdcli.Discover(l.Root)
	x.NoError(err)
	x.Equal(&pdcli.AppDomains{Archived: 240, Access: 241, At: "proto/app/robot.proto"}, l.Domains)
	x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))

	generated, err := os.ReadFile(l.Path(pdcli.DirPd_, "pd.g.go"))
	x.NoError(err)
	x.Regexp(`AccessDomain\s+pdid\.Domain\s*=\s*241\b`, string(generated))
	x.Regexp(`ArchivedDomain\s+pdid\.Domain\s*=\s*240\b`, string(generated))

	run := exec.CommandContext(t.Context(), "go", "test", "-count=1", "-v", "./...")
	run.Dir = l.Root
	out, err := run.CombinedOutput()
	x.NoError(err, "%s", out)

	for _, v := range renumberedRuns {
		x.Contains(string(out), "--- PASS: "+v+" ", "%s did not run in the renumbered app", v)
	}
}
