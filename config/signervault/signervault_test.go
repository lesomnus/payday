package signervault_test

import (
	"bytes"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/trail/trailvault"
)

// TestAKeyInVaultIsALineOfConfiguration, with the address and the token where
// Vault's own tools read them.
//
//	PDTEST_VAULT=http://127.0.0.1:8200 go test ./config/signervault/
func TestAKeyInVaultIsALineOfConfiguration(t *testing.T) {
	x := require.New(t)
	x.Contains(config.Signers(), trailvault.Scheme)

	addr := os.Getenv("PDTEST_VAULT")
	if addr == "" {
		t.Skip("PDTEST_VAULT is not set; this makes a key in Vault's Transit")
	}
	token := os.Getenv("PDTEST_VAULT_TOKEN")
	if token == "" {
		token = "root"
	}

	post := func(path, body string) {
		req, err := http.NewRequest(http.MethodPost, addr+"/v1/"+path, bytes.NewBufferString(body))
		x.NoError(err)
		req.Header.Set("X-Vault-Token", token)
		rsp, err := http.DefaultClient.Do(req)
		x.NoError(err)
		rsp.Body.Close()
	}
	post("sys/mounts/transit", `{"type":"transit"}`)
	post("transit/keys/config-checkpoints", `{"type":"ed25519"}`)

	t.Setenv("VAULT_ADDR", addr)
	t.Setenv("VAULT_TOKEN", token)
	p, err := config.AuditConfig{Checkpoints: config.AuditCheckpointsConfig{Key: "hashivault://config-checkpoints"}}.Policy()
	x.NoError(err)
	x.NotNil(p.Signer)
}
