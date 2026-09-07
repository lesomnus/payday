package grpcx

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/lesomnus/otx"
)

// sayIfSilent reports, once, that this server will log nothing.
//
// Every handler this package installs reads the providers off the context --
// `otxgrpc.NewServerHandler(otx.From(ctx))` and the request logger beside it.
// A context that carries none is not an error to [otx.From]: it answers with
// the OpenTelemetry globals, which discard what they are given until somebody
// installs a provider. So a server built on such a context serves correctly,
// answers every call, and writes nothing anywhere.
//
// That is the whole failure, and it has no other symptom. There is no error, no
// dropped-record count, and no line in a log -- the log is the thing that is
// missing. It cost a day downstream, where `otel:` had been in the
// configuration since the app was scaffolded and nothing had ever built it.
//
// # Why stderr, and why not an error
//
// stderr because it is what is left: the log is what does not work, so
// reporting this through it would be reporting it into the hole. On Wasm that
// still reaches the browser's console.
//
// Not an error because this is a warning about a thing that is allowed. A test
// stands a server up on a context with no telemetry and means it, and a program
// that only ever answers is not obliged to be observed. What is not allowed is
// finding out by wondering where the log went.
//
// Once, because it is a property of the process and not of the call: a server
// per test would otherwise print this per test.
func sayIfSilent(ctx context.Context) {
	if _, ok := otx.FromOK(ctx); ok {
		return
	}

	said.Do(func() {
		fmt.Fprint(os.Stderr, ""+
			"grpcx: serving on a context with no telemetry, so this server logs, traces and measures nothing.\n"+
			"       Everything here reads the providers off the context, and one that carries none answers\n"+
			"       with the OpenTelemetry globals, which discard. Build it before the server:\n"+
			"\n"+
			"           ctx, o, err := cfg.Otel.Build(ctx, config.Service{Name: ..., Scope: ...})\n"+
			"           defer o.Shutdown(ctx)\n"+
			"           o.Start(ctx)\n"+
			"\n"+
			"       and pass that ctx on. See config.OtelConfig.Build.\n")
	})
}

var said sync.Once
