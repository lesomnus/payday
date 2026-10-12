package config_test

import (
	"bytes"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/plugin"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
)

// The age plugin these are tried against is this test binary, run under the
// plugin's name: its "device" is the X25519 key its recipient and its identity
// carry, which is as much as the protocol between age and a plugin can tell
// from a YubiKey.
const pluginName = "paydaytest"

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "age-plugin-"+pluginName {
		os.Exit(fakePlugin())
	}

	os.Exit(m.Run())
}

func fakePlugin() int {
	p, err := plugin.New(pluginName)
	if err != nil {
		return 1
	}
	p.HandleRecipient(func(data []byte) (age.Recipient, error) {
		return age.ParseX25519Recipient(string(data))
	})
	p.HandleIdentity(func(data []byte) (age.Identity, error) {
		return age.ParseX25519Identity(string(data))
	})

	fs := flag.NewFlagSet("age-plugin-"+pluginName, flag.ContinueOnError)
	p.RegisterFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 1
	}

	return p.Main()
}

// onPath puts the plugin where age looks for it.
func onPath(t *testing.T) {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.Symlink(exe, filepath.Join(dir, "age-plugin-"+pluginName)))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// inDevice is a master whose private half is the plugin's to hold.
func inDevice(t *testing.T) (recipient, identity string) {
	t.Helper()

	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)

	return plugin.EncodeRecipient(pluginName, []byte(id.Recipient().String())), plugin.EncodeIdentity(pluginName, []byte(id.String()))
}

// TestTheMasterMayBeInADevice, through the age plugin that speaks to it: the
// process that serves seals to its recipient by running the plugin, and a
// command that reads opens with its identity, the device's to answer for.
func TestTheMasterMayBeInADevice(t *testing.T) {
	x := require.New(t)
	onPath(t)

	recipient, identity := inDevice(t)
	native, err := age.GenerateX25519Identity()
	x.NoError(err)

	serving, err := config.AuditConfig{Archive: t.TempDir(), Retain: 1, Keys: config.AuditKeysConfig{
		Dir: t.TempDir(), Master: []string{recipient, native.Recipient().String()},
	}}.Policy()
	x.NoError(err)
	x.Len(serving.Keys.Master, 2, "a device's, and one kept offline beside it")

	sealed := &bytes.Buffer{}
	w, err := age.Encrypt(sealed, serving.Keys.Master...)
	x.NoError(err)
	_, err = io.WriteString(w, "a tenant's key")
	x.NoError(err)
	x.NoError(w.Close())

	path := filepath.Join(t.TempDir(), "master.txt")
	x.NoError(os.WriteFile(path, []byte("# the device's\n"+identity+"\n"), 0o600))
	for _, v := range []string{identity, path} {
		reading, err := config.AuditConfig{Archive: t.TempDir(), Retain: 1, Keys: config.AuditKeysConfig{
			Dir: t.TempDir(), Master: []string{recipient}, Identity: v,
		}}.Policy()
		x.NoError(err)

		r, err := age.Decrypt(bytes.NewReader(sealed.Bytes()), reading.Keys.Identities...)
		x.NoError(err)
		b, err := io.ReadAll(r)
		x.NoError(err)
		x.Equal("a tenant's key", string(b))
	}
}

// TestAPluginThatCannotRunIsRefusedWhereTheProcessComesUp, rather than at the
// first tenant whose key cannot be sealed.
func TestAPluginThatCannotRunIsRefusedWhereTheProcessComesUp(t *testing.T) {
	x := require.New(t)

	recipient, identity := inDevice(t)
	native, err := age.GenerateX25519Identity()
	x.NoError(err)
	t.Setenv("PATH", t.TempDir())
	_, err = config.AuditConfig{Archive: t.TempDir(), Retain: 1, Keys: config.AuditKeysConfig{Dir: t.TempDir(), Master: []string{recipient}}}.Policy()
	x.ErrorContains(err, "audit.keys.master[0]: age-plugin-"+pluginName+" is not on PATH")
	_, err = config.AuditConfig{Archive: t.TempDir(), Retain: 1, Keys: config.AuditKeysConfig{
		Dir: t.TempDir(), Master: []string{native.Recipient().String()}, Identity: identity,
	}}.Policy()
	x.ErrorContains(err, "audit.keys.identity: age-plugin-"+pluginName+" is not on PATH")
	x.NotContains(err.Error(), identity)

	onPath(t)
	broken := plugin.EncodeRecipient(pluginName, []byte("not a key"))
	_, err = config.AuditConfig{Archive: t.TempDir(), Retain: 1, Keys: config.AuditKeysConfig{Dir: t.TempDir(), Master: []string{broken}}}.Policy()
	x.ErrorContains(err, "audit.keys.master:", "the plugin, made to seal once")

	_, err = config.AuditConfig{Archive: t.TempDir(), Retain: 1, Keys: config.AuditKeysConfig{
		Dir: t.TempDir(), Master: []string{recipient}, Identity: "AGE-PLUGIN-" + strings.ToUpper(pluginName) + "-1NOTBECH32",
	}}.Policy()
	x.ErrorContains(err, "audit.keys.identity: an AGE-PLUGIN- identity that does not read as one")
}

// TestAMasterAgeWillNotSealToIsRefusedWhereTheProcessComesUp: a post-quantum
// recipient beside a classic one is a pair age refuses to seal to, and it used
// to be the first tenant's row that found out.
func TestAMasterAgeWillNotSealToIsRefusedWhereTheProcessComesUp(t *testing.T) {
	x := require.New(t)

	classic, err := age.GenerateX25519Identity()
	x.NoError(err)
	pq, err := age.GenerateHybridIdentity()
	x.NoError(err)

	_, err = config.AuditConfig{Archive: t.TempDir(), Retain: 1, Keys: config.AuditKeysConfig{
		Dir: t.TempDir(), Master: []string{classic.Recipient().String(), pq.Recipient().String()},
	}}.Policy()
	x.ErrorContains(err, "audit.keys.master: incompatible recipients")
}
