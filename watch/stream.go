package watch

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
)

// ErrBehind is what a stream that could not keep up is told.
//
// ResourceExhausted, and the message says what to do about it: ask again. A
// fresh stream begins with everything that matches now, so a client that was
// cut off converges by reconnecting rather than by being sent what it missed.
// That is the whole of the recovery story, and it is only enough because what
// is sent is state.
var ErrBehind = status.Error(codes.ResourceExhausted,
	"this stream fell behind and was cut off; ask again and you will be given what is there now")

// ErrNoBroker is a Watch asked of a deployment that publishes nowhere.
//
// # Why this is an error and used not to be
//
// `watch.broker: none` is documented as *serves no Watch at all*, and it did
// not. [New] answers with a non-nil `*Watch` whichever broker it was given --
// it has to, because [Watch.Note] still has to be callable -- so a generated
// `if s.w == nil` guard never fired, and [Watch.Subscribe] handed back a
// channel nothing would ever send on.
//
// What a client got was a snapshot and then silence, on a stream that stayed
// open and looked healthy. Which made `none` **quieter than leaving Watch on**:
// the setting somebody reaches for to turn a feature off was the one that hid
// its absence best.
//
// Unimplemented rather than FailedPrecondition, and it is the same word the
// generated guard uses: nobody may, and no credential or retry changes it.
var ErrNoBroker = status.Error(codes.Unimplemented,
	"this deployment publishes no changes, so there is nothing to watch")

// Seen is which rows a stream has carried, so that one leaving the answer can
// be said and one that was never in it is not news.
//
// It grows with what a caller is shown rather than with what happens, which is
// what keeps it bounded by the filters they asked with.
type Seen map[pdid.Id]bool

// Stream is the shape every Watch has, and the reason it is written once is the
// **order** rather than the length.
//
// Subscribing has to come before the first read, or a change that lands between
// them is lost with nothing to say it was. That is easy to write correctly, and
// easier to write correctly three times and then wrongly the fourth -- which is
// exactly the kind of thing worth generating a caller for rather than a copy.
//
//   - `service` is the prefix of the RPCs whose writes this stream is about.
//   - `snapshot` sends what matches now, and may be nil for an entity that has
//     nothing to snapshot.
//   - `send` reads the rows the keys name and sends what it should.
//
// It returns when the caller hangs up, when a send fails, or when this
// subscriber has fallen too far behind.
func Stream(
	ctx context.Context,
	w *Watch,
	service string,
	snapshot func(sent Seen) error,
	send func(ks map[pdid.Id]string, sent Seen) error,
) error {
	// Before the subscription rather than after, so that a deployment with no
	// broker refuses here instead of sending a snapshot it will never follow up
	// on. A first message and then nothing is the shape a client cannot tell
	// from a quiet system. See [ErrNoBroker].
	if w == nil || w.Broker() == nil {
		return ErrNoBroker
	}

	// First, and before anything is read. See above.
	events, stop := w.Subscribe()
	defer stop()

	sent := Seen{}
	if snapshot != nil {
		if err := snapshot(sent); err != nil {
			return err
		}
	}

	for {
		ks, err := Next(ctx, events, service)
		if err != nil {
			return err
		}

		if err := send(ks, sent); err != nil {
			return err
		}
	}
}

// Next waits for changes to an entity and answers with the rows they name, each
// once, with the RPC that last touched it.
//
// Everything already queued is taken and not just the first of it. A row that
// changed three times while a stream was busy is one row to read, and reading
// it once answers with the last of the three -- so a stream that is behind gets
// **cheaper** rather than more expensive, which is what a stream of state buys
// and a stream of deltas cannot.
func Next(ctx context.Context, events <-chan Event, service string) (map[pdid.Id]string, error) {
	ks := map[pdid.Id]string{}
	take := func(v Event) {
		for _, c := range v.Changes {
			// The service is named for the entity it is about, so the name of
			// the RPC that made the write says which entity this was. It is
			// `By` and never `Method`: an RPC written by hand is dispatched
			// under its own name and could be about anything.
			if !strings.HasPrefix(c.By, service) {
				continue
			}

			ks[c.Key] = v.Method
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case v, ok := <-events:
			if !ok {
				return nil, ErrBehind
			}

			take(v)
		}

		// And whatever else is already here.
	drained:
		for {
			select {
			case v, ok := <-events:
				if !ok {
					return nil, ErrBehind
				}

				take(v)
			default:
				break drained
			}
		}

		if len(ks) > 0 {
			return ks, nil
		}

		// Everything that arrived was about something else, which is most of
		// what arrives. Wait again rather than answering with nothing.
	}
}

// ServiceOf is the prefix of every RPC of one service, taken from the name of
// one of them.
//
// A change says which RPC made it, and a service is named for the entity it is
// about, so the prefix is what tells a stream whether a write is its business.
func ServiceOf(fullMethod string) string {
	i := strings.LastIndex(fullMethod, "/")
	if i < 0 {
		return fullMethod
	}

	return fullMethod[:i+1]
}
