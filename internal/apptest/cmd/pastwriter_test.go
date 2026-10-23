package cmd_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lesomnus/protobuf-patch/patch"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/core"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// The past has one writer (payday#151). The Audit layer refuses every write of
// payday's Event and Fact but the ones `pd.Past`'s store hands the servers --
// over the wire, inside a batch, through the ungated server, from the app's
// own code -- since a fact written by hand is a row no read of the past can be
// sure of, and an event that names its own actor is who did it, forged. And
// the store's writes are let through wherever in a stack the layer that
// records the past is.

// byHand is a write of the past's tables as a caller would ask for one, and
// what it would be answered with if anything let it through.
type byHand struct {
	method string
	req    proto.Message
	res    proto.Message
}

// handWritten is every write a caller could ask of the past's two servers, of
// rows of the caller's own tenant, each a row that would be written: an event
// naming somebody else as who did it, two at once, a fact of a machine, two at
// once, and a patch and an apply of a fact the past wrote, which would change
// what it said. And that fact, as the past wrote it.
func (b *built) handWritten(t *testing.T, ctx context.Context) ([]byHand, pdid.Id) {
	t.Helper()
	as := b.as(ctx)

	m := machine(t, b, as)
	mid := idOf(t, m.GetId())
	model := m.ProtoReflect().Descriptor().Fields().ByName("model")
	started := app.MachineStarted_builder{Machine: m.GetId()}.Build()

	var ev pdid.Id
	require.NoError(t, b.operate(t, as, func(ctx context.Context, s past.Store) error {
		ctx, op, err := past.Begin(ctx, s, past.Event{Subject: mid, Value: started})
		if err != nil {
			return err
		}
		ev = op.Id()

		return op.Set(ctx, past.Fact{Subject: mid, Values: []past.Value{past.To(model, protoreflect.ValueOfString("X-2"))}})
	}))
	fact := b.factOf(t, ctx, ev, model)

	tenant := app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build()
	held, err := anypb.New(started)
	require.NoError(t, err)
	event := func() *app.EventAddRequest {
		return app.EventAddRequest_builder{
			Tenant:     tenant,
			Kind:       "app.MachineStarted",
			ActorId:    pdid.New(pd.HolderDomain).Bytes(),
			OccurredAt: timestamppb.Now(),
			SubjectId:  m.GetId(),
			Value:      held,
		}.Build()
	}
	said := func(v string) *app.FactAddRequest {
		return app.FactAddRequest_builder{
			Tenant:      tenant,
			SubjectId:   m.GetId(),
			Value:       v,
			FieldNumber: z.Ptr(uint32(model.Number())),
			Domain:      z.Ptr(uint32(mid.Domain())),
			ValidFrom:   timestamppb.Now(),
			EventId:     ev.Bytes(),
		}.Build()
	}
	ref := app.FactRef_builder{Id: fact.Bytes()}.Build()

	return []byHand{
		{app.EventService_Add_FullMethodName, event(), &app.Event{}},
		{app.EventService_AddMany_FullMethodName, app.EventAddManyRequest_builder{Items: []*app.EventAddRequest{event(), event()}}.Build(), &app.EventAddManyResponse{}},
		{app.FactService_Add_FullMethodName, said(`"X-9"`), &app.Fact{}},
		{app.FactService_AddMany_FullMethodName, app.FactAddManyRequest_builder{Items: []*app.FactAddRequest{said(`"X-8"`), said(`"X-9"`)}}.Build(), &app.FactAddManyResponse{}},
		{app.FactService_Patch_FullMethodName, app.FactPatchRequest_builder{Ref: ref, DateUpdatedForce: z.Ptr(true), Value: z.Ptr(`"X-9"`)}.Build(), &app.Fact{}},
		{app.FactService_Apply_FullMethodName, app.FactApplyRequest_builder{
			Ref:   ref,
			Patch: patch.MustNewUntyped(patch.Target(patch.Name("value")).Assign(patch.Str(`"X-9"`))),
		}.Build(), &app.Fact{}},
	}, fact
}

// through is `w` asked of `s`, of the server its method names.
func (w byHand) through(ctx context.Context, s app.Server) error {
	var err error
	switch r := w.req.(type) {
	case *app.EventAddRequest:
		_, err = s.Event().Add(ctx, r)
	case *app.EventAddManyRequest:
		_, err = s.Event().AddMany(ctx, r)
	case *app.FactAddRequest:
		_, err = s.Fact().Add(ctx, r)
	case *app.FactAddManyRequest:
		_, err = s.Fact().AddMany(ctx, r)
	case *app.FactPatchRequest:
		_, err = s.Fact().Patch(ctx, r)
	case *app.FactApplyRequest:
		_, err = s.Fact().Apply(ctx, r)
	default:
		panic(fmt.Sprintf("%T is no write of the past", r))
	}

	return err
}

// thePast is what of the past -- and of the robots a batch writes beside it --
// a refused write leaves as it found it: how many rows there are, and what the
// fact a patch would change says, and at which version.
type thePast struct {
	Events, Facts, Robots int
	Said                  string
	Version               time.Time
}

func (b *built) thePast(t *testing.T, ctx context.Context, fact pdid.Id) thePast {
	t.Helper()
	x := require.New(t)

	var v thePast
	var err error
	v.Events, err = b.Ent.Event.Query().Count(ctx)
	x.NoError(err)
	v.Facts, err = b.Ent.Fact.Query().Count(ctx)
	x.NoError(err)
	v.Robots, err = b.Ent.Robot.Query().Count(ctx)
	x.NoError(err)

	f, err := b.Ent.Fact.Get(ctx, fact.Uuid())
	x.NoError(err)
	v.Said, v.Version = f.Value, f.DateUpdated

	return v
}

// TestThePastIsWrittenByThePackageAlone: a write of payday's Event or Fact that
// `pd.Past`'s store did not make is refused, however it arrives -- over the
// wire, inside a batch, through the ungated server, or from the app's own code
// through the stack a caller reaches -- with PermissionDenied and a sentence
// that says what writes one, and nothing written. What reads them reads as
// before.
func TestThePastIsWrittenByThePackageAlone(t *testing.T) {
	b, ctx := build(t)
	as := b.as(ctx)
	ws, fact := b.handWritten(t, ctx)

	// Each against what was there just before it, so that one let through
	// is the one that says so.
	refused := func(t *testing.T, write func() error) {
		t.Helper()

		was := b.thePast(t, ctx, fact)
		err := write()

		x := pdtest.NewX(t)
		x.ErrCode(codes.PermissionDenied, err)
		x.Contains(status.Convert(err).Message(), "a past is written by the past package")
		x.Equal(was, b.thePast(t, ctx, fact), "and nothing was written")
	}

	t.Run("over the wire", func(t *testing.T) {
		conn := b.dialed(t, ctx)
		for _, w := range ws {
			t.Run(w.method, func(t *testing.T) {
				refused(t, func() error { return conn.Invoke(b.travels(ctx), w.method, w.req, w.res) })
			})
		}

		t.Run("and read as any entity is", func(t *testing.T) {
			x := require.New(t)

			got := &app.Fact{}
			x.NoError(conn.Invoke(b.travels(ctx), app.FactService_Get_FullMethodName, app.FactGetRequest_builder{
				Ref: app.FactRef_builder{Id: fact.Bytes()}.Build(),
			}.Build(), got))
			x.Equal(fact.Bytes(), got.GetId())

			ls := &app.EventListResponse{}
			x.NoError(conn.Invoke(b.travels(ctx), app.EventService_List_FullMethodName, app.EventListRequest_builder{}.Build(), ls))
			x.Len(ls.GetItems(), b.thePast(t, ctx, fact).Events)
		})
	})

	t.Run("inside a batch, and the batch with it", func(t *testing.T) {
		// The guard of a deployment that serves the general writes, as this
		// one does, so that a patch and an apply reach the stack and are not
		// turned away at the door for being general writes.
		s, err := pd.Batch(b.Walled, b.Drv, cmd0.Server.Guard(b.Policy))
		require.NoError(t, err)

		for i, w := range ws {
			t.Run(w.method, func(t *testing.T) {
				refused(t, func() error {
					_, err := s.Do(as, pdpb.BatchRequest_builder{Ops: []*pdpb.Op{
						op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
							Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
							Alias:  fmt.Sprintf("arm-%d", 70+i),
						}.Build()),
						op(t, w.method, w.req),
					}}.Build())
					if err != nil {
						require.Contains(t, err.Error(), "ops[1]")
					}
					return err
				})
			})
		}
	})

	t.Run("through the ungated server", func(t *testing.T) {
		for _, w := range ws {
			t.Run(w.method, func(t *testing.T) {
				refused(t, func() error { return w.through(ctx, b.Ungated) })
			})
		}
	})

	t.Run("from the app's own code, through the stack a caller reaches", func(t *testing.T) {
		for _, w := range ws {
			t.Run(w.method, func(t *testing.T) {
				refused(t, func() error { return w.through(as, b.Walled) })
			})
		}
	})
}

// aboveTheGate is the stack a caller reaches with this app's core outermost
// -- above the gate, where rove's domain layer is -- rather than beneath
// Audit, where `cmd.Build` puts it: each write of an operation of core's, the
// past's too, goes through the gate, the secret and the Audit layer on its way
// down to the servers.
func (b *built) aboveTheGate(t *testing.T) app.Server {
	t.Helper()

	walled, err := pd.NewSink(b.Ent,
		bare.WithMinter(pd.Minter()),
		bare.WithRecorder(pd.Recorder(b.Contents)),
		bare.WithScope(pd.Wall()),
	)
	require.NoError(t, err)

	s, err := app.Build(walled.WithWatch(b.Watch),
		pd.AuditBuild(),
		pd.SecretBuild(),
		pd.GateBuild(),
		core.Build(b.Drv, b.Contents),
	)
	require.NoError(t, err)

	return s
}

// TestThePastIsWrittenWhereverItsWriterIs: what records the past writes it
// through the store, and the store's writes are let through wherever in the
// stack the layer that makes them is -- beneath Audit, where `cmd.Build` puts
// this app's core and where no write of it meets the layer at all, or above
// the gate, where every one does -- and inside a batch, whose transaction the
// operation joins.
func TestThePastIsWrittenWhereverItsWriterIs(t *testing.T) {
	b, ctx := build(t)
	as := b.as(ctx)

	cell := func(alias string) []byte {
		v, err := b.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  alias,
		}.Build())
		require.NoError(t, err)
		return v.GetId()
	}
	cells := [][]byte{cell("floor-1"), cell("floor-2")}
	r, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
		Cell:   app.CellRef_builder{Id: cells[0]}.Build(),
	}.Build())
	require.NoError(t, err)
	rid := idOf(t, r.GetId())

	// The next move, to the cell the robot is not in.
	n := 0
	move := func() *app.RobotMoveRequest {
		n++
		return app.RobotMoveRequest_builder{
			Ref: app.RobotRef_builder{Id: r.GetId()}.Build(),
			To:  app.CellRef_builder{Id: cells[n%2]}.Build(),
		}.Build()
	}

	// How many moves the past has of the robot, each the event and the fact
	// of the cell it changed.
	moves := func(t *testing.T) int {
		t.Helper()

		s := b.pastOf(t, b.Drv)
		evs, err := past.Events(as, s, past.EventQuery{Subjects: []pdid.Id{rid}})
		require.NoError(t, err)
		sts, err := past.Facts(as, s, past.FactQuery{Subjects: []pdid.Id{rid}})
		require.NoError(t, err)
		require.Len(t, sts, len(evs), "a fact of each move")

		return len(evs)
	}

	for _, c := range []struct {
		name string
		s    app.Server
	}{
		{"beneath Audit", b.Walled},
		{"above the gate", b.aboveTheGate(t)},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := require.New(t)

			was := moves(t)
			_, err := c.s.Robot().Move(as, move())
			x.NoError(err)
			x.Equal(was+1, moves(t))
		})

		t.Run(c.name+", inside a batch", func(t *testing.T) {
			x := require.New(t)

			s, err := pd.Batch(c.s, b.Drv, cmd0.Server.Guard(b.Policy))
			x.NoError(err)

			was := moves(t)
			_, err = s.Do(as, pdpb.BatchRequest_builder{Ops: []*pdpb.Op{
				op(t, app.RobotService_Move_FullMethodName, move()),
				op(t, app.RobotService_Move_FullMethodName, move()),
			}}.Build())
			x.NoError(err)
			x.Equal(was+2, moves(t))
		})
	}
}

// TestNothingThePastIsWrittenWithWritesItByHand: the store marks the contexts
// it hands the servers and no other -- not the one `pd.PastIn` hands the
// deployment's work in a tenant, nor the one `past.Begin` answers an operation
// with -- so a row written by hand with either is refused, on the very
// transaction the store writes on.
func TestNothingThePastIsWrittenWithWritesItByHand(t *testing.T) {
	b, ctx := build(t)
	ws, _ := b.handWritten(t, ctx)

	refused := func(t *testing.T, ctx context.Context, next app.Server, what string) {
		t.Helper()

		for _, w := range ws {
			err := w.through(ctx, next)
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%s, %s: %v", what, w.method, err)
		}
	}

	var ev pdid.Id
	err := pd.PastIn(b.Ungated, b.Drv, b.Contents)(ctx, b.Tenant, func(ctx context.Context, s past.Store) error {
		// The app's own stack on the same transaction, as its expiry writes
		// its own rows.
		next, err := enttx.Rebind(b.Ungated, s.Driver())
		if err != nil {
			return err
		}
		refused(t, ctx, next, "what PastIn hands its work")

		ctx, op, err := past.Begin(ctx, s, past.Event{Value: app.MachineStarted_builder{}.Build()})
		if err != nil {
			return err
		}
		ev = op.Id()
		refused(t, ctx, next, "what Begin answers")

		return nil
	})
	require.NoError(t, err, "and the store on that transaction writes")

	_, err = b.Ent.Event.Get(ctx, ev.Uuid())
	require.NoError(t, err)
}
