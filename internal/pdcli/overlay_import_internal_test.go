package pdcli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContractImportDiagnosticsAvoidMisleadingSuggestions(t *testing.T) {
	for _, tc := range []struct {
		name, target, symbol, want string
	}{
		{"nested enum", `package app; message Move { enum State { UNKNOWN = 0; } }`, ".app.Move.State", `add import "app/target_svc.g.proto";`},
		{"foreign short name", `package other; message Move {}`, "Move", ""},
		{"unknown", `package app; message Move {}`, "Typo", ""},
		{"cycle", `package app; import "app/source_svc.g.proto"; message Move {}`, "Move", "would create an import cycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for p, body := range map[string]string{
				"proto/app/source_svc.g.proto":       "edition = \"2023\"; package app;",
				"proto/app/target_svc.g.proto":       "edition = \"2023\"; " + tc.target,
				"proto/ext/app/source_svc.ext.proto": "edition = \"2023\"; package app;",
			} {
				p = filepath.Join(root, p)
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0755))
				require.NoError(t, os.WriteFile(p, []byte(body), 0644))
			}
			cause := errors.New("app/proto/app/source_svc.g.proto:5:3:cannot find `" + tc.symbol + "` in this scope\n")
			got := (Gen{Layout: Layout{Root: root}, Out: root}).contractImportHints(cause)
			require.ErrorIs(t, got, cause)
			if tc.want == "" {
				require.Equal(t, cause, got)
			} else {
				require.Contains(t, got.Error(), tc.want)
				require.Contains(t, got.Error(), "proto/ext/app/source_svc.ext.proto")
			}
		})
	}
}
