package signerpkcs11_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/trail/trailpkcs11"
)

// TestAKeyInATokenIsALineOfConfiguration, made the way an operator makes one --
// `softhsm2-util` for the token, `pkcs11-tool` for the key -- and named by the
// URI `pkcs11-tool` prints for it.
//
//	PDTEST_SOFTHSM2=/usr/lib/softhsm/libsofthsm2.so go test ./config/signerpkcs11/
func TestAKeyInATokenIsALineOfConfiguration(t *testing.T) {
	x := require.New(t)
	x.Contains(config.Signers(), trailpkcs11.Scheme)

	module := os.Getenv("PDTEST_SOFTHSM2")
	if module == "" {
		t.Skip("PDTEST_SOFTHSM2 is not set; this makes a token on SoftHSM")
	}

	dir := t.TempDir()
	x.NoError(os.Mkdir(filepath.Join(dir, "tokens"), 0o700))
	conf := filepath.Join(dir, "softhsm2.conf")
	x.NoError(os.WriteFile(conf, fmt.Appendf(nil, "directories.tokendir = %s\nobjectstore.backend = file\n", filepath.Join(dir, "tokens")), 0o600))
	t.Setenv("SOFTHSM2_CONF", conf)

	run := func(name string, args ...string) string {
		out, err := exec.Command(name, args...).CombinedOutput()
		x.NoError(err, "%s: %s", name, out)
		return string(out)
	}
	run("softhsm2-util", "--init-token", "--free", "--label", "payday", "--pin", "1234", "--so-pin", "5678")
	run("pkcs11-tool", "--module", module, "--token-label", "payday", "--login", "--pin", "1234",
		"--keypairgen", "--key-type", "EC:prime256v1", "--label", "checkpoints", "--id", "01")
	listed := run("pkcs11-tool", "--module", module, "--token-label", "payday", "--login", "--pin", "1234", "--list-objects", "--type", "privkey")
	uri := regexp.MustCompile(`pkcs11:\S+type=private`).FindString(listed)
	x.NotEmpty(uri, listed)

	pin := filepath.Join(dir, "pin")
	x.NoError(os.WriteFile(pin, []byte("1234\n"), 0o600))

	p, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{
		Key: uri + "?module-path=" + module + "&pin-source=" + pin,
	}}.Policy()
	x.NoError(err)
	x.NotNil(p.Signer)
	x.NoError(p.Signer.(*trailpkcs11.Signer).Close())
}
