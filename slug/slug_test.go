package slug_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"
)

// Domains used by the tests. They are registered in TestMain so that the word
// after the "#" resolves to something; nothing here depends on them being these
// particular numbers.
const (
	Tenant pdid.Domain = 1
	Holder pdid.Domain = 2
	Robot  pdid.Domain = 7
)

func TestMain(m *testing.M) {
	pdid.Register("test.Tenant", Tenant, "tenant")
	pdid.Register("test.Holder", Holder, "holder")
	pdid.Register("test.Robot", Robot, "robot")

	m.Run()
}

func TestParse(t *testing.T) {
	t.Run("reads every shape the format has", func(t *testing.T) {
		for _, tc := range []struct {
			v      string
			what   string
			tenant string
			alias  string
			domain pdid.Domain
		}{
			{"@acme/arm-01#robot", "all of it", "acme", "arm-01", Robot},
			{"@acme/arm-01", "no domain, which the reader knows", "acme", "arm-01", pdid.Unknown},
			{"@acme#tenant", "a tenant, which nothing is above", "", "acme", Tenant},
			{"@arm-01", "neither, which the caller's context knows", "", "arm-01", pdid.Unknown},
			{"arm-01", "not even the mark", "", "arm-01", pdid.Unknown},
			{"acme/arm-01#robot", "all of it without the mark", "acme", "arm-01", Robot},
		} {
			t.Run(tc.what, func(t *testing.T) {
				x := require.New(t)

				v, err := slug.Parse(tc.v)
				x.NoError(err)
				x.Equal(tc.tenant, v.Tenant())
				x.Equal(tc.alias, v.Alias())
				x.Equal(tc.domain, v.Domain())
				x.Equal(tc.tenant != "", v.HasTenant())
				x.Equal(tc.domain != pdid.Unknown, v.HasDomain())
			})
		}
	})

	t.Run("folds the ways of writing one name into one name", func(t *testing.T) {
		x := require.New(t)

		want := slug.MustParse("@acme/arm-01#robot")
		for _, v := range []string{
			"@acme/arm-01#robot",
			"  @acme/arm-01#robot  ",
			"@ACME/Arm-01#Robot",
			"acme/arm-01#robot",

			// Room around the separators is the same untidiness as room around
			// the whole thing, and normalizing is what makes two spellings one
			// value.
			"@acme / arm-01 # robot",
		} {
			u, err := slug.Parse(v)
			x.NoError(err, "%q", v)
			x.Equal(want, u, "%q named something else", v)
		}
	})

	t.Run("refuses a part that is missing rather than untidy", func(t *testing.T) {
		for _, tc := range []struct {
			v    string
			what string
		}{
			{"", "nothing at all"},
			{"@", "the mark alone"},
			{"/arm-01", "a tenant that is not there"},

			// The form this came from built exactly this out of an empty
			// tenant, and read it back as a slug with no tenant -- so one row
			// had two names that were not the same string.
			{"@/arm-01", "the same, marked"},

			{"acme/", "an alias that is not there"},
			{"#robot", "a kind of thing and nothing it applies to"},
			{"@acme/arm-01#", "a hash that promises a kind of thing"},
			{"@acme/robots/arm-01", "a tenant of a tenant"},
			{"@acme/Arm_01", "an alias that is not one"},
			{"@1cme/arm-01", "a tenant that is not one"},
		} {
			t.Run(tc.what, func(t *testing.T) {
				x := require.New(t)

				_, err := slug.Parse(tc.v)
				x.Error(err)
				x.Equal(codes.InvalidArgument, status.Code(err))
			})
		}
	})

	t.Run("says which half was wrong", func(t *testing.T) {
		x := require.New(t)

		_, err := slug.Parse("@1cme/arm-01")
		x.ErrorIs(err, slug.ErrAlias)
		x.Contains(err.Error(), "tenant")

		_, err = slug.Parse("@acme/-arm")
		x.ErrorIs(err, slug.ErrAlias)
		x.Contains(err.Error(), "alias")
	})

	t.Run("panics on a literal that is not one", func(t *testing.T) {
		x := require.New(t)

		x.Panics(func() { slug.MustParse("@acme/Arm_01") })
		x.NotPanics(func() { slug.MustParse("@acme/arm-01") })
	})
}

// TestRoundTrip is the asymmetry this had: parsing dropped the "@" and printing
// never put it back, so a slug that went through both came out as a different
// string than it went in as.
func TestRoundTrip(t *testing.T) {
	t.Run("printing a slug writes the mark that proves it is one", func(t *testing.T) {
		x := require.New(t)

		for _, v := range []string{
			"@acme/arm-01#robot",
			"@acme/arm-01",
			"@acme#tenant",
			"@arm-01",
		} {
			u := slug.MustParse(v)
			x.Equal(v, u.String())
			x.True(slug.Is(u.String()), "%q would be taken for an identifier", u)
		}
	})

	t.Run("and reading it back names the same row", func(t *testing.T) {
		x := require.New(t)

		for _, v := range []string{
			"@acme/arm-01#robot",
			"  ACME/Arm-01  ",
			"acme#tenant",
			"arm-01",
		} {
			u := slug.MustParse(v)
			w, err := slug.Parse(u.String())
			x.NoError(err)
			x.Equal(u, w)
			x.Equal(u.String(), w.String())
		}
	})

	t.Run("a slug that names nothing prints as nothing", func(t *testing.T) {
		x := require.New(t)

		var z slug.Slug
		x.True(z.IsZero())
		x.Equal("", z.String())
		x.False(slug.MustParse("@arm-01").IsZero())
	})
}

func TestIs(t *testing.T) {
	x := require.New(t)

	// The whole reason the mark exists: a UUID and an alias cannot be told
	// apart by looking at them, so something has to say.
	const id = "abcd1234-2a10-8abc-8a03-9f2e1c4d5b6a"
	x.NoError(slug.Validate(id), "the overlap this guards is gone; re-read the package comment")

	x.False(slug.Is(id))
	x.True(slug.Is("@"+id), "a name that looks like an identifier is still a name")
	x.True(slug.Is("@acme/arm-01"))
	x.True(slug.Is("  @acme/arm-01"))
	x.False(slug.Is("acme/arm-01"))
	x.False(slug.Is(""))
}

func TestNew(t *testing.T) {
	t.Run("takes the parts and normalizes them", func(t *testing.T) {
		x := require.New(t)

		v, err := slug.New("  Acme ", "Arm-01", Robot)
		x.NoError(err)
		x.Equal("@acme/arm-01#robot", v.String())
	})

	t.Run("an empty part is one left to the reader", func(t *testing.T) {
		x := require.New(t)

		v, err := slug.New("", "arm-01", pdid.Unknown)
		x.NoError(err)
		x.False(v.HasTenant())
		x.False(v.HasDomain())
		x.Equal("@arm-01", v.String())
	})

	t.Run("refuses a part that cannot be a name", func(t *testing.T) {
		x := require.New(t)

		_, err := slug.New("acme", "", Robot)
		x.ErrorIs(err, slug.ErrAlias)

		_, err = slug.New("ACME CORP", "arm-01", Robot)
		x.ErrorIs(err, slug.ErrAlias)
		x.Contains(err.Error(), "tenant")
	})
}

func TestDomain(t *testing.T) {
	t.Run("one that was left out is taken from where it was read", func(t *testing.T) {
		x := require.New(t)

		v, err := slug.MustParse("@acme/arm-01").Expect(Robot)
		x.NoError(err)
		x.Equal(Robot, v.Domain())
		x.Equal("@acme/arm-01#robot", v.String())
	})

	t.Run("one that agrees is kept", func(t *testing.T) {
		x := require.New(t)

		v, err := slug.MustParse("@acme/arm-01#robot").Expect(Robot)
		x.NoError(err)
		x.Equal(Robot, v.Domain())
	})

	t.Run("one that disagrees is refused, not obeyed", func(t *testing.T) {
		x := require.New(t)

		_, err := slug.MustParse("@acme/admin#holder").Expect(Robot)

		// The same error a reference of the wrong kind gets, since it is the
		// same mistake written another way.
		x.ErrorIs(err, pdid.ErrDomain)
		x.Equal(codes.InvalidArgument, status.Code(err))

		// And it says what it actually was, which is the whole point of
		// refusing here rather than looking for a robot named "admin".
		x.Contains(err.Error(), "holder")
		x.Contains(err.Error(), "robot")
	})

	t.Run("a word nothing registered cannot be checked, so it is refused", func(t *testing.T) {
		x := require.New(t)

		// The form this came from answered "unknown" for a word it did not
		// have, which is what it also answered for a slug that said nothing --
		// so a typo here silently became no assertion at all, and the check
		// that would have caught it never ran.
		_, err := slug.Parse("@acme/admin#robto")
		x.ErrorIs(err, slug.ErrNoSuchDomain)
		x.Equal(codes.InvalidArgument, status.Code(err))

		// It says what this app does have, since somebody who wrote the wrong
		// word wants the list of right ones.
		x.Contains(err.Error(), "robot")

		_, err = slug.Parse("@acme/admin#")
		x.ErrorIs(err, slug.ErrNoSuchDomain)
	})

	t.Run("one this deployment does not know prints as its number", func(t *testing.T) {
		x := require.New(t)

		// An identifier from another deployment reads like this, and printing
		// the number is the honest answer: this app cannot say what a 200 is,
		// so it cannot check anything written about one either -- which is why
		// reading it back is refused rather than guessed at.
		v := slug.MustParse("@acme/arm-01").WithDomain(pdid.Domain(200))
		x.Equal("@acme/arm-01#domain(200)", v.String())

		_, err := slug.Parse(v.String())
		x.ErrorIs(err, slug.ErrNoSuchDomain)
	})
}

// TestWithDomain is the first of the three defects: this was assembled by
// cutting the text at the "#" and appending the new word, and the "#" was in
// neither half.
func TestWithDomain(t *testing.T) {
	t.Run("writes the separator with the word", func(t *testing.T) {
		x := require.New(t)

		v := slug.MustParse("@acme/admin").WithDomain(Robot)
		x.Equal("@acme/admin#robot", v.String())
	})

	t.Run("and the concatenation it replaced named another row entirely", func(t *testing.T) {
		x := require.New(t)

		// The concatenation it replaced: a[:i] + Slug(v.String()), where i is
		// the index of the "#" or the end. This is what made it dangerous
		// rather than merely ugly -- "acme/adminrobot" is a well-formed slug
		// that parses, names a different row, and says nothing about having
		// been mangled.
		const a = "acme/admin"
		i := strings.Index(a, "#")
		if i < 0 {
			i = len(a)
		}

		got := a[:i] + Robot.String()
		x.Equal("acme/adminrobot", got)

		u, err := slug.Parse(got)
		x.NoError(err, "this was expected to still parse; that is the danger")
		x.Equal("adminrobot", u.Alias())
	})

	t.Run("replaces the word rather than adding to it", func(t *testing.T) {
		x := require.New(t)

		v := slug.MustParse("@acme/admin#holder").WithDomain(Robot)
		x.Equal("@acme/admin#robot", v.String())
	})

	t.Run("leaves the slug it was asked about alone", func(t *testing.T) {
		x := require.New(t)

		v := slug.MustParse("@acme/admin#holder")
		u := v.WithDomain(Robot)

		x.Equal(Robot, u.Domain())
		x.Equal(Holder, v.Domain(), "the original was changed")
	})
}

func TestWithTenant(t *testing.T) {
	t.Run("puts the slug under the tenant it names", func(t *testing.T) {
		x := require.New(t)

		// The form this came from took a slug here and read its *tenant*, so
		// handing it the tenant itself changed nothing at all -- and said
		// nothing about it.
		v, err := slug.MustParse("@admin#holder").WithTenant("acme")
		x.NoError(err)
		x.Equal("@acme/admin#holder", v.String())
	})

	t.Run("replaces one, and an empty one drops it", func(t *testing.T) {
		x := require.New(t)

		v, err := slug.MustParse("@acme/admin").WithTenant("other")
		x.NoError(err)
		x.Equal("@other/admin", v.String())

		v, err = v.WithTenant("")
		x.NoError(err)
		x.False(v.HasTenant())
		x.Equal("@admin", v.String())
	})

	t.Run("refuses a tenant that is not a name", func(t *testing.T) {
		x := require.New(t)

		_, err := slug.MustParse("@admin").WithTenant("Acme Corp")
		x.ErrorIs(err, slug.ErrAlias)
	})
}

func TestText(t *testing.T) {
	t.Run("goes into a document and comes back the same", func(t *testing.T) {
		x := require.New(t)

		v := slug.MustParse("@acme/arm-01#robot")
		b, err := v.MarshalText()
		x.NoError(err)
		x.Equal("@acme/arm-01#robot", string(b))

		var u slug.Slug
		x.NoError(u.UnmarshalText(b))
		x.Equal(v, u)
	})

	t.Run("a field nobody filled in is absent, not wrong", func(t *testing.T) {
		x := require.New(t)

		var v slug.Slug
		x.NoError(v.UnmarshalText([]byte("  ")))
		x.True(v.IsZero())

		// Parse still refuses it: whoever called Parse had a string they
		// believed was a name.
		_, err := slug.Parse("")
		x.Error(err)
	})

	t.Run("and one filled in wrongly is refused where it is read", func(t *testing.T) {
		x := require.New(t)

		var v slug.Slug
		x.Error(v.UnmarshalText([]byte("@acme/Arm_01")))
	})
}
