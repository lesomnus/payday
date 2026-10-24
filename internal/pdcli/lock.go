package pdcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lesomnus/payday/internal/pdgen"
)

// LockFile is the past's lock, beside the copies of payday's entities in
// [Layout.DirPd]: the record of every kind of event the app ever declared, and
// of the message its rows hold (payday#151). See [pdgen.Lock] for why it
// exists and what it records.
//
// Beside the copies, because what it is about is payday's Event, and because
// `pd gen --check` hashes that directory already: a lock left behind a schema
// is a file not in step like any other.
const LockFile = "past.lock"

// lock holds the app's event messages to its past's lock, and writes the lock
// back with what is new in them added.
//
// # Where in a generation
//
// After the merge, because the schema compiles only once every contract is
// written; and before the code, so that a schema the lock refuses leaves the
// generated code as it was rather than generated from what was refused.
//
// # Of a schema that compiles and that `pdgen.Read` accepts
//
// The tree is built here the way the next step builds it -- the app's proto
// directory, which is that step's input -- and read the way the plugin reads
// it. A tree that does not build, or a schema the reader refuses, is said by
// the next step in its own words, and nothing is locked of it: what a lock
// records is what payday generated from, never what it refused.
//
// # Only ever added to
//
// The answer is the committed lock with what the schema declares now added to
// it ([pdgen.Lock.Admit]). Taking something back is a hand edit, which shows
// in review: it is only right for a type no deployment ever wrote a row of.
func (r run) lock(ctx context.Context) error {
	set, err := describe(ctx, r.Layout, r.Layout.Rel(DirProto))
	if err != nil {
		return nil
	}

	generate, err := mine(r.Layout, set)
	if err != nil {
		return err
	}

	s, err := read(set, generate)
	if err != nil {
		return nil
	}

	at := filepath.Join(r.Out, r.Layout.DirPd(), LockFile)
	name := r.Layout.rel(filepath.Join(r.Layout.Root, r.Layout.DirPd(), LockFile))

	b, err := os.ReadFile(at)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	was, err := pdgen.ReadLock(b)
	if err != nil {
		return errors.New(unreadLock(name, err))
	}

	now, err := was.Admit(pdgen.EventsLock(s))
	if err != nil {
		return fmt.Errorf("%s %w", name, err)
	}

	v := now.Marshal()
	if bytes.Equal(v, b) {
		return nil
	}

	r.say("  %s", name)
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		return err
	}

	return os.WriteFile(at, v, 0o644)
}

// unreadLock is why the lock `name` could not be read, as `pd gen` and `pd
// doctor` both say it: a lock of the payday before a message held several
// kinds in the words that say how it is written now, and anything else as the
// JSON it is.
func unreadLock(name string, err error) string {
	if errors.Is(err, pdgen.ErrLockOfEvents) {
		return fmt.Sprintf("%s %s", name, err)
	}

	return fmt.Sprintf("%s: %s; it is JSON `pd gen` writes, and a hand edit of it has to stay that", name, err)
}
