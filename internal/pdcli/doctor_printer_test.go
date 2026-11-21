package pdcli_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/payday/internal/pdcli"
	"github.com/stretchr/testify/require"
)

// A real module graph: the app still requires the old printer, but a roster
// dependency raises the selected version. Reading only the app's go.mod misses
// exactly the change this diagnostic is meant to explain.
func TestDoctorReportsPrinterVersionsSelectedByAnotherDependency(t *testing.T) {
	x := require.New(t)
	proxy := t.TempDir()
	for k, v := range map[string]string{
		"GOPROXY": "file://" + filepath.ToSlash(proxy), "GOSUMDB": "off", "GONOSUMDB": "", "GOPRIVATE": "", "GONOPROXY": "",
		"GOMODCACHE": t.TempDir(), "GOWORK": "off", "GOTOOLCHAIN": "local", "GOFLAGS": "-modcacherw",
	} {
		t.Setenv(k, v)
	}
	publish := func(path, version, requirements string) {
		mod := []byte("module " + path + "\n\ngo 1.16\n" + requirements)
		dir := filepath.Join(proxy, filepath.FromSlash(path), "@v")
		x.NoError(os.MkdirAll(dir, 0755))
		x.NoError(os.WriteFile(filepath.Join(dir, version+".mod"), mod, 0644))
		x.NoError(os.WriteFile(filepath.Join(dir, version+".info"), fmt.Appendf(nil, `{"Version":%q,"Time":"2026-01-01T00:00:00Z"}`, version), 0644))
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		w, err := z.Create(path + "@" + version + "/go.mod")
		x.NoError(err)
		_, err = w.Write(mod)
		x.NoError(err)
		x.NoError(z.Close())
		x.NoError(os.WriteFile(filepath.Join(dir, version+".zip"), buf.Bytes(), 0644))
	}
	const compiler = "github.com/bufbuild/protocompile"
	const patch = "github.com/lesomnus/protobuf-patch"
	for _, path := range []string{compiler, patch} {
		publish(path, "v1.0.0", "")
		publish(path, "v1.1.0", "")
	}
	publish("github.com/lesomnus/payday", "v0.1.0", "\nrequire (\n"+compiler+" v1.0.0\n"+patch+" v1.0.0\n)\n")
	publish("example.com/roster", "v1.0.0", "\nrequire (\n"+compiler+" v1.1.0\n"+patch+" v1.1.0\n)\n")
	root := app(t, "example.com/app")
	module := "module example.com/app\n\ngo 1.16\n\nrequire (\ngithub.com/lesomnus/payday v0.1.0\n" + compiler + " v1.0.0\n" + patch + " v1.0.0\n)\n"
	write := func(extra string) {
		x.NoError(os.WriteFile(filepath.Join(root, "go.mod"), []byte(module+extra), 0644))
		cmd := exec.CommandContext(t.Context(), "go", "mod", "download", "all")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		x.NoError(err, string(out))
	}
	find := func() []pdcli.Finding {
		l, err := pdcli.Discover(root)
		x.NoError(err)
		var got []pdcli.Finding
		for _, f := range pdcli.Doctor(t.Context(), l) {
			if strings.Contains(f.What, "schema printer") {
				got = append(got, f)
			}
		}
		return got
	}
	write("")
	x.Empty(find(), "aligned printer versions must be quiet")
	write("\nrequire example.com/roster v1.0.0\n")
	before, err := os.ReadFile(filepath.Join(root, "go.mod"))
	x.NoError(err)
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	x.NoError(err)
	got := find()
	x.Len(got, 2)
	for _, f := range got {
		x.Contains(f.What, "v1.1.0")
		x.Contains(f.What, "declares v1.0.0")
		x.False(f.Fatal)
	}
	after, err := os.ReadFile(filepath.Join(root, "go.mod"))
	x.NoError(err)
	x.Equal(before, after)
	afterSum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	x.NoError(err)
	x.Equal(sum, afterSum)

	// Workspace replacements are effective even when the required version
	// matches payday. Do not hide them by setting GOWORK=off in doctor.
	local := t.TempDir()
	x.NoError(os.WriteFile(filepath.Join(local, "go.mod"), []byte("module "+compiler+"\n\ngo 1.16\n"), 0644))
	write("")
	work := filepath.Join(t.TempDir(), "go.work")
	x.NoError(os.WriteFile(work, fmt.Appendf(nil, "go 1.27\n\nuse %q\n\nreplace %s => %q\n", root, compiler, local), 0644))
	t.Setenv("GOWORK", work)
	got = find()
	x.Len(got, 1)
	x.Contains(got[0].What, "local replacement")
	x.Contains(got[0].What, local)
}
