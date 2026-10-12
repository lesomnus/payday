package trailtpm

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/linuxudstpm"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/trail/internal/signed"
)

// swtpm is the TPM these are tried against, over its Unix socket:
//
//	swtpm socket --tpm2 --server type=unixio,path=/tmp/swtpm/sock \
//	    --ctrl type=unixio,path=/tmp/swtpm/sock.ctrl --tpmstate dir=/tmp/swtpm \
//	    --flags not-need-init,startup-clear --daemon
//	PDTEST_SWTPM=/tmp/swtpm/sock go test ./trail/trailtpm/
//
// They make keys in its owner hierarchy and evict them after, which is not a
// thing to ask of a TPM a machine uses.
const envSwtpm = "PDTEST_SWTPM"

func socket(t *testing.T) string {
	t.Helper()

	v := os.Getenv(envSwtpm)
	if v == "" {
		t.Skipf("%s is not set; these make keys in swtpm", envSwtpm)
	}
	emptied.Do(func() { empty(t, v) })

	return v
}

var emptied sync.Once

// empty flushes every transient object, once, before the first test: swtpm has
// no resource manager to flush what another client left loaded -- tpm2-tools
// leave the objects they make -- and it holds only a few at a time.
func empty(t *testing.T, path string) {
	tpm, err := linuxudstpm.Open(path)
	require.NoError(t, err)
	defer tpm.Close()

	rsp, err := tpm2.GetCapability{Capability: tpm2.TPMCapHandles, Property: uint32(tpm2.TPMHTTransient) << 24, PropertyCount: 64}.Execute(tpm)
	require.NoError(t, err)
	hs, err := rsp.CapabilityData.Data.Handles()
	require.NoError(t, err)
	for _, h := range hs.Handle {
		_, _ = tpm2.FlushContext{FlushHandle: h}.Execute(tpm)
	}
}

func dial(t *testing.T) transport.TPMCloser {
	t.Helper()

	tpm, err := linuxudstpm.Open(socket(t))
	require.NoError(t, err)

	return tpm
}

var handles atomic.Uint32

func init() { handles.Store(0x81000100) }

func evict(tpm transport.TPM, h tpm2.TPMHandle) {
	rp, err := tpm2.ReadPublic{ObjectHandle: h}.Execute(tpm)
	if err != nil {
		return
	}
	_, _ = tpm2.EvictControl{
		Auth:             tpm2.TPMRHOwner,
		ObjectHandle:     tpm2.NamedHandle{Handle: h, Name: rp.Name},
		PersistentHandle: h,
	}.Execute(tpm)
}

// persist makes a key from `tmpl` in the owner hierarchy and keeps it at a
// handle of the test's own, which it evicts after.
func persist(t *testing.T, tmpl tpm2.TPMTPublic, auth []byte) tpm2.TPMHandle {
	t.Helper()

	h := tpm2.TPMHandle(handles.Add(1))
	persistAt(t, h, tmpl, auth)
	t.Cleanup(func() {
		tpm := dial(t)
		defer tpm.Close()
		evict(tpm, h)
	})

	return h
}

func persistAt(t *testing.T, h tpm2.TPMHandle, tmpl tpm2.TPMTPublic, auth []byte) {
	t.Helper()
	x := require.New(t)

	tpm := dial(t)
	defer tpm.Close()
	evict(tpm, h)

	rsp, err := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHOwner,
		InSensitive:   tpm2.TPM2BSensitiveCreate{Sensitive: &tpm2.TPMSSensitiveCreate{UserAuth: tpm2.TPM2BAuth{Buffer: auth}}},
		InPublic:      tpm2.New2B(tmpl),
	}.Execute(tpm)
	x.NoError(err)
	defer tpm2.FlushContext{FlushHandle: rsp.ObjectHandle}.Execute(tpm)

	_, err = tpm2.EvictControl{
		Auth:             tpm2.TPMRHOwner,
		ObjectHandle:     tpm2.NamedHandle{Handle: rsp.ObjectHandle, Name: rsp.Name},
		PersistentHandle: h,
	}.Execute(tpm)
	x.NoError(err)
}

// The attributes tpm2_createprimary is told for a key here, and the same
// for one with an authorization value, which a wrong try does not count
// against: the dictionary attack lockout is the whole TPM's.
var (
	signing = tpm2.TPMAObject{FixedTPM: true, FixedParent: true, SensitiveDataOrigin: true, UserWithAuth: true, SignEncrypt: true}
	authed  = tpm2.TPMAObject{FixedTPM: true, FixedParent: true, SensitiveDataOrigin: true, UserWithAuth: true, SignEncrypt: true, NoDA: true}
)

func ecc(curve tpm2.TPMECCCurve, a tpm2.TPMAObject, scheme tpm2.TPMAlgID, hash tpm2.TPMAlgID) tpm2.TPMTPublic {
	s := tpm2.TPMTECCScheme{Scheme: tpm2.TPMAlgNull}
	if scheme != tpm2.TPMAlgNull {
		s = tpm2.TPMTECCScheme{Scheme: scheme, Details: tpm2.NewTPMUAsymScheme(scheme, &tpm2.TPMSSigSchemeECDSA{HashAlg: hash})}
	}

	return tpm2.TPMTPublic{
		Type:             tpm2.TPMAlgECC,
		NameAlg:          tpm2.TPMAlgSHA256,
		ObjectAttributes: a,
		Parameters: tpm2.NewTPMUPublicParms(tpm2.TPMAlgECC, &tpm2.TPMSECCParms{
			Scheme:  s,
			CurveID: curve,
			KDF:     tpm2.TPMTKDFScheme{Scheme: tpm2.TPMAlgNull},
		}),
	}
}

func rsaKey(bits tpm2.TPMKeyBits, a tpm2.TPMAObject, scheme tpm2.TPMAlgID) tpm2.TPMTPublic {
	s := tpm2.TPMTRSAScheme{Scheme: tpm2.TPMAlgNull}
	switch scheme {
	case tpm2.TPMAlgRSASSA:
		s = tpm2.TPMTRSAScheme{Scheme: scheme, Details: tpm2.NewTPMUAsymScheme(scheme, &tpm2.TPMSSigSchemeRSASSA{HashAlg: tpm2.TPMAlgSHA256})}
	case tpm2.TPMAlgRSAPSS:
		s = tpm2.TPMTRSAScheme{Scheme: scheme, Details: tpm2.NewTPMUAsymScheme(scheme, &tpm2.TPMSSigSchemeRSAPSS{HashAlg: tpm2.TPMAlgSHA256})}
	}

	return tpm2.TPMTPublic{
		Type:             tpm2.TPMAlgRSA,
		NameAlg:          tpm2.TPMAlgSHA256,
		ObjectAttributes: a,
		Parameters:       tpm2.NewTPMUPublicParms(tpm2.TPMAlgRSA, &tpm2.TPMSRSAParms{Scheme: s, KeyBits: bits}),
	}
}

func uri(t *testing.T, h tpm2.TPMHandle, query string) string {
	return fmt.Sprintf("tpm:%#x?device=%s%s", uint32(h), socket(t), query)
}

// TestEachKindOfKeySignsACheckpointThatVerifies: what the TPM answers is what a
// checkpoint's verifier reads -- r and s as ASN.1, PSS where a key allows no
// other -- for each kind a TPM has.
func TestEachKindOfKeySignsACheckpointThatVerifies(t *testing.T) {
	socket(t)

	for name, tmpl := range map[string]tpm2.TPMTPublic{
		"p256":              ecc(tpm2.TPMECCNistP256, signing, tpm2.TPMAlgNull, 0),
		"p256 ecdsa-sha256": ecc(tpm2.TPMECCNistP256, signing, tpm2.TPMAlgECDSA, tpm2.TPMAlgSHA256),
		"p384":              ecc(tpm2.TPMECCNistP384, signing, tpm2.TPMAlgNull, 0),
		"p521":              ecc(tpm2.TPMECCNistP521, signing, tpm2.TPMAlgNull, 0),
		"rsa2048":           rsaKey(2048, signing, tpm2.TPMAlgNull),
		"rsa2048 rsassa":    rsaKey(2048, signing, tpm2.TPMAlgRSASSA),
		"rsa2048 rsapss":    rsaKey(2048, signing, tpm2.TPMAlgRSAPSS),
		"rsa3072":           rsaKey(3072, signing, tpm2.TPMAlgNull),
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			s, err := Open(uri(t, persist(t, tmpl, nil), ""))
			x.NoError(err)
			defer s.Close()

			x.NoError(trail.TrySigner(s))
			if r, ok := s.Public().(*rsa.PublicKey); ok {
				d := sha256.Sum256([]byte("a checkpoint"))
				sig, err := s.Sign(rand.Reader, d[:], crypto.SHA256)
				x.NoError(err)
				v15 := rsa.VerifyPKCS1v15(r, crypto.SHA256, d[:], sig) == nil
				x.Equal(name != "rsa2048 rsapss", v15, "PKCS #1 v1.5 as asked, but of a key made for PSS")
			}
		})
	}

	t.Run("rsa2048 asked for PSS", func(t *testing.T) {
		x := require.New(t)

		s, err := Open(uri(t, persist(t, rsaKey(2048, signing, tpm2.TPMAlgNull), nil), ""))
		x.NoError(err)
		defer s.Close()

		d := sha256.Sum256([]byte("a checkpoint"))
		sig, err := s.Sign(rand.Reader, d[:], &rsa.PSSOptions{Hash: crypto.SHA256})
		x.NoError(err)
		x.NoError(rsa.VerifyPSS(s.Public().(*rsa.PublicKey), crypto.SHA256, d[:], sig, nil))
		x.Error(rsa.VerifyPKCS1v15(s.Public().(*rsa.PublicKey), crypto.SHA256, d[:], sig), "PSS, as asked")
	})
}

// TestAKeyThatCanLeaveTheTPMIsRefused, and so is one that cannot sign what a
// checkpoint is.
func TestAKeyThatCanLeaveTheTPMIsRefused(t *testing.T) {
	x := require.New(t)
	socket(t)

	tpm := dial(t)
	srk, err := tpm2.CreatePrimary{PrimaryHandle: tpm2.TPMRHOwner, InPublic: tpm2.New2B(tpm2.ECCSRKTemplate)}.Execute(tpm)
	x.NoError(err)
	defer tpm2.FlushContext{FlushHandle: srk.ObjectHandle}.Execute(tpm)

	// A child that may be duplicated to another parent, and so out of this
	// TPM: a primary key cannot be.
	dup := ecc(tpm2.TPMECCNistP256, tpm2.TPMAObject{SensitiveDataOrigin: true, UserWithAuth: true, SignEncrypt: true}, tpm2.TPMAlgNull, 0)
	created, err := tpm2.Create{ParentHandle: tpm2.NamedHandle{Handle: srk.ObjectHandle, Name: srk.Name}, InPublic: tpm2.New2B(dup)}.Execute(tpm)
	x.NoError(err)
	loaded, err := tpm2.Load{ParentHandle: tpm2.NamedHandle{Handle: srk.ObjectHandle, Name: srk.Name}, InPrivate: created.OutPrivate, InPublic: created.OutPublic}.Execute(tpm)
	x.NoError(err)
	defer tpm2.FlushContext{FlushHandle: loaded.ObjectHandle}.Execute(tpm)
	h := tpm2.TPMHandle(handles.Add(1))
	_, err = tpm2.EvictControl{Auth: tpm2.TPMRHOwner, ObjectHandle: tpm2.NamedHandle{Handle: loaded.ObjectHandle, Name: loaded.Name}, PersistentHandle: h}.Execute(tpm)
	x.NoError(err)
	defer evict(tpm, h)
	tpm.Close()

	_, err = Open(uri(t, h, ""))
	x.ErrorContains(err, "duplicated out of this TPM")

	restricted := ecc(tpm2.TPMECCNistP256, tpm2.TPMAObject{FixedTPM: true, FixedParent: true, SensitiveDataOrigin: true, UserWithAuth: true, SignEncrypt: true, Restricted: true}, tpm2.TPMAlgECDSA, tpm2.TPMAlgSHA256)
	_, err = Open(uri(t, persist(t, restricted, nil), ""))
	x.ErrorContains(err, "restricted")

	_, err = Open(uri(t, persist(t, tpm2.ECCSRKTemplate, nil), ""))
	x.ErrorContains(err, "does not sign")

	// Made to sign SHA-384 digests, and a checkpoint is SHA-256's.
	s, err := Open(uri(t, persist(t, ecc(tpm2.TPMECCNistP384, signing, tpm2.TPMAlgECDSA, tpm2.TPMAlgSHA384), nil), ""))
	x.NoError(err)
	defer s.Close()
	x.ErrorContains(trail.TrySigner(s), "made to sign")

	// And one made for PKCS #1 v1.5 is not asked for PSS.
	s, err = Open(uri(t, persist(t, rsaKey(2048, signing, tpm2.TPMAlgRSASSA), nil), ""))
	x.NoError(err)
	defer s.Close()
	d := sha256.Sum256([]byte("a checkpoint"))
	_, err = s.Sign(rand.Reader, d[:], &rsa.PSSOptions{Hash: crypto.SHA256})
	x.ErrorContains(err, "asked for PSS")
}

// TestAKeyWithAnAuthorizationValueIsGivenIt, from the file it is in.
func TestAKeyWithAnAuthorizationValueIsGivenIt(t *testing.T) {
	x := require.New(t)
	socket(t)

	h := persist(t, ecc(tpm2.TPMECCNistP256, authed, tpm2.TPMAlgNull, 0), []byte("s3cret"))

	s, err := Open(uri(t, h, ""))
	x.NoError(err, "the public half is anybody's to read")
	x.ErrorContains(trail.TrySigner(s), "TPM2_Sign")
	s.Close()

	auth := filepath.Join(t.TempDir(), "auth")
	x.NoError(os.WriteFile(auth, []byte("s3cret\n"), 0o600))
	s, err = Open(uri(t, h, "&auth-source="+auth))
	x.NoError(err)
	defer s.Close()
	x.NoError(trail.TrySigner(s))
}

// TestTheTPMAnswersForTheKeyItSaidItWas: a handle made a different key under a
// running process signs with that key, and what it signs is refused rather
// than handed over as a checkpoint nobody can verify.
func TestTheTPMAnswersForTheKeyItSaidItWas(t *testing.T) {
	x := require.New(t)
	socket(t)

	h := persist(t, ecc(tpm2.TPMECCNistP256, signing, tpm2.TPMAlgNull, 0), nil)
	s, err := Open(uri(t, h, ""))
	x.NoError(err)
	defer s.Close()
	x.NoError(trail.TrySigner(s))

	// Another key, from another seed's worth of template: a P-256 key with
	// a different unique field is a different key.
	other := ecc(tpm2.TPMECCNistP256, signing, tpm2.TPMAlgNull, 0)
	other.Unique = tpm2.NewTPMUPublicID(tpm2.TPMAlgECC, &tpm2.TPMSECCPoint{X: tpm2.TPM2BECCParameter{Buffer: []byte("another")}})
	persistAt(t, h, other, nil)

	d := sha256.Sum256([]byte("a checkpoint"))
	_, err = s.Sign(rand.Reader, d[:], crypto.SHA256)
	x.ErrorIs(err, signed.ErrMismatch)
}

// TestAURINamesAPersistentHandle, and what it may say besides.
func TestAURINamesAPersistentHandle(t *testing.T) {
	x := require.New(t)

	u, err := ParseURI("tpm:0x81000100")
	x.NoError(err)
	x.Equal(tpm2.TPMHandle(0x81000100), u.Handle)
	x.Equal(DefaultDevice, u.Device)
	x.Nil(u.Auth)

	auth := filepath.Join(t.TempDir(), "auth")
	x.NoError(os.WriteFile(auth, []byte("s3cret\n"), 0o600))
	u, err = ParseURI("TPM:0X8100FFFF?device=/dev/tpm0&auth-source=" + auth)
	x.NoError(err)
	x.Equal(tpm2.TPMHandle(0x8100ffff), u.Handle)
	x.Equal("/dev/tpm0", u.Device)
	x.Equal([]byte("s3cret"), u.Auth)

	for v, want := range map[string]string{
		"tpm:81000100":                        "persistent handle",
		"tpm:0x80000001":                      "persistent handle",
		"tpm:0x01000001":                      "persistent handle",
		"tpm:0x81000100?devise=/dev/tpm0":     "devise",
		"tpm:0x81000100?device=a&device=b":    "said twice",
		"tpm:0x81000100?auth-source=/nothing": "auth-source",
		"tpm:0x81000100?device":               "attribute and a value",
		"pkcs11:0x81000100":                   "tpm:",
	} {
		_, err := ParseURI(v)
		x.ErrorContains(err, want, v)
	}

	_, err = Open("tpm:0x81000100?device=" + t.TempDir())
	x.ErrorContains(err, "neither a TPM's device nor an emulator's socket")
}
