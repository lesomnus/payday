// Package trailawskms signs the trail's checkpoints with a key in AWS KMS
// (#52).
//
// It is a module of its own, which does not import payday, so that the AWS
// SDK is in no binary that does not sign with KMS. An app registers it, once,
// where it says what else it is built out of:
//
//	import (
//		"github.com/lesomnus/payday/config"
//		"github.com/lesomnus/payday/trail/trailawskms"
//	)
//
//	func init() { config.RegisterSigner(trailawskms.Scheme, trailawskms.Open) }
//
// and then a key is named in `audit.checkpoints.key` the way sigstore names
// one -- a key id, an alias or an ARN, after an endpoint that is usually not
// there:
//
//	awskms:///alias/payday-checkpoints
//	awskms:///arn:aws:kms:ap-northeast-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab
//	awskms://kms.internal.example:443/alias/payday-checkpoints
//
// Everything else is the SDK's to find, as the AWS CLI finds it: credentials
// from the environment, a profile, a role for the task or the pod or the
// instance; the region from an ARN, or `AWS_REGION`; an endpoint from the URI,
// or `AWS_ENDPOINT_URL_KMS`. An alias is resolved at Open, and the key it
// named then is the one that signs: an alias moved under a running process
// moves for the next.
//
// # What signs
//
//   - `ECC_NIST_P256`, `ECC_NIST_P384`, `ECC_NIST_P521`: ECDSA over a SHA-256
//     digest. KMS signs P-384 only over a SHA-384 digest and P-521 over a
//     SHA-512 one, by their lengths, so the SHA-256 digest is handed over
//     padded with zeros in front -- which is the same integer, and so the
//     signature ECDSA makes of the digest itself. ECDSA reads a digest as a
//     number: one shorter than the curve's order is used whole.
//   - `RSA_2048`, `RSA_3072`, `RSA_4096`: PKCS #1 v1.5 over SHA-256, or PSS
//     when asked.
//
// Not `ECC_NIST_EDWARDS25519`, which is refused: KMS signs a message itself of
// 4 KiB at most, a checkpoint is longer once an archive has a few dozen
// chunks, and what it signs of a digest instead is Ed25519ph, which is not
// what a checkpoint's verifier reads. Whatever KMS answers is verified against
// the key's public half before it is handed over.
//
// # What it cannot give away
//
// Nothing: KMS has no way to hand out a key's private half, so there is
// nothing here to refuse. Whoever holds the process holds credentials that can
// sign while they hold them -- give the role `kms:Sign`, `kms:GetPublicKey` and
// `kms:DescribeKey` on the one key, and nothing else -- and not the key. A key
// of `EXTERNAL` origin was made somewhere else and imported, and whoever made
// it may have kept it; that is the operator's to decide, and KMS says it in
// the key's metadata.
package trailawskms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// Scheme is what `audit.checkpoints.key` begins with to name a key here.
const Scheme = "awskms"

// Timeout is how long a request to KMS may take.
const Timeout = 30 * time.Second

// URI is a key as `audit.checkpoints.key` names it here.
type URI struct {
	// Key is a key id, `alias/<name>`, or an ARN of either.
	Key string

	// Endpoint is where KMS is, when it is not where the SDK would look.
	Endpoint string

	// Region is an ARN's, or empty for the SDK's.
	Region string
}

// ParseURI reads a key's URI.
func ParseURI(v string) (*URI, error) {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil {
		return nil, errors.New("not a URI")
	}
	if !strings.EqualFold(u.Scheme, Scheme) {
		return nil, fmt.Errorf("not an %s:// URI", Scheme)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("an awskms:// URI is an endpoint and a key, and nothing else: credentials are the SDK's to find")
	}

	key := strings.TrimPrefix(u.Path, "/")
	if key == "" {
		return nil, errors.New("no key: awskms:///<key id, alias/<name>, or an ARN>")
	}
	r := &URI{Key: key}
	if u.Host != "" {
		r.Endpoint = "https://" + u.Host
	}
	if strings.HasPrefix(key, "arn:") {
		parts := strings.SplitN(key, ":", 6)
		if len(parts) != 6 || parts[2] != "kms" || parts[3] == "" {
			return nil, errors.New("an ARN that is not a KMS key's or alias's")
		}
		r.Region = parts[3]
	}

	return r, nil
}

// Signer is a key in KMS, which signs and does not give its private half out.
type Signer struct {
	client *kms.Client
	arn    string
	spec   types.KeySpec
	pub    crypto.PublicKey
}

var _ crypto.Signer = (*Signer)(nil)

// Open answers with the key `uri` names, its alias resolved, with its public
// half read.
func Open(uri string) (*Signer, error) {
	u, err := ParseURI(uri)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()

	opts := []func(*config.LoadOptions) error{}
	if u.Region != "" {
		opts = append(opts, config.WithRegion(u.Region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("awskms: %w", err)
	}
	client := kms.NewFromConfig(cfg, func(o *kms.Options) {
		if u.Endpoint != "" {
			o.BaseEndpoint = aws.String(u.Endpoint)
		}
	})

	return New(ctx, client, u.Key)
}

// New answers with the key `key` names -- a key id, an alias, or an ARN -- of
// a client already made, for an app that makes its own.
func New(ctx context.Context, client *kms.Client, key string) (*Signer, error) {
	d, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("awskms: %w", err)
	}
	m := d.KeyMetadata
	switch {
	case m.KeyUsage != types.KeyUsageTypeSignVerify:
		return nil, fmt.Errorf("a key for %s, and a checkpoint is signed by a key for SIGN_VERIFY", m.KeyUsage)
	case m.KeyState != types.KeyStateEnabled:
		return nil, fmt.Errorf("a key that is %s", m.KeyState)
	}
	if err := signable(m.KeySpec); err != nil {
		return nil, err
	}

	arn := aws.ToString(m.Arn)
	p, err := client.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(arn)})
	if err != nil {
		return nil, fmt.Errorf("awskms: %w", err)
	}
	pub, err := x509.ParsePKIXPublicKey(p.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("the key's public half: %w", err)
	}

	return &Signer{client: client, arn: arn, spec: m.KeySpec, pub: pub}, nil
}

// signable refuses a kind of key a checkpoint is not signed with here.
func signable(spec types.KeySpec) error {
	switch spec {
	case types.KeySpecEccNistP256, types.KeySpecEccNistP384, types.KeySpecEccNistP521,
		types.KeySpecRsa2048, types.KeySpecRsa3072, types.KeySpecRsa4096:
		return nil

	case types.KeySpecEccNistEdwards25519:
		return errors.New("an ECC_NIST_EDWARDS25519 key: KMS signs a message itself of 4 KiB at most, and a checkpoint is longer; " +
			"what it signs of a digest is Ed25519ph, which a checkpoint's verifier does not read. Make an ECC_NIST_P256 or RSA key")
	}

	return fmt.Errorf("a %s key, and a checkpoint is signed with ECDSA on P-256, P-384 or P-521, or RSA", spec)
}

// Public is the key's public half, as KMS said it at Open.
func (s *Signer) Public() crypto.PublicKey { return s.pub }

// ARN is the key that signs, its alias resolved.
func (s *Signer) ARN() string { return s.arn }

// algorithm is what KMS is asked to sign with, and the length of the digest
// it wants for it.
func (s *Signer) algorithm(h crypto.Hash, pss bool) (types.SigningAlgorithmSpec, int) {
	switch s.spec {
	case types.KeySpecEccNistP256:
		return types.SigningAlgorithmSpecEcdsaSha256, 32
	case types.KeySpecEccNistP384:
		return types.SigningAlgorithmSpecEcdsaSha384, 48
	case types.KeySpecEccNistP521:
		return types.SigningAlgorithmSpecEcdsaSha512, 64
	}

	v15 := map[crypto.Hash]types.SigningAlgorithmSpec{
		crypto.SHA256: types.SigningAlgorithmSpecRsassaPkcs1V15Sha256,
		crypto.SHA384: types.SigningAlgorithmSpecRsassaPkcs1V15Sha384,
		crypto.SHA512: types.SigningAlgorithmSpecRsassaPkcs1V15Sha512,
	}
	if pss {
		v15 = map[crypto.Hash]types.SigningAlgorithmSpec{
			crypto.SHA256: types.SigningAlgorithmSpecRsassaPssSha256,
			crypto.SHA384: types.SigningAlgorithmSpecRsassaPssSha384,
			crypto.SHA512: types.SigningAlgorithmSpecRsassaPssSha512,
		}
	}

	return v15[h], h.Size()
}

// Sign signs a digest as [crypto.Signer] does: ECDSA, in ASN.1, or RSA with
// PKCS #1 v1.5 or, given [rsa.PSSOptions], PSS. What KMS answers is verified
// against [Signer.Public] before it is handed over.
func (s *Signer) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	h := opts.HashFunc()
	switch h {
	case crypto.SHA256, crypto.SHA384, crypto.SHA512:
	default:
		return nil, fmt.Errorf("a %s digest, and KMS signs SHA-256, SHA-384 or SHA-512", h)
	}
	if len(digest) != h.Size() {
		return nil, fmt.Errorf("a digest of %d bytes, and %s is %d", len(digest), h, h.Size())
	}
	_, pss := opts.(*rsa.PSSOptions)
	alg, n := s.algorithm(h, pss)

	msg := digest
	if _, ok := s.pub.(*ecdsa.PublicKey); ok {
		if len(digest) > n {
			return nil, fmt.Errorf("a %s digest, longer than %s signs", h, alg)
		}
		// The same number, at the length KMS asks for; see the package.
		msg = make([]byte, n)
		copy(msg[n-len(digest):], digest)
	}

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	out, err := s.client.Sign(ctx, &kms.SignInput{
		KeyId:            aws.String(s.arn),
		Message:          msg,
		MessageType:      types.MessageTypeDigest,
		SigningAlgorithm: alg,
	})
	if err != nil {
		return nil, fmt.Errorf("awskms: %w", err)
	}
	if err := check(s.pub, digest, h, out.Signature); err != nil {
		return nil, err
	}

	return out.Signature, nil
}

// ErrMismatch is a signature KMS answered with that the key's public half
// does not verify.
var ErrMismatch = errors.New("KMS signed with a key other than the one it answered for: its signature does not verify against the public half it was opened with")

// check verifies a signature as a checkpoint is verified, over the digest
// itself: RSA with PKCS #1 v1.5 or PSS.
func check(pub crypto.PublicKey, digest []byte, h crypto.Hash, sig []byte) error {
	ok := false
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		ok = ecdsa.VerifyASN1(k, digest, sig)
	case *rsa.PublicKey:
		ok = rsa.VerifyPKCS1v15(k, h, digest, sig) == nil || rsa.VerifyPSS(k, h, digest, sig, nil) == nil
	}
	if !ok {
		return ErrMismatch
	}

	return nil
}
