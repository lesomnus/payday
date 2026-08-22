package cmd_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/batch"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// op packs one call as an operation.
func op(t *testing.T, method string, req proto.Message) *pdpb.Op {
	t.Helper()

	v, err := anypb.New(req)
	require.NoError(t, err)

	return pdpb.Op_builder{Method: method, Request: v}.Build()
}

// batched builds the batch server with the rules a real deployment gives it.
func (b *built) batched(t *testing.T, g batch.Guard) pdpb.BatchServiceServer {
	t.Helper()

	v, err := pd.Batch(b.Walled, b.Drv, g)
	require.NoError(t, err)

	return v
}

// wrote is what a batch of Adds put up, by identifier, read off the batch's own
// answers.
//
// The trail is asked with these rather than sliced off the end of it. Nothing
// promises a query answers in the order rows went in -- SQLite happens to,
// PostgreSQL is free not to -- and the trail's own key is no order to fall back
// on: an audit row's identifier is minted at random, unlike the identifiers it
// holds.
func wrote(x *require.Assertions, res *pdpb.BatchResponse) []uuid.UUID {
	ks := make([]uuid.UUID, len(res.GetResults()))
	for i, v := range res.GetResults() {
		r := &app.Robot{}
		x.NoError(v.UnmarshalTo(r))

		ks[i] = mustId(x, r.GetId()).Uuid()
	}

	return ks
}

// closedGuard is what a deployment that serves the general writes to nobody
// has, which is the default.
var closedGuard = batch.Guard{Closed: grpcx.GeneralWrite, By: gate.ByTenant()}

// TestABatchIsOneTransaction is the whole of what it is for.
func TestABatchIsOneTransaction(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.batched(t, closedGuard).Do(b.as(ctx), pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  "arm-01",
			}.Build()),
			op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  "arm-02",
			}.Build()),
		},
	}.Build())
	x.NoError(err)

	n, err := b.Ent.Robot.Query().Count(ctx)
	x.NoError(err)
	x.Equal(2, n)
}

// TestOneOperationRefusingUndoesTheRest, which is the difference between a
// batch and a loop over the same calls.
func TestOneOperationRefusingUndoesTheRest(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.batched(t, closedGuard).Do(b.as(ctx), pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  "arm-01",
			}.Build()),
			// The same name twice, which the unique index refuses.
			op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  "arm-01",
			}.Build()),
		},
	}.Build())
	x.Error(err)

	// And it says which one, since that is the only thing that names an
	// operation and a caller has to know.
	x.Contains(err.Error(), "ops[1]")

	n, err := b.Ent.Robot.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n, "the first operation was committed on its own")
}

// TestWhatIsClosedIsClosedInsideABatch is the first of the four holes.
//
// `Patch` and `Apply` are unreachable because an interceptor looks at the
// method gRPC dispatched. A batch is one method carrying many, so without this
// the two are reachable by wrapping them -- which is not a gap to fix later, it
// is the entire reason `Closed` exists, undone.
func TestWhatIsClosedIsClosedInsideABatch(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	_, err = b.batched(t, closedGuard).Do(b.as(ctx), pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, app.RobotService_Patch_FullMethodName, app.RobotPatchRequest_builder{
				Ref:         app.RobotRef_builder{Id: v.GetId()}.Build(),
				Alias:       z.Ptr("smuggled"),
				DateUpdated: v.GetDateUpdated(),
			}.Build()),
		},
	}.Build())
	x.Equal(codes.Unimplemented, status.Code(err))
	x.Contains(err.Error(), "not served")

	// And the row is untouched, so nothing ran before the refusal either.
	got, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{Id: v.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("arm-01", got.GetAlias())
}

// TestACredentialIsNotWidenedByABatch is the second hole, and the worst of
// them.
//
// A token attenuated to two methods is the shape of every scoped credential
// there is. If a batch is checked as one method, that token reaches every RPC
// this server has -- and the trail records that it was allowed.
func TestACredentialIsNotWidenedByABatch(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// A credential that may read a robot and nothing else.
	narrow := frame.New(b.Holder, b.Tenant, frame.Whole().To(app.RobotService_Get_FullMethodName)).
		WithScope(frame.Only(b.Tenant))
	ctx = frame.Into(ctx, narrow)

	_, err := b.batched(t, closedGuard).Do(ctx, pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  "arm-01",
			}.Build()),
		},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.Contains(err.Error(), "not for that")

	n, err := b.Ent.Robot.Query().Count(t.Context())
	x.NoError(err)
	x.Zero(n)
}

// TestABatchCountsAsItsOperations is the third hole.
//
// A limiter counts calls. A batch is one call, so a thousand operations for the
// price of one is exactly the thing a rate limit is against.
func TestABatchCountsAsItsOperations(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Three tokens and no refill worth the name.
	g := batch.Guard{
		Closed:  grpcx.GeneralWrite,
		Limiter: grpcx.NewLimiter(0.001, 3),
		By:      gate.ByTenant(),
	}

	ops := make([]*pdpb.Op, 5)
	for i := range ops {
		ops[i] = op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  slugOf(i),
		}.Build())
	}

	_, err := b.batched(t, g).Do(b.as(ctx), pdpb.BatchRequest_builder{Ops: ops}.Build())
	x.Equal(codes.ResourceExhausted, status.Code(err))

	// The fourth of five, which is what "counted per operation" means.
	x.Contains(err.Error(), "ops[3]")
}

func slugOf(i int) string { return "arm-0" + string(rune('1'+i)) }

// TestAPolicyAppliesToEachOperation is the fourth hole, and it was the one with
// no test.
//
// The other three are checks against a method name and the guard applies them
// itself. This one is a question asked of something the deployment injected,
// and what it answers is not yes or no: `Where` hands back a scope, which has
// to travel with the operation rather than be checked and dropped. So it is
// both the easiest of the four to leave out and the only one that changes what
// the operation sees.
//
// Left out, a batch would be authorised as `BatchService/Do` -- a method the
// policy has never heard of and would have to have an opinion about -- and
// every operation inside it would run as whatever that decided.
func TestAPolicyAppliesToEachOperation(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	one := pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  "arm-01",
			}.Build()),
		},
	}.Build()

	// The same batch under the guard the deployment has without a policy, so
	// that what the policy refuses is read against a batch that goes through.
	_, err := b.batched(t, closedGuard).Do(b.as(ctx), one)
	x.NoError(err)

	g := closedGuard
	g.Policy = console{}

	_, err = b.batched(t, g).Do(b.as(ctx), one)
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.Contains(err.Error(), "reads and does not write")

	// And it names the operation, which is the only thing that says the policy
	// was asked about `RobotService/Add` rather than about `BatchService/Do`.
	x.Contains(err.Error(), "ops[0]")
	x.Contains(err.Error(), app.RobotService_Add_FullMethodName)

	// One row, from the batch that was allowed, so the refused one wrote
	// nothing. Which is worth reading back rather than inferring from the
	// error: a refusal that arrived after the operation ran would answer
	// exactly the same way.
	n, err := b.Ent.Robot.Query().Count(t.Context())
	x.NoError(err)
	x.Equal(1, n)
}

// TestABatchWithoutRulesIsNotBuilt is the refusal that keeps the three above
// from being something somebody has to remember.
func TestABatchWithoutRulesIsNotBuilt(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	_, err := pd.Batch(b.Walled, b.Drv, batch.Guard{})
	x.ErrorIs(err, batch.ErrNoGuard)

	// A deployment that really has no rules says so in a way that reads as a
	// decision rather than as an empty struct.
	_, err = pd.Batch(b.Walled, b.Drv, batch.Guard{Closed: func(string) bool { return false }})
	x.NoError(err)
}

// TestTheWallStillNarrowsInsideABatch, which is the one of the four that is not
// a method-name check -- it is a predicate, and it travels with the context the
// guard answers with.
func TestTheWallStillNarrowsInsideABatch(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)

	theirs, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
		Alias:  "theirs",
	}.Build())
	x.NoError(err)

	_, err = b.batched(t, closedGuard).Do(b.as(ctx), pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, app.RobotService_Get_FullMethodName, app.RobotGetRequest_builder{
				Ref: app.RobotRef_builder{Id: theirs.GetId()}.Build(),
			}.Build()),
		},
	}.Build())
	x.Equal(codes.NotFound, status.Code(err))
}

// TestABatchNeedsNoPlaceholderLanguage is the decision from §3.3 paying off.
//
// The classic difficulty of a batch API is "make a tenant, then a holder inside
// it" -- the second operation needs the first one's identifier, and the usual
// answer is a mini-language of `$0.id` references. Mini-languages grow.
//
// payday needs none, because a client mints its own identifiers: `Mint` takes
// the one it was given and checks only that its domain is right. So the caller
// writes both keys before sending anything, and the server does not have to
// know it was a batch.
func TestABatchNeedsNoPlaceholderLanguage(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Both identifiers, made by the caller before anything is sent.
	robot := pdid.New(pd.RobotDomain)
	joint := pdid.New(pd.JointDomain)

	_, err := b.batched(t, closedGuard).Do(b.as(ctx), pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
				Id:     robot.Bytes(),
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  "arm-01",
			}.Build()),
			// Referring to a row that does not exist yet at the time this
			// message was written, by a name its author chose.
			op(t, app.JointService_Add_FullMethodName, app.JointAddRequest_builder{
				Id:    joint.Bytes(),
				Robot: app.RobotRef_builder{Id: robot.Bytes()}.Build(),
				Alias: "elbow",
			}.Build()),
		},
	}.Build())
	x.NoError(err)

	v, err := b.Walled.Joint().Get(b.as(ctx), app.JointGetRequest_builder{
		Ref: app.JointRef_builder{Id: joint.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("elbow", v.GetAlias())
}

// TestABatchIsBounded, because a batch is a transaction and one held open is a
// lock held against every other writer.
func TestABatchIsBounded(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	g := closedGuard
	g.Max = 2

	ops := make([]*pdpb.Op, 3)
	for i := range ops {
		ops[i] = op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  slugOf(i),
		}.Build())
	}

	_, err := b.batched(t, g).Do(b.as(ctx), pdpb.BatchRequest_builder{Ops: ops}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(err.Error(), "lock held against every other writer")

	// And no operations at all is refused too, rather than being an empty
	// transaction nobody meant to open.
	_, err = b.batched(t, g).Do(b.as(ctx), pdpb.BatchRequest_builder{}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
}

// TestAnOperationNamingSomethingElseIsRefused: an `Any` is checked by its type
// URL, so a request that decoded into a different message -- which would be a
// write the caller did not ask for -- has an answer rather than a coercion.
func TestAnOperationNamingSomethingElseIsRefused(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	s := b.batched(t, closedGuard)

	_, err := s.Do(b.as(ctx), pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, app.RobotService_Add_FullMethodName, app.TenantAddRequest_builder{Alias: "acme"}.Build()),
		},
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))

	// And a method this server does not have.
	_, err = s.Do(b.as(ctx), pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, "/made.Up/Entirely", app.TenantAddRequest_builder{Alias: "acme"}.Build()),
		},
	}.Build())
	x.Equal(codes.Unimplemented, status.Code(err))
}

// TestEveryOperationIsOnTheTrail: the layers a batch runs through are the ones
// a call runs through, because the whole stack is put on the transaction rather
// than the batch reaching past it.
func TestEveryOperationIsOnTheTrail(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	before, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)

	res, err := b.batched(t, closedGuard).Do(b.as(ctx), pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  "arm-01",
			}.Build()),
			op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  "arm-02",
			}.Build()),
		},
	}.Build())
	x.NoError(err)

	after, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Equal(before+2, after, "two writes, two lines of the trail")

	// And they are the two, asked for by what they are about; see [wrote].
	rows, err := b.Ent.Audit.Query().Where(entaudit.ObjectIDIn(wrote(x, res)...)).All(ctx)
	x.NoError(err)
	x.Len(rows, 2)

	// Each under the operation's own name. Held here too, though this path
	// alone could not vouch for it: called directly there is no transport, and
	// an Add's fallback -- the leg that wrote -- spells the same name. The
	// wire test is where the two come apart.
	for _, row := range rows {
		x.Equal(app.RobotService_Add_FullMethodName, row.Action)
	}
}

// TestABatchIsOneEventOverTheWire is the claim the tests above cannot make,
// because they call the handler directly.
//
// The watch recorder remembers into the context and the interceptor publishes
// once the handler has answered -- which for a batch is after the commit. So
// what a subscriber gets is one event holding every change, which is what a
// batch was: a UI sees the transaction rather than the pieces of it.
//
// It also proves the wiring. `Grpc` registers the batch beside everything else,
// and a batch nobody registered is a set of guarantees about a service that is
// not served.
func TestABatchIsOneEventOverTheWire(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	before, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)

	events, stop := b.Watch.Subscribe()
	defer stop()

	conn := pdtest.Serve(t, b.grpc(t, pdtest.Logging(t)))

	res, err := pdpb.NewBatchServiceClient(conn).Do(b.travels(ctx), pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  "arm-01",
			}.Build()),
			op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  "arm-02",
			}.Build()),
		},
	}.Build())
	x.NoError(err)

	select {
	case e := <-events:
		x.Len(e.Changes, 2, "two writes, and a subscriber saw the transaction")
		x.Equal(pdpb.BatchService_Do_FullMethodName, e.Method,
			"the event says what the caller asked for, which was a batch")
	case <-time.After(3 * time.Second):
		t.Fatal("nothing was published")
	}

	// And the trail is the other way around. The recorder asks gRPC what the
	// caller asked for, and over the wire gRPC has an answer -- the batch --
	// which is true of the envelope and false of every operation: the direct
	// calls above cannot catch this, because without a transport the recorder
	// falls back to the leg that wrote, which for an Add spells the same.
	after, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Equal(before+2, after,
		"one row per operation and none for the envelope, which wrote nothing: "+
			"the batch shows whole as the one event, not as a line of the trail")

	rows, err := b.Ent.Audit.Query().Where(entaudit.ObjectIDIn(wrote(x, res)...)).All(ctx)
	x.NoError(err)
	x.Len(rows, 2)

	for _, row := range rows {
		x.Equal(app.RobotService_Add_FullMethodName, row.Action,
			"filed under the operation's own name, not the batch's")
	}
}
