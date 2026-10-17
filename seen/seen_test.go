package seen_test

import (
	"context"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/seen"
)

// driver is a database that answers nothing, which is all [seen.For] asks of
// one: whether it is inside a transaction.
type driver struct{}

func (driver) Exec(context.Context, string, any, any) error  { return nil }
func (driver) Query(context.Context, string, any, any) error { return nil }
func (d driver) Tx(context.Context) (dialect.Tx, error)      { return dialect.NopTx(d), nil }
func (driver) Close() error                                  { return nil }
func (driver) Dialect() string                               { return dialect.SQLite }

type ref struct {
	id    []byte
	byKey bool
}

func (r ref) HasId() bool   { return r.byKey }
func (r ref) GetId() []byte { return r.id }

func byKey(id pdid.Id) ref { return ref{id: id.Bytes(), byKey: true} }

// asks counts the reads it stands for.
type asks struct {
	n   int
	err error
}

func (a *asks) visible() error {
	a.n++
	return a.err
}

func framed(ctx context.Context) context.Context {
	return frame.Into(ctx, frame.New(pdid.New(1), pdid.New(2), frame.Whole()))
}

func TestARowIsAskedAboutOnceATransaction(t *testing.T) {
	x := require.New(t)
	ctx := framed(seen.For(t.Context(), driver{}))

	robot, other := pdid.New(7), pdid.New(7)
	a := &asks{}
	for range 3 {
		x.NoError(seen.Visible(ctx, "Robot", byKey(robot), a.visible))
	}
	x.Equal(1, a.n, "the same row three times, asked once")

	x.NoError(seen.Visible(ctx, "Robot", byKey(other), a.visible))
	x.Equal(2, a.n, "and another row is another question")
}

// A refusal is not remembered, so a row the transaction goes on to add is
// found once it is there -- and that is remembered.
func TestARefusalIsAskedAgain(t *testing.T) {
	x := require.New(t)
	ctx := framed(seen.For(t.Context(), driver{}))

	robot := pdid.New(7)
	a := &asks{err: status.Error(codes.NotFound, "Robot not found")}
	x.Equal(codes.NotFound, status.Code(seen.Visible(ctx, "Robot", byKey(robot), a.visible)))
	x.Equal(codes.NotFound, status.Code(seen.Visible(ctx, "Robot", byKey(robot), a.visible)))
	x.Equal(2, a.n)

	a.err = nil
	x.NoError(seen.Visible(ctx, "Robot", byKey(robot), a.visible))
	x.NoError(seen.Visible(ctx, "Robot", byKey(robot), a.visible))
	x.Equal(3, a.n)
}

// What one frame may see is not an answer about another, even when the two say
// the same thing: a batch narrows each operation into a frame of its own.
func TestAnotherFrameIsAnotherQuestion(t *testing.T) {
	x := require.New(t)
	ctx := seen.For(t.Context(), driver{})

	robot := pdid.New(7)
	a := &asks{}
	one, two := framed(ctx), framed(ctx)
	x.NoError(seen.Visible(one, "Robot", byKey(robot), a.visible))
	x.NoError(seen.Visible(two, "Robot", byKey(robot), a.visible))
	x.NoError(seen.Visible(one, "Robot", byKey(robot), a.visible))
	x.Equal(2, a.n)

	// And a call nobody vouched for is not one to answer from memory.
	x.NoError(seen.Visible(ctx, "Robot", byKey(robot), a.visible))
	x.NoError(seen.Visible(ctx, "Robot", byKey(robot), a.visible))
	x.Equal(4, a.n)
}

// A reference by alias names whichever row holds the alias now, and a write in
// the same transaction can give it to another.
func TestOnlyARowNamedByItsKeyIsRemembered(t *testing.T) {
	x := require.New(t)
	ctx := framed(seen.For(t.Context(), driver{}))

	byAlias := ref{}
	a := &asks{}
	x.NoError(seen.Visible(ctx, "Robot", byAlias, a.visible))
	x.NoError(seen.Visible(ctx, "Robot", byAlias, a.visible))
	x.Equal(2, a.n)

	tenant := 0
	for range 2 {
		_, err := seen.Tenant(ctx, "Robot.tenant", byAlias, func() ([]byte, error) {
			tenant++
			return []byte{1}, nil
		})
		x.NoError(err)
	}
	x.Equal(2, tenant)
}

func TestNothingIsRememberedOutsideATransaction(t *testing.T) {
	x := require.New(t)
	ctx := framed(t.Context())

	robot := pdid.New(7)
	a := &asks{}
	x.NoError(seen.Visible(ctx, "Robot", byKey(robot), a.visible))
	x.NoError(seen.Visible(ctx, "Robot", byKey(robot), a.visible))
	x.Equal(2, a.n)
}

// An operation made of other operations is one transaction, and asks each
// question once for all of them. A transaction begun afresh asks again, even
// from a context that carried another one's answers.
func TestWhatATransactionSawIsItsOwn(t *testing.T) {
	x := require.New(t)
	ctx := framed(t.Context())

	outer := seen.For(ctx, driver{})
	robot := pdid.New(7)
	a := &asks{}
	x.NoError(seen.Visible(outer, "Robot", byKey(robot), a.visible))
	x.Equal(1, a.n)

	// Inside: the driver an inner operation begins on is the outer's
	// transaction.
	drv, tx, err := dialect.BeginTx(outer, driver{})
	x.NoError(err)
	defer tx.Rollback()

	inner := seen.For(outer, drv)
	x.NoError(seen.Visible(inner, "Robot", byKey(robot), a.visible))
	x.Equal(1, a.n, "an inner operation asked again what its transaction already knew")

	// Afresh: a driver that is not in one begins a transaction of its own.
	again := seen.For(outer, driver{})
	x.NoError(seen.Visible(again, "Robot", byKey(robot), a.visible))
	x.Equal(2, a.n, "a new transaction answered from another one's memory")

	// And a driver inside a transaction, with nothing carried, starts empty
	// rather than with nothing at all.
	none := seen.For(ctx, drv)
	x.NoError(seen.Visible(none, "Robot", byKey(robot), a.visible))
	x.NoError(seen.Visible(none, "Robot", byKey(robot), a.visible))
	x.Equal(3, a.n)
}

func TestATenantIsAskedOnceATransaction(t *testing.T) {
	x := require.New(t)
	ctx := seen.For(t.Context(), driver{})

	robot := pdid.New(7)
	n := 0
	ask := func() ([]byte, error) {
		n++
		return []byte{1, 2, 3}, nil
	}

	v, err := seen.Tenant(ctx, "Robot.tenant", byKey(robot), ask)
	x.NoError(err)
	x.Equal([]byte{1, 2, 3}, v)

	// What a caller does with the answer is theirs. A stamp is set from it.
	v[0] = 9

	v, err = seen.Tenant(ctx, "Robot.tenant", byKey(robot), ask)
	x.NoError(err)
	x.Equal([]byte{1, 2, 3}, v)
	x.Equal(1, n)

	// Not about the caller, so every frame shares it.
	_, err = seen.Tenant(framed(ctx), "Robot.tenant", byKey(robot), ask)
	x.NoError(err)
	x.Equal(1, n)

	// Another path from the same row is another walk.
	_, err = seen.Tenant(ctx, "Robot.fleet.tenant", byKey(robot), ask)
	x.NoError(err)
	x.Equal(2, n)
}

func TestATenantNotFoundIsAskedAgain(t *testing.T) {
	x := require.New(t)
	ctx := seen.For(t.Context(), driver{})

	robot := pdid.New(7)
	n := 0
	ask := func() ([]byte, error) {
		n++
		if n == 1 {
			return nil, status.Error(codes.InvalidArgument, "robot: it does not name one row")
		}
		return []byte{1}, nil
	}

	_, err := seen.Tenant(ctx, "Robot.tenant", byKey(robot), ask)
	x.Equal(codes.InvalidArgument, status.Code(err))

	for range 2 {
		_, err = seen.Tenant(ctx, "Robot.tenant", byKey(robot), ask)
		x.NoError(err)
	}
	x.Equal(2, n)
}
