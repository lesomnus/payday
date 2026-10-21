package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/lesomnus/otx/log"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

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

	return plain()
}

// PlainInProcess is [Plain] for a server nothing outside this process can
// reach: one served on an in-memory listener (`bufconn`), as an app does when it
// runs another payday app inside itself and calls it the way it would a
// library.
//
// It believes its callers as Plain does, and only callers in this process. A
// name is believed when the call came over a connection this process made to
// itself -- grpc's `bufconn`, which is what the peer of such a call says it is
// -- and a name that came any other way is refused with [ErrFromOutside]: over
// a port, a Unix socket, or the HTTP transcoder, which hands a call over with
// the address of whoever sent it. So "only this process reaches it" is what the
// handler checks rather than what the app that wired it says. A server given
// this and served on a port refuses every name it is told, and says so in the
// log, instead of believing the whole network without a word.
//
// A call that says no name is not refused, which is what lets it sit in a
// [Seq] beside a handler for the callers who do come over the network: one
// server, served on a port and on `bufconn` both, reads a cookie off the one
// and a name off the other.
//
// # What it says
//
// Plain's warning is about a port anybody might reach, and said of a server
// that has none it reads as a hole that is not there -- with nothing in it to
// say which of a process's servers it is about, an operator cannot tell it from
// the one that matters. So this says, once a process and at info, that a server
// in it believes its callers and that only the process reaches it. It is a
// function an app calls, and never a setting, for the reason Plain is.
func PlainInProcess() Handler {
	plainInProcessSaid.Do(func() {
		slog.Info("auth: serving with Plain on a connection only this process reaches; every caller is believed to be whoever it says",
			slog.String("scheme", PlainScheme))
	})

	believe := plain()

	return HandlerFunc(func(ctx context.Context) (Identity, error) {
		id, err := believe.Handle(ctx)
		if err != nil {
			return Identity{}, err
		}

		if network, addr, ok := inProcess(ctx); !ok {
			// Once, for the reason Plain warns once: what is worth saying is
			// that this server is reached from outside, and every call it
			// refuses is in the request log anyway.
			fromOutsideSaid.Do(func() {
				log.From(ctx).ErrorContext(ctx, "auth: a name arrived from outside this process at a server that believes only this process; refused",
					slog.String("scheme", PlainScheme), slog.String("network", network), slog.String("addr", addr))
			})

			return Identity{}, fmt.Errorf("%s: %w", PlainScheme, ErrFromOutside)
		}

		return id, nil
	})
}

// ErrFromOutside is a name [PlainInProcess] was told by a caller that is not in
// this process.
//
// It is a bad credential and not a missing one: somebody said who they are,
// over a connection where saying it proves nothing, and a [Seq] stops here
// rather than asking the next handler what to make of them.
var ErrFromOutside = errors.New("a name is believed only from inside this process")

// inProcessNetwork is what the peer of a call says its connection is when this
// process made it to itself: grpc's `bufconn`. A call that came in over the
// wire says `tcp` or `unix`, and one the HTTP transcoder handed over says
// `tcp`, so none of them can say this.
const inProcessNetwork = "bufconn"

// inProcess is whether a call came over a connection this process made to
// itself, with what its peer said otherwise. A call with no peer at all did not
// come through a server, and is not taken for one that did.
func inProcess(ctx context.Context) (network string, addr string, ok bool) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return "", "", false
	}

	network, addr = p.Addr.Network(), p.Addr.String()

	return network, addr, network == inProcessNetwork
}

// plainSaid keeps the warning to one a process, however many handlers are made
// -- a test binary makes one per test. plainInProcessSaid is the same for
// [PlainInProcess], apart, so that a process with a server of each kind says
// both, and fromOutsideSaid is the error one of those servers logs the first
// time it is reached from outside.
var (
	plainSaid          sync.Once
	plainInProcessSaid sync.Once
	fromOutsideSaid    sync.Once
)

// plain is what both of them believe.
func plain() Handler {
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
