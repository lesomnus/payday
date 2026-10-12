package trailpkcs11

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAURINamesALibraryATokenAndAKey, as RFC 7512 writes one.
func TestAURINamesALibraryATokenAndAKey(t *testing.T) {
	x := require.New(t)

	pin := filepath.Join(t.TempDir(), "pin")
	x.NoError(os.WriteFile(pin, []byte("8341\n"), 0o600))

	u, err := ParseURI("PKCS11:token=pay%20day;serial=0123;slot-id=7;object=check;id=%01%A0;type=private;library-manufacturer=SoftHSM" +
		"?module-path=/usr/lib/softhsm/libsofthsm2.so&pin-source=" + pin)
	x.NoError(err)
	x.Equal("/usr/lib/softhsm/libsofthsm2.so", u.Module)
	x.Equal(map[string]string{"token": "pay day", "serial": "0123"}, u.Match)
	x.Equal(map[string]string{"library-manufacturer": "SoftHSM"}, u.Library)
	x.True(u.HasSlot)
	x.Equal(uint(7), u.Slot)
	x.True(u.HasObject)
	x.Equal("check", u.Object)
	x.Equal([]byte{0x01, 0xa0}, u.ID)
	x.Equal("8341", u.Pin, "the file's, without the line's end")

	u, err = ParseURI("pkcs11:?module-path=/lib/x.so&pin-source=file://" + pin)
	x.NoError(err)
	x.Equal("8341", u.Pin, "a file: URI")
	x.False(u.HasObject)
	x.Nil(u.ID)

	u, err = ParseURI("pkcs11:object=a+b?module-path=/lib/x.so&pin-value=1%2B2")
	x.NoError(err)
	x.Equal("a+b", u.Object, "a plus is a plus, not a space")
	x.Equal("1+2", u.Pin)
}

// TestAURIThatSaysWhatTheRFCDoesNotIsRefused rather than read past: an
// attribute nothing reads matches every key on the token.
func TestAURIThatSaysWhatTheRFCDoesNotIsRefused(t *testing.T) {
	x := require.New(t)

	for v, want := range map[string]string{
		"pkcs11:objet=checkpoints?module-path=/lib/x.so":                    "objet",
		"pkcs11:object=a;object=b?module-path=/lib/x.so":                    "said twice",
		"pkcs11:object=a?module-path=/lib/x.so&pin-sorce=/run/pin":          "pin-sorce",
		"pkcs11:object=a?module-name=softhsm2":                              "module-path=",
		"pkcs11:object=a":                                                   "module-path",
		"pkcs11:type=public?module-path=/lib/x.so":                          "private",
		"pkcs11:slot-id=one?module-path=/lib/x.so":                          "slot-id",
		"pkcs11:object?module-path=/lib/x.so":                               "attribute and a value",
		"pkcs11:object=%zz?module-path=/lib/x.so":                           "percent",
		"tpm:object=a?module-path=/lib/x.so":                                "pkcs11:",
		"pkcs11:?module-path=/lib/x.so&pin-source=|/bin/askpin":             "command",
		"pkcs11:?module-path=/lib/x.so&pin-source=file://elsewhere/run/pin": "this host",
	} {
		_, err := ParseURI(v)
		x.ErrorContains(err, want, v)
	}

	_, err := ParseURI("pkcs11:?module-path=/lib/x.so&pin-value=8341&pin-source=/run/pin")
	x.ErrorContains(err, "two PINs")
	x.NotContains(err.Error(), "8341")
	_, err = ParseURI("pkcs11:?module-path=/lib/x.so&pin-source=" + filepath.Join(t.TempDir(), "nothing"))
	x.ErrorContains(err, "pin-source")
}
