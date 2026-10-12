package signertpm_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/trail/trailtpm"
)

// TestAKeyInATPMIsALineOfConfiguration, made the way `trail/trailtpm` says to
// make one -- tpm2-tools, against swtpm here -- and verified elsewhere by the
// public half `tpm2_readpublic` writes.
//
//	PDTEST_SWTPM=/tmp/swtpm/sock go test ./config/signertpm/
//
// tpm2-tools reach swtpm through its control socket too, which is the server's
// path and `.ctrl`.
func TestAKeyInATPMIsALineOfConfiguration(t *testing.T) {
	x := require.New(t)
	x.Contains(config.Signers(), trailtpm.Scheme)

	sock := os.Getenv("PDTEST_SWTPM")
	if sock == "" {
		t.Skip("PDTEST_SWTPM is not set; this makes a key in swtpm")
	}

	dir := t.TempDir()
	tool := func(name string, args ...string) ([]byte, error) {
		cmd := exec.Command(name, args...)
		cmd.Env = append(os.Environ(), "TPM2TOOLS_TCTI=swtpm:path="+sock)
		return cmd.CombinedOutput()
	}
	run := func(name string, args ...string) {
		out, err := tool(name, args...)
		x.NoError(err, "%s: %s", name, out)
	}
	const handle = "0x81000200"
	_, _ = tool("tpm2_evictcontrol", "-C", "o", "-c", handle) // what an earlier run left, if it left anything
	run("tpm2_createprimary", "-Q", "-C", "o", "-G", "ecc256:ecdsa-sha256", "-c", filepath.Join(dir, "checkpoints.ctx"),
		"-a", "fixedtpm|fixedparent|sensitivedataorigin|userwithauth|sign")
	run("tpm2_evictcontrol", "-C", "o", "-c", filepath.Join(dir, "checkpoints.ctx"), handle)
	defer run("tpm2_evictcontrol", "-C", "o", "-c", handle)
	// What the two left loaded, which the kernel's resource manager flushes
	// and swtpm, which has none, keeps until it is told to.
	run("tpm2_flushcontext", "-t")
	run("tpm2_readpublic", "-Q", "-c", handle, "-f", "pem", "-o", filepath.Join(dir, "checkpoints.pub"))

	p, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Key: "tpm:" + handle + "?device=" + sock}}.Policy()
	x.NoError(err)
	defer p.Signer.(*trailtpm.Signer).Close()

	pem, err := os.ReadFile(filepath.Join(dir, "checkpoints.pub"))
	x.NoError(err)
	trusted, err := trail.ParsePublicKey(string(pem))
	x.NoError(err, "what tpm2_readpublic writes is what audit.checkpoints.trust reads")
	want, err := trail.PublicKeyString(trusted)
	x.NoError(err)
	got, err := trail.PublicKeyString(p.Signer.Public())
	x.NoError(err)
	x.Equal(want, got)
}
