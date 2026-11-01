package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"syscall"
	"time"

	"github.com/lesomnus/otx/log"
)

// How long a deployment's first contact with its database is tried again for,
// and how often (#192).
//
// A process opens its database once, as it starts, and stops if it cannot
// ([DbConfig.Open]): a deployment pointed at a database that is not there finds
// out then, in the process that could not start, rather than at its first
// request. What that did not allow for is a database that cannot be reached
// **yet**. In a Kubernetes cluster that enforces NetworkPolicy, a new pod's
// first connections are refused for about a second, until the policy that lets
// it out is in place -- so an app's `migrate` init container failed once on
// every rollout, was restarted, and then succeeded, which a check of the main
// containers' restarts does not see. A proxy started beside the app and a
// database finishing its own start are the same moment under other names.
//
// So the first contact is tried again while nothing has answered: after 200ms,
// then twice as long each time up to 2s, and no attempt begins more than ten
// seconds after the first. That is nine attempts for a database that never
// comes, and the one a policy holds up for a second is reached on the third or
// fourth.
//
// # Constants, and not configuration
//
// Because what they are for is a moment, and not an outage. A database that is
// down for longer is the orchestrator's to wait for -- a restart, with its own
// backoff, where it is seen as one -- and a setting would be the way to make
// this the mechanism for that instead: a start that hangs for minutes is a
// failure nothing reports. No deployment has to say anything for the rollout
// above to stop failing, and none can turn it into a hang.
//
// What it costs is a misconfiguration that looks the same as the moment. A
// wrong port is refused exactly as a policy refuses, so it is ten seconds
// before the process stops, and every attempt says what it was told. Anything
// the database answered is returned at once; see [unreachable].
//
// The waits are not jittered. What they space out is a handful of refused
// connections from one process, which is nothing to spread.
const (
	reachWithin = 10 * time.Second
	reachFirst  = 200 * time.Millisecond
	reachMost   = 2 * time.Second
)

// reach makes the first contact with what a deployment's configuration names
// -- `try` -- and tries it again for a moment if what it says is that nothing
// could be reached. `what` is how the log names it: `db`, `watch.broker: postgres`.
//
// It is for the first contact only, and is called from where a process makes
// one: [DbConfig.Open], and where [WatchConfig.Build] and [SpinConfig.Build]
// call what was registered. A server that loses its database later is told so
// by the statement that finds out, as it always was; nothing here is between a
// running app and its connections.
func reach(ctx context.Context, what string, try func(context.Context) error) error {
	return patience{within: reachWithin, first: reachFirst, most: reachMost}.reach(ctx, what, try)
}

// patience is how long a first contact is tried again for, and how often:
// `first` is the wait after the first refusal, doubled after each one since up
// to `most`, and `within` is how long after the first attempt the last may
// begin.
type patience struct {
	within time.Duration
	first  time.Duration
	most   time.Duration

	// clock is what is waited on, the wall's when nil. A test drives one of
	// its own, so that what ten seconds of patience does is not ten seconds
	// of test.
	clock clock
}

type clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type wall struct{}

func (wall) Now() time.Time { return time.Now() }

func (wall) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (p patience) reach(ctx context.Context, what string, try func(context.Context) error) error {
	c := p.clock
	if c == nil {
		c = wall{}
	}
	l := log.From(ctx)

	start := c.Now()
	wait := p.first
	for n := 1; ; n++ {
		// The caller's context, as it is: what a registered broker or lease is
		// handed is the one it was built under, values and all.
		err := try(ctx)
		if err == nil {
			if n > 1 {
				l.InfoContext(ctx, what+": reached",
					slog.Int("attempts", n), slog.Duration("after", ms(c.Now().Sub(start))))
			}
			return nil
		}
		if ctx.Err() != nil || !unreachable(err) {
			return err
		}

		after := c.Now().Sub(start)
		left := p.within - after
		if left <= 0 {
			l.ErrorContext(ctx, what+": cannot be reached; giving up",
				slog.Int("attempts", n), slog.Duration("after", ms(after)), slog.Any("err", err))
			return fmt.Errorf("not reached in %s: %w", ms(after), err)
		}

		// The last wait is cut to the bound rather than run past it, so that
		// the last attempt is made at ten seconds and not at nine.
		w := min(wait, left)
		l.WarnContext(ctx, what+": cannot be reached; trying again",
			slog.Int("attempt", n), slog.Duration("wait", ms(w)), slog.Any("err", err))
		if err := c.Sleep(ctx, w); err != nil {
			return err
		}
		wait = min(2*wait, p.most)
	}
}

// ms is `d` to the millisecond, which is all a person reading how long a
// database took to come up wants of it.
func ms(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

// unreachable says whether `err` is only that nothing could be reached: a
// connection refused, or reset or closed before a word was said; a host or a
// network that cannot be routed to; a dial or a handshake that timed out; a
// resolver that could not be asked -- and PostgreSQL's 57P03, the one answer of
// its that means ask again.
//
// Anything else is an answer, and an answer is final: a password, a database
// that does not exist, a certificate, a name no resolver knows (NXDOMAIN), a
// server that will not speak TLS -- and every error SQLite gives, since there
// is no network between a process and its file to wait for. An answer is what
// a person has to change something about, and waiting ten seconds to say so
// helps nobody.
//
// A driver that tried more than one address answers with every attempt, and
// pgx does: a name may be several addresses, `sslmode=prefer` tries each twice,
// `host=a,b` is a list. **Every** attempt has to be one that reached nothing.
// One address refusing while another says the password is wrong is a wrong
// password, and is not waited on.
//
// That has one cost, and it is taken knowingly. Under `prefer`, the attempt
// with TLS at a server without it is answered "no TLS", which is not nothing,
// so a 57P03 from the attempt without TLS beside it is returned at once -- as
// it was before any of this. The alternative, an attempt that reached nothing
// beside one whose error is not known, would wait the whole ten seconds on
// whatever that error is.
func unreachable(err error) bool {
	return reachedNothing(err, false)
}

// reachedNothing is [unreachable], told whether `err` is under a network
// operation: an errno counts only there. SQLite's errors carry the operating
// system's too -- a missing directory is ENOENT under an `fs.PathError` -- and
// that ENOENT is an answer, where a Unix socket's is a server not started yet.
func reachedNothing(err error, network bool) bool {
	switch e := err.(type) {
	case nil:
		return false

	case interface{ Unwrap() []error }:
		attempts := e.Unwrap()
		for _, v := range attempts {
			if !reachedNothing(v, network) {
				return false
			}
		}
		return len(attempts) > 0

	case *net.OpError:
		return e.Timeout() || reachedNothing(e.Err, true)

	case *net.DNSError:
		// Not found is the resolver's answer. A resolver that could not be
		// asked -- its own traffic held up by the same policy -- or that failed
		// for now is not.
		return e.IsTimeout || e.IsTemporary

	case syscall.Errno:
		return network && slices.Contains(refusals, e)

	case interface{ SQLState() string }:
		return e.SQLState() == notYet
	}

	switch err {
	case io.EOF, io.ErrUnexpectedEOF:
		// Closed before the database said anything: a proxy in front of one
		// that is not there.
		return true

	case context.DeadlineExceeded:
		// An attempt's own -- a DSN's `connect_timeout` -- since the caller's
		// is asked about before this is.
		return true
	}

	return reachedNothing(errors.Unwrap(err), network)
}

// refusals are what the operating system says of a connection nothing took.
//
// A policy that rejects says one of the first two, by a reset or by ICMP, and
// one that drops says nothing at all: the kernel sends its SYN again, and the
// dial that was held up for a second succeeds a second late. A Unix socket's
// file is there only while its server runs -- PostgreSQL removes its own as it
// stops -- so a missing one is a server that has not started.
var refusals = []syscall.Errno{
	syscall.ECONNREFUSED,
	syscall.EHOSTUNREACH,
	syscall.ENETUNREACH,
	syscall.ENETDOWN,
	syscall.ECONNRESET,
	syscall.ECONNABORTED,
	syscall.EPIPE,
	syscall.ETIMEDOUT,
	syscall.EADDRNOTAVAIL,
	syscall.ENOENT,
}

// notYet is SQLSTATE 57P03, cannot_connect_now: "the database system is
// starting up", or shutting down, or not yet consistent in recovery -- a
// server between not running and running, which `pg_isready` reports as
// rejecting connections rather than as no response.
const notYet = "57P03"
