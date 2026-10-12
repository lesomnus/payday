package trailawskms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/require"
)

// local-kms is the KMS these are tried against, as CI runs it:
//
//	docker run -d -p 8080:8080 nsmithuk/local-kms
//	PDTEST_AWSKMS=http://127.0.0.1:8080 go test ./...
//
// It is the one emulator found that signs a digest as KMS does: LocalStack
// now wants an account, and moto hashes what it is told is a digest. It has no
// Ed25519 keys, which are refused here anyway.
const envKMS = "PDTEST_AWSKMS"

// local points the SDK at local-kms, and at nothing of the desk's.
func local(t *testing.T) *kms.Client {
	t.Helper()

	ep := os.Getenv(envKMS)
	if ep == "" {
		t.Skipf("%s is not set; these make keys in local-kms", envKMS)
	}
	for k, v := range map[string]string{
		"AWS_ENDPOINT_URL_KMS":        ep,
		"AWS_REGION":                  "eu-west-2",
		"AWS_ACCESS_KEY_ID":           "local",
		"AWS_SECRET_ACCESS_KEY":       "local",
		"AWS_SESSION_TOKEN":           "",
		"AWS_PROFILE":                 "",
		"AWS_CONFIG_FILE":             os.DevNull,
		"AWS_SHARED_CREDENTIALS_FILE": os.DevNull,
		"AWS_EC2_METADATA_DISABLED":   "true",
	} {
		t.Setenv(k, v)
	}

	cfg, err := config.LoadDefaultConfig(context.Background())
	require.NoError(t, err)

	return kms.NewFromConfig(cfg)
}

func key(t *testing.T, c *kms.Client, spec types.KeySpec, usage types.KeyUsageType) *types.KeyMetadata {
	t.Helper()

	out, err := c.CreateKey(context.Background(), &kms.CreateKeyInput{KeySpec: spec, KeyUsage: usage})
	require.NoError(t, err)

	return out.KeyMetadata
}

// verified is a checkpoint's verifier, as `trail` has it: this module does not
// import payday, so it is said again here, and it is short.
func verified(pub crypto.PublicKey, msg, sig []byte) bool {
	d := sha256.Sum256(msg)
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return ecdsa.VerifyASN1(k, d[:], sig)
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, d[:], sig) == nil || rsa.VerifyPSS(k, crypto.SHA256, d[:], sig, nil) == nil
	}

	return false
}

func sign(t *testing.T, s crypto.Signer, msg []byte) []byte {
	t.Helper()

	d := sha256.Sum256(msg)
	sig, err := s.Sign(rand.Reader, d[:], crypto.SHA256)
	require.NoError(t, err)

	return sig
}

// TestEachKindOfKeySignsACheckpointThatVerifies, P-384 and P-521 over a
// SHA-256 digest among them.
func TestEachKindOfKeySignsACheckpointThatVerifies(t *testing.T) {
	c := local(t)

	for _, spec := range []types.KeySpec{
		types.KeySpecEccNistP256, types.KeySpecEccNistP384, types.KeySpecEccNistP521,
		types.KeySpecRsa2048, types.KeySpecRsa3072, types.KeySpecRsa4096,
	} {
		t.Run(string(spec), func(t *testing.T) {
			x := require.New(t)

			s, err := Open("awskms:///" + aws.ToString(key(t, c, spec, types.KeyUsageTypeSignVerify).KeyId))
			x.NoError(err)
			msg := []byte("a checkpoint")
			x.True(verified(s.Public(), msg, sign(t, s, msg)))

			if r, ok := s.Public().(*rsa.PublicKey); ok {
				d := sha256.Sum256(msg)
				x.NoError(rsa.VerifyPKCS1v15(r, crypto.SHA256, d[:], sign(t, s, msg)), "PKCS #1 v1.5, as asked")

				sig, err := s.Sign(rand.Reader, d[:], &rsa.PSSOptions{Hash: crypto.SHA256})
				x.NoError(err)
				x.NoError(rsa.VerifyPSS(r, crypto.SHA256, d[:], sig, nil))
				x.Error(rsa.VerifyPKCS1v15(r, crypto.SHA256, d[:], sig), "PSS, as asked")
			}
		})
	}
}

// TestAnAliasIsTheKeyItNamedAtOpen: moved under a running process, the
// process goes on signing with what it said it was.
func TestAnAliasIsTheKeyItNamedAtOpen(t *testing.T) {
	x := require.New(t)
	c := local(t)
	ctx := context.Background()

	a := key(t, c, types.KeySpecEccNistP256, types.KeyUsageTypeSignVerify)
	b := key(t, c, types.KeySpecEccNistP256, types.KeyUsageTypeSignVerify)
	alias := "alias/checkpoints-" + aws.ToString(a.KeyId)[:8]
	_, err := c.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String(alias), TargetKeyId: a.KeyId})
	x.NoError(err)

	s, err := Open("awskms:///" + alias)
	x.NoError(err)
	x.Equal(aws.ToString(a.Arn), s.ARN())

	_, err = c.UpdateAlias(ctx, &kms.UpdateAliasInput{AliasName: aws.String(alias), TargetKeyId: b.KeyId})
	x.NoError(err)
	msg := []byte("a checkpoint")
	x.True(verified(s.Public(), msg, sign(t, s, msg)), "with the key the alias named at Open")

	next, err := Open("awskms:///" + alias)
	x.NoError(err)
	x.Equal(aws.ToString(b.Arn), next.ARN())

	byArn, err := Open("awskms:///" + aws.ToString(b.Arn))
	x.NoError(err)
	x.True(next.Public().(*ecdsa.PublicKey).Equal(byArn.Public()))
}

// TestAKeyThatDoesNotSignACheckpointIsRefused at Open.
func TestAKeyThatDoesNotSignACheckpointIsRefused(t *testing.T) {
	x := require.New(t)
	c := local(t)

	_, err := Open("awskms:///" + aws.ToString(key(t, c, types.KeySpecSymmetricDefault, types.KeyUsageTypeEncryptDecrypt).KeyId))
	x.ErrorContains(err, "SIGN_VERIFY")

	k256 := key(t, c, types.KeySpecEccSecgP256k1, types.KeyUsageTypeSignVerify)
	_, err = Open("awskms:///" + aws.ToString(k256.KeyId))
	x.ErrorContains(err, "ECC_SECG_P256K1")

	off := key(t, c, types.KeySpecEccNistP256, types.KeyUsageTypeSignVerify)
	_, err = c.DisableKey(context.Background(), &kms.DisableKeyInput{KeyId: off.KeyId})
	x.NoError(err)
	_, err = Open("awskms:///" + aws.ToString(off.KeyId))
	x.ErrorContains(err, "a key that is Disabled", "said here, before KMS says it its own way")
}

// TestAnEd25519KeyIsRefused, which local-kms cannot make: what KMS signs of a
// checkpoint with one is either too short or not what is verified.
func TestAnEd25519KeyIsRefused(t *testing.T) {
	x := require.New(t)

	x.ErrorContains(signable(types.KeySpecEccNistEdwards25519), "4 KiB")
	x.ErrorContains(signable(types.KeySpecMlDsa65), "ML_DSA_65")
	x.NoError(signable(types.KeySpecEccNistP521))
}

// TestASignatureIsCheckedAgainstTheKeyItWasOpenedWith, by the verifier a
// checkpoint is verified with.
func TestASignatureIsCheckedAgainstTheKeyItWasOpenedWith(t *testing.T) {
	x := require.New(t)

	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	x.NoError(err)
	other, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	x.NoError(err)
	d := sha256.Sum256([]byte("a checkpoint"))
	sig, err := ecdsa.SignASN1(rand.Reader, k, d[:])
	x.NoError(err)

	x.NoError(check(k.Public(), d[:], crypto.SHA256, sig))
	x.ErrorIs(check(other.Public(), d[:], crypto.SHA256, sig), ErrMismatch)
}

// TestAURINamesAKeyAndPerhapsAnEndpoint, as sigstore writes one.
func TestAURINamesAKeyAndPerhapsAnEndpoint(t *testing.T) {
	x := require.New(t)

	u, err := ParseURI("awskms:///alias/payday-checkpoints")
	x.NoError(err)
	x.Equal(&URI{Key: "alias/payday-checkpoints"}, u)

	u, err = ParseURI("AWSKMS://kms.internal:443/arn:aws:kms:ap-northeast-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab")
	x.NoError(err)
	x.Equal(&URI{
		Key:      "arn:aws:kms:ap-northeast-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		Endpoint: "https://kms.internal:443",
		Region:   "ap-northeast-2",
	}, u)

	for v, want := range map[string]string{
		"awskms://":                          "no key",
		"awskms:///alias/x?region=us-east-1": "nothing else",
		"awskms://AKIA:secret@kms/alias/x":   "nothing else",
		"awskms:///arn:aws:s3:::bucket":      "not a KMS",
		"gcpkms:///alias/x":                  "awskms://",
	} {
		_, err := ParseURI(v)
		x.ErrorContains(err, want, v)
	}
}

// TestWhatKMSAnswersIsCheckedBeforeItIsHandedOver: a signer whose public half
// is not its key's -- which KMS gives no way to arrange, so it is arranged
// here -- fails to sign rather than signing what nobody can verify.
func TestWhatKMSAnswersIsCheckedBeforeItIsHandedOver(t *testing.T) {
	x := require.New(t)
	c := local(t)

	a, err := Open("awskms:///" + aws.ToString(key(t, c, types.KeySpecEccNistP256, types.KeyUsageTypeSignVerify).KeyId))
	x.NoError(err)
	b, err := Open("awskms:///" + aws.ToString(key(t, c, types.KeySpecEccNistP256, types.KeyUsageTypeSignVerify).KeyId))
	x.NoError(err)

	a.pub = b.pub
	d := sha256.Sum256([]byte("a checkpoint"))
	_, err = a.Sign(rand.Reader, d[:], crypto.SHA256)
	x.ErrorIs(err, ErrMismatch)
}
