package trail

import (
	"context"
	"errors"

	"github.com/lesomnus/flob"
)

// ErrWriteOnce is what a [WriteOnce] store answers an erase or a relabel with.
var ErrWriteOnce = errors.New("a write-once store keeps what it is given, as it was given")

// WriteOnce is a store that keeps what it is given, as it was given: it adds,
// and refuses to erase a blob or to change its labels. It is what a checkpoint
// store is handed in as, for [Policy.Checkpoints].
//
// # The process's half
//
// A checkpoint store is worth what it keeps from whoever can write both the
// database and the archive, and that is two promises. The storage's, that what
// it was given is not taken away or rewritten whoever's credentials ask: S3
// Object Lock in compliance mode, a WORM appliance. And the process's, that it
// never asks -- so that nothing a pass does, or a bug in one, or an erase aimed
// at the wrong store, comes near a checkpoint -- and that it says where it
// comes up that the deployment told it the storage keeps the first.
//
// This is the second. Nothing a process does can make the first, and a store
// that is not write-once beneath this is not made one by it: whoever has its
// credentials, or the disk under it, erases past this as easily as before.
//
// # On S3 with Object Lock
//
// The two meet as they should. An erase there only hides an object behind a
// delete marker and a relabel writes a new version of its reference -- both of
// which Object Lock allows, and neither of which this asks for -- and a
// checkpoint hidden or relabelled by somebody else is still found:
// [Policy.Verify] follows the chain by digest, and reads a checkpoint's number
// from what it says rather than from its labels. Every version is still there
// to be recovered.
func WriteOnce(s flob.Store) flob.Store { return writeOnce{s} }

type writeOnce struct{ flob.Store }

func (s writeOnce) Label(context.Context, flob.Digest, flob.Labels) error { return ErrWriteOnce }

func (s writeOnce) Erase(context.Context, flob.Digest) error { return ErrWriteOnce }

// Unwrap keeps what the store beneath can do besides -- listing, above all --
// reachable through this.
func (s writeOnce) Unwrap() flob.Store { return s.Store }

// WriteOnce is how a store says it keeps what it is given; see [IsWriteOnce].
func (s writeOnce) WriteOnce() bool { return true }

// IsWriteOnce answers whether a store says it keeps what it is given, through
// whatever is wrapped around it. [WriteOnce] says so, and so does any store
// with a method `WriteOnce() bool` that answers true: one that knows its bucket
// is under Object Lock, say.
func IsWriteOnce(s flob.Store) bool {
	for s != nil {
		if v, ok := s.(interface{ WriteOnce() bool }); ok && v.WriteOnce() {
			return true
		}

		u, ok := s.(interface{ Unwrap() flob.Store })
		if !ok {
			return false
		}

		s = u.Unwrap()
	}

	return false
}
