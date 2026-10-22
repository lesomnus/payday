package pdcli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWhatAPluginSaysOfAGenerationIsSaid: a generation that succeeds and was
// warned about says the warning. It went into a buffer and out with it, so the
// warning about a list that scans its table never reached anybody
// (lesomnus/rove#122).
func TestWhatAPluginSaysOfAGenerationIsSaid(t *testing.T) {
	dir := t.TempDir()
	buf := filepath.Join(dir, "buf")
	require.NoError(t, os.WriteFile(buf, []byte("#!/bin/sh\n"+
		"echo 'protoc-gen-pd: app.Robot: list: no index begins with (tenant, date_created, id)' >&2\n"), 0o755))
	t.Setenv(BufEnv, buf)

	var log bytes.Buffer
	g := Gen{Layout: Layout{Work: dir}, Log: &log}
	require.NoError(t, g.buf(context.Background(), "version: v2\n"))
	require.Contains(t, log.String(), "app.Robot: list: no index begins with (tenant, date_created, id)")
}

// TestTheDoctorSaysWhatIsBelowAFloor: a page package below payday's floor is a
// finding with the command that moves it, and one at its floor is none -- and
// neither stops a generation.
func TestTheDoctorSaysWhatIsBelowAFloor(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/acme/thing\n\ngo 1.25\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "buf.yaml"), []byte("version: v2\nmodules:\n  - path: ./proto\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proto", "app"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "ts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "ts", "package.json"),
		[]byte(`{"dependencies": {"@lesomnus/payday": "^0.1.1"}, "devDependencies": {"typescript": "~5.8.3", "vite": "^7.2.0"}}`), 0o644))

	l, err := Discover(root)
	require.NoError(t, err)

	var named, payday []Finding
	for _, v := range doctorVersions(context.Background(), l) {
		require.False(t, v.Fatal, v.What)
		if strings.Contains(v.What, "typescript") || strings.Contains(v.What, "vite") {
			named = append(named, v)
		}
		if strings.Contains(v.What, "@lesomnus/payday") {
			payday = append(payday, v)
		}
	}
	require.Len(t, named, 1, "%+v", named)
	require.Contains(t, named[0].What, "typescript 5.8.3, older than 5.9.3")
	require.Equal(t, "go tool pd upgrade", named[0].Fix)

	// A page from before the template's store.ts swept with deleteOtherDisks
	// is told so, and why, and how to move -- not stopped.
	require.Len(t, payday, 1, "%+v", payday)
	require.Contains(t, payday[0].What, "@lesomnus/payday 0.1.1, older than 0.1.2")
	require.Contains(t, payday[0].What, "deleteOtherDisks")
	require.Equal(t, "go tool pd upgrade", payday[0].Fix)
}
