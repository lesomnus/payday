package pdcli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lesomnus/payday/internal/pdcli"
	"github.com/stretchr/testify/require"
)

func TestOverlayMissingContractImportNamesTheFile(t *testing.T) {
	l := genApp(t)
	other := filepath.Join(l.Path(pdcli.DirExt), "app", "job_svc.ext.proto")
	require.NoError(t, os.WriteFile(other, []byte(`edition = "2023";
package app;
message ContractMove {
  enum State { STATE_UNSPECIFIED = 0; STATE_MOVED = 1; }
  State state = 1;
}
`), 0644))
	p := filepath.Join(l.Path(pdcli.DirExt), "app", "robot_svc.ext.proto")
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	b = append(b, []byte(`
message RobotJobRequest {
  JobRef job = 1;
  ContractMove move = 2;
  .app.ContractMove.State state = 3;
}
`)...)
	require.NoError(t, os.WriteFile(p, b, 0644))
	err = (pdcli.Gen{Layout: l}).Run(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), `import "app/job_svc.g.proto";`)
	require.Contains(t, err.Error(), "proto/ext/app/robot_svc.ext.proto")
	require.Contains(t, err.Error(), "app.ContractMove is declared in app/job_svc.g.proto")
	require.Contains(t, err.Error(), "app.ContractMove.State is declared in app/job_svc.g.proto")
	// Applying the suggested import must make an actual generation succeed.
	b = append(b, []byte("\nimport \"app/job_svc.g.proto\";\n")...)
	require.NoError(t, os.WriteFile(p, b, 0644))
	require.NoError(t, (pdcli.Gen{Layout: l}).Run(t.Context()))
}
