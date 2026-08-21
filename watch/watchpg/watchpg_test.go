package watchpg_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/watch/watchpg"
)

// A broker that crosses processes, tested by being two of them.
//
// One broker is one replica: it has its own listener connection, its own
// publisher, and its own set of local subscribers. So a second `New` against
// the same database is, for every purpose this package has, a second replica --
// and *does a subscriber on one hear a write on the other* is the whole
// question the memory broker answers no to.

func dsn(t *testing.T) string {
	t.Helper()

	v := os.Getenv(pdtest.Postgres)
	if v == "" {
		t.Skipf("%s is not set; this is the one package that cannot be tested without one", pdtest.Postgres)
	}

	return v
}

func brokers(t *testing.T, n int) []watch.Broker {
	t.Helper()

	v := dsn(t)
	ctx := t.Context()

	vs := make([]watch.Broker, 0, n)
	for range n {
		b, err := watchpg.New(ctx, v)
		require.NoError(t, err)
		vs = append(vs, b)
	}

	// Listening is a round trip, and `New` answers before the `LISTEN` has
	// necessarily landed. A publish that beats it is lost -- which is
	// `LISTEN`/`NOTIFY`'s own guarantee and not a defect -- so a test that
	// publishes immediately would be measuring the race rather than the broker.
	time.Sleep(500 * time.Millisecond)

	return vs
}

func arrives(t *testing.T, c <-chan watch.Event) watch.Event {
	t.Helper()

	select {
	case v, ok := <-c:
		if !ok {
			t.Fatal("the subscription was cut")
		}

		return v

	case <-time.After(10 * time.Second):
		t.Fatal("nothing arrived")

		return watch.Event{}
	}
}

// domain is a number nothing has registered, which is what an identifier from
// somewhere else looks like -- and is right here, since this package carries
// identifiers and never asks what they name.
const domain pdid.Domain = 200

func an(method string, cs ...watch.Change) watch.Event {
	return watch.Event{
		Actor:   pdid.New(domain),
		Tenant:  pdid.New(domain),
		Method:  method,
		Changes: cs,
	}
}

func row(by string) watch.Change {
	return watch.Change{By: by, Key: pdid.New(domain)}
}

// TestAWriteOnOneReplicaReachesASubscriberOnAnother is the whole reason this
// package exists.
//
// With `broker: memory` this is the case that fails, and fails silently: the
// subscriber's stream stays open, looks healthy, and never hears anything
// again.
func TestAWriteOnOneReplicaReachesASubscriberOnAnother(t *testing.T) {
	x := require.New(t)

	vs := brokers(t, 2)
	a, b := vs[0], vs[1]

	c, stop := a.Subscribe()
	defer stop()

	sent := an("/app.RobotService/Patch", row("/app.RobotService/Patch"))
	b.Publish(t.Context(), sent)

	v := arrives(t, c)
	x.Equal(sent.Method, v.Method)
	x.Len(v.Changes, 1)
	x.Equal(sent.Changes[0].By, v.Changes[0].By)
	x.Equal(sent.Changes[0].Key, v.Changes[0].Key)

	// And who did it, which is small enough to carry and is what a trail-shaped
	// consumer would want.
	x.Equal(sent.Actor, v.Actor)
	x.Equal(sent.Tenant, v.Tenant)
}

// TestAPublisherHearsItself, because there is no local shortcut.
//
// One path for everybody means a subscriber here and a subscriber elsewhere see
// the same thing, and it means the round trip is exercised by every deployment
// rather than only by the ones with two replicas.
func TestAPublisherHearsItself(t *testing.T) {
	x := require.New(t)

	a := brokers(t, 1)[0]

	c, stop := a.Subscribe()
	defer stop()

	a.Publish(t.Context(), an("/app.RobotService/Add", row("/app.RobotService/Add")))

	v := arrives(t, c)
	x.Equal("/app.RobotService/Add", v.Method)
}

// TestAReadIsNotNews, which is most of what a server does.
func TestAReadIsNotNews(t *testing.T) {
	x := require.New(t)

	a := brokers(t, 1)[0]

	c, stop := a.Subscribe()
	defer stop()

	a.Publish(t.Context(), an("/app.RobotService/Get"))
	a.Publish(t.Context(), an("/app.RobotService/Add", row("/app.RobotService/Add")))

	// The second one, because the first was never sent.
	v := arrives(t, c)
	x.Equal("/app.RobotService/Add", v.Method)
}

// TestACallThatChangedTooMuchArrivesInPieces.
//
// PostgreSQL will not carry more than 8000 bytes in one notification and that
// is not configurable, so a call that wrote more rows than fit becomes several
// -- which a subscriber cannot tell from several calls, since `watch.Next`
// gathers keys until there is nothing queued.
func TestACallThatChangedTooMuchArrivesInPieces(t *testing.T) {
	x := require.New(t)

	a := brokers(t, 1)[0]

	c, stop := a.Subscribe()
	defer stop()

	const method = "/app.RobotService/Batch"

	want := map[pdid.Id]bool{}
	cs := make([]watch.Change, 0, 400)
	for range 400 {
		k := pdid.New(domain)
		want[k] = true
		cs = append(cs, watch.Change{By: method, Key: k})
	}

	a.Publish(t.Context(), an(method, cs...))

	// Read until every key has been seen, which is exactly what a subscriber
	// does: `Next` takes everything queued and answers with the set.
	got := map[pdid.Id]bool{}
	for len(got) < len(want) {
		for _, ch := range arrives(t, c).Changes {
			got[ch.Key] = true
		}
	}

	x.Equal(want, got, "a batch lost rows on the way through")
}

// TestASubscriberThatFallsBehindIsCutOff, which is the same answer the memory
// broker gives and for the same reason.
//
// Waiting turns a slow consumer into a slow listener and stalls every other
// subscriber with it; skipping in silence leaves one believing it has seen
// everything.
func TestASubscriberThatFallsBehindIsCutOff(t *testing.T) {
	x := require.New(t)

	a := brokers(t, 1)[0]

	c, stop := a.Subscribe()
	defer stop()

	// Slowly enough that the publisher keeps up, so what fills is the
	// **subscriber's** channel and not the queue to the database -- which is a
	// different failure with a different answer.
	for range watch.Backlog * 2 {
		a.Publish(t.Context(), an("/app.RobotService/Add", row("/app.RobotService/Add")))
		time.Sleep(2 * time.Millisecond)
	}

	// Nothing read while they arrived, so the channel filled and the
	// subscription ended rather than silently skipping.
	deadline := time.After(15 * time.Second)
	for {
		select {
		case _, ok := <-c:
			if !ok {
				return
			}

		case <-deadline:
			x.Fail("a subscriber that never read was never cut off")

			return
		}
	}
}

// TestNothingCarriesTheRow is the decision this package makes about what
// travels.
//
// What a subscriber may see is decided per subscriber, by re-reading each row
// through their own narrowing. Putting the row's content on a channel every
// replica reads would answer that question once, in the wrong place, for
// everybody.
func TestNothingCarriesTheRow(t *testing.T) {
	x := require.New(t)

	a := brokers(t, 1)[0]

	c, stop := a.Subscribe()
	defer stop()

	v := an("/app.RobotService/Patch", watch.Change{
		By:    "/app.RobotService/Patch",
		Key:   pdid.New(domain),
		Patch: []byte("this is the document the write was compiled from"),
	})
	a.Publish(t.Context(), v)

	got := arrives(t, c)
	x.Len(got.Changes, 1)
	x.Empty(got.Changes[0].Patch, "the patch travelled")
	x.Nil(got.Request)
	x.Nil(got.Response)
}
