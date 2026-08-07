// Package version says what build is running.
//
// It reads what the Go toolchain already stamps into every binary rather than
// having a build script write a `.go` file with an `init()` in it. The
// generated-file approach works, and its cost is that the source tree is only
// correct after a script has been run: a plain `go build`, a `go test`, an
// editor's own build all produce a binary that says something false, and a
// stale generated file is indistinguishable from a fresh one.
//
// What the toolchain stamps is `vcs.revision` and `vcs.modified`, which it
// takes from the repository at build time and cannot be wrong. What it cannot
// know is what a release is called, so that -- and only that -- is left to
// `-ldflags`.
//
//	go build -ldflags "-X github.com/lesomnus/payday/version.version=$(git describe --tags)"
//
// # Why the revision is not left to ldflags too
//
// It could be, and then a build that forgot the flag would report nothing
// rather than something wrong. The toolchain's answer is better than both: it
// is there without being asked and it says whether the tree was clean, which is
// the part somebody reading a version actually needs. A binary built from
// uncommitted work is the one whose revision means the least, and it is exactly
// the one a hand-written stamp gets right least often.
package version

import (
	"runtime/debug"
	"strings"
)

// version is what a release is called, and is set by the linker. Left unset it
// falls back to the module version the toolchain recorded, which for a build
// from source is "(devel)".
var version string

// Build is what is known about the binary that is running.
type Build struct {
	// Version is what this release is called. It is [Unknown] for a build that
	// was neither given one nor made from a module.
	Version string

	// Revision is the commit the tree was at, and is empty when the build
	// happened outside a repository -- which is what a `go install` of a
	// published module looks like.
	Revision string

	// Dirty says the tree had uncommitted changes, which makes [Build.Revision]
	// a place the build started from rather than a thing it is.
	Dirty bool

	// Time is when the commit was made, in RFC 3339, and empty for the same
	// reason Revision can be.
	Time string
}

// Unknown is what a version nobody said and nothing recorded reads as. It is a
// word rather than an empty string so that a log line saying it looks like an
// answer instead of a missing field.
const Unknown = "unknown"

// Get answers with what is known about this binary.
func Get() Build {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		// Only for a binary the toolchain did not build, which in practice
		// means a test harness that linked this in by hand.
		return Build{Version: fallback(version, "")}
	}

	return read(info)
}

// read is [Get] with the build info handed in, which is what makes any of this
// testable: `debug.ReadBuildInfo` answers about the binary that is running and
// there is no way to make it answer about a different one.
func read(info *debug.BuildInfo) Build {
	v := Build{Version: fallback(version, info.Main.Version)}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			v.Revision = s.Value
		case "vcs.modified":
			v.Dirty = s.Value == "true"
		case "vcs.time":
			v.Time = s.Value
		}
	}

	return v
}

// fallback is the linker's word, then the toolchain's, then [Unknown].
//
// "(devel)" is what the toolchain records for a build from a source tree, and
// it is passed over: it says less than nothing to somebody reading a version,
// since it looks like a release nobody has heard of.
func fallback(vs ...string) string {
	for _, v := range vs {
		v = strings.TrimSpace(v)
		if v != "" && v != "(devel)" {
			return v
		}
	}

	return Unknown
}

// Short is the revision as it is written in a log line: the first twelve
// characters, and a `+` for a tree that had uncommitted changes.
//
// Twelve rather than seven. Seven is what git shows and what everybody reads,
// and it stops being unique in a repository of any size -- which is noticed the
// first time two builds a week apart cannot be told apart.
func (v Build) Short() string {
	if v.Revision == "" {
		return ""
	}

	s := v.Revision
	if len(s) > 12 {
		s = s[:12]
	}
	if v.Dirty {
		s += "+"
	}

	return s
}

// String is the whole of it in one line.
func (v Build) String() string {
	s := v.Version
	if r := v.Short(); r != "" {
		s += " (" + r + ")"
	}

	return s
}
