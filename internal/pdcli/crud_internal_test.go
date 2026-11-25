package pdcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestASchemaThatStillSaysCrudIsToldWhatToDo: `orm` has no `crud` any more,
// and buf says of one only that it "cannot resolve message field name for
// `orm.RpcOptions`". So `pd gen` and `pd doctor` find it first, in every form
// a schema can say it in, in the app's own files and in its overlays, and say
// where and what to write instead. A `crud` that is not in an `(orm.message)`'s
// `rpc` -- a field of the app's own, a comment -- is somebody else's.
func TestASchemaThatStillSaysCrudIsToldWhatToDo(t *testing.T) {
	x := require.New(t)

	root := t.TempDir()
	write := func(at, src string) {
		p := filepath.Join(root, at)
		x.NoError(os.MkdirAll(filepath.Dir(p), 0o755))
		x.NoError(os.WriteFile(p, []byte(src), 0o644))
	}

	l := Layout{Root: root, ProtoPkg: "app"}
	write("proto/app/robot.proto", `edition = "2023";
package app;
import "orm.proto";

// crud: true is what this used to say.
message Robot {
  bytes id = 1;
  bool crud = 2;
  option (orm.message) = {rpc: {add: {}, get: {}, patch: {}, erase: {}}};
}
`)
	x.NoError(checkCrud(l), "a schema that names its verbs was refused")

	write("proto/app/arm.proto", `edition = "2023";
package app;
import "orm.proto";

message Arm {
  bytes id = 1;
  option (orm.message) = {
    rpc: {crud: true}
  };
}

message Leg {
  bytes id = 1;
  option (orm.message).rpc.crud = true;
}
`)
	write("proto/ext/payday/tenant.ext.proto", `edition = "2023";
package app;

message Tenant {
  option (orm.message) = {rpc {crud: true}};
}
`)
	// payday's own copies are not the app's to mend, and are written again on
	// every `pd gen`.
	write("proto/app/payday/holder.proto", `edition = "2023";
package app;
message Holder {
  option (orm.message) = {rpc: {crud: true}};
}
`)
	write("proto/app/robot_svc.g.proto", `option (orm.message) = {rpc: {crud: true}};`)

	err := checkCrud(l)
	x.Error(err)
	for _, v := range []string{
		"proto/app/arm.proto:8",
		"proto/app/arm.proto:14",
		"proto/ext/payday/tenant.ext.proto:5",
		"`crud` is gone",
		"`add`, `get`, `patch`, `apply` and `erase`",
		"`apply` no longer comes with `patch`",
	} {
		x.Contains(err.Error(), v)
	}
	x.NotContains(err.Error(), "robot.proto")
	x.NotContains(err.Error(), "holder.proto")
	x.Equal(3, strings.Count(err.Error(), ": says `crud`"), err.Error())

	// And `pd doctor` says the same, as a finding the generation cannot get
	// past.
	f := doctorCrud(l)
	x.Len(f, 1)
	x.True(f[0].Fatal)
	x.Contains(f[0].What, "proto/app/arm.proto:8")

	// A file nothing can parse is buf's to say.
	write("proto/app/broken.proto", "message {")
	x.Error(checkCrud(l))
	x.NoError(os.Remove(filepath.Join(root, "proto/app/arm.proto")))
	x.NoError(os.Remove(filepath.Join(root, "proto/ext/payday/tenant.ext.proto")))
	x.NoError(checkCrud(l))
}
