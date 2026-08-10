package auth

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/frame"
)

// MethodPlain is what [Plain] calls itself.
const MethodPlain = "plain"

// PlainScheme is the authorization scheme [Plain] reads and [PlainProvider]
// writes.
const PlainScheme = "Plain"

// Plain believes whatever the caller says it is:
//
//	authorization: Plain @<tenant-alias>/<actor-alias>
//	authorization: Plain <actor-id>
//
// There is nothing to check and it checks nothing, which is the point: a test
// or a hand written call says who it is and gets on with it. It must not be
// reachable by anyone who is not already trusted to say the truth.
//
// The "@" is new; [ParseName] says why.
//
// # It says so, once
//
// Building one logs a warning, because the way this goes wrong is silence. A
// deployment serving `Plain` where anyone can reach it has no authentication at
// all, and nothing else about it looks unusual -- it starts, it answers, the
// tests pass, and every caller is whoever they typed.
//
// Once per process rather than per call: what is worth saying is that this
// process authenticates this way, and a line per request is a line nobody
// reads. It goes to the default logger rather than a request's, since there is
// no request yet.
//
// What it is **not** is a switch. Whether a deployment believes its callers is
// a line of wiring in the app rather than a setting, for the reason payday
// suggests two deployments rather than a flag: a configuration mistake must not
// be able to turn authentication off.
func Plain() Handler {
	plainSaid.Do(func() {
		slog.Warn("auth: serving with Plain; every caller is believed to be whoever it says",
			slog.String("scheme", PlainScheme))
	})

	return HandlerFunc(func(ctx context.Context) (Identity, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return Identity{}, ErrNoCredential
		}

		for _, v := range md.Get(Header) {
			rest, ok := strings.CutPrefix(v, PlainScheme+" ")
			if !ok {
				continue
			}

			id, err := ParseName(strings.TrimSpace(rest))
			if err != nil {
				// Something was said, and it was not a name; that is not the
				// same as saying nothing.
				return Identity{}, fmt.Errorf("%s: %w", PlainScheme, err)
			}

			// A header has nowhere to carry an attenuation, so it narrows
			// nothing and says so rather than leaving the zero Grant, which
			// allows nothing at all.
			id.Method, id.Grant = MethodPlain, frame.Whole()

			return id, nil
		}

		return Identity{}, ErrNoCredential
	})
}

// PlainProvider says who the caller is in the way [Plain] reads.
//
// What it takes is the written name, which is what [slug.Slug.String] and
// [pdid.Id.String] both answer with -- so there is nothing here for a caller
// holding either of those to convert. [Identity.Name] is the same form.
//
// It replaces whatever was said before rather than adding to it. Two answers
// to "who is calling" is not twice as much information; it is a question with
// no answer, and the one that would win is whichever came first.
// plainSaid keeps the warning to one a process, however many handlers are made
// -- a test binary makes one per test.
var plainSaid sync.Once

func PlainProvider(v string) Provider {
	return ProviderFunc(func(ctx context.Context) context.Context {
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			md = metadata.MD{}
		} else {
			md = md.Copy()
		}

		md.Set(Header, PlainScheme+" "+v)
		return metadata.NewOutgoingContext(ctx, md)
	})
}
