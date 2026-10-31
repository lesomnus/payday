package gate

import (
	"context"

	"google.golang.org/grpc"

	"github.com/lesomnus/payday/frame"
)

// AsAccount refuses a call an account makes as itself ([frame.OfAccount]) to a
// method it may not make that way: what the generated gate asks of every
// method it serves, in an app whose holders are memberships of accounts
// (payday#184). `methods` is the generated `pd.AccountMethod`, which answers
// for the methods the schema marks `(payday.method) = {account: {}}`.
//
// Any other call it says nothing about, and what that caller may call is a
// policy's to say.
//
// # Which method it asks about
//
// The one gRPC dispatched -- what the caller asked for, and what a batch says
// each of its operations is -- and not the one being served. A layer serving
// an account's method reads through the servers behind it, and those reads
// are the wall's to narrow, by what that layer declared it reads; refusing
// them here as methods of their own would leave the method nothing to read
// with. A call that names a method of its own is the one refused. With
// nothing dispatched -- a server called in the same process, with no
// transport -- the method asked about is the one being served, which refuses
// the most.
//
// It is asked again here, behind `Decide`, because the frame is what a call
// carries and the gate is what every stack has: a frame put into a context by
// hand, or a resolver that made one for a method it should not have, meets
// this whatever the interceptors were.
func AsAccount(ctx context.Context, served string, methods func(method string) bool) error {
	f, ok := frame.From(ctx)
	if !ok || !f.AsAccount() {
		return nil
	}

	m, ok := grpc.Method(ctx)
	if !ok {
		m = served
	}
	if methods == nil || !methods(m) || !f.Calls(m) {
		return ErrNotAsAccount(m)
	}

	return nil
}
