package frame_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/frame"
)

const (
	get   = "/app.RobotService/Get"
	list  = "/app.RobotService/List"
	erase = "/app.RobotService/Erase"

	cellGet = "/app.CellService/Get"
	other   = "/hq.RobotService/Get"
)

// TestCovers is the whole of what a pattern means, and every case here is
// written from the side that matters: what a pattern must **not** reach.
//
// It is three string comparisons, which is the point. What it is protecting
// against is not a clever attack -- it is the version of this that grew a
// second rule and then could not be evaluated by reading it.
func TestCovers(t *testing.T) {
	t.Run("a name covers itself and nothing else", func(t *testing.T) {
		x := require.New(t)

		x.True(frame.Covers(get, get))
		x.False(frame.Covers(get, list))
		x.False(frame.Covers(get, cellGet))
		x.False(frame.Covers(get, other))
	})

	t.Run("a service pattern covers its own methods", func(t *testing.T) {
		x := require.New(t)

		const svc = "/app.RobotService/*"
		x.True(frame.Covers(svc, get))
		x.True(frame.Covers(svc, erase))

		// Another service of the same package, and the same service of another
		// package. Neither.
		x.False(frame.Covers(svc, cellGet))
		x.False(frame.Covers(svc, other))
	})

	t.Run("a package pattern covers its own services", func(t *testing.T) {
		x := require.New(t)

		const pkg = "/app.*/*"
		x.True(frame.Covers(pkg, get))
		x.True(frame.Covers(pkg, cellGet))
		x.False(frame.Covers(pkg, other))
	})

	t.Run("a method pattern crosses services and not packages", func(t *testing.T) {
		x := require.New(t)

		const every = "/app.*/Get"
		x.True(frame.Covers(every, get))
		x.True(frame.Covers(every, cellGet))
		x.False(frame.Covers(every, list))
		x.False(frame.Covers(every, other))
	})

	t.Run("everything covers everything", func(t *testing.T) {
		x := require.New(t)

		for _, m := range []string{get, list, erase, cellGet, other} {
			x.True(frame.Covers("/*.*/*", m), m)
		}
	})
}

// TestCoversNeverWidens is the direction a mistake here would be an escalation.
//
// Every case is somebody holding one thing and asking to hand out something
// larger. There is no case in this test that should answer true, and that is
// deliberate: the failure mode being guarded against is a rule that is too
// generous, so a test full of things that must be refused is the test that
// catches it.
func TestCoversNeverWidens(t *testing.T) {
	for _, tc := range []struct{ held, want string }{
		// One method does not hand out its service.
		{get, "/app.RobotService/*"},
		{get, "/app.*/Get"},
		{get, "/app.*/*"},
		{get, "/*.*/*"},

		// One service does not hand out its package.
		{"/app.RobotService/*", "/app.*/*"},
		{"/app.RobotService/*", "/*.*/*"},
		{"/app.RobotService/*", "/app.*/Get"},

		// One package does not hand out every package.
		{"/app.*/*", "/*.*/*"},

		// Nor does a method pattern hand out a service pattern, which is the
		// pair neither of which contains the other.
		{"/app.*/Get", "/app.RobotService/*"},
		{"/app.RobotService/*", "/app.*/Get"},

		// And nothing reaches sideways.
		{"/app.*/*", "/hq.*/*"},
		{"/app.RobotService/*", "/app.CellService/*"},
	} {
		t.Run(tc.held+" -/-> "+tc.want, func(t *testing.T) {
			require.False(t, frame.Covers(tc.held, tc.want))
		})
	}
}

// TestCoversIsNotAGlob is the one restriction that keeps this to three
// comparisons.
//
// A part is `*` or it is a name. `*Get*` is a name that happens to contain
// asterisks, and it names a method nobody has. Reading it as a glob is how this
// becomes a regular expression, and a permission check that needs a regular
// expression to explain is one nobody reviews.
func TestCoversIsNotAGlob(t *testing.T) {
	x := require.New(t)

	x.False(frame.Covers("/app.RobotService/*Get*", get))
	x.False(frame.Covers("/app.RobotService/G*", get))
	x.False(frame.Covers("/app.Robot*/Get", get))
	x.False(frame.Covers("/*/*", get))
	x.False(frame.Covers("*", get))

	// It is still a name, so it matches itself.
	x.True(frame.Covers("/app.RobotService/*Get*", "/app.RobotService/*Get*"))
}

// TestCoversRefusesWhatItCannotRead keeps the failure closed.
//
// A string that is not shaped like a method is not a pattern, so it grants
// nothing -- and nothing but an identical string reaches it either. That way a
// grant carrying a typo allows exactly nothing rather than allowing something
// nobody meant.
func TestCoversRefusesWhatItCannotRead(t *testing.T) {
	x := require.New(t)

	for _, v := range []string{
		"",
		"app.RobotService/Get", // no leading slash
		"/app.RobotService",    // no method
		"/app.RobotService/",   // empty method
		"//Get",                // empty service
		"/RobotService/Get",    // no package
		"/app.RobotService/Get/Extra",
	} {
		x.False(frame.Covers(v, get), "as a pattern: %q", v)
		x.False(frame.Covers("/*.*/*", v), "as a method: %q", v)
	}

	// Except itself, which is what keeps an unpackaged service working as the
	// literal it is.
	x.True(frame.Covers("/RobotService/Get", "/RobotService/Get"))
	x.False(frame.Covers("/*.*/*", "/RobotService/Get"))
}

// TestCoversSplitsThePackageAtTheLastDot, because a package has dots in it.
//
// Split at the first, `/google.protobuf.Any/Pack` is package "google" and
// `/google.*/*` quietly means every package that starts that way.
func TestCoversSplitsThePackageAtTheLastDot(t *testing.T) {
	x := require.New(t)

	const nested = "/google.protobuf.Any/Pack"

	x.True(frame.Covers("/google.protobuf.*/*", nested))
	x.True(frame.Covers("/google.protobuf.Any/*", nested))
	x.False(frame.Covers("/google.*/*", nested))
	x.False(frame.Covers("/google.protobuf.Other/*", nested))
}

// TestGrantTakesPatterns is the reason any of this exists: what a credential
// allows is where these are read.
func TestGrantTakesPatterns(t *testing.T) {
	t.Run("a service pattern in a grant", func(t *testing.T) {
		x := require.New(t)

		g := frame.Whole().To("/app.RobotService/*")
		x.True(g.Allows(get))
		x.True(g.Allows(erase))
		x.False(g.Allows(cellGet))
		x.False(g.Allows(other))
	})

	t.Run("names and patterns together", func(t *testing.T) {
		x := require.New(t)

		g := frame.Whole().To(cellGet, "/app.RobotService/*")
		x.True(g.Allows(get))
		x.True(g.Allows(cellGet))
		x.False(g.Allows("/app.CellService/Erase"))
	})

	t.Run("a grant of only names is what it always was", func(t *testing.T) {
		x := require.New(t)

		g := frame.Whole().To(get, list)
		x.True(g.Allows(get))
		x.True(g.Allows(list))
		x.False(g.Allows(erase))
	})

	t.Run("naming none still allows none", func(t *testing.T) {
		x := require.New(t)

		x.False(frame.Whole().To().Allows(get))
		x.False(frame.Grant{}.Allows(get))
	})
}
