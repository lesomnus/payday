//go:build cgo

package trailpkcs11

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"

	"github.com/miekg/pkcs11"

	"github.com/lesomnus/payday/trail/internal/signed"
)

// What PKCS #11 3.0 added for Ed25519, which the headers the binding was
// generated from predate.
const (
	ckkECEdwards = 0x00000040
	ckmEdDSA     = 0x00001057
)

// The DER of each curve's OID, as `CKA_EC_PARAMS` names it.
var (
	oidP256    = asn1.ObjectIdentifier{1, 2, 840, 10045, 3, 1, 7}
	oidP384    = asn1.ObjectIdentifier{1, 3, 132, 0, 34}
	oidP521    = asn1.ObjectIdentifier{1, 3, 132, 0, 35}
	oidEd25519 = asn1.ObjectIdentifier{1, 3, 101, 112}
)

// digestInfo is what PKCS #1 v1.5 puts before a digest, which `CKM_RSA_PKCS`
// is handed whole.
var digestInfo = map[crypto.Hash][]byte{
	crypto.SHA256: {0x30, 0x31, 0x30, 0x0d, 0x06, 0x09, 0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02, 0x01, 0x05, 0x00, 0x04, 0x20},
	crypto.SHA384: {0x30, 0x41, 0x30, 0x0d, 0x06, 0x09, 0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02, 0x02, 0x05, 0x00, 0x04, 0x30},
	crypto.SHA512: {0x30, 0x51, 0x30, 0x0d, 0x06, 0x09, 0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02, 0x03, 0x05, 0x00, 0x04, 0x40},
}

var pss = map[crypto.Hash][2]uint{
	crypto.SHA256: {pkcs11.CKM_SHA256, pkcs11.CKG_MGF1_SHA256},
	crypto.SHA384: {pkcs11.CKM_SHA384, pkcs11.CKG_MGF1_SHA384},
	crypto.SHA512: {pkcs11.CKM_SHA512, pkcs11.CKG_MGF1_SHA512},
}

// A library is loaded and initialized once a process, and never let go of:
// `C_Initialize` is the process's, a second one is refused, and a `C_Finalize`
// would end every other session the process has on it.
var (
	libsMu sync.Mutex
	libs   = map[string]*pkcs11.Ctx{}
)

func library(path string) (*pkcs11.Ctx, error) {
	libsMu.Lock()
	defer libsMu.Unlock()

	if c, ok := libs[path]; ok {
		return c, nil
	}

	c := pkcs11.New(path)
	if c == nil {
		return nil, fmt.Errorf("%s does not load as a PKCS #11 library", path)
	}
	// Already initialized is somebody else in this process who loaded it
	// first -- the app itself, say -- and is as good.
	if err := c.Initialize(); err != nil && !is(err, pkcs11.CKR_CRYPTOKI_ALREADY_INITIALIZED) {
		c.Destroy()
		return nil, fmt.Errorf("%s: C_Initialize: %w", path, err)
	}

	libs[path] = c
	return c, nil
}

func is(err error, rv uint) bool {
	var e pkcs11.Error
	return errors.As(err, &e) && uint(e) == rv
}

// Signer is a private key in a PKCS #11 token, which signs and does not give
// its private half out. It holds one session, and signs one thing at a time.
type Signer struct {
	ctx    *pkcs11.Ctx
	slot   uint
	pin    string
	find   []*pkcs11.Attribute
	pub    crypto.PublicKey
	always bool

	mu   sync.Mutex
	sess pkcs11.SessionHandle
	live bool
	key  pkcs11.ObjectHandle
}

var (
	_ crypto.Signer = (*Signer)(nil)
	_ io.Closer     = (*Signer)(nil)
)

// Open answers with the key `uri` names (see [URI]), logged in to, with its
// public half read.
//
// It refuses a key the token would give out -- `CKA_EXTRACTABLE`, or not
// `CKA_SENSITIVE` -- since a key here is for the process not to have, and one
// it can take is one it has.
func Open(uri string) (*Signer, error) {
	u, err := ParseURI(uri)
	if err != nil {
		return nil, err
	}
	c, err := library(u.Module)
	if err != nil {
		return nil, err
	}
	if err := matchLibrary(c, u); err != nil {
		return nil, err
	}

	slot, flags, err := slotOf(c, u)
	if err != nil {
		return nil, err
	}
	if flags&pkcs11.CKF_LOGIN_REQUIRED != 0 && u.Pin == "" {
		return nil, errors.New("the token wants a PIN to sign, and none is given: pin-source=<the file it is in>")
	}

	s := &Signer{ctx: c, slot: slot, pin: u.Pin}
	s.find = []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_PRIVATE_KEY)}
	if u.HasObject {
		s.find = append(s.find, pkcs11.NewAttribute(pkcs11.CKA_LABEL, u.Object))
	}
	if u.ID != nil {
		s.find = append(s.find, pkcs11.NewAttribute(pkcs11.CKA_ID, u.ID))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.session(); err != nil {
		return nil, err
	}
	if err := s.read(); err != nil {
		s.end()
		return nil, err
	}

	return s, nil
}

// matchLibrary holds the library to what the URI says of it.
func matchLibrary(c *pkcs11.Ctx, u *URI) error {
	if len(u.Library) == 0 {
		return nil
	}

	info, err := c.GetInfo()
	if err != nil {
		return fmt.Errorf("C_GetInfo: %w", err)
	}
	for k, want := range u.Library {
		have := ""
		switch k {
		case "library-manufacturer":
			have = info.ManufacturerID
		case "library-description":
			have = info.LibraryDescription
		case "library-version":
			have = fmt.Sprintf("%d.%d", info.LibraryVersion.Major, info.LibraryVersion.Minor)
			if !strings.Contains(want, ".") {
				have = fmt.Sprintf("%d", info.LibraryVersion.Major)
			}
		}
		if strings.TrimSpace(have) != want {
			return fmt.Errorf("the library's %s is %q, and the URI says %q", k, strings.TrimSpace(have), want)
		}
	}

	return nil
}

// slotOf is the one slot whose token the URI names, and what the token says
// of itself.
func slotOf(c *pkcs11.Ctx, u *URI) (uint, uint, error) {
	ids, err := c.GetSlotList(true)
	if err != nil {
		return 0, 0, fmt.Errorf("C_GetSlotList: %w", err)
	}

	found := []uint{}
	flags := uint(0)
	for _, id := range ids {
		if u.HasSlot && id != u.Slot {
			continue
		}
		si, err := c.GetSlotInfo(id)
		if err != nil {
			return 0, 0, fmt.Errorf("C_GetSlotInfo: %w", err)
		}
		ti, err := c.GetTokenInfo(id)
		if err != nil {
			return 0, 0, fmt.Errorf("C_GetTokenInfo: %w", err)
		}
		if ti.Flags&pkcs11.CKF_TOKEN_INITIALIZED == 0 {
			continue
		}

		have := map[string]string{
			"token":             ti.Label,
			"manufacturer":      ti.ManufacturerID,
			"serial":            ti.SerialNumber,
			"model":             ti.Model,
			"slot-description":  si.SlotDescription,
			"slot-manufacturer": si.ManufacturerID,
		}
		ok := true
		for k, want := range u.Match {
			if strings.TrimRight(have[k], " \x00") != want {
				ok = false
				break
			}
		}
		if ok {
			found = append(found, id)
			flags = ti.Flags
		}
	}

	switch len(found) {
	case 0:
		return 0, 0, errors.New("no token matches")
	case 1:
		return found[0], flags, nil
	}

	return 0, 0, fmt.Errorf("%d tokens match; name one by token=, serial= or slot-id=", len(found))
}

// session opens a session and logs in, unless one is open.
func (s *Signer) session() error {
	if s.live {
		return nil
	}

	sess, err := s.ctx.OpenSession(s.slot, pkcs11.CKF_SERIAL_SESSION)
	if err != nil {
		return fmt.Errorf("C_OpenSession: %w", err)
	}
	if s.pin != "" {
		// Logged in already is another session of this process on the
		// token: a login is the application's, not the session's.
		if err := s.ctx.Login(sess, pkcs11.CKU_USER, s.pin); err != nil && !is(err, pkcs11.CKR_USER_ALREADY_LOGGED_IN) {
			_ = s.ctx.CloseSession(sess)
			return fmt.Errorf("C_Login: %w", err)
		}
	}

	key, err := s.findOne(sess, s.find, "private key")
	if err != nil {
		_ = s.ctx.CloseSession(sess)
		return err
	}

	s.sess, s.key, s.live = sess, key, true
	return nil
}

// end closes the session, which a token that dropped it has done already.
func (s *Signer) end() {
	if s.live {
		_ = s.ctx.CloseSession(s.sess)
	}
	s.live = false
}

func (s *Signer) findOne(sess pkcs11.SessionHandle, find []*pkcs11.Attribute, what string) (pkcs11.ObjectHandle, error) {
	vs, err := s.findAll(sess, find)
	if err != nil {
		return 0, err
	}

	switch len(vs) {
	case 0:
		if what == "private key" && len(find) == 1 {
			return 0, errors.New("no private key on the token, or none this PIN may see")
		}
		return 0, fmt.Errorf("no %s matches", what)
	case 1:
		return vs[0], nil
	}

	return 0, fmt.Errorf("%d %ss match; name one by object= or id=", len(vs), what)
}

func (s *Signer) findAll(sess pkcs11.SessionHandle, find []*pkcs11.Attribute) ([]pkcs11.ObjectHandle, error) {
	if err := s.ctx.FindObjectsInit(sess, find); err != nil {
		return nil, fmt.Errorf("C_FindObjectsInit: %w", err)
	}
	defer func() { _ = s.ctx.FindObjectsFinal(sess) }()

	vs := []pkcs11.ObjectHandle{}
	for {
		got, _, err := s.ctx.FindObjects(sess, 8)
		if err != nil {
			return nil, fmt.Errorf("C_FindObjects: %w", err)
		}
		if len(got) == 0 {
			return vs, nil
		}
		vs = append(vs, got...)
	}
}

// attr reads one attribute of an object, and answers nil for one the object
// does not have.
func (s *Signer) attr(o pkcs11.ObjectHandle, t uint) ([]byte, error) {
	vs, err := s.ctx.GetAttributeValue(s.sess, o, []*pkcs11.Attribute{pkcs11.NewAttribute(t, nil)})
	if err != nil {
		if is(err, pkcs11.CKR_ATTRIBUTE_TYPE_INVALID) {
			return nil, nil
		}
		return nil, err
	}

	return vs[0].Value, nil
}

func (s *Signer) flag(o pkcs11.ObjectHandle, t uint, name string) (bool, error) {
	v, err := s.attr(o, t)
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	if len(v) != 1 {
		return false, fmt.Errorf("the token does not say whether the key is %s", name)
	}

	return v[0] != 0, nil
}

// read is what Open learns of the key: whether it may be used, and whether it
// can be taken; its kind; and its public half.
func (s *Signer) read() error {
	for _, f := range []struct {
		t    uint
		name string
		want bool
		why  string
	}{
		{pkcs11.CKA_SIGN, "CKA_SIGN", true, "the key may not sign"},
		{pkcs11.CKA_SENSITIVE, "CKA_SENSITIVE", true, "the token would give the key's value to anybody who asked: it is not CKA_SENSITIVE, and a key here is for the process not to have"},
		{pkcs11.CKA_EXTRACTABLE, "CKA_EXTRACTABLE", false, "the token would give the key out, wrapped, to anybody who asked: it is CKA_EXTRACTABLE, and a key here is for the process not to have. Make one that is not"},
	} {
		v, err := s.flag(s.key, f.t, f.name)
		if err != nil {
			return err
		}
		if v != f.want {
			return errors.New(f.why)
		}
	}
	if v, err := s.attr(s.key, pkcs11.CKA_ALWAYS_AUTHENTICATE); err != nil {
		return fmt.Errorf("CKA_ALWAYS_AUTHENTICATE: %w", err)
	} else if len(v) == 1 && v[0] != 0 {
		if s.pin == "" {
			return errors.New("the key wants its PIN at every signature, and no PIN is given: pin-source=…")
		}
		s.always = true
	}

	kind, err := s.attr(s.key, pkcs11.CKA_KEY_TYPE)
	if err != nil {
		return fmt.Errorf("CKA_KEY_TYPE: %w", err)
	}
	pub, err := s.public(ulong(kind))
	if err != nil {
		return err
	}

	s.pub = pub
	return nil
}

// public is the key's public half: the private key's own attributes for RSA,
// which has them, and otherwise its public key object -- or a certificate --
// of the same `CKA_ID`.
func (s *Signer) public(kind uint) (crypto.PublicKey, error) {
	if kind == pkcs11.CKK_RSA {
		n, err := s.attr(s.key, pkcs11.CKA_MODULUS)
		if err != nil {
			return nil, fmt.Errorf("CKA_MODULUS: %w", err)
		}
		e, err := s.attr(s.key, pkcs11.CKA_PUBLIC_EXPONENT)
		if err != nil {
			return nil, fmt.Errorf("CKA_PUBLIC_EXPONENT: %w", err)
		}
		if len(n) > 0 && len(e) > 0 && len(e) <= 4 {
			return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
		}
	}
	if kind != pkcs11.CKK_RSA && kind != pkcs11.CKK_EC && kind != ckkECEdwards {
		return nil, fmt.Errorf("a key of type %#x, and checkpoints are signed with Ed25519, ECDSA or RSA", kind)
	}

	id, err := s.attr(s.key, pkcs11.CKA_ID)
	if err != nil {
		return nil, fmt.Errorf("CKA_ID: %w", err)
	}
	if len(id) == 0 {
		return nil, errors.New("the key has no CKA_ID, which is how its public half is found beside it")
	}

	o, err := s.findOne(s.sess, []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_PUBLIC_KEY),
		pkcs11.NewAttribute(pkcs11.CKA_ID, id),
	}, "public key")
	if err == nil {
		return s.point(o, kind)
	}

	cert, cerr := s.findOne(s.sess, []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_CERTIFICATE),
		pkcs11.NewAttribute(pkcs11.CKA_CERTIFICATE_TYPE, pkcs11.CKC_X_509),
		pkcs11.NewAttribute(pkcs11.CKA_ID, id),
	}, "certificate")
	if cerr != nil {
		return nil, fmt.Errorf("the key's public half is not on the token -- no public key object of its CKA_ID (%v), and no certificate (%v)", err, cerr)
	}
	der, err := s.attr(cert, pkcs11.CKA_VALUE)
	if err != nil {
		return nil, fmt.Errorf("the certificate's CKA_VALUE: %w", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("the certificate: %w", err)
	}

	return c.PublicKey, nil
}

// point reads an EC or Edwards public key object.
func (s *Signer) point(o pkcs11.ObjectHandle, kind uint) (crypto.PublicKey, error) {
	if kind == pkcs11.CKK_RSA {
		n, err := s.attr(o, pkcs11.CKA_MODULUS)
		if err != nil {
			return nil, fmt.Errorf("CKA_MODULUS: %w", err)
		}
		e, err := s.attr(o, pkcs11.CKA_PUBLIC_EXPONENT)
		if err != nil {
			return nil, fmt.Errorf("CKA_PUBLIC_EXPONENT: %w", err)
		}
		if len(n) == 0 || len(e) == 0 || len(e) > 4 {
			return nil, errors.New("the public key object has no modulus or exponent")
		}

		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	}

	params, err := s.attr(o, pkcs11.CKA_EC_PARAMS)
	if err != nil {
		return nil, fmt.Errorf("CKA_EC_PARAMS: %w", err)
	}
	raw, err := s.attr(o, pkcs11.CKA_EC_POINT)
	if err != nil {
		return nil, fmt.Errorf("CKA_EC_POINT: %w", err)
	}
	// The point is an OCTET STRING, as the standard says, or by some tokens
	// the bytes alone.
	pt := raw
	var inner []byte
	if rest, err := asn1.Unmarshal(raw, &inner); err == nil && len(rest) == 0 {
		pt = inner
	}

	if kind == ckkECEdwards {
		if !curve(params, oidEd25519, "edwards25519") {
			return nil, errors.New("an Edwards key on a curve other than Ed25519, and checkpoints are signed with Ed25519")
		}
		if len(pt) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("an Ed25519 public key of %d bytes", len(pt))
		}

		return ed25519.PublicKey(pt), nil
	}

	var c elliptic.Curve
	switch {
	case curve(params, oidP256, "prime256v1"):
		c = elliptic.P256()
	case curve(params, oidP384, "secp384r1"):
		c = elliptic.P384()
	case curve(params, oidP521, "secp521r1"):
		c = elliptic.P521()
	default:
		return nil, errors.New("an EC key on a curve other than P-256, P-384 or P-521, and checkpoints are signed on those")
	}

	k, err := ecdsa.ParseUncompressedPublicKey(c, pt)
	if err != nil {
		return nil, fmt.Errorf("CKA_EC_POINT: %w", err)
	}

	return k, nil
}

// curve answers whether CKA_EC_PARAMS names a curve, by its OID or its name.
func curve(params []byte, oid asn1.ObjectIdentifier, name string) bool {
	var o asn1.ObjectIdentifier
	if rest, err := asn1.Unmarshal(params, &o); err == nil && len(rest) == 0 {
		return o.Equal(oid)
	}

	var s string
	if rest, err := asn1.UnmarshalWithParams(params, &s, "printable"); err == nil && len(rest) == 0 {
		return s == name
	}

	return false
}

// ulong reads a CK_ULONG, which is the machine's own, in its own order.
func ulong(b []byte) uint {
	switch len(b) {
	case 8:
		return uint(binary.NativeEndian.Uint64(b))
	case 4:
		return uint(binary.NativeEndian.Uint32(b))
	}

	return ^uint(0)
}

// Public is the key's public half, as the token said it at Open.
func (s *Signer) Public() crypto.PublicKey { return s.pub }

// Sign signs as [crypto.Signer] does: Ed25519 over the message itself
// (CKM_EDDSA), ECDSA over a digest (CKM_ECDSA, answered in ASN.1), RSA with
// PKCS #1 v1.5 or, given [rsa.PSSOptions], PSS. What the token answers is
// verified against [Signer.Public] before it is handed over.
func (s *Signer) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	h, err := signed.Hash(s.pub, digest, opts)
	if err != nil {
		return nil, err
	}

	var m *pkcs11.Mechanism
	data := digest
	switch s.pub.(type) {
	case ed25519.PublicKey:
		m = pkcs11.NewMechanism(ckmEdDSA, nil)
	case *ecdsa.PublicKey:
		m = pkcs11.NewMechanism(pkcs11.CKM_ECDSA, nil)
	case *rsa.PublicKey:
		if o, ok := opts.(*rsa.PSSOptions); ok {
			salt := uint(h.Size())
			if o.SaltLength > 0 {
				salt = uint(o.SaltLength)
			}
			m = pkcs11.NewMechanism(pkcs11.CKM_RSA_PKCS_PSS, pkcs11.NewPSSParams(pss[h][0], pss[h][1], salt))
		} else {
			m = pkcs11.NewMechanism(pkcs11.CKM_RSA_PKCS, nil)
			data = append(append([]byte{}, digestInfo[h]...), digest...)
		}
	}

	sig, err := s.sign(m, data)
	if err != nil {
		return nil, err
	}
	if _, ok := s.pub.(*ecdsa.PublicKey); ok {
		if sig, err = signed.Halves(sig); err != nil {
			return nil, err
		}
	}
	if err := signed.Check(s.pub, digest, opts, sig); err != nil {
		return nil, err
	}

	return sig, nil
}

// sign is one signature on the session, and on a new one when the token has
// dropped the old -- a network HSM restarted, a card pulled and put back.
func (s *Signer) sign(m *pkcs11.Mechanism, data []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var err error
	for range 2 {
		if err = s.session(); err != nil {
			return nil, err
		}

		var sig []byte
		if sig, err = s.once(m, data); err == nil {
			return sig, nil
		}
		if !lost(err) {
			return nil, err
		}
		s.end()
	}

	return nil, err
}

func (s *Signer) once(m *pkcs11.Mechanism, data []byte) ([]byte, error) {
	if err := s.ctx.SignInit(s.sess, []*pkcs11.Mechanism{m}, s.key); err != nil {
		return nil, fmt.Errorf("C_SignInit: %w", err)
	}
	if s.always {
		if err := s.ctx.Login(s.sess, pkcs11.CKU_CONTEXT_SPECIFIC, s.pin); err != nil {
			// With the operation it began, which nothing else ends.
			s.end()
			return nil, fmt.Errorf("C_Login for the signature: %w", err)
		}
	}

	sig, err := s.ctx.Sign(s.sess, data)
	if err != nil {
		return nil, fmt.Errorf("C_Sign: %w", err)
	}

	return sig, nil
}

// lost answers whether the session is gone, and a new one would do.
func lost(err error) bool {
	for _, rv := range []uint{
		pkcs11.CKR_SESSION_HANDLE_INVALID,
		pkcs11.CKR_SESSION_CLOSED,
		pkcs11.CKR_DEVICE_REMOVED,
		pkcs11.CKR_TOKEN_NOT_PRESENT,
		pkcs11.CKR_USER_NOT_LOGGED_IN,
		pkcs11.CKR_KEY_HANDLE_INVALID,
		pkcs11.CKR_OBJECT_HANDLE_INVALID,
	} {
		if is(err, rv) {
			return true
		}
	}

	return false
}

// Close ends the session. The library stays loaded for the process.
func (s *Signer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.end()
	return nil
}
