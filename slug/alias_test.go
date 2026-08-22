package slug_test

import (
	"encoding/base32"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/slug"
)

// confusable is what the alphabet exists to keep out of a name a person has to
// read off a screen and type somewhere else.
const confusable = "il01o"

func TestValidate(t *testing.T) {
	t.Run("accepts", func(t *testing.T) {
		for _, tc := range []struct {
			v    string
			what string
		}{
			{"a", "a single letter"},
			{"ab", "letters"},
			{"a1", "a digit after a letter"},
			{"arm-01", "a hyphen"},
			{"a-b-c", "several hyphens"},
			{"acme-corp", "the ordinary case"},
			{"z", "the last letter, which a generator once could not produce"},
			{strings.Repeat("a", slug.AliasMaxLen), "exactly the limit"},

			// The reason the "@" exists. This is a UUID and it breaks none of
			// the rules, so nothing about the shape of a string says which of
			// the two kinds of reference it is.
			{"abcd1234-2a10-8abc-8a03-9f2e1c4d5b6a", "a UUID beginning with a hex letter"},
		} {
			t.Run(tc.what, func(t *testing.T) {
				require.NoError(t, slug.Validate(tc.v))
			})
		}
	})

	t.Run("refuses", func(t *testing.T) {
		for _, tc := range []struct {
			v    string
			what string
		}{
			{"", "nothing at all"},
			{" ", "a space"},
			{"1a", "a leading digit, which reads as a number"},
			{"-a", "a leading hyphen"},
			{"a-", "a trailing hyphen"},
			{"a--b", "two hyphens together"},
			{"-", "a hyphen alone"},

			// The one rule taken from an earlier system rather than from
			// the form this came from: an underscore is legal in neither a
			// DNS label nor a subdomain, and allowing it here would spend a
			// door that costs nothing to keep shut.
			{"a_b", "an underscore"},
			{"_a", "a leading underscore"},

			{"Acme", "an uppercase letter, which ParseAlias would have folded"},
			{"a b", "a space inside"},
			{"a.b", "a dot"},
			{"a/b", "a slash, which is the tenant separator"},
			{"a#b", "a hash, which is the domain separator"},
			{"@a", "the mark that is not part of the name"},
			{"aä", "a letter that is not one of the twenty-six"},
			{"a\n", "a newline at the end"},
			{strings.Repeat("a", slug.AliasMaxLen+1), "one character past the limit"},
		} {
			t.Run(tc.what, func(t *testing.T) {
				x := require.New(t)

				err := slug.Validate(tc.v)
				x.ErrorIs(err, slug.ErrAlias)
				x.Equal(codes.InvalidArgument, status.Code(err))
			})
		}
	})
}

func TestParseAlias(t *testing.T) {
	t.Run("folds the ways of writing one name into one name", func(t *testing.T) {
		x := require.New(t)

		for _, v := range []string{"acme", "  acme ", "ACME", "  Acme ", "\tAcMe\n"} {
			u, err := slug.ParseAlias(v)
			x.NoError(err)
			x.Equal("acme", u, "%q named something else", v)
		}
	})

	t.Run("judges what it normalized, not what it was given", func(t *testing.T) {
		x := require.New(t)

		// Trimming happens first, so a name that is only spaces is empty rather
		// than malformed, and the message says so.
		_, err := slug.ParseAlias("   ")
		x.ErrorIs(err, slug.ErrAlias)
		x.Contains(err.Error(), "empty")

		// And folding happens first, so an uppercase name is accepted here and
		// refused by Validate. Only one of the two is asked about typing.
		_, err = slug.ParseAlias("ACME")
		x.NoError(err)
		x.Error(slug.Validate("ACME"))
	})
}

func TestRandomAlias(t *testing.T) {
	t.Run("is an alias", func(t *testing.T) {
		x := require.New(t)

		for range 1000 {
			v := slug.RandomAlias()
			x.Len(v, 7)
			x.NoError(slug.Validate(v), "%q", v)
		}
	})

	// The birthday bound is the point rather than a nuisance, and demanding
	// none was this test being wrong about its own subject.
	//
	// Ten thousand draws from 23^7 collide about 1.5% of the time -- n^2/2N, or
	// 1e8 over 6.8e9 -- so a test that asserted uniqueness failed about one run
	// in seventy, and it did. What is worth asserting is that the draws are
	// wide and independent; uniqueness is the database's to enforce, and
	// [slug.Tries] is what answers when it says no.
	t.Run("collides about as often as the arithmetic says", func(t *testing.T) {
		x := require.New(t)

		const n = 10000
		seen := make(map[string]struct{}, n)
		dups := 0
		for range n {
			v := slug.RandomAlias()
			if _, dup := seen[v]; dup {
				dups++
			}
			seen[v] = struct{}{}
		}

		// One is expected about one run in seventy; three would mean the draws
		// are not what they claim to be, and that is what this catches.
		x.Less(dups, 3, "the names are narrower than %d^%d", len(slug.Alphabet), 7)
	})

	t.Run("holds nothing that can be misread", func(t *testing.T) {
		x := require.New(t)

		for range 1000 {
			v := slug.RandomAliasN(slug.AliasMaxLen)
			x.NotContains(v, "i")
			x.NotContains(v, "l")
			x.NotContains(v, "o")
			x.False(strings.ContainsAny(v, "0123456789"), "%q", v)
		}
	})

	// This is the defect the letters-only alphabet closed. Drawing the first
	// character from its own modulus is what made it possible, and the modulus
	// was one short.
	t.Run("any letter of the alphabet can begin one", func(t *testing.T) {
		x := require.New(t)

		seen := map[byte]struct{}{}
		for range 5000 {
			seen[slug.RandomAlias()[0]] = struct{}{}
		}

		for i := range len(slug.Alphabet) {
			x.Contains(seen, slug.Alphabet[i], "no name ever began with %q", slug.Alphabet[i])
		}
	})

	t.Run("and the form this came from could not begin one with z", func(t *testing.T) {
		x := require.New(t)

		// The form this came from: vs[0] = Charset[int(v0)%('z'-'a')], over a
		// 36-character charset. 'z'-'a' is 25, so the first character came out
		// of the first 25 letters however the byte fell.
		const charset = "abcdefghijklmnopqrstuvwxyz0123456789"

		seen := map[byte]struct{}{}
		for v := range 256 {
			seen[charset[v%('z'-'a')]] = struct{}{}
		}

		x.NotContains(seen, byte('z'),
			"this was expected to be unable to produce a z; if it can, re-read why RandomAlias does not do it this way")
	})

	t.Run("refuses a length that is not an alias", func(t *testing.T) {
		x := require.New(t)

		x.Panics(func() { slug.RandomAliasN(0) })
		x.Panics(func() { slug.RandomAliasN(-1) })
		x.Panics(func() { slug.RandomAliasN(slug.AliasMaxLen + 1) })
	})
}

func TestEncodeAlias(t *testing.T) {
	// Every byte there is, which is what makes the assertions below exhaustive
	// rather than lucky.
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}

	t.Run("is an alias whatever the bytes are", func(t *testing.T) {
		x := require.New(t)

		x.NoError(slug.Validate(slug.EncodeAliasN(all, slug.AliasMaxLen)))

		// Length is the input's length and nothing else. The form this came
		// from trimmed leading digits off a base32 string, so how long a name
		// was depended on what its bytes started with -- which is why it needed
		// a second encoding for when too little was left.
		for n := 1; n <= 40; n++ {
			v := slug.EncodeAlias(all[:n])
			x.Len(v, n)
			x.NoError(slug.Validate(v), "%d bytes spelled %q", n, v)
		}
	})

	t.Run("the same bytes always spell the same name", func(t *testing.T) {
		x := require.New(t)

		x.Equal(slug.EncodeAlias(all[:16]), slug.EncodeAlias(all[:16]))
		x.NotEqual(slug.EncodeAlias(all[:16]), slug.EncodeAlias(all[1:17]))
	})

	// The third defect. There were two paths through this and they disagreed
	// about the alphabet, so whether a name could hold an "l" depended on how
	// many digits its bytes happened to begin with.
	t.Run("holds nothing that can be misread, at any length", func(t *testing.T) {
		x := require.New(t)

		for n := 1; n <= len(all); n++ {
			v := slug.EncodeAlias(all[:n])
			x.False(strings.ContainsAny(v, confusable), "%d bytes spelled %q", n, v)
		}
	})

	t.Run("and the base32 path it had did", func(t *testing.T) {
		x := require.New(t)

		v := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(all))
		x.Contains(v, "i")
		x.Contains(v, "l")
		x.Contains(v, "o")
	})

	t.Run("refuses what cannot be a name", func(t *testing.T) {
		x := require.New(t)

		x.Panics(func() { slug.EncodeAlias(nil) })
		x.Panics(func() { slug.EncodeAlias([]byte{}) })
		x.Panics(func() { slug.EncodeAliasN(all, 0) })
	})
}
