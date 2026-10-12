//go:build cgo

package trailpkcs11

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/pkcs11"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/trail/internal/signed"
)

// SoftHSM is the token these are tried against: a PKCS #11 library that keeps
// its tokens in a directory, which a test can make and throw away.
//
//	PDTEST_SOFTHSM2=/usr/lib/softhsm/libsofthsm2.so go test ./trail/trailpkcs11/
//
// They make tokens, which is not a thing to ask of an HSM somebody uses, so
// they ask SoftHSM and nothing else.
const envSoftHSM = "PDTEST_SOFTHSM2"

const (
	soPin   = "5678"
	userPin = "1234"
)

func TestMain(m *testing.M) {
	if os.Getenv(envSoftHSM) != "" {
		// Read once, when the library is first initialized, so before any
		// test does.
		dir, err := os.MkdirTemp("", "softhsm")
		if err != nil {
			panic(err)
		}
		conf := filepath.Join(dir, "softhsm2.conf")
		if err := os.MkdirAll(filepath.Join(dir, "tokens"), 0o700); err != nil {
			panic(err)
		}
		if err := os.WriteFile(conf, fmt.Appendf(nil, "directories.tokendir = %s\nobjectstore.backend = file\nlog.level = ERROR\n", filepath.Join(dir, "tokens")), 0o600); err != nil {
			panic(err)
		}
		os.Setenv("SOFTHSM2_CONF", conf)

		code := m.Run()
		os.RemoveAll(dir)
		os.Exit(code)
	}

	os.Exit(m.Run())
}

func softhsm(t *testing.T) string {
	t.Helper()

	v := os.Getenv(envSoftHSM)
	if v == "" {
		t.Skipf("%s is not set; these make tokens on SoftHSM", envSoftHSM)
	}

	return v
}

// token is a token of a test's own, and a session on it as its user, which
// the test ends before it opens a key: a login is the whole process's.
type token struct {
	t     *testing.T
	ctx   *pkcs11.Ctx
	label string
	sess  pkcs11.SessionHandle
}

func newToken(t *testing.T) *token {
	t.Helper()
	x := require.New(t)

	c, err := library(softhsm(t))
	x.NoError(err)

	ids, err := c.GetSlotList(true)
	x.NoError(err)
	free := ^uint(0)
	for _, id := range ids {
		ti, err := c.GetTokenInfo(id)
		x.NoError(err)
		if ti.Flags&pkcs11.CKF_TOKEN_INITIALIZED == 0 {
			free = id
			break
		}
	}
	x.NotEqual(^uint(0), free, "SoftHSM keeps a free slot")

	// Labels are 32 bytes at most.
	label := fmt.Sprintf("t%d", time.Now().UnixNano()%1e15)
	x.NoError(c.InitToken(free, soPin, label))

	tk := &token{t: t, ctx: c, label: label}
	sess, err := c.OpenSession(tk.slot(), pkcs11.CKF_SERIAL_SESSION|pkcs11.CKF_RW_SESSION)
	x.NoError(err)
	x.NoError(c.Login(sess, pkcs11.CKU_SO, soPin))
	x.NoError(c.InitPIN(sess, userPin))
	x.NoError(c.Logout(sess))
	x.NoError(c.Login(sess, pkcs11.CKU_USER, userPin))
	tk.sess = sess

	t.Cleanup(tk.done)
	return tk
}

// slot finds the token again: SoftHSM moves a token to a slot of its own once
// it is initialized.
func (tk *token) slot() uint {
	tk.t.Helper()

	ids, err := tk.ctx.GetSlotList(true)
	require.NoError(tk.t, err)
	for _, id := range ids {
		ti, err := tk.ctx.GetTokenInfo(id)
		require.NoError(tk.t, err)
		if ti.Label == tk.label {
			return id
		}
	}

	tk.t.Fatalf("no token %s", tk.label)
	return 0
}

// done logs the test's session out and closes it.
func (tk *token) done() {
	if tk.sess == 0 {
		return
	}
	_ = tk.ctx.Logout(tk.sess)
	_ = tk.ctx.CloseSession(tk.sess)
	tk.sess = 0
}

func (tk *token) uri(path string) string {
	return fmt.Sprintf("pkcs11:token=%s%s?module-path=%s&pin-value=%s", tk.label, path, softhsm(tk.t), userPin)
}

type keyOpts struct {
	extractable bool
	insensitive bool
	noSign      bool
	always      bool
}

// key makes a key pair on the token, labelled and identified by `name`.
func (tk *token) key(kind string, name string, o keyOpts) (pkcs11.ObjectHandle, pkcs11.ObjectHandle) {
	tk.t.Helper()
	x := require.New(tk.t)

	common := func(vs ...*pkcs11.Attribute) []*pkcs11.Attribute {
		return append([]*pkcs11.Attribute{
			pkcs11.NewAttribute(pkcs11.CKA_TOKEN, true),
			pkcs11.NewAttribute(pkcs11.CKA_LABEL, name),
			pkcs11.NewAttribute(pkcs11.CKA_ID, []byte(name)),
		}, vs...)
	}
	priv := common(
		pkcs11.NewAttribute(pkcs11.CKA_PRIVATE, true),
		pkcs11.NewAttribute(pkcs11.CKA_SIGN, !o.noSign),
		pkcs11.NewAttribute(pkcs11.CKA_SENSITIVE, !o.insensitive),
		pkcs11.NewAttribute(pkcs11.CKA_EXTRACTABLE, o.extractable),
	)
	if o.always {
		priv = append(priv, pkcs11.NewAttribute(pkcs11.CKA_ALWAYS_AUTHENTICATE, true))
	}

	var m uint
	var pub []*pkcs11.Attribute
	ec := func(oid asn1.ObjectIdentifier) []*pkcs11.Attribute {
		der, err := asn1.Marshal(oid)
		x.NoError(err)
		return common(pkcs11.NewAttribute(pkcs11.CKA_VERIFY, true), pkcs11.NewAttribute(pkcs11.CKA_EC_PARAMS, der))
	}
	switch kind {
	case "ed25519":
		m, pub = 0x00001055, ec(oidEd25519)
	case "p256":
		m, pub = pkcs11.CKM_EC_KEY_PAIR_GEN, ec(oidP256)
	case "p384":
		m, pub = pkcs11.CKM_EC_KEY_PAIR_GEN, ec(oidP384)
	case "p521":
		m, pub = pkcs11.CKM_EC_KEY_PAIR_GEN, ec(oidP521)
	case "rsa2048", "rsa1024":
		bits := 2048
		if kind == "rsa1024" {
			bits = 1024
		}
		m, pub = pkcs11.CKM_RSA_PKCS_KEY_PAIR_GEN, common(
			pkcs11.NewAttribute(pkcs11.CKA_VERIFY, true),
			pkcs11.NewAttribute(pkcs11.CKA_MODULUS_BITS, bits),
			pkcs11.NewAttribute(pkcs11.CKA_PUBLIC_EXPONENT, []byte{1, 0, 1}),
		)
	default:
		tk.t.Fatalf("no key of kind %s", kind)
	}

	pubH, privH, err := tk.ctx.GenerateKeyPair(tk.sess, []*pkcs11.Mechanism{pkcs11.NewMechanism(m, nil)}, pub, priv)
	x.NoError(err, kind)

	return pubH, privH
}

// TestEachKindOfKeySignsACheckpointThatVerifies: what the token answers is
// what a checkpoint's verifier reads, for each kind of key a checkpoint is
// signed with -- which [trail.TrySigner] says by signing as a checkpoint is
// signed and verifying as one is verified.
func TestEachKindOfKeySignsACheckpointThatVerifies(t *testing.T) {
	tk := newToken(t)
	kinds := []string{"ed25519", "p256", "p384", "p521", "rsa2048"}
	for _, k := range kinds {
		tk.key(k, k, keyOpts{})
	}
	tk.done()

	for _, k := range kinds {
		t.Run(k, func(t *testing.T) {
			x := require.New(t)

			s, err := Open(tk.uri(";object=" + k))
			x.NoError(err)
			defer s.Close()

			x.NoError(trail.TrySigner(s))
			pub, err := trail.PublicKeyString(s.Public())
			x.NoError(err, "a public half a checkpoint carries")
			x.NotEmpty(pub)
		})
	}

	t.Run("rsa2048 with PSS", func(t *testing.T) {
		x := require.New(t)

		s, err := Open(tk.uri(";object=rsa2048"))
		x.NoError(err)
		defer s.Close()

		d := sha256.Sum256([]byte("a checkpoint"))
		sig, err := s.Sign(rand.Reader, d[:], crypto.SHA256)
		x.NoError(err)
		x.NoError(rsa.VerifyPKCS1v15(s.Public().(*rsa.PublicKey), crypto.SHA256, d[:], sig), "PKCS #1 v1.5, as asked")

		sig, err = s.Sign(rand.Reader, d[:], &rsa.PSSOptions{Hash: crypto.SHA256})
		x.NoError(err)
		x.NoError(rsa.VerifyPSS(s.Public().(*rsa.PublicKey), crypto.SHA256, d[:], sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}))
		x.Error(rsa.VerifyPKCS1v15(s.Public().(*rsa.PublicKey), crypto.SHA256, d[:], sig), "PSS, as asked")
	})
}

// TestAKeyTheTokenWouldGiveOutIsRefused: a key that is extractable, or not
// sensitive, is one the process can take, and a key in a device is there for
// the process not to have.
func TestAKeyTheTokenWouldGiveOutIsRefused(t *testing.T) {
	x := require.New(t)

	tk := newToken(t)
	tk.key("p256", "extractable", keyOpts{extractable: true})
	tk.key("p256", "insensitive", keyOpts{insensitive: true})
	tk.key("p256", "unsigning", keyOpts{noSign: true})
	tk.done()

	_, err := Open(tk.uri(";object=extractable"))
	x.ErrorContains(err, "CKA_EXTRACTABLE")
	_, err = Open(tk.uri(";object=insensitive"))
	x.ErrorContains(err, "CKA_SENSITIVE")
	_, err = Open(tk.uri(";object=unsigning"))
	x.ErrorContains(err, "may not sign")
}

// TestAKeyIsOneKey: named by its label or its id, and refused when what is
// named is more than one -- which of two keys signs is not a thing to leave
// to the order a token lists them in.
func TestAKeyIsOneKey(t *testing.T) {
	x := require.New(t)

	tk := newToken(t)
	tk.key("p256", "a", keyOpts{})
	tk.key("p256", "b", keyOpts{})
	tk.done()

	_, err := Open(tk.uri(""))
	x.ErrorContains(err, "2 private keys match")
	_, err = Open(tk.uri(";object=c"))
	x.ErrorContains(err, "no private key matches")

	a, err := Open(tk.uri(";object=a"))
	x.NoError(err)
	defer a.Close()
	b, err := Open(tk.uri(";id=b"))
	x.NoError(err)
	defer b.Close()
	x.NotEqual(a.Public(), b.Public())
	x.NoError(trail.TrySigner(b))

	// And a token is one token: with a second one, a URI that names none
	// names two.
	newToken(t).done()
	_, err = Open(fmt.Sprintf("pkcs11:object=a?module-path=%s&pin-value=%s", softhsm(t), userPin))
	x.ErrorContains(err, "tokens match")
}

// TestTheTokenIsLoggedInToWithThePinItWants, from the file the PIN is in.
func TestTheTokenIsLoggedInToWithThePinItWants(t *testing.T) {
	x := require.New(t)

	tk := newToken(t)
	tk.key("p256", "k", keyOpts{})
	tk.done()

	base := fmt.Sprintf("pkcs11:token=%s;object=k?module-path=%s", tk.label, softhsm(t))
	_, err := Open(base)
	x.ErrorContains(err, "wants a PIN")

	_, err = Open(base + "&pin-value=0000")
	x.ErrorContains(err, "C_Login")
	x.NotContains(err.Error(), "0000")

	pin := filepath.Join(t.TempDir(), "pin")
	x.NoError(os.WriteFile(pin, []byte(userPin+"\n"), 0o600))
	s, err := Open(base + "&pin-source=" + url.PathEscape(pin))
	x.NoError(err)
	defer s.Close()
	x.NoError(trail.TrySigner(s))
}

// TestTheTokenAnswersForTheKeyItSaysItIs: a token whose public key object is
// not its private key's -- two objects of one id -- signs, and what it signs
// is refused rather than handed over to be a checkpoint nobody can verify.
func TestTheTokenAnswersForTheKeyItSaysItIs(t *testing.T) {
	x := require.New(t)

	tk := newToken(t)
	aPub, _ := tk.key("p256", "a", keyOpts{})
	bPub, _ := tk.key("p256", "b", keyOpts{})

	// b's point, under a's id.
	vs, err := tk.ctx.GetAttributeValue(tk.sess, bPub, []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_EC_PARAMS, nil),
		pkcs11.NewAttribute(pkcs11.CKA_EC_POINT, nil),
	})
	x.NoError(err)
	x.NoError(tk.ctx.DestroyObject(tk.sess, aPub))
	_, err = tk.ctx.CreateObject(tk.sess, []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_PUBLIC_KEY),
		pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, pkcs11.CKK_EC),
		pkcs11.NewAttribute(pkcs11.CKA_TOKEN, true),
		pkcs11.NewAttribute(pkcs11.CKA_ID, []byte("a")),
		pkcs11.NewAttribute(pkcs11.CKA_EC_PARAMS, vs[0].Value),
		pkcs11.NewAttribute(pkcs11.CKA_EC_POINT, vs[1].Value),
	})
	x.NoError(err)
	tk.done()

	s, err := Open(tk.uri(";object=a"))
	x.NoError(err, "nothing the token says disagrees, until it signs")
	defer s.Close()

	d := sha256.Sum256([]byte("a checkpoint"))
	_, err = s.Sign(rand.Reader, d[:], crypto.SHA256)
	x.ErrorIs(err, signed.ErrMismatch)
	x.Error(trail.TrySigner(s))
}

// TestAPublicHalfMayBeACertificate: a token that keeps a key's certificate
// rather than its public key object, as one a CA was asked to sign for.
func TestAPublicHalfMayBeACertificate(t *testing.T) {
	x := require.New(t)

	tk := newToken(t)
	pubH, _ := tk.key("p384", "k", keyOpts{})
	vs, err := tk.ctx.GetAttributeValue(tk.sess, pubH, []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_EC_POINT, nil)})
	x.NoError(err)
	var pt []byte
	_, err = asn1.Unmarshal(vs[0].Value, &pt)
	x.NoError(err)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P384(), pt)
	x.NoError(err)

	ca, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	x.NoError(err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "checkpoints"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, ca)
	x.NoError(err)

	x.NoError(tk.ctx.DestroyObject(tk.sess, pubH))
	_, err = tk.ctx.CreateObject(tk.sess, []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_CERTIFICATE),
		pkcs11.NewAttribute(pkcs11.CKA_CERTIFICATE_TYPE, pkcs11.CKC_X_509),
		pkcs11.NewAttribute(pkcs11.CKA_TOKEN, true),
		pkcs11.NewAttribute(pkcs11.CKA_ID, []byte("k")),
		pkcs11.NewAttribute(pkcs11.CKA_SUBJECT, []byte{0x30, 0x00}),
		pkcs11.NewAttribute(pkcs11.CKA_VALUE, der),
	})
	x.NoError(err)
	tk.done()

	s, err := Open(tk.uri(";object=k"))
	x.NoError(err)
	defer s.Close()
	x.True(pub.Equal(s.Public()))
	x.NoError(trail.TrySigner(s))

	// And with neither, it says what it looked for.
	tk2 := newToken(t)
	pubH, _ = tk2.key("p256", "k", keyOpts{})
	x.NoError(tk2.ctx.DestroyObject(tk2.sess, pubH))
	tk2.done()
	_, err = Open(tk2.uri(";object=k"))
	x.ErrorContains(err, "public half is not on the token")
}

// TestASessionTheTokenDroppedIsOpenedAgain: a network HSM restarts, a card
// comes out and goes back in, and the next checkpoint is signed anyway.
func TestASessionTheTokenDroppedIsOpenedAgain(t *testing.T) {
	x := require.New(t)

	tk := newToken(t)
	tk.key("p256", "k", keyOpts{})
	slot := tk.slot()
	tk.done()

	s, err := Open(tk.uri(";object=k"))
	x.NoError(err)
	defer s.Close()
	x.NoError(trail.TrySigner(s))

	x.NoError(tk.ctx.CloseAllSessions(slot))
	x.NoError(trail.TrySigner(s), "after the token dropped the session")
}

// TestAKeyThatWantsItsPinAtEverySignatureIsGivenIt (CKA_ALWAYS_AUTHENTICATE),
// and one that is given none is refused at Open.
func TestAKeyThatWantsItsPinAtEverySignatureIsGivenIt(t *testing.T) {
	x := require.New(t)

	tk := newToken(t)
	tk.key("p256", "k", keyOpts{always: true})
	tk.done()

	s, err := Open(tk.uri(";object=k"))
	x.NoError(err)
	defer s.Close()
	x.True(s.always)
	x.NoError(trail.TrySigner(s))
	x.NoError(trail.TrySigner(s), "and at the next")
}

// TestAKeyACheckpointIsNotSignedWithIsRefused by what the token says of it, at
// Open, rather than at the first checkpoint.
func TestAKeyACheckpointIsNotSignedWithIsRefused(t *testing.T) {
	x := require.New(t)

	tk := newToken(t)
	tk.key("rsa1024", "short", keyOpts{})
	tk.done()

	s, err := Open(tk.uri(";object=short"))
	x.NoError(err, "the token's to have")
	defer s.Close()
	x.ErrorContains(trail.TrySigner(s), "1024 bits")
}
