// Package page walks generated List responses without collecting all rows in
// memory. All preserves the caller's filters, order, size and starting cursor
// on a cloned request, fetching another page only when iteration needs it.
//
// For an in-process server, use page.All(ctx, server.List, request). For a gRPC
// client, adapt its call options with page.Client(client.List, options...).
// Each iteration is a new traversal. It is not a database snapshot: writes
// between pages have the same visibility as ordinary List calls. Iteration
// stops on break, cancellation or one yielded error; errors are never retried.
package page

import (
	"context"
	"errors"
	"iter"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// Request is the cursor-bearing part of a generated List request.
type Request interface {
	proto.Message
	GetAfter() string
	SetAfter(string)
}

// Response is the page-bearing part of a generated List response.
type Response[T any] interface {
	proto.Message
	GetItems() []T
	GetNext() string
}

// ErrCursor means a server returned a continuation token already visited by
// this traversal. Returning an error prevents an unbounded stream of repeats.
var ErrCursor = errors.New("page: repeated continuation token")

// All lazily yields every matching item, starting at req's cursor. It clones
// req when iteration starts and never edits the caller's message. Keep req
// unchanged while an iteration starts, as with any protobuf read.
//
// An empty page with a new next token is followed; only an empty next token
// ends the traversal. On failure the item is T's zero value and the error is
// yielded once. Already yielded items remain valid; no retry is attempted.
func All[T any, Q Request, R Response[T]](ctx context.Context, list func(context.Context, Q) (R, error), req Q) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		fail := func(err error) { yield(zero, err) }
		if err := ctx.Err(); err != nil {
			fail(err)
			return
		}
		if any(req) == nil || !req.ProtoReflect().IsValid() {
			fail(errors.New("page: nil request"))
			return
		}
		if list == nil {
			fail(errors.New("page: nil List function"))
			return
		}
		at := proto.Clone(req).(Q)
		seen := map[string]bool{at.GetAfter(): true}
		for {
			if err := ctx.Err(); err != nil {
				fail(err)
				return
			}
			res, err := list(ctx, at)
			if err != nil {
				fail(err)
				return
			}
			if any(res) == nil || !res.ProtoReflect().IsValid() {
				fail(errors.New("page: nil List response"))
				return
			}
			for _, item := range res.GetItems() {
				if err := ctx.Err(); err != nil {
					fail(err)
					return
				}
				if !yield(item, nil) {
					return
				}
			}
			next := res.GetNext()
			if next == "" {
				return
			}
			if seen[next] {
				fail(ErrCursor)
				return
			}
			seen[next] = true
			at.SetAfter(next)
		}
	}
}

// Client adapts a gRPC List method to All while retaining its call options.
// The option slice is copied so later appends or replacements cannot alter
// the traversal. Option values keep their normal gRPC ownership rules.
func Client[Q proto.Message, R proto.Message](list func(context.Context, Q, ...grpc.CallOption) (R, error), opts ...grpc.CallOption) func(context.Context, Q) (R, error) {
	if list == nil {
		return nil
	}
	options := append([]grpc.CallOption(nil), opts...)
	return func(ctx context.Context, req Q) (R, error) {
		return list(ctx, req, options...)
	}
}
