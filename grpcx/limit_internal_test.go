package grpcx

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The clock is driven here rather than waited on, which is why these are inside
// the package: what a bucket does over a minute is a minute of test otherwise,
// and a test that sleeps for what it is measuring is a test that fails on a
// loaded machine.

func TestMemLimiterRefills(t *testing.T) {
	x := require.New(t)

	t0 := time.Now()
	l := NewLimiter(2, 1) // two a second, one at a time

	_, ok := l.allow(t0, "tenant:acme")
	x.True(ok)

	retry, ok := l.allow(t0, "tenant:acme")
	x.False(ok)
	x.Equal(500*time.Millisecond, retry, "two a second is a token every half second")

	// Not yet.
	_, ok = l.allow(t0.Add(250*time.Millisecond), "tenant:acme")
	x.False(ok)

	// And now.
	_, ok = l.allow(t0.Add(500*time.Millisecond), "tenant:acme")
	x.True(ok)
}

func TestMemLimiterForgets(t *testing.T) {
	x := require.New(t)

	t0 := time.Now()
	l := NewLimiter(1, 4) // one a second, four at a time: full again after four
	x.Equal(4*time.Second, l.full)

	drain := func(at time.Time, key string) {
		t.Helper()
		for range 4 {
			_, ok := l.allow(at, key)
			x.True(ok)
		}
	}

	drain(t0, "tenant:acme")
	x.Equal(1, l.Len())

	// A second later, which is sooner than a sweep: nothing is looked at.
	drain(t0.Add(time.Second), "tenant:hooli")
	x.Equal(2, l.Len())

	// Four seconds in, a sweep runs. acme has had its four back and is
	// indistinguishable from a key that was never seen, so it is forgotten;
	// hooli is still a second behind and is kept.
	_, ok := l.allow(t0.Add(4*time.Second), "tenant:initech")
	x.True(ok)

	x.Equal(2, l.Len(), "hooli, which is behind, and initech, which just arrived")
	_, held := l.at["tenant:acme"]
	x.False(held)
	_, held = l.at["tenant:hooli"]
	x.True(held)

	// Forgetting one changes no answer: what acme gets when it comes back is
	// the full bucket it left behind, which is what it would have had anyway.
	drain(t0.Add(4*time.Second), "tenant:acme")
	_, ok = l.allow(t0.Add(4*time.Second), "tenant:acme")
	x.False(ok)
}
