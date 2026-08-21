package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/watch"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/pdtest"
)

// The two halves of a seam that was only half open.
//
// `watch.Broker` has always been an interface, and that was called the seam an
// external stream is attached at. It was half of one: an app could implement a
// broker and could not **select** it, because the configuration was a closed
// switch over the two names payday ships.
//
// And the escape hatch was worse than the thing it was an escape from. A
// deployment that said `none` -- documented as *serves no Watch at all* -- got
// a stream that sent one snapshot and then never spoke again, because nothing
// refused it: `New` answers with a non-nil `*Watch` whichever broker it was
// given, so the generated `if s.w == nil` guard never fired.

// TestABrokerCanBeRegistered is the first half.
func TestABrokerCanBeRegistered(t *testing.T) {
	x := require.New(t)

	t.Run("a name nothing registered is refused, and says what there is", func(t *testing.T) {
		_, err := config.WatchConfig{Broker: "nats"}.Build(config.DbConfig{})
		x.ErrorContains(err, "nats")
		x.ErrorContains(err, "memory")
		x.ErrorContains(err, "RegisterBroker")
	})

	// Registered from a test rather than from an init, which is the one
	// difference from how an app would do it -- and the reason the name is
	// this one: a registry is process-wide, and a test that took a plausible
	// name would be deciding it for every other test in this binary.
	config.RegisterBroker("apptest-echo", func(config.WatchConfig, config.DbConfig) (watch.Broker, error) {
		return watch.Memory(), nil
	})

	t.Run("and one that was is built", func(t *testing.T) {
		b, err := config.WatchConfig{Broker: "apptest-echo"}.Build(config.DbConfig{})
		x.NoError(err)
		x.NotNil(b)
	})

	t.Run("it is listed beside the two payday ships", func(t *testing.T) {
		x.Subset(config.Brokers(), []string{config.BrokerMemory, config.BrokerNone, "apptest-echo"})
	})

	// A registration answering with nothing is `none` under another name, and
	// arriving at that by accident is how a deployment ends up with the silent
	// stream this file's other test is about.
	t.Run("a registration that builds nothing is refused", func(t *testing.T) {
		config.RegisterBroker("apptest-nothing", func(config.WatchConfig, config.DbConfig) (watch.Broker, error) { return nil, nil })

		_, err := config.WatchConfig{Broker: "apptest-nothing"}.Build(config.DbConfig{})
		x.ErrorContains(err, "built no broker")
		x.ErrorContains(err, config.BrokerNone)
	})
}

// TestNoBrokerIsARefusalAndNotASilence is the second half, and it is the one
// that was a live defect rather than a missing feature.
//
// Before this, `none` was the **quietest** setting available: a client got its
// snapshot, the stream stayed open and looked healthy, and no further message
// ever arrived. Somebody turning a feature off got something indistinguishable
// from a system where nothing was happening.
func TestNoBrokerIsARefusalAndNotASilence(t *testing.T) {
	x := require.New(t)

	b, ctx := buildWith(t, config.WatchConfig{Broker: config.BrokerNone})

	vs := b.sow(ctx, x, b.Tenant, 1, "arm-")

	conn := pdtest.Serve(t, b.grpc(t, pdtest.Logging(t)))
	out, err := app.NewClient(conn).Robot().Watch(b.travels(t.Context()),
		app.RobotWatchRequest_builder{
			Filters: []*app.RobotFilter{
				app.RobotFilter_builder{Ref: app.RobotRef_builder{Id: vs[0].GetId()}.Build()}.Build(),
			},
		}.Build())
	x.NoError(err)

	// The first Recv, and not a timeout: the refusal comes before the snapshot,
	// which is the whole point -- a snapshot followed by nothing is the shape a
	// client cannot tell from a quiet system.
	_, err = out.Recv()
	x.Equal(codes.Unimplemented, status.Code(err))
	x.Contains(err.Error(), "nothing to watch")
}
