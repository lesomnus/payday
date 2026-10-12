//go:build linux

package trail

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"
)

// The inode attributes a WORM mount is closest to: a file that can be neither
// changed, renamed nor removed, and a directory that gains entries and loses
// none. linux/fs.h.
const (
	fsImmutable = 0x00000010
	fsAppend    = 0x00000020

	fsGetFlags = 0x80086601
	fsSetFlags = 0x40086602
)

// setFlags sets or clears inode attributes on what `path` names.
func setFlags(path string, set, clear int32) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var v int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fsGetFlags, uintptr(unsafe.Pointer(&v))); e != 0 {
		return fmt.Errorf("%s: get flags: %w", path, e)
	}
	v = v&^clear | set
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fsSetFlags, uintptr(unsafe.Pointer(&v))); e != 0 {
		return fmt.Errorf("%s: set flags: %w", path, e)
	}

	return nil
}

// TestCheckpointsInADirectoryThatKeepsWhatItIsGiven.
//
// flob writes a blob and its labels into a staging directory and moves the
// directory into place, links the blob beside it, and relabels by moving a new
// labels file over the old. What was not known is whether that layout is one a
// WORM mount takes. A real one was out of reach; this is the closest: after
// every checkpoint, everything flob has put in place is committed -- each file
// immutable, each directory append-only -- as a mount that commits a file once
// it is written does, and the staging directories are left as they are.
//
// PDTEST_WRITE_ONCE_DIR names a directory on a filesystem that keeps those
// attributes (ext4, xfs, or tmpfs since Linux 6.0), and the test has to be able
// to set them: root, or CAP_LINUX_IMMUTABLE.
func TestCheckpointsInADirectoryThatKeepsWhatItIsGiven(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	dir := os.Getenv("PDTEST_WRITE_ONCE_DIR")
	if dir == "" {
		t.Skip("PDTEST_WRITE_ONCE_DIR names no directory")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("the attribute ioctls are written out for amd64 and arm64")
	}

	root := filepath.Join(dir, fmt.Sprintf("pdtest-%d", time.Now().UnixNano()))
	x.NoError(os.MkdirAll(root, 0o755))
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
			if err == nil {
				_ = setFlags(p, 0, fsImmutable|fsAppend)
			}
			return nil
		})
		_ = os.RemoveAll(root)
	})

	commit := func() {
		for _, top := range []string{"repos", "share"} {
			x.NoError(filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return setFlags(p, fsAppend, 0)
				}
				return setFlags(p, fsImmutable, 0)
			}))
		}
	}

	cp := flob.NewOsStores(root).Use(CheckpointNamespace)
	tenant := newTenant()
	s := &fakeStore{}
	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Signer: newKey(t), Checkpoints: WriteOnce(cp)}
	for range 3 {
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(40 * day), value: "a"})
		p.Pass(ctx, s)
		commit()
	}

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(3, v.Checkpoint, "a checkpoint was not written onto what was committed")

	// And what was committed stays, past the process.
	ls, err := listCheckpoints(ctx, cp)
	x.NoError(err)
	x.Len(ls, 3)
	for _, c := range ls {
		x.Error(cp.Erase(ctx, c.d), "checkpoint %d was erased", c.k)
		x.Error(cp.Label(ctx, c.d, flob.Labels{}), "checkpoint %d was relabelled", c.k)
	}
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
}
