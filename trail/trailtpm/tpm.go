package trailtpm

import (
	"crypto"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"

	"github.com/lesomnus/payday/trail/internal/signed"
)

// Scheme is what `audit.checkpoints.key` begins with to name a key here.
const Scheme = "tpm"

// DefaultDevice is the kernel's resource manager, which shares the TPM.
const DefaultDevice = "/dev/tpmrm0"

// URI is a key as `audit.checkpoints.key` names it here; see the package.
type URI struct {
	Handle tpm2.TPMHandle
	Device string
	Auth   []byte
}

// ParseURI reads a key's URI. An error never carries the authorization value.
func ParseURI(v string) (*URI, error) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(v), ":")
	if !ok || !strings.EqualFold(scheme, Scheme) {
		return nil, fmt.Errorf("not a %s: URI", Scheme)
	}

	handle, query, _ := strings.Cut(rest, "?")
	hex, ok := strings.CutPrefix(strings.ToLower(handle), "0x")
	n, err := strconv.ParseUint(hex, 16, 32)
	if !ok || err != nil || n < 0x81000000 || n > 0x81ffffff {
		return nil, fmt.Errorf("%q is not a persistent handle, as tpm2_evictcontrol names one: tpm:0x81000100", handle)
	}

	u := &URI{Handle: tpm2.TPMHandle(n), Device: DefaultDevice}
	if query == "" {
		return u, nil
	}
	seen := map[string]bool{}
	for _, a := range strings.Split(query, "&") {
		k, val, ok := strings.Cut(a, "=")
		if !ok {
			return nil, fmt.Errorf("%q is not an attribute and a value", k)
		}
		if seen[k] {
			return nil, fmt.Errorf("%s is said twice", k)
		}
		seen[k] = true
		d, err := url.PathUnescape(val)
		if err != nil {
			return nil, fmt.Errorf("%s: not percent-encoded", k)
		}

		switch k {
		case "device":
			u.Device = d
		case "auth-source":
			b, err := os.ReadFile(d)
			if err != nil {
				return nil, fmt.Errorf("auth-source: %w", err)
			}
			u.Auth = []byte(strings.TrimRight(string(b), "\r\n"))
		default:
			return nil, fmt.Errorf("%s is not something a tpm: URI says; it says device= and auth-source=", k)
		}
	}

	return u, nil
}

// Signer is a key at a persistent handle of a TPM, which signs and does not
// give its private half out. It signs one thing at a time.
type Signer struct {
	tpm    transport.TPMCloser
	handle tpm2.TPMHandle
	name   tpm2.TPM2BName
	auth   []byte
	pub    crypto.PublicKey

	// What the key was made to sign with, if it was made to sign with one
	// thing only: an algorithm, and a hash. Zero is anything.
	alg  tpm2.TPMAlgID
	hash tpm2.TPMAlgID

	mu sync.Mutex
}

var (
	_ crypto.Signer = (*Signer)(nil)
	_ io.Closer     = (*Signer)(nil)
)

// Open answers with the key `uri` names, on the device it names.
func Open(uri string) (*Signer, error) {
	u, err := ParseURI(uri)
	if err != nil {
		return nil, err
	}

	t, err := openDevice(u.Device)
	if err != nil {
		return nil, err
	}
	s, err := New(t, u.Handle, u.Auth)
	if err != nil {
		_ = t.Close()
		return nil, err
	}

	return s, nil
}

// New answers with the key at `handle` of a TPM already open -- one on
// Windows, through `go-tpm/tpm2/transport/windowstpm`, which [Open] does not
// reach -- authorized by `auth`. The signer closes `t` when it is closed.
//
// It refuses a key that can be duplicated out of the TPM, one that does not
// sign, and a restricted one; see the package.
func New(t transport.TPMCloser, handle tpm2.TPMHandle, auth []byte) (*Signer, error) {
	rp, err := tpm2.ReadPublic{ObjectHandle: handle}.Execute(t)
	if err != nil {
		return nil, fmt.Errorf("TPM2_ReadPublic %#x: %w", uint32(handle), err)
	}
	pub, err := rp.OutPublic.Contents()
	if err != nil {
		return nil, fmt.Errorf("the key's public area: %w", err)
	}

	a := pub.ObjectAttributes
	switch {
	case !a.SignEncrypt:
		return nil, errors.New("the key does not sign: it was made without `sign`")
	case a.Restricted:
		return nil, errors.New("the key is restricted -- an attestation key, which signs only what the TPM hashed itself -- and a checkpoint is hashed here; make one that is not")
	case !a.FixedTPM || !a.FixedParent:
		return nil, errors.New("the key can be duplicated out of this TPM -- it is not both `fixedtpm` and `fixedparent` -- and a key here is for the process not to have")
	}

	s := &Signer{tpm: t, handle: handle, name: rp.Name, auth: auth}
	switch pub.Type {
	case tpm2.TPMAlgECC:
		p, err := pub.Parameters.ECCDetail()
		if err != nil {
			return nil, err
		}
		switch p.Scheme.Scheme {
		case tpm2.TPMAlgNull:
		case tpm2.TPMAlgECDSA:
			d, err := p.Scheme.Details.ECDSA()
			if err != nil {
				return nil, err
			}
			s.alg, s.hash = tpm2.TPMAlgECDSA, d.HashAlg
		default:
			return nil, fmt.Errorf("an ECC key made to sign with %v, and a checkpoint is signed with ECDSA", p.Scheme.Scheme)
		}

	case tpm2.TPMAlgRSA:
		p, err := pub.Parameters.RSADetail()
		if err != nil {
			return nil, err
		}
		switch p.Scheme.Scheme {
		case tpm2.TPMAlgNull:
		case tpm2.TPMAlgRSASSA:
			d, err := p.Scheme.Details.RSASSA()
			if err != nil {
				return nil, err
			}
			s.alg, s.hash = tpm2.TPMAlgRSASSA, d.HashAlg
		case tpm2.TPMAlgRSAPSS:
			d, err := p.Scheme.Details.RSAPSS()
			if err != nil {
				return nil, err
			}
			s.alg, s.hash = tpm2.TPMAlgRSAPSS, d.HashAlg
		default:
			return nil, fmt.Errorf("an RSA key made for %v, which does not sign", p.Scheme.Scheme)
		}

	default:
		return nil, fmt.Errorf("a key of type %v, and a checkpoint is signed with ECDSA or RSA here", pub.Type)
	}

	if s.pub, err = tpm2.Pub(*pub); err != nil {
		return nil, fmt.Errorf("the key's public half: %w", err)
	}

	return s, nil
}

// Public is the key's public half, as the TPM said it at Open.
func (s *Signer) Public() crypto.PublicKey { return s.pub }

var hashes = map[crypto.Hash]tpm2.TPMAlgID{
	crypto.SHA256: tpm2.TPMAlgSHA256,
	crypto.SHA384: tpm2.TPMAlgSHA384,
	crypto.SHA512: tpm2.TPMAlgSHA512,
}

// Sign signs a digest as [crypto.Signer] does: ECDSA, answered in ASN.1, or RSA
// with PKCS #1 v1.5, or PSS when asked -- and PSS when asked for the other of a
// key made to sign nothing else, which is what a checkpoint's verifier allows
// for. What the TPM answers is verified against [Signer.Public] before it is
// handed over.
func (s *Signer) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	h, err := signed.Hash(s.pub, digest, opts)
	if err != nil {
		return nil, err
	}
	alg := hashes[h]
	if s.hash != 0 && s.hash != alg {
		return nil, fmt.Errorf("the key was made to sign %v digests, and was handed %s", s.hash, h)
	}

	scheme := tpm2.TPMAlgECDSA
	if _, ok := s.pub.(*rsa.PublicKey); ok {
		_, asked := opts.(*rsa.PSSOptions)
		switch {
		case s.alg == tpm2.TPMAlgRSASSA && asked:
			return nil, errors.New("the key was made to sign with PKCS #1 v1.5, and was asked for PSS")
		case s.alg == tpm2.TPMAlgRSAPSS || asked:
			scheme = tpm2.TPMAlgRSAPSS
		default:
			scheme = tpm2.TPMAlgRSASSA
		}
	}

	s.mu.Lock()
	rsp, err := tpm2.Sign{
		KeyHandle: tpm2.AuthHandle{Handle: s.handle, Name: s.name, Auth: tpm2.PasswordAuth(s.auth)},
		Digest:    tpm2.TPM2BDigest{Buffer: digest},
		InScheme: tpm2.TPMTSigScheme{
			Scheme:  scheme,
			Details: tpm2.NewTPMUSigScheme(scheme, &tpm2.TPMSSchemeHash{HashAlg: alg}),
		},
		// No ticket: one is for a restricted key, which this is not.
		Validation: tpm2.TPMTTKHashCheck{Tag: tpm2.TPMSTHashCheck, Hierarchy: tpm2.TPMRHNull},
	}.Execute(s.tpm)
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("TPM2_Sign: %w", err)
	}

	var sig []byte
	switch scheme {
	case tpm2.TPMAlgECDSA:
		e, err := rsp.Signature.Signature.ECDSA()
		if err != nil {
			return nil, err
		}
		if sig, err = signed.ECDSA(e.SignatureR.Buffer, e.SignatureS.Buffer); err != nil {
			return nil, err
		}
	case tpm2.TPMAlgRSASSA:
		r, err := rsp.Signature.Signature.RSASSA()
		if err != nil {
			return nil, err
		}
		sig = r.Sig.Buffer
	case tpm2.TPMAlgRSAPSS:
		r, err := rsp.Signature.Signature.RSAPSS()
		if err != nil {
			return nil, err
		}
		sig = r.Sig.Buffer
	}
	if err := signed.Check(s.pub, digest, opts, sig); err != nil {
		return nil, err
	}

	return sig, nil
}

// Close lets the TPM go. The key stays where it is.
func (s *Signer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.tpm.Close()
}
