package auth

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// Domains the way generated code registers them: the tenant is payday's own and
// is always 1, and an app's start at 7.
const (
	domainTenant pdid.Domain = 1
	domainRobot  pdid.Domain = 7
	domainCell   pdid.Domain = 10
)

func init() {
	// What generated code does: the entities by name, and separately which of
	// them is the wall. The name is the app's -- these may be in its own proto
	// package -- so the tenant is registered as a number and not looked up.
	pdid.Register("fleet.Tenant", domainTenant, "tenant")
	pdid.Register("fleet.Robot", domainRobot, "robot")
	pdid.Register("fleet.Cell", domainCell, "cell")
	pdid.RegisterTenant(domainTenant)
}

func certOf(t *testing.T, vs ...string) *x509.Certificate {
	t.Helper()

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

// TestACertificateMayNameMoreThanOneThing.
//
// The form this came from counted URI SANs and refused two, which refused every
// certificate that says both which device is calling and which tenant holds it
// -- the ordinary shape for a device credential, and the one payday's own
// identifiers were designed to make readable.
//
// What made two names dangerous was never that there were two. It was having no
// rule for which is which, so that the answer falls to the order of a field and
// nothing errors on either end. The domain byte is the rule.
func TestACertificateMayNameMoreThanOneThing(t *testing.T) {
	robot := pdid.New(domainRobot)
	tenant := pdid.New(domainTenant)

	t.Run("the caller and the tenant that holds them", func(t *testing.T) {
		x := require.New(t)

		v, ok, err := certIdentity(certOf(t, "fleet:"+robot.String(), "fleet:"+tenant.String()))
		x.NoError(err)
		x.True(ok)
		x.Equal(robot.String(), v.Id)
		x.Equal(tenant.String(), v.TenantId)

		// And the other way round, because a test that tried one order would
		// pass on an implementation that takes the first or the last.
		v, ok, err = certIdentity(certOf(t, "fleet:"+tenant.String(), "fleet:"+robot.String()))
		x.NoError(err)
		x.True(ok)
		x.Equal(robot.String(), v.Id)
		x.Equal(tenant.String(), v.TenantId)
	})

	t.Run("the caller alone, which is the long-lived certificate", func(t *testing.T) {
		x := require.New(t)

		v, ok, err := certIdentity(certOf(t, "fleet:"+robot.String()))
		x.NoError(err)
		x.True(ok)
		x.Equal(robot.String(), v.Id)
		x.Empty(v.TenantId, "a tenant nobody wrote there")
	})

	t.Run("the path form still reads, and says nothing about the tenant", func(t *testing.T) {
		x := require.New(t)

		v, ok, err := certIdentity(certOf(t, "spiffe://host/@acme/arm-01"))
		x.NoError(err)
		x.True(ok)
		x.Equal("acme", v.Tenant)
		x.Equal("arm-01", v.Alias)
		x.Empty(v.TenantId)
	})
}

// TestTheNamespaceInFrontOfTheNameIsNotRead.
//
// A URI SAN is a name inside somebody's namespace, and what this reads is the
// name. The scheme was already ignored -- `fleet:` above means nothing, and any
// other word would have done -- so ignoring whatever else is in front of the
// name is the same rule rather than a new liberty.
//
// It is here because of a real certificate. An app issuing device certificates
// wrote `urn:fleet:{uuid}`, which is the ordinary way to spell a name in a
// namespace of one's own, and this refused every one of them: the opaque part
// of that URI is `fleet:{uuid}`, which is neither an identifier nor a name. Two
// systems that had independently agreed on UUIDv8 with a domain byte, and on a
// certificate carrying both the caller and its tenant, could not read each
// other over one colon.
func TestTheNamespaceInFrontOfTheNameIsNotRead(t *testing.T) {
	robot := pdid.New(domainRobot)
	tenant := pdid.New(domainTenant)

	for _, form := range []string{
		// The scheme carries it, which is what payday's own examples write.
		"fleet:",
		// A namespace of one's own, and what an app was actually issuing.
		"urn:fleet:",
		// The one URN namespace that is registered (RFC 4122), for a deployment
		// that wants a form nobody has to be told about.
		"urn:uuid:",
	} {
		t.Run(form, func(t *testing.T) {
			x := require.New(t)

			v, ok, err := certIdentity(certOf(t, form+robot.String(), form+tenant.String()))
			x.NoError(err)
			x.True(ok)
			x.Equal(robot.String(), v.Id)
			x.Equal(tenant.String(), v.TenantId)
		})
	}

	// And the half that makes cutting safe. Nothing payday accepts holds a
	// colon, so a value that has one was never a name this could read -- and
	// cutting at the last one must not turn that refusal into an acceptance of
	// whatever came after it.
	t.Run("a name it never could read is still refused", func(t *testing.T) {
		x := require.New(t)

		_, _, err := certIdentity(certOf(t, "urn:dev:mac:0024beffff804ff1"))
		x.ErrorIs(err, ErrAmbiguous)
	})
}

// TestACertificateThatAnswersOneQuestionTwiceIsRefused.
//
// Sorting by domain is what makes several names readable, and it is exactly why
// two of a kind cannot be: there is no second rule underneath to break the tie,
// so whichever this took would be the order of a field deciding who is calling.
func TestACertificateThatAnswersOneQuestionTwiceIsRefused(t *testing.T) {
	robot, other := pdid.New(domainRobot), pdid.New(domainRobot)
	tenant, elsewhere := pdid.New(domainTenant), pdid.New(domainTenant)

	for _, tc := range []struct {
		name string
		uris []string
		says string
	}{
		{
			"two callers of the same kind",
			[]string{"fleet:" + robot.String(), "fleet:" + other.String()},
			"nothing says which is calling",
		},
		{
			"two tenants",
			[]string{"fleet:" + tenant.String(), "fleet:" + elsewhere.String()},
			"names two tenants",
		},
		{
			// Both say which tenant. Nothing here reads a row, so there is
			// nothing to tell which of them is right.
			"a name and an identifier that both say the tenant",
			[]string{"spiffe://host/@acme/arm-01", "fleet:" + tenant.String()},
			"says which tenant twice",
		},
		{
			// The tenant is who holds a caller. On its own it names one row in
			// every tenant there is, which is the nobody a bare alias names.
			"a tenant and nobody in it",
			[]string{"fleet:" + tenant.String()},
			"names a tenant and nobody in it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			_, ok, err := certIdentity(certOf(t, tc.uris...))
			x.ErrorIs(err, ErrAmbiguous)
			x.ErrorContains(err, tc.says)
			x.False(ok)
		})
	}
}

// TestANameWithNowhereToGoIsRefusedRatherThanDropped.
//
// This is what the refusal is for. A certificate whose issuer narrowed a caller
// to one site, read by something that drops the site in silence, is a caller who
// is not narrowed and a certificate that says they are -- and both ends believe
// the narrowing happened.
//
// So it is refused until an [Identity] has somewhere to put it, and the refusal
// says what it saw.
func TestANameWithNowhereToGoIsRefusedRatherThanDropped(t *testing.T) {
	x := require.New(t)

	robot, cell := pdid.New(domainRobot), pdid.New(domainCell)

	_, ok, err := certIdentity(certOf(t, "fleet:"+robot.String(), "fleet:"+cell.String()))
	x.ErrorIs(err, ErrAmbiguous)
	x.False(ok)

	// By the words the schema registered, since that is what somebody looking
	// at the certificate has to change.
	x.ErrorContains(err, "robot")
	x.ErrorContains(err, "cell")
}

// TestAnUnreadableNameDoesNotFallThroughToTheCommonName.
//
// A certificate that carries a URI SAN meant to say something with it, and
// answering with a Common Name that happens to be there answers a question
// nobody asked -- which is how a name nobody manages any more comes back to
// life.
func TestAnUnreadableNameDoesNotFallThroughToTheCommonName(t *testing.T) {
	for _, tc := range []struct {
		name string
		uri  string
	}{
		// Opaque and not an identifier. The opaque form itself is readable --
		// it is how `fleet:<uuid>` is written -- so what is unreadable here is
		// what it says, not the shape it is in.
		{"an opaque Uri that is not a name", "urn:x:y"},

		// A UUID, and not one of ours: no app ever wrote a row with it.
		{"a Uuid from somewhere else", "fleet:f81d4fae-7dec-11d0-a765-00a0c91e6bf6"},

		// Nothing after the scheme at all.
		{"a Uri that says nothing", "https://host/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			c := certOf(t, tc.uri)
			c.Subject = pkix.Name{CommonName: "@acme/admin"}

			_, ok, err := certIdentity(c)
			x.ErrorIs(err, ErrAmbiguous)
			x.False(ok)
		})
	}

	t.Run("and is read when there is no Uri name at all", func(t *testing.T) {
		x := require.New(t)

		c := certOf(t)
		c.Subject = pkix.Name{CommonName: "@acme/admin"}

		v, ok, err := certIdentity(c)
		x.NoError(err)
		x.True(ok)
		x.Equal("acme", v.Tenant)
		x.Equal("admin", v.Alias)
	})

	t.Run("a certificate that names nobody is absent rather than wrong", func(t *testing.T) {
		x := require.New(t)

		// Another handler may still know this caller, so it is not an error.
		_, ok, err := certIdentity(certOf(t))
		x.NoError(err)
		x.False(ok)
	})
}
