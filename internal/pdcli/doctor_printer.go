package pdcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/lesomnus/payday/version"
	"golang.org/x/mod/modfile"
)

var printerModules = []string{
	"github.com/bufbuild/protocompile",
	"github.com/lesomnus/protobuf-patch",
}

type printerModule struct {
	Path, Version, GoMod string
	Replace              *printerModule
	Error                *struct{ Err string }
}

// A tool directive shares the app's module graph. Pinning a requirement in
// payday does not pin the selected printer: MVS can raise it for an unrelated
// app dependency. Diagnose the effective graph, including workspace replaces,
// against that payday's own go.mod instead of silently downgrading the app.
func doctorPrinter(ctx context.Context, l Layout) []Finding {
	args := append([]string{"list", "-m", "-e", "-json", "-mod=readonly", version.Module}, printerModules...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = l.Root
	out, err := cmd.Output()
	if err != nil {
		return []Finding{{What: fmt.Sprintf("cannot inspect the schema printer module graph: %s", err),
			Fix: "run go list -m -json github.com/lesomnus/payday in the app directory"}}
	}
	modules := map[string]printerModule{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var m printerModule
		err := dec.Decode(&m)
		if err == io.EOF {
			break
		}
		if err != nil {
			return []Finding{{What: fmt.Sprintf("cannot read the schema printer module graph: %s", err)}}
		}
		modules[m.Path] = m
	}
	pd, ok := modules[version.Module]
	if !ok || (pd.Error != nil && strings.Contains(pd.Error.Err, "not a known dependency")) {
		// An app without payday or its tools already has a diagnostic. There
		// is no payday baseline to compare until that dependency exists.
		return nil
	}
	if pd.Error != nil {
		return []Finding{{What: "cannot inspect payday's schema printer baseline: " + pd.Error.Err}}
	}
	manifest := pd.GoMod
	if pd.Replace != nil && pd.Replace.GoMod != "" {
		manifest = pd.Replace.GoMod
	}
	b, err := os.ReadFile(manifest)
	if err != nil {
		return []Finding{{What: fmt.Sprintf("cannot read payday's schema printer baseline: %s", err)}}
	}
	f, err := modfile.ParseLax(manifest, b, nil)
	if err != nil {
		return []Finding{{What: fmt.Sprintf("cannot read payday's schema printer baseline: %s", err)}}
	}
	want := map[string]string{}
	for _, req := range f.Require {
		want[req.Mod.Path] = req.Mod.Version
	}
	return printerFindings(modules, want)
}

func printerFindings(modules map[string]printerModule, want map[string]string) []Finding {
	var findings []Finding
	for _, path := range printerModules {
		expected := want[path]
		if expected == "" {
			continue
		}
		m, ok := modules[path]
		if !ok || m.Error != nil {
			findings = append(findings, Finding{What: "cannot resolve schema printer " + path,
				Fix: "run go list -m -json " + path + " in the app directory"})
			continue
		}
		effective := m
		selected := m.Version
		if m.Replace != nil {
			effective = *m.Replace
			selected += " => " + effective.Path
			if effective.Version == "" {
				selected += " (local replacement)"
			} else {
				selected += "@" + effective.Version
			}
		}
		if effective.Path == path && effective.Version == expected {
			continue
		}
		findings = append(findings, Finding{
			What: fmt.Sprintf("schema printer %s: the app selects %s; payday's go.mod declares %s; copied schema formatting may differ", path, strings.TrimSpace(selected), expected),
			Fix:  "inspect go mod graph and go list -m -json " + path + " in the app directory; align the dependency versions, or run go tool pd gen --ts, review and commit its output, then run go tool pd gen --check --ts",
		})
	}
	return findings
}
