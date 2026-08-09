package auth

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/lesomnus/payday/frame"
)

// MethodMTLS is what [MTLS] calls itself.
const MethodMTLS = "mtls"

// MTLS reads who the caller is from the certificate the connection was made
// with.
//
//	URI SAN     spiffe://example.com/@acme/admin   -> @acme/admin
//	Common Name @acme/admin
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
func MTLS() Handler {
	return HandlerFunc(func(ctx context.Context) (Identity, error) {
		cert, err := peerCert(ctx)
		if err != nil {
			return Identity{}, err
		}

		v, ok, err := certName(cert)
		if err != nil {
			// A certificate that says two things is wrong rather than absent,
			// and it stops the search: falling through to another handler would
			// serve the request as somebody, which is the whole of what this
			// refuses to guess at.
			return Identity{}, fmt.Errorf("%s: %w", MethodMTLS, err)
		}
		if !ok {
			// A verified certificate that names nobody this app knows about is
			// not a bad credential -- it is a caller who has not said who they
			// are this way. Another handler may still know them.
			return Identity{}, ErrNoCredential
		}

		id, err := ParseName(v)
		if err != nil {
			// It did say a name, and the name is not one. That is wrong rather
			// than absent, and it stops the search.
			return Identity{}, fmt.Errorf("%s: %w", MethodMTLS, err)
		}

		// A certificate has nowhere to carry an attenuation, so it narrows
		// nothing and says so rather than leaving the zero Grant, which allows
		// nothing at all.
		id.Method, id.Grant = MethodMTLS, frame.Whole()

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

// ErrAmbiguous is a certificate that names more than one caller.
//
// It is its own error because it is the one failure here that is not about a
// caller doing something wrong: a certificate with two names is a certificate
// somebody issued, and what has to change is the issuing.
var ErrAmbiguous = errors.New("names more than one caller")

// certName is the name a certificate carries, if it carries exactly one.
//
// # Why it counts rather than takes the first
//
// This is the only place in payday where a trust boundary could be settled by
// **the order of a field**. With two URI SANs in one certificate, taking the
// first makes who authenticates a property of how the encoder happened to lay
// them out -- and no error is raised, on either end, ever.
//
// The second SAN does not need an attacker. A SPIFFE identity beside a service
// URL, a rename that adds the new name before removing the old, a certificate
// tool that appends a default: any of those quietly changes who the caller is.
// So more than one is refused, and the refusal names the problem rather than
// the caller.
//
// # And it does not fall through to the Common Name
//
// Only when there was no URI SAN at all. A certificate that carries one this
// cannot read is a certificate that meant to say something, and answering with
// a Common Name that happens to be there is answering a question that was not
// asked -- which is how a name nobody manages any more comes back to life.
//
// The scheme is still not read. `spiffe://host/a/b` and `https://host/a/b` both
// say `a/b`, because the scheme is about who issues names and this app has one
// issuer. What changed is that a URI whose path says nothing -- an opaque one
// like `urn:x:y`, where everything is in `Opaque` -- is counted as a SAN this
// cannot read rather than skipped in silence.
func certName(cert *x509.Certificate) (string, bool, error) {
	var name string
	var found int

	for _, u := range cert.URIs {
		found++
		if v := trimSlash(u.Path); v != "" && name == "" {
			name = v
		}
	}

	switch {
	case found > 1:
		return "", false, fmt.Errorf("%w: %d URI names", ErrAmbiguous, found)
	case found == 1 && name == "":
		// It carries one and this cannot read it. Answering with a Common Name
		// instead would be answering about a name nobody wrote there.
		return "", false, fmt.Errorf("%w: a URI name with no path", ErrAmbiguous)
	case found == 1:
		return name, true, nil
	}

	if v := cert.Subject.CommonName; v != "" {
		return v, true, nil
	}

	return "", false, nil
}

func trimSlash(v string) string {
	for len(v) > 0 && v[0] == '/' {
		v = v[1:]
	}
	return v
}
