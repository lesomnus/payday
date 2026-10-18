package pdcli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
