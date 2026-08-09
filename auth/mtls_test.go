package auth

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCertNameCountsRatherThanChoosing.
//
// It is the only place in payday where a trust boundary could have been settled
// by the order of a field: with two URI SANs, taking the first makes who
// authenticates a property of how the encoder laid them out, and nothing errors
// on either end, ever.
//
// The second SAN needs no attacker -- a SPIFFE identity beside a service URL, a
// rename that adds the new name before removing the old, a tool that appends a
// default. So the test is about what happens with two, not about which one is
// picked.
func TestCertNameCountsRatherThanChoosing(t *testing.T) {
	at := func(vs ...string) *x509.Certificate {
		c := &x509.Certificate{}
		for _, v := range vs {
			u, err := url.Parse(v)
			if err != nil {
				t.Fatal(err)
			}
			c.URIs = append(c.URIs, u)
		}

		return c
	}

	t.Run("one URI name is the name", func(t *testing.T) {
		x := require.New(t)

		v, ok, err := certName(at("spiffe://host/@acme/arm-01"))
		x.NoError(err)
		x.True(ok)
		x.Equal("@acme/arm-01", v)
	})

	t.Run("two are refused rather than one being chosen", func(t *testing.T) {
		x := require.New(t)

		_, _, err := certName(at("spiffe://host/@acme/arm-01", "https://host/@other/admin"))
		x.ErrorIs(err, ErrAmbiguous)

		// And the other way round, because a test that only tried one order
		// would pass on the implementation that takes the last.
		_, _, err = certName(at("https://host/@other/admin", "spiffe://host/@acme/arm-01"))
		x.ErrorIs(err, ErrAmbiguous)
	})

	t.Run("a URI whose path says nothing is refused, not skipped", func(t *testing.T) {
		x := require.New(t)

		// An opaque URI -- `hday:0199...`, `urn:x:y` -- keeps everything in
		// Opaque and leaves Path empty. Skipping it fell through to the Common
		// Name, so a certificate that meant to say one thing authenticated as
		// another.
		c := at("hday:0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a")
		c.Subject = pkix.Name{CommonName: "@acme/admin"}

		_, ok, err := certName(c)
		x.ErrorIs(err, ErrAmbiguous)
		x.False(ok)
	})

	t.Run("the Common Name is read when there is no URI name at all", func(t *testing.T) {
		x := require.New(t)

		c := at()
		c.Subject = pkix.Name{CommonName: "@acme/admin"}

		v, ok, err := certName(c)
		x.NoError(err)
		x.True(ok)
		x.Equal("@acme/admin", v)
	})

	t.Run("a certificate that names nobody is absent rather than wrong", func(t *testing.T) {
		x := require.New(t)

		// Another handler may still know this caller, so it is not an error.
		_, ok, err := certName(at())
		x.NoError(err)
		x.False(ok)
	})
}
