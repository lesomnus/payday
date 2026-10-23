package past

import (
	"context"

	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
)

// Redact blanks the reason of the event `event`, for a person who asked for a
// text that names them to be taken out: the one free text the past keeps, of
// which nothing can tell whether somebody is in it, so it is taken out when
// somebody points at it.
//
// An event is never changed otherwise, and has no Patch, so this is a write
// beneath the servers -- and is on the trail as the act it is,
// [RedactAction], by whoever `ctx` is framed as, in the same transaction:
// the line keeps the digest of the event as it is now, and its row's seal is
// made again of it, so a check of the rows finds it as the trail says it is.
// What the reason was is in neither, as it never was: it is personal.
//
// The store is on a transaction, as [Begin]'s is, and the event one the
// caller may see; one already blank is left as it is.
func Redact(ctx context.Context, s Store, event pdid.Id) error {
	if !dialect.InTx(s.Driver()) {
		return ErrNotInTx
	}
	if _, err := gate.Actor(ctx); err != nil {
		return err
	}

	wall, err := walled(ctx)
	if err != nil {
		return err
	}
	vs, err := s.Events(ctx, Select{Where: append(wall, is(colId, event)), Limit: 1})
	if err != nil {
		return err
	}
	if len(vs) == 0 {
		return status.Errorf(codes.NotFound, "event %s not found", event)
	}
	if vs[0].Reason == "" {
		return nil
	}

	return s.Redact(ctx, event, RedactAction)
}
