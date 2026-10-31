package cmd_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/meter"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// The meters this app declares (payday#186), measured on its database: each
// query `pd gen` wrote for one, held to what it counts -- one tenant's rows and
// not another's, never an erased one, and each condition as the schema says it
// -- on SQLite, and on PostgreSQL where PDTEST_POSTGRES names one.

// taken is a sink that keeps what it is handed.
type taken struct {
	mu  sync.Mutex
	got []meter.Reading
}

func (r *taken) Send(_ context.Context, rs []meter.Reading) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.got = append(r.got, rs...)
	return nil
}

// of is the readings of `tenant`, by meter.
func (r *taken) of(tenant pdid.Id) map[string]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	vs := map[string]int64{}
	for _, v := range r.got {
		if v.Tenant == tenant {
			vs[v.Meter] = v.Value
		}
	}

	return vs
}

// holding is what the tests below put in a tenant, through the servers the
// deployment does its own work with.
type holding struct {
	t   *testing.T
	b   *built
	ctx context.Context
}

func (h holding) tenant(alias string) pdid.Id {
	h.t.Helper()

	v, err := h.b.Ungated.Tenant().Add(h.ctx, app.TenantAddRequest_builder{Alias: alias}.Build())
	require.NoError(h.t, err)

	return must(pdid.From(v.GetId()))
}

func (h holding) robot(tenant pdid.Id, alias string, attested bool) []byte {
	h.t.Helper()

	req := app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(), Alias: alias}
	if attested {
		req.DateAttested = timestamppb.Now()
	}
	v, err := h.b.Ungated.Robot().Add(h.ctx, req.Build())
	require.NoError(h.t, err)

	return v.GetId()
}

func (h holding) machine(tenant pdid.Id, req app.MachineAddRequest_builder) []byte {
	h.t.Helper()

	req.Tenant = app.TenantRef_builder{Id: tenant.Bytes()}.Build()
	v, err := h.b.Ungated.Machine().Add(h.ctx, req.Build())
	require.NoError(h.t, err)

	return v.GetId()
}

func (h holding) joint(robot []byte, alias string) []byte {
	h.t.Helper()

	req := app.JointAddRequest_builder{Alias: alias}
	if robot != nil {
		req.Robot = app.RobotRef_builder{Id: robot}.Build()
	}
	v, err := h.b.Ungated.Joint().Add(h.ctx, req.Build())
	require.NoError(h.t, err)

	return v.GetId()
}

func (h holding) reading(robot []byte) []byte {
	h.t.Helper()

	v, err := h.b.Ungated.Reading().Add(h.ctx, app.ReadingAddRequest_builder{
		Robot:   app.RobotRef_builder{Id: robot}.Build(),
		Celsius: 21,
	}.Build())
	require.NoError(h.t, err)

	return v.GetId()
}

func (h holding) holder(tenant pdid.Id, alias string) []byte {
	h.t.Helper()

	v, err := h.b.Ungated.Holder().Add(h.ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	require.NoError(h.t, err)

	return v.GetId()
}

// maker is somebody who made a machine, whom this app does not keep.
func maker() []byte { return pdid.New(pdid.Domain(200)).Bytes() }

// TestEveryDeclaredMeterCountsWhatItSays, of two tenants and of one that holds
// nothing -- whose sum is zero and not an error, which is what SQL's SUM of no
// rows is not.
func TestEveryDeclaredMeterCountsWhatItSays(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	h := holding{t, b, ctx}

	acme := b.Tenant // with its "admin" holder already in it
	beta := h.tenant("beta")
	none := h.tenant("none")

	// Robots: one attested, and an attested one erased.
	r1 := h.robot(acme, "r1", true)
	r2 := h.robot(acme, "r2", false)
	r3 := h.robot(acme, "r3", true)
	r4 := h.robot(beta, "r4", true)
	_, err := b.Ungated.Robot().Erase(ctx, app.RobotRef_builder{Id: r3}.Build())
	x.NoError(err)

	// Machines. At work -- in a mode, not idle, and running: m1 and m2, and
	// not m5, whose mode is unset. Spare -- on no robot, and no prototype:
	// m3, whose model is empty, and m5. New: m2 and m3. Their maker known:
	// m1, m3 and m5, whose cycles are past what an int32 holds. And m4,
	// which would be every one of them, erased.
	h.machine(acme, app.MachineAddRequest_builder{Alias: "m1", Mode: app.Mode_MODE_RUN, Running: true,
		Robot: app.RobotRef_builder{Id: r1}.Build(), Model: "x1", Hours: 5, Cycles: 100, MakerId: maker()})
	h.machine(acme, app.MachineAddRequest_builder{Alias: "m2", Mode: app.Mode_MODE_SERVICE, Running: true,
		Model: "prototype", Cycles: 50})
	h.machine(acme, app.MachineAddRequest_builder{Alias: "m3", Mode: app.Mode_MODE_RUN, Running: false,
		Cycles: 7, MakerId: maker()})
	m4 := h.machine(acme, app.MachineAddRequest_builder{Alias: "m4", Mode: app.Mode_MODE_RUN, Running: true,
		Model: "x2", Cycles: 1000, MakerId: maker()})
	h.machine(acme, app.MachineAddRequest_builder{Alias: "m5", Running: false,
		Model: "x3", Hours: 1, Cycles: 1 << 40, MakerId: maker()})
	h.machine(beta, app.MachineAddRequest_builder{Alias: "m6", Mode: app.Mode_MODE_RUN, Running: true,
		Model: "x", Cycles: 9, MakerId: maker()})
	_, err = b.Ungated.Machine().Erase(ctx, app.MachineRef_builder{Id: m4}.Build())
	x.NoError(err)

	// Joints, whose tenant is their robot's -- one with none is nobody's --
	// and one erased.
	h.joint(r1, "j1")
	h.joint(r2, "j2")
	j3 := h.joint(r1, "j3")
	h.joint(nil, "j4")
	h.joint(r4, "j5")
	_, err = b.Ungated.Joint().Erase(ctx, app.JointRef_builder{Id: j3}.Build())
	x.NoError(err)

	// Readings, stamped with their robot's tenant, and one erased hard.
	h.reading(r1)
	h.reading(r2)
	e := h.reading(r2)
	h.reading(r4)
	_, err = b.Ungated.Reading().Erase(ctx, app.ReadingRef_builder{Id: e}.Build())
	x.NoError(err)

	// And the seats, payday's Holder, one of them gone.
	h.holder(acme, "two")
	gone := h.holder(acme, "three")
	h.holder(beta, "four")
	_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: gone}.Build())
	x.NoError(err)

	sink := &taken{}
	r := meter.Reader{Meters: pd.Meters(b.Ent), Sink: sink}
	for _, v := range []pdid.Id{acme, beta, none} {
		x.NoError(r.Read(ctx, v))
	}

	x.Equal(map[string]int64{
		"apptest.robots":           2,
		"apptest.robots.attested":  1,
		"apptest.machines.working": 2,
		"apptest.machines.spare":   2,
		"apptest.machines.new":     2,
		"apptest.machines.cycles":  100 + 7 + 1<<40,
		"apptest.joints":           2,
		"apptest.readings":         2,
		"apptest.seats":            2,
	}, sink.of(acme))
	x.Equal(map[string]int64{
		"apptest.robots":           1,
		"apptest.robots.attested":  1,
		"apptest.machines.working": 1,
		"apptest.machines.spare":   1,
		"apptest.machines.new":     1,
		"apptest.machines.cycles":  9,
		"apptest.joints":           1,
		"apptest.readings":         1,
		"apptest.seats":            1,
	}, sink.of(beta))
	x.Equal(map[string]int64{
		"apptest.robots":           0,
		"apptest.robots.attested":  0,
		"apptest.machines.working": 0,
		"apptest.machines.spare":   0,
		"apptest.machines.new":     0,
		"apptest.machines.cycles":  0,
		"apptest.joints":           0,
		"apptest.readings":         0,
		"apptest.seats":            0,
	}, sink.of(none))

	// Each in the unit the schema says it in, and every one of a tenant's at
	// one moment.
	for _, v := range sink.got {
		x.NotEmpty(v.Unit)
		x.Equal(sink.got[0].MeasuredAt.Location(), v.MeasuredAt.Location())
	}
	x.Equal("{cycle}", unitOf(sink.got, "apptest.machines.cycles"))
	x.Equal("{seat}", unitOf(sink.got, "apptest.seats"))
}

func unitOf(rs []meter.Reading, key string) string {
	for _, v := range rs {
		if v.Meter == key {
			return v.Unit
		}
	}

	return ""
}

// TestMeterTenantsIsEveryTenant, a page at a time in the order of their
// identifiers -- closed or not, since a tenant holds what it holds until it is
// gone.
func TestMeterTenantsIsEveryTenant(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	h := holding{t, b, ctx}

	want := []pdid.Id{b.Tenant, h.tenant("beta"), h.tenant("gamma")}
	slices.SortFunc(want, func(a, b pdid.Id) int { return compareUuid(a, b) })

	list := pd.MeterTenants(b.Ent)
	got := []pdid.Id{}
	after := pdid.Nil
	for {
		vs, err := list(ctx, after, 1)
		x.NoError(err)
		if len(vs) == 0 {
			break
		}
		x.Len(vs, 1)
		got = append(got, vs...)
		after = vs[0]
	}
	x.Equal(want, got)

	all, err := list(ctx, pdid.Nil, 100)
	x.NoError(err)
	x.Equal(want, all)
}

func compareUuid(a, b pdid.Id) int {
	ab, bb := a.Bytes(), b.Bytes()
	for i := range ab {
		switch {
		case ab[i] < bb[i]:
			return -1
		case ab[i] > bb[i]:
			return 1
		}
	}

	return 0
}

// TestTheAppReadsItsMetersOnASchedule: the reader this app serves with is in
// the spin list, made of what the schema declares and a sink, and is the one
// a lease holds by name.
func TestTheAppReadsItsMetersOnASchedule(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	x.Contains(b.Spin, any(b.Meters))
	x.Equal(meter.ReaderName, b.Meters.SpinName())
	x.NotNil(b.Meters.Sink)
	x.NotNil(b.Meters.Tenants)

	keys := []string{}
	for _, m := range b.Meters.Meters {
		keys = append(keys, m.Key)
	}
	x.Contains(keys, "apptest.robots")
	x.Contains(keys, "apptest.seats")

	// And what it is made of reads: the sink this app has says what it was
	// handed and keeps nothing.
	x.NoError(b.Meters.Read(ctx, b.Tenant))
	x.NoError(b.Meters.Read(ctx, pdid.New(1)), "a tenant nobody has holds nothing")
}
