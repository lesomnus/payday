package auth

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// MethodMTls is what [MTls] calls itself.
const MethodMTls = "mtls"

// MTls reads who the caller is from the certificate the connection was made
// with.
//
//	URI SAN     spiffe://example.com/@acme/admin   -> @acme/admin
//	URI SAN     x:0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a
//	URI SAN     urn:fleet:0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a
//	Common Name @acme/admin
//
// A certificate may carry **several** names, and they are told apart by the
// domain byte rather than by their order; see [certIdentity].
//
// It checks nothing, and that is right: by the time a request arrives, the TLS
// layer has already verified the chain against the certificate authorities the
// server was configured with, and a connection whose certificate did not check
// out never became a connection. What is left is to read the name.
//
// It reads it only out of a chain the server verified, never out of what the
// peer merely sent. Those are the same thing when client certificates are
// required or verified-if-given, and they are not the same thing under
// `tls.RequestClientCert`, where anyone may send anything. Reading the
// verified chain is what makes the difference not matter.
//
// A URI SAN is preferred over the Common Name because it is the place a name
// belongs -- a CN is a display string that happens to be usable -- but the CN
// is read too, since that is where a name usually is.
//
// The "@" is read here for the same reason it is read in a header, and by the
// same code: a certificate names an actor either way an actor is named, and
// [ParseName] holds why the two cannot be told apart by looking. It is a legal
// character in the path of a URI and in a Common Name, so nothing about
// issuing the certificate changes.
//
// There is no Provider counterpart. A certificate is presented when the
// connection is made, not written into a request, so the other half of this is
// `grpc.WithTransportCredentials` and a TLS configuration.
func MTls() Handler {
	return HandlerFunc(func(ctx context.Context) (Identity, error) {
		cert, err := peerCert(ctx)
		if err != nil {
			return Identity{}, err
		}

		id, ok, err := certIdentity(cert)
		if err != nil {
			// A certificate that says two things is wrong rather than absent,
			// and it stops the search: falling through to another handler would
			// serve the request as somebody, which is the whole of what this
			// refuses to guess at.
			return Identity{}, fmt.Errorf("%s: %w", MethodMTls, err)
		}
		if !ok {
			// A verified certificate that names nobody this app knows about is
			// not a bad credential -- it is a caller who has not said who they
			// are this way. Another handler may still know them.
			return Identity{}, ErrNoCredential
		}

		// A certificate has nowhere to carry an attenuation, so it narrows
		// nothing and says so rather than leaving the zero Grant, which allows
		// nothing at all.
		id.Method, id.Grant = MethodMTls, frame.Whole()

		return id, nil
	})
}

// peerCert is the leaf of the chain the server verified, if there is one.
func peerCert(ctx context.Context) (*x509.Certificate, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, ErrNoCredential
	}

	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		// Not TLS at all, so there is no certificate to read.
		return nil, ErrNoCredential
	}

	// VerifiedChains rather than PeerCertificates: the first is what this
	// server checked, the second is what the other end sent.
	if len(info.State.VerifiedChains) == 0 || len(info.State.VerifiedChains[0]) == 0 {
		return nil, ErrNoCredential
	}

	return info.State.VerifiedChains[0][0], nil
}

// ErrAmbiguous is a certificate this cannot read exactly one caller out of --
// because it names none, or because it names several and nothing says which.
//
// It is its own error because it is the one failure here that is not about a
// caller doing something wrong: such a certificate is one somebody issued, and
// what has to change is the issuing.
var ErrAmbiguous = errors.New("cannot say who this certificate is for")

// certIdentity is who a certificate names, out of every name it carries.
//
// # Several names is structure, not ambiguity
//
// A certificate for a device says more than one thing about it: which device,
// and which tenant holds it. Those are two URI SANs, and the form this came
// from refused the certificate outright -- it counted them and called two an
// ambiguity.
//
// That was the right refusal resting on a wrong premise. What makes two names
// dangerous is having no rule for which is which, so that the answer falls to
// **the order of a field** and no error is ever raised on either end. payday
// has a rule: an identifier carries its domain, so a name says what kind of
// thing it names before anything looks it up. Two names of different kinds are
// not two answers to one question; they are answers to two.
//
// So this sorts rather than counts, and what it refuses is a certificate that
// answers one question twice:
//
//	device + tenant   the short-lived certificate. Read as both.
//	device            the long-lived one. Read as the caller.
//	device + device   refused -- which one is calling?
//	tenant            refused -- names a tenant and nobody in it.
//	device + site     refused -- see below.
//
// A name whose domain is neither the tenant's nor anything an [Identity] can
// hold is refused rather than skipped, and that is the point of refusing: a
// certificate that meant to narrow a caller to one site, read by something that
// drops the site in silence, is a caller who is not narrowed and a certificate
// that says they are.
//
// # And it does not fall through to the Common Name
//
// Only when there was no URI SAN at all. A certificate that carries one this
// cannot read is a certificate that meant to say something, and answering with
// a Common Name that happens to be there is answering a question that was not
// asked -- which is how a name nobody manages any more comes back to life.
//
// # The scheme is not read
//
// `spiffe://host/a/b` and `https://host/a/b` both say `a/b`, because the scheme
// is about who issues names and this app has one issuer. An opaque URI -- one
// with no authority and no path, like `fleet:0199c3f4-…`, where everything is
// in `Opaque` -- says what is after the colon. Both forms reach [ParseName],
// which is the same code a header goes through: a certificate names an actor
// either way an actor is named.
func certIdentity(cert *x509.Certificate) (Identity, bool, error) {
	if len(cert.URIs) == 0 {
		if v := cert.Subject.CommonName; v != "" {
			id, err := ParseName(v)
			if err != nil {
				return Identity{}, false, err
			}

			return id, true, nil
		}

		return Identity{}, false, nil
	}

	// What the schema said a tenant is. Generated code registers it out of the
	// entity marked `own: OWN_TENANT`, whatever an app called the proto package
	// -- so a process that has a schema always has this. One that does not
	// cannot have written a tenant identifier into a certificate either, and
	// every name is read as a caller, which is what this did before there was a
	// second one.
	tenant, sorted := pdid.TenantDomain()

	var (
		who  Identity // the caller, once one has been found
		what string   // and what named them, for the refusal
		held string   // the tenant it says holds them
	)

	for _, u := range cert.URIs {
		v := sanValue(u)
		if v == "" {
			return Identity{}, false, fmt.Errorf("%w: %q says nothing this can read", ErrAmbiguous, u)
		}

		id, err := ParseName(v)
		if err != nil {
			return Identity{}, false, fmt.Errorf("%w: %q: %w", ErrAmbiguous, v, err)
		}

		if k, ok := idOf(id); sorted && ok && k.Domain() == tenant {
			if held != "" {
				return Identity{}, false, fmt.Errorf("%w: names two tenants", ErrAmbiguous)
			}

			held = id.Id
			continue
		}

		if what != "" {
			return Identity{}, false, fmt.Errorf("%w: names both a %s and a %s, and nothing says which is calling",
				ErrAmbiguous, what, kindOf(id))
		}

		who, what = id, kindOf(id)
	}

	if what == "" {
		// Every name it carried was the tenant's. A tenant is who holds a
		// caller, so on its own it names one row in every tenant there is,
		// which is the same nobody a bare alias names; see [parseSlug].
		return Identity{}, false, fmt.Errorf("%w: names a tenant and nobody in it", ErrAmbiguous)
	}

	if held != "" {
		if who.Tenant != "" {
			// `@acme/admin` beside the tenant's identifier. Both say which
			// tenant, and this does not look one up to find out whether they
			// agree -- a resolver does that, with the row in front of it.
			return Identity{}, false, fmt.Errorf("%w: says which tenant twice, as %q and as an identifier",
				ErrAmbiguous, who.Tenant)
		}

		who.TenantId = held
	}

	return who, true, nil
}

// sanValue is what a Uri SAN says, whichever of the two shapes it is written
// in.
//
// # The namespace in front of the name is not read
//
// A URI SAN is a name inside somebody's namespace, and payday reads the name.
// It already ignored the **scheme** -- `x:` in the example above means nothing,
// and any other scheme would have done -- so this ignores whatever else is in
// front of the name for the same reason: which namespace a deployment issues
// its certificates in is the deployment's, and payday has no opinion to have
// about it.
//
//	x:0199c3f4-...           -> 0199c3f4-...
//	urn:fleet:0199c3f4-...   -> 0199c3f4-...
//	urn:uuid:0199c3f4-...    -> 0199c3f4-...
//
// The last one is worth knowing about: `uuid` is the one URN namespace that is
// actually registered (RFC 4122), so a deployment that wanted a form nobody has
// to be told about would write that.
//
// It is safe to cut at the last colon because nothing payday accepts contains
// one. An identifier is a UUID and a name is `@tenant/alias`, whose alias is
// lowercase letters, digits and single hyphens -- see [ParseName]. A value with
// a colon in it was never a name this could read, so cutting cannot turn a
// refusal into an acceptance of something else.
//
// The hierarchical shape needs none of this. `spiffe://example.com/@acme/admin`
// puts the name in the path, and what is in front of it is the authority, which
// is already not read.
func sanValue(u *url.URL) string {
	if u.Opaque != "" {
		if i := strings.LastIndexByte(u.Opaque, ':'); i >= 0 {
			return u.Opaque[i+1:]
		}

		return u.Opaque
	}

	v := u.Path
	for len(v) > 0 && v[0] == '/' {
		v = v[1:]
	}

	return v
}

// idOf is the identifier an identity names, if it named one directly.
func idOf(v Identity) (pdid.Id, bool) {
	if v.Id == "" {
		return pdid.Nil, false
	}

	id, err := pdid.Parse(v.Id)

	return id, err == nil
}

// kindOf is what a name is, for a refusal that has to tell two of them apart.
// The domain's registered word -- "robot", "cell" -- when there is one, since
// that is what somebody looking at the certificate has to change.
func kindOf(v Identity) string {
	if id, ok := idOf(v); ok {
		return id.Domain().String()
	}
	if v.Id != "" {
		return "identifier"
	}

	return "name"
}
