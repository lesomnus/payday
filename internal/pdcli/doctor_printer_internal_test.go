package pdcli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrinterFindingsUseTheEffectiveReplacement(t *testing.T) {
	path := printerModules[0]
	want := map[string]string{path: "v1.0.0"}
	for _, tc := range []struct {
		name   string
		module printerModule
		warn   bool
		text   string
	}{
		{"same", printerModule{Path: path, Version: "v1.0.0"}, false, ""},
		{"raised", printerModule{Path: path, Version: "v1.1.0"}, true, "v1.1.0"},
		{"local", printerModule{Path: path, Version: "v1.0.0", Replace: &printerModule{Path: "../printer"}}, true, "../printer (local replacement)"},
		{"fork", printerModule{Path: path, Version: "v1.0.0", Replace: &printerModule{Path: "example.com/fork", Version: "v1.0.0"}}, true, "example.com/fork@v1.0.0"},
		{"replacement version", printerModule{Path: path, Version: "v1.0.0", Replace: &printerModule{Path: path, Version: "v1.2.0"}}, true, "v1.2.0"},
		{"same source replacement", printerModule{Path: path, Version: "v1.1.0", Replace: &printerModule{Path: path, Version: "v1.0.0"}}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			got := printerFindings(map[string]printerModule{path: tc.module}, want)
			if !tc.warn {
				x.Empty(got)
				return
			}
			x.Len(got, 1)
			x.False(got[0].Fatal)
			x.Contains(got[0].What, tc.text)
			x.Contains(got[0].What, "v1.0.0")
			x.Contains(got[0].Fix, "go mod graph")
		})
	}
	require.Len(t, printerFindings(nil, want), 1, "an unreadable selected module is not a clean comparison")
}
