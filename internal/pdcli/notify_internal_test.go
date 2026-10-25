package pdcli

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/schema"
)

func TestNotifyCopiesAreOptionalAndNumbered(t *testing.T) {
	x := require.New(t)
	ships, err := fs.ReadDir(schema.FS(), schema.DirNotify)
	x.NoError(err)
	files := []string{}
	for _, v := range ships {
		files = append(files, v.Name())
	}
	copies := []string{}
	features, err := schema.Features()
	x.NoError(err)
	for _, v := range notifyFiles {
		copies = append(copies, v.Name)
		x.Equal(schema.DirNotify, features[v.Own])
	}
	x.ElementsMatch(files, copies)

	root, dst := t.TempDir(), t.TempDir()
	l := Layout{Root: root, ProtoPkg: "app", Module: "example.com/app", Pkg: "example.com/app", Notify: &Notify{Notification: 40, Receipt: 41, At: "app.proto"}}
	x.NoError(os.MkdirAll(l.Path(DirExt, "payday"), 0o755))
	g := Gen{Layout: l}
	x.NoError(g.notify(context.Background(), dst))
	b, err := os.ReadFile(filepath.Join(dst, "notification.proto"))
	x.NoError(err)
	x.Contains(string(b), "domain: 40")
	x.Contains(string(b), "package app;")
	x.Contains(string(b), "service NotificationService")
	g.Layout.Notify = nil
	x.NoError(g.notify(context.Background(), dst))
	for _, v := range notifyFiles {
		_, err := os.Stat(filepath.Join(dst, v.Name))
		x.True(os.IsNotExist(err), "%s survived opt-out", v.Name)
	}
}

func TestNotifyProtectsPublicNamesOnlyWhenEnabled(t *testing.T) {
	x := require.New(t)
	root := t.TempDir()
	x.NoError(os.MkdirAll(filepath.Join(root, DirProto, "app"), 0o755))
	p := filepath.Join(root, DirProto, "app", "work.proto")
	x.NoError(os.WriteFile(p, []byte("edition = \"2023\"; package app; message Notification {}"), 0o644))
	l := Layout{Root: root, ProtoPkg: "app"}
	x.NoError(checkNotifyNames(l))
	l.Notify = &Notify{Notification: 40, Receipt: 41, At: "app.proto"}
	x.ErrorContains(checkNotifyNames(l), "declares Notification")
	x.NoError(os.WriteFile(p, []byte("edition = \"2023\"; package app; message Job { message Notification {} }"), 0o644))
	x.NoError(checkNotifyNames(l))
}
