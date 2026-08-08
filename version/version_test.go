package version

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
)

// info is a build the toolchain might have recorded.
func info(main string, settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{
		Main:     debug.Module{Version: main},
		Settings: settings,
	}
}

func set(k, v string) debug.BuildSetting { return debug.BuildSetting{Key: k, Value: v} }

func TestRead(t *testing.T) {
	t.Run("takes what the toolchain stamped", func(t *testing.T) {
		x := require.New(t)

		v := read(info("v1.2.3",
			set("vcs.revision", "0123456789abcdef0123456789abcdef01234567"),
			set("vcs.modified", "false"),
			set("vcs.time", "2026-08-07T12:00:00Z"),
		))

		x.Equal("v1.2.3", v.Version)
		x.Equal("0123456789abcdef0123456789abcdef01234567", v.Revision)
		x.False(v.Dirty)
		x.Equal("2026-08-07T12:00:00Z", v.Time)
	})

	t.Run("says when the tree was not clean", func(t *testing.T) {
		x := require.New(t)

		v := read(info("v1.2.3", set("vcs.revision", "abc"), set("vcs.modified", "true")))
		x.True(v.Dirty)

		// A dirty build is the one whose revision means the least, which is
		// why saying so is most of what this is for.
		x.Equal("abc+", v.Short())
	})

	t.Run("a build outside a repository says nothing rather than something wrong", func(t *testing.T) {
		x := require.New(t)

		// What `go install github.com/...@v1.2.3` looks like: a version and no
		// commit, because there was no tree.
		v := read(info("v1.2.3"))
		x.Equal("v1.2.3", v.Version)
		x.Empty(v.Revision)
		x.Empty(v.Short())
		x.Equal("v1.2.3", v.String())
	})
}

func TestVersionFallsBack(t *testing.T) {
	t.Cleanup(func() { version = "" })

	t.Run("the linker's word wins", func(t *testing.T) {
		x := require.New(t)

		version = "v9.9.9"
		x.Equal("v9.9.9", read(info("v1.2.3")).Version)
	})

	t.Run("then the toolchain's", func(t *testing.T) {
		x := require.New(t)

		version = ""
		x.Equal("v1.2.3", read(info("v1.2.3")).Version)
	})

	t.Run("and a build from source is not a release nobody heard of", func(t *testing.T) {
		x := require.New(t)

		// "(devel)" is what the toolchain records for a tree, and passing it
		// through would put it in a log line looking like a version.
		version = ""
		x.Equal(Unknown, read(info("(devel)")).Version)
		x.Equal(Unknown, read(info("")).Version)
	})
}

func TestShort(t *testing.T) {
	x := require.New(t)

	// Twelve and not seven: seven is what git shows and stops being unique in
	// a repository of any size.
	long := read(info("v1", set("vcs.revision", "0123456789abcdef0123456789abcdef01234567")))
	x.Equal("0123456789ab", long.Short())

	short := read(info("v1", set("vcs.revision", "abc")))
	x.Equal("abc", short.Short())

	x.Equal("v1 (0123456789ab)", long.String())
}

// TestGetReadsThisBinary is the one thing the rest cannot check: that what is
// stamped is actually reachable.
func TestGetReadsThisBinary(t *testing.T) {
	x := require.New(t)

	v := Get()
	x.NotEmpty(v.Version, "a version is always answered, even if it is %q", Unknown)
	x.NotEmpty(v.String())
}

// dep is a build in which `Module` is a dependency at `v`.
func dep(v string, replaced bool) *debug.BuildInfo {
	d := debug.Module{Path: Module, Version: v}
	if replaced {
		d.Replace = &debug.Module{Path: "/somewhere/payday"}
	}

	return &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/acme/widget", Version: "v1.2.3"},
		Deps: []*debug.Module{{Path: "example.com/other", Version: "v9.9.9"}, &d},
	}
}

// TestOf.
//
// Every false here is a build somebody is working in, and the point of the test
// is that all four of them answer the same way. A comparison that fired in any
// of them would refuse `go test` in payday's own repository.
func TestOf(t *testing.T) {
	t.Run("a released dependency", func(t *testing.T) {
		x := require.New(t)

		v, ok := of(dep("v0.3.1", false), Module)
		x.True(ok)
		x.Equal("v0.3.1", v)
	})

	t.Run("says nothing about itself", func(t *testing.T) {
		x := require.New(t)

		// payday's own binaries: `pd` links no payday dependency because it is
		// the main module.
		_, ok := of(&debug.BuildInfo{Main: debug.Module{Path: Module, Version: "v0.3.1"}}, Module)
		x.False(ok)
	})

	t.Run("says nothing when it is not a dependency", func(t *testing.T) {
		x := require.New(t)

		_, ok := of(&debug.BuildInfo{
			Main: debug.Module{Path: "github.com/acme/widget"},
			Deps: []*debug.Module{{Path: "example.com/other", Version: "v9.9.9"}},
		}, Module)
		x.False(ok)
	})

	t.Run("says nothing about a development build", func(t *testing.T) {
		x := require.New(t)

		_, ok := of(dep("(devel)", false), Module)
		x.False(ok)

		_, ok = of(dep("", false), Module)
		x.False(ok)
	})

	t.Run("says nothing about one that was replaced", func(t *testing.T) {
		x := require.New(t)

		// The recorded version is the one that was *required*, and the code
		// compiled is whatever is in that directory. Both sides then agree on a
		// number describing neither, which is worse than not knowing -- so a
		// replace is silence rather than a false pass.
		_, ok := of(dep("v0.3.1", true), Module)
		x.False(ok)
	})
}

// TestSame is the comparison itself, with `Of` stubbed out by calling through
// `of` -- which is why the mismatch case is expressed as a table rather than by
// building a binary.
func TestSame(t *testing.T) {
	// same is [Same] with the linked side handed in.
	same := func(generated string, i *debug.BuildInfo) error {
		linked, ok := of(i, Module)
		if !ok {
			return nil
		}
		if generated == "" || generated == "(devel)" || generated == linked {
			return nil
		}

		return ErrStale
	}

	for _, tc := range []struct {
		what      string
		generated string
		info      *debug.BuildInfo
		stale     bool
	}{
		{"agreeing", "v0.3.1", dep("v0.3.1", false), false},
		{"an upgrade nobody regenerated after", "v0.3.0", dep("v0.3.1", false), true},
		{"a downgrade", "v0.4.0", dep("v0.3.1", false), true},
		{"generated in a workspace", "(devel)", dep("v0.3.1", false), false},
		{"generated before the stamp existed", "", dep("v0.3.1", false), false},
		{"linked from a checkout", "v0.3.0", dep("v0.3.1", true), false},
		{"payday's own binary", "v0.3.0", &debug.BuildInfo{Main: debug.Module{Path: Module}}, false},
	} {
		t.Run(tc.what, func(t *testing.T) {
			err := same(tc.generated, tc.info)
			if tc.stale {
				require.ErrorIs(t, err, ErrStale)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestSameSaysNothingHere is the real [Same] against the binary this test runs
// in, which is payday's own -- so it must be silent.
func TestSameSaysNothingHere(t *testing.T) {
	require.NoError(t, Same("v0.0.1-something-else"))
}
