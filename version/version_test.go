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
