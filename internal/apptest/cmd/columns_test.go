package cmd_test

import (
	"bytes"
	"strings"
	"testing"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
)

func TestGeneratedColumnsReachThePrinter(t *testing.T) {
	id := pdid.New(19)
	row := app.Machine_builder{Id: id.Bytes(), Model: "lathe", Hours: 42, Running: true, Spec: app.Machine_Spec_builder{Axes: 3}.Build()}.Build()
	for wide, want := range map[bool]string{false: "MODEL HOURS RUNNING AXES lathe 42 true 3", true: "MODEL HOURS RUNNING AXES ID lathe 42 true 3 " + id.String()} {
		var out bytes.Buffer
		require.NoError(t, pdcmd.Table(wide).Print(&out, row))
		require.Equal(t, want, strings.Join(strings.Fields(out.String()), " "))
	}
}
