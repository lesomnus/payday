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

// driver is a database that answers nothing. What [seen] asks of one is the
// transaction it runs in, which `dialect.BeginTx` makes of it.
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

// begun is a transaction begun with `dialect.BeginTx` on `drv`, rolled back
// when the test is over.
func begun(t *testing.T, drv dialect.Driver) dialect.Driver {
	t.Helper()

	v, tx, err := dialect.BeginTx(t.Context(), drv)
	require.NoError(t, err)
	t.Cleanup(func() { tx.Rollback() })
	return v
}

func TestARowIsAskedAboutOnceATransaction(t *testing.T) {
	x := require.New(t)
	ctx, drv := framed(t.Context()), begun(t, driver{})

	robot, other := pdid.New(7), pdid.New(7)
	a := &asks{}
	for range 3 {
		x.NoError(seen.Visible(ctx, drv, "Robot", byKey(robot), a.visible))
	}
	x.Equal(1, a.n, "the same row three times, asked once")

	x.NoError(seen.Visible(ctx, drv, "Robot", byKey(other), a.visible))
	x.Equal(2, a.n, "and another row is another question")
}

// A refusal is not remembered, so a row the transaction goes on to add is
// found once it is there -- and that is remembered.
func TestARefusalIsAskedAgain(t *testing.T) {
	x := require.New(t)
	ctx, drv := framed(t.Context()), begun(t, driver{})

	robot := pdid.New(7)
	a := &asks{err: status.Error(codes.NotFound, "Robot not found")}
	x.Equal(codes.NotFound, status.Code(seen.Visible(ctx, drv, "Robot", byKey(robot), a.visible)))
	x.Equal(codes.NotFound, status.Code(seen.Visible(ctx, drv, "Robot", byKey(robot), a.visible)))
	x.Equal(2, a.n)

	a.err = nil
	x.NoError(seen.Visible(ctx, drv, "Robot", byKey(robot), a.visible))
	x.NoError(seen.Visible(ctx, drv, "Robot", byKey(robot), a.visible))
	x.Equal(3, a.n)
}

// What one frame may see is not an answer about another, even when the two say
// the same thing: a batch narrows each operation into a frame of its own.
func TestAnotherFrameIsAnotherQuestion(t *testing.T) {
	x := require.New(t)
	ctx, drv := t.Context(), begun(t, driver{})

	robot := pdid.New(7)
	a := &asks{}
	one, two := framed(ctx), framed(ctx)
	x.NoError(seen.Visible(one, drv, "Robot", byKey(robot), a.visible))
	x.NoError(seen.Visible(two, drv, "Robot", byKey(robot), a.visible))
	x.NoError(seen.Visible(one, drv, "Robot", byKey(robot), a.visible))
	x.Equal(2, a.n)

	// And a call nobody vouched for is not one to answer from memory.
	x.NoError(seen.Visible(ctx, drv, "Robot", byKey(robot), a.visible))
	x.NoError(seen.Visible(ctx, drv, "Robot", byKey(robot), a.visible))
	x.Equal(4, a.n)
}

// A reference by alias names whichever row holds the alias now, and a write in
// the same transaction can give it to another.
func TestOnlyARowNamedByItsKeyIsRemembered(t *testing.T) {
	x := require.New(t)
	ctx, drv := framed(t.Context()), begun(t, driver{})

	byAlias := ref{}
	a := &asks{}
	x.NoError(seen.Visible(ctx, drv, "Robot", byAlias, a.visible))
	x.NoError(seen.Visible(ctx, drv, "Robot", byAlias, a.visible))
	x.Equal(2, a.n)

	tenant := 0
	for range 2 {
		_, err := seen.Tenant(drv, "Robot.tenant", byAlias, func() ([]byte, error) {
			tenant++
			return []byte{1}, nil
		})
		x.NoError(err)
	}
	x.Equal(2, tenant)
}

// A write in no transaction keeps nothing, and is asked about every time.
func TestNothingIsRememberedOutsideATransaction(t *testing.T) {
	ctx := framed(t.Context())
	robot := pdid.New(7)

	for name, drv := range map[string]dialect.Driver{
		"no driver":      nil,
		"no transaction": driver{},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			a := &asks{}
			x.NoError(seen.Visible(ctx, drv, "Robot", byKey(robot), a.visible))
			x.NoError(seen.Visible(ctx, drv, "Robot", byKey(robot), a.visible))
			x.Equal(2, a.n)
		})
	}
}

// An operation made of other operations is one transaction, and asks each
// question once for all of them. A transaction begun afresh asks again, and
// one rolled back takes what it was told with it.
func TestWhatATransactionSawIsItsOwn(t *testing.T) {
	x := require.New(t)
	ctx := framed(t.Context())

	outer := begun(t, driver{})
	robot := pdid.New(7)
	a := &asks{}
	x.NoError(seen.Visible(ctx, outer, "Robot", byKey(robot), a.visible))
	x.Equal(1, a.n)

	// Inside: an inner operation begins on the outer's transaction.
	inner := begun(t, outer)
	x.NoError(seen.Visible(ctx, inner, "Robot", byKey(robot), a.visible))
	x.Equal(1, a.n, "an inner operation asked again what its transaction already knew")

	// Afresh: a driver that is not in one begins a transaction of its own.
	again := begun(t, driver{})
	x.NoError(seen.Visible(ctx, again, "Robot", byKey(robot), a.visible))
	x.Equal(2, a.n, "a new transaction answered from another one's memory")

	// Rolled back: what it was told went with it.
	drv, tx, err := dialect.BeginTx(t.Context(), driver{})
	x.NoError(err)
	x.NoError(seen.Visible(ctx, drv, "Robot", byKey(robot), a.visible))
	x.NoError(tx.Rollback())
	x.NoError(seen.Visible(ctx, drv, "Robot", byKey(robot), a.visible))
	x.Equal(4, a.n, "a transaction rolled back remembered what it was told")
}

func TestATenantIsAskedOnceATransaction(t *testing.T) {
	x := require.New(t)
	drv := begun(t, driver{})

	robot := pdid.New(7)
	n := 0
	ask := func() ([]byte, error) {
		n++
		return []byte{1, 2, 3}, nil
	}

	v, err := seen.Tenant(drv, "Robot.tenant", byKey(robot), ask)
	x.NoError(err)
	x.Equal([]byte{1, 2, 3}, v)

	// What a caller does with the answer is theirs. A stamp is set from it.
	v[0] = 9

	v, err = seen.Tenant(drv, "Robot.tenant", byKey(robot), ask)
	x.NoError(err)
	x.Equal([]byte{1, 2, 3}, v)
	x.Equal(1, n)

	// Another path from the same row is another walk.
	_, err = seen.Tenant(drv, "Robot.fleet.tenant", byKey(robot), ask)
	x.NoError(err)
	x.Equal(2, n)
}

func TestATenantNotFoundIsAskedAgain(t *testing.T) {
	x := require.New(t)
	drv := begun(t, driver{})

	robot := pdid.New(7)
	n := 0
	ask := func() ([]byte, error) {
		n++
		if n == 1 {
			return nil, status.Error(codes.InvalidArgument, "robot: it does not name one row")
		}
		return []byte{1}, nil
	}

	_, err := seen.Tenant(drv, "Robot.tenant", byKey(robot), ask)
	x.Equal(codes.InvalidArgument, status.Code(err))

	for range 2 {
		_, err = seen.Tenant(drv, "Robot.tenant", byKey(robot), ask)
		x.NoError(err)
	}
	x.Equal(2, n)
}
