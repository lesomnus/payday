package pdcli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lesomnus/payday/internal/pdcli"
	"github.com/stretchr/testify/require"
)

func TestReadResourceCanShareAnEntitySourceWithoutReplacingItsContract(t *testing.T) {
	x := require.New(t)
	l := genApp(t)
	at := filepath.Join(l.Root, pdcli.DirProto, "app", "robot.proto")
	source, err := os.ReadFile(at)
	x.NoError(err)
	const resource = `
message RobotStatus {
  bytes id = 1;
  string state = 2;
  option (payday.resource) = {name: "robot-status", filter: ".app.RobotStatusFilter"};
}
message RobotStatusFilter { bytes robot_id = 1; }
`
	x.NoError(os.WriteFile(at, append(source, resource...), 0o644))
	x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))
	contract, err := os.ReadFile(filepath.Join(l.Root, pdcli.DirProto, "app", "robot_svc.g.proto"))
	x.NoError(err)
	x.Contains(string(contract), "service RobotService")
	x.Contains(string(contract), "service RobotStatusService")
	x.Contains(string(contract), "rpc List(RobotListRequest)")
	x.Contains(string(contract), "rpc List(RobotStatusListRequest)")
	x.NotContains(string(contract), "RobotStatusWatchRequest", "watch remains opt-in")
	x.NotContains(string(contract), "rpc Add(RobotStatus", "computed resources do not gain write RPCs")
	_, err = os.Stat(filepath.Join(l.Root, pdcli.DirEnt, "robotstatus.go"))
	x.True(os.IsNotExist(err), "the read-only message must stay outside Ent")

	// The trusted generated fragment may add its Get; an application overlay
	// still cannot replace that contract after it has been generated.
	overlay := filepath.Join(l.Path(pdcli.DirExt), "app", "robot_svc.ext.proto")
	original, err := os.ReadFile(overlay)
	x.NoError(err)
	const shadow = "\nservice RobotStatusService {\n  rpc Get(RobotStatusGetRequest) returns (RobotStatus);\n}\n"
	x.NoError(os.WriteFile(overlay, append(original, shadow...), 0o644))
	err = pdcli.Gen{Layout: l}.Run(t.Context())
	x.ErrorContains(err, "rpc Get")
	x.ErrorContains(err, "An overlay **adds**")
}

func TestRemovingTheLastReadResourceSweepsItsGeneratedAPI(t *testing.T) {
	x := require.New(t)
	l := genApp(t)
	for _, name := range []string{
		filepath.Join(pdcli.DirProto, "app", "computed_reading.proto"),
		filepath.Join(pdcli.DirExt, "app", "computed_reading_svc.ext.proto"),
	} {
		x.NoError(os.Remove(filepath.Join(l.Root, name)))
	}
	x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))
	for _, name := range []string{
		"read_resources.g.go", "computed_reading.pb.go", "computed_reading_svc.pb.go", "computed_reading_svc_grpc.pb.go",
		filepath.Join(pdcli.DirProto, "app", "computed_reading_svc.g.proto"),
	} {
		_, err := os.Stat(filepath.Join(l.Root, name))
		x.True(os.IsNotExist(err), "removed resource left %s behind: %v", name, err)
	}
	_, err := os.Stat(filepath.Join(l.Root, "robot_svc.pb.go"))
	x.NoError(err, "existing entity services remain generated")
}
