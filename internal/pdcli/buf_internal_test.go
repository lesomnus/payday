package pdcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A fetch of buf against a server that answers as GitHub's release downloads
// do on a bad day. The release is a few bytes of anything, pinned by its
// digest here as the real ones are in bufSums.

var (
	released    = []byte("#!/bin/sh\necho 1.72.0\n")
	releasedSum = func() string {
		h := sha256.Sum256(released)
		return hex.EncodeToString(h[:])
	}()
)

// releases is a server that answers its n-th request with the n-th of
// `answers`, and every request after them with the last; the URL of a release
// on it, and how many requests it was sent.
//
// The waits between attempts are a millisecond each, as many as there are, so
// that a test counts the attempts a fetch makes without waiting for them. What
// a fetch says while it waits is kept out of the test's output; a test that
// listens sets bufSay itself.
func releases(t *testing.T, answers ...http.HandlerFunc) (string, *atomic.Int32) {
	t.Helper()

	waits := make([]time.Duration, len(bufBackoff))
	for i := range waits {
		waits[i] = time.Millisecond
	}
	was := bufBackoff
	bufBackoff = waits
	t.Cleanup(func() { bufBackoff = was })

	said := bufSay
	bufSay = io.Discard
	t.Cleanup(func() { bufSay = said })

	n := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		answers[min(i, len(answers)-1)](w, r)
	}))
	t.Cleanup(srv.Close)

	return srv.URL + "/buf-Linux-x86_64", n
}

func answering(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}
}

func sending(b []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Write(b)
	}
}

// into is where a fetch puts buf, in a directory of its own.
func into(t *testing.T) string {
	return filepath.Join(t.TempDir(), "buf")
}

// left is what is in the directory a fetch put buf into: buf, if it was
// fetched, and nothing else -- an attempt that failed leaves no file behind.
func left(t *testing.T, at string) []string {
	t.Helper()

	vs, err := os.ReadDir(filepath.Dir(at))
	require.NoError(t, err)

	names := []string{}
	for _, v := range vs {
		names = append(names, v.Name())
	}

	return names
}

// TestAGatewayThatTimesOutIsAskedAgain.
//
// What failed payday's CI: GitHub's gateway answered 504 for a release that
// was there. Asked again, it is fetched, and each attempt that failed left
// nothing behind -- and said, before it waited, what it was answered and
// which attempt was next.
func TestAGatewayThatTimesOutIsAskedAgain(t *testing.T) {
	x := require.New(t)

	url, n := releases(t,
		answering(http.StatusGatewayTimeout),
		answering(http.StatusGatewayTimeout),
		sending(released),
	)
	said := &strings.Builder{}
	bufSay = said
	at := into(t)

	x.NoError(fetchBufFrom(t.Context(), url, releasedSum, at))
	x.EqualValues(3, n.Load())

	b, err := os.ReadFile(at)
	x.NoError(err)
	x.Equal(released, b)
	x.Equal([]string{"buf"}, left(t, at))

	s, err := os.Stat(at)
	x.NoError(err)
	x.NotZero(s.Mode()&0o100, "it is run")

	all := len(bufBackoff) + 1
	x.Equal(
		fmt.Sprintf("pd: fetch buf: %s: 504 Gateway Timeout; attempt 2 of %d in 1ms\n", url, all)+
			fmt.Sprintf("pd: fetch buf: %s: 504 Gateway Timeout; attempt 3 of %d in 1ms\n", url, all),
		said.String(),
	)
}

// TestAFetchAsksForAMinute.
//
// The schedule itself, which every other test here shortens to a millisecond
// a wait: seven attempts, the waits doubling from a second to a minute and
// three seconds in all, and no server kept waited on for longer than
// bufWaitAtMost. Five attempts and fifteen seconds were not enough on
// 2026-11-09.
func TestAFetchAsksForAMinute(t *testing.T) {
	x := require.New(t)

	x.Len(bufBackoff, 6, "seven attempts")
	x.Equal(time.Second, bufBackoff[0])

	var all time.Duration
	for i, v := range bufBackoff {
		if i > 0 {
			x.Equal(2*bufBackoff[i-1], v, "each wait twice the one before")
		}
		all += v
	}
	x.Equal(63*time.Second, all)
	x.Equal(30*time.Second, bufWaitAtMost)
}

// TestAServerThatStaysDownIsGivenUpOn.
//
// A gateway that does not come back is given up on after every attempt, and
// the error says how many and what the last was answered.
func TestAServerThatStaysDownIsGivenUpOn(t *testing.T) {
	x := require.New(t)

	url, n := releases(t, answering(http.StatusServiceUnavailable))
	at := into(t)

	err := fetchBufFrom(t.Context(), url, releasedSum, at)
	x.ErrorContains(err, fmt.Sprintf("after %d attempts", len(bufBackoff)+1))
	x.ErrorContains(err, url+": 503 Service Unavailable")
	x.EqualValues(len(bufBackoff)+1, n.Load())
	x.Empty(left(t, at))
}

// TestAReleaseThatIsNotThereIsNotAskedAgain.
//
// A 404 is a version or an asset that does not exist, and it does not exist
// any more the second time.
func TestAReleaseThatIsNotThereIsNotAskedAgain(t *testing.T) {
	x := require.New(t)

	url, n := releases(t, answering(http.StatusNotFound), sending(released))
	at := into(t)

	err := fetchBufFrom(t.Context(), url, releasedSum, at)
	x.ErrorContains(err, url+": 404 Not Found")
	x.NotContains(err.Error(), "attempts")
	x.EqualValues(1, n.Load())
	x.Empty(left(t, at))
}

// TestAFileThatIsNotTheOnePinnedIsNotAskedAgain.
//
// A digest that does not match is a wrong file, not a flaky network: it is
// refused, it is not fetched again, and it is not left where a buf would be.
func TestAFileThatIsNotTheOnePinnedIsNotAskedAgain(t *testing.T) {
	x := require.New(t)

	url, n := releases(t, sending([]byte("something else")), sending(released))
	at := into(t)

	err := fetchBufFrom(t.Context(), url, releasedSum, at)
	x.ErrorContains(err, "this payday pins "+releasedSum)
	x.EqualValues(1, n.Load())
	x.NoFileExists(at)
	x.Empty(left(t, at))
}

// TestAFileThatCannotBeWrittenIsNotAskedAgain.
//
// A write that fails is this machine's -- a full disk -- and not the
// network's. io.Copy answers it just as it answers a body cut short, and the
// two are told apart, so that it is said at once rather than after every
// attempt has failed the same way.
func TestAFileThatCannotBeWrittenIsNotAskedAgain(t *testing.T) {
	x := require.New(t)

	url, n := releases(t, sending(released))

	err := download(t.Context(), url, fullDisk{})
	x.ErrorIs(err, errDiskFull)

	var v *transient
	x.False(errors.As(err, &v), "not asked again")
	x.EqualValues(1, n.Load())
}

var errDiskFull = errors.New("no space left on device")

type fullDisk struct{}

func (fullDisk) Write([]byte) (int, error) { return 0, errDiskFull }

// TestARateLimitIsWaitedOut.
//
// A 429 is asked again no sooner than its Retry-After says, and no later than
// bufWaitAtMost, which is what keeps a limit that asks for an hour from
// being a `pd gen` that looks hung.
func TestARateLimitIsWaitedOut(t *testing.T) {
	limited := func(after string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", after)
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}

	// When each request came, so that the wait is read off the server's side:
	// timed answers as `h` does and keeps the time, and gap is how long after
	// the first request the second came.
	type clock struct {
		sync.Mutex
		at []time.Time
	}
	timed := func(c *clock, h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c.Lock()
			c.at = append(c.at, time.Now())
			c.Unlock()
			h(w, r)
		}
	}
	gap := func(c *clock) time.Duration {
		c.Lock()
		defer c.Unlock()
		return c.at[1].Sub(c.at[0])
	}

	t.Run("as long as it asks", func(t *testing.T) {
		x := require.New(t)

		c := &clock{}
		url, n := releases(t, timed(c, limited("1")), timed(c, sending(released)))
		at := into(t)

		x.NoError(fetchBufFrom(t.Context(), url, releasedSum, at))
		x.EqualValues(2, n.Load())
		x.GreaterOrEqual(gap(c), time.Second)
	})

	t.Run("and no longer than bufWaitAtMost", func(t *testing.T) {
		x := require.New(t)

		was := bufWaitAtMost
		bufWaitAtMost = 50 * time.Millisecond
		t.Cleanup(func() { bufWaitAtMost = was })

		c := &clock{}
		url, n := releases(t, timed(c, limited("3600")), timed(c, sending(released)))
		at := into(t)

		x.NoError(fetchBufFrom(t.Context(), url, releasedSum, at))
		x.EqualValues(2, n.Load())
		x.GreaterOrEqual(gap(c), bufWaitAtMost)
		x.Less(gap(c), 10*time.Second)
	})
}

// TestACallerThatHasGoneIsNotKeptWaiting.
//
// A fetch stops when its context does: neither another attempt nor the rest
// of a wait for one.
func TestACallerThatHasGoneIsNotKeptWaiting(t *testing.T) {
	t.Run("while it waits to try again", func(t *testing.T) {
		x := require.New(t)

		url, n := releases(t, answering(http.StatusServiceUnavailable))
		// Long, so that a fetch that waited one out is seen to have.
		for i := range bufBackoff {
			bufBackoff[i] = 10 * time.Second
		}
		at := into(t)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		time.AfterFunc(100*time.Millisecond, cancel)

		began := time.Now()
		err := fetchBufFrom(ctx, url, releasedSum, at)
		x.ErrorIs(err, context.Canceled)
		x.Less(time.Since(began), 5*time.Second)
		x.LessOrEqual(n.Load(), int32(1), "no attempt after it")
		x.Empty(left(t, at))
	})

	t.Run("before it asks", func(t *testing.T) {
		x := require.New(t)

		url, n := releases(t, answering(http.StatusServiceUnavailable))
		at := into(t)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := fetchBufFrom(ctx, url, releasedSum, at)
		x.ErrorIs(err, context.Canceled)
		x.EqualValues(0, n.Load())
		x.Empty(left(t, at))
	})
}

// TestAConnectionThatIsCutIsAskedAgain.
//
// A connection the server closes, before it answers or halfway through the
// file, is the network and not the release: asked again, it is fetched. The
// second is an error reading the body rather than one from the request, and
// is told apart from one writing the file, which is not asked again.
func TestAConnectionThatIsCutIsAskedAgain(t *testing.T) {
	// ErrAbortHandler is how a handler has its server close the connection
	// without finishing what it was answering.
	for name, fail := range map[string]http.HandlerFunc{
		"before it answers": func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		},
		"halfway through the file": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(released)))
			w.WriteHeader(http.StatusOK)
			w.Write(released[:len(released)/2])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			url, n := releases(t, fail, sending(released))
			at := into(t)

			x.NoError(fetchBufFrom(t.Context(), url, releasedSum, at))
			x.EqualValues(2, n.Load())

			b, err := os.ReadFile(at)
			x.NoError(err)
			x.Equal(released, b)
			x.Equal([]string{"buf"}, left(t, at))
		})
	}
}

// TestRetryAfterIsSecondsOrADate.
//
// What it says or nothing, and a number too large to read is as long as one
// can be -- a server asking for longer than anybody waits, which
// bufWaitAtMost cuts down -- and not nothing.
func TestRetryAfterIsSecondsOrADate(t *testing.T) {
	now := time.Date(2026, 11, 8, 8, 50, 0, 0, time.UTC)

	for v, want := range map[string]time.Duration{
		"":                              0,
		"0":                             0,
		"120":                           2 * time.Minute,
		"-1":                            0,
		"+1":                            0,
		"soon":                          0,
		"Sun, 08 Nov 2026 08:51:30 GMT": 90 * time.Second,
		"Sun, 08 Nov 2026 08:49:00 GMT": 0,
		"99999999999999999999":          (1<<32 - 1) * time.Second,
	} {
		require.Equal(t, want, retryAfter(v, now), "Retry-After: %q", v)
	}
}
