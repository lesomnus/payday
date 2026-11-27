package watchpg_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/watch/watchpg"
)

// What a broker holds is two connections of its own and a goroutine for each,
// and these say they are gone once it is closed -- through the Watch, which is
// where an app's server closes it.
//
// Its connections are told from everybody else's by an application name of
// the test's own, so a run beside other packages on one server counts only
// what this test opened.

// named is the database `dsn` names, with connections that say `name`.
func named(t *testing.T, name string) string {
	t.Helper()

	v := dsn(t)
	if strings.HasPrefix(v, "postgres://") || strings.HasPrefix(v, "postgresql://") {
		sep := "?"
		if strings.Contains(v, "?") {
			sep = "&"
		}

		return v + sep + "application_name=" + name
	}

	return v + " application_name=" + name
}

// unique is an application name nothing else on the server has.
func unique() string {
	return fmt.Sprintf("pdtest-watchpg-%x", pdid.New(domain).Bytes()[:6])
}

// admin is a connection of the test's own, to look at and kill the broker's.
func admin(t *testing.T) *pgx.Conn {
	t.Helper()

	c, err := pgx.Connect(t.Context(), dsn(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close(context.Background()) })

	return c
}

func theirs(t *testing.T, c *pgx.Conn, name string) int {
	t.Helper()

	var n int
	require.NoError(t, c.QueryRow(t.Context(), "select count(*) from pg_stat_activity where application_name = $1", name).Scan(&n))

	return n
}

// settles is what `count` comes to once it stops moving, or what it is after
// `within`: a closed connection leaves pg_stat_activity once its backend has
// ended, which is a moment after the client hangs up.
func settles(within time.Duration, want int, count func() int) int {
	deadline := time.Now().Add(within)
	for {
		n := count()
		if n == want || time.Now().After(deadline) {
			return n
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// goroutines is how many goroutines are running one of the broker's own.
func goroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}

	n := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "payday/watch/watchpg.(*broker).") {
			n++
		}
	}

	return n
}

// TestBrokersMadeAndClosedInTurnLeaveNothingBehind is what was seen in an
// app's suite: every test built a server and closed it, and about thirty in,
// PostgreSQL's hundred connections were all a closed server's broker's.
//
// Each broker here has both its connections open before it is closed -- it has
// published and heard itself -- and a hundred and fifty of them is more than a
// default server would let stand.
func TestBrokersMadeAndClosedInTurnLeaveNothingBehind(t *testing.T) {
	x := require.New(t)

	name := unique()
	v := named(t, name)
	look := admin(t)
	before := goroutines()

	for i := range 150 {
		b, err := watchpg.New(t.Context(), v)
		x.NoError(err, "broker %d", i)
		w := watch.New(b)

		c, stop := b.Subscribe()
		b.Publish(t.Context(), an("/app.RobotService/Add", row("/app.RobotService/Add")))
		arrives(t, c)
		stop()

		x.NoError(w.Close())
	}

	x.Zero(settles(10*time.Second, 0, func() int { return theirs(t, look, name) }),
		"connections of closed brokers are still open")
	x.Equal(before, settles(10*time.Second, before, goroutines),
		"goroutines of closed brokers are still running")
}

// TestAClosedBrokersListenerDoesNotComeBack: the listener dials again a second
// after its connection ends, which is right while the broker is wanted and is
// why killing its connections from outside never helped. Closed, it stays
// gone.
func TestAClosedBrokersListenerDoesNotComeBack(t *testing.T) {
	x := require.New(t)

	name := unique()
	look := admin(t)
	before := goroutines()

	b, err := watchpg.New(t.Context(), named(t, name))
	x.NoError(err)
	x.Equal(1, theirs(t, look, name), "the listener's connection")

	_, err = look.Exec(t.Context(), "select pg_terminate_backend(pid) from pg_stat_activity where application_name = $1", name)
	x.NoError(err)
	x.NoError(watch.New(b).Close())

	// Three times the wait before dialing again, which is long enough to see
	// one that came back.
	time.Sleep(3 * watchpg.Backoff)
	x.Zero(theirs(t, look, name), "the listener of a closed broker came back")
	x.Equal(before, settles(10*time.Second, before, goroutines),
		"goroutines of a closed broker are still running")
}

// TestAClosedBrokerIsNoReplicaAndTheOthersCarryOn: one replica's broker
// closed is that replica gone, and nothing else. What is published through it
// reaches nobody; a subscription asked of it is cut as one is when its
// listener is lost; and the other two hear each other as before.
func TestAClosedBrokerIsNoReplicaAndTheOthersCarryOn(t *testing.T) {
	x := require.New(t)

	vs := brokers(t, 3)
	gone, a, b := vs[0], vs[1], vs[2]

	held, stop := gone.Subscribe()
	defer stop()
	x.NoError(watch.New(gone).Close())

	select {
	case _, ok := <-held:
		x.False(ok, "an event arrived on a closed broker's subscription")
	case <-time.After(time.Second):
		x.Fail("a closed broker's subscription was not cut")
	}

	late, stop := gone.Subscribe()
	defer stop()
	_, ok := <-late
	x.False(ok, "a closed broker opened a subscription")

	c, stop := b.Subscribe()
	defer stop()

	gone.Publish(t.Context(), an("/app.RobotService/Delete", row("/app.RobotService/Delete")))
	x.ErrorIs(gone.(watch.ConfirmedBroker).PublishConfirmed(t.Context(), an("/app.RobotService/Delete", row("/app.RobotService/Delete"))), watchpg.ErrClosed)

	a.Publish(t.Context(), an("/app.RobotService/Add", row("/app.RobotService/Add")))

	// The first to arrive is the second broker's, which says the closed one's
	// went nowhere.
	x.Equal("/app.RobotService/Add", arrives(t, c).Method)
}
