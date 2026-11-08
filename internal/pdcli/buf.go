package pdcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// BufVersion is the buf every generation runs, and it is a constant here rather
// than a thing an app is asked to configure.
//
// **Which buf ran is part of what a generation produced.** buf compiles the
// schema, so its version decides what the descriptors every plugin reads carry:
// two of them disagree about whether a file's leading comment reaches the
// generated code, and about where the formatter breaks an option -- which moves
// files nothing in an app has touched. `pd gen --check` is a CI gate on exactly
// that output, so a buf that differs between two machines is a red build nobody
// caused.
//
// It is payday's to pin because the rest of the generation is: `pd` writes the
// buf template, so the version of the thing that reads it is the same decision.
// An app that has to be told is an app that can be told wrong.
const BufVersion = "1.72.0"

// BufEnv names a buf to run instead of the one below.
//
// It is the whole of the configuration, and it is for the two cases the
// download cannot serve: a machine with no route to GitHub, and somebody
// bisecting buf itself. Everything else gets the pinned one without being asked
// anything.
const BufEnv = "PD_BUF"

// bufSums is the release, by asset, as published in `sha256.txt` beside it.
//
// The download is verified against this and there is no flag to skip it. What
// this replaces is `go.sum`: buf used to be a tool of the app's module, which
// made the module graph carry buf's -- 60 more requires, docker, quic, cel and
// an LSP, none of which the app links. The integrity that came with that is not
// what was being paid for, so it is kept and the graph is not.
var bufSums = map[string]string{
	"Darwin-arm64":       "5176f23a6118b9978de1340c3e3301a4ed0d48e16a669510be44b4c355170d57",
	"Darwin-x86_64":      "eb815a2708d4a43d31799049d5a2987ea81d0a9e98b53976d47bd1e78d154a8f",
	"Linux-aarch64":      "bdbb275fb9624104ef4d8513d269cc410a153138646e67136cb3f8cc185be289",
	"Linux-x86_64":       "8720830e26a733da55bb89bcd3cb44849c0965fc0c44fb5d691cccdc64dca5af",
	"Windows-arm64.exe":  "cc06910c1b69715b598fc8d1958538c86b656c05f6dd0a516dfa90c325dcbead",
	"Windows-x86_64.exe": "6e8f6d043e520bc81cae7b85d4cd6d93e57716a8a9842d5d18200191ee259cb5",
}

// bufAsset is what this machine's buf is called in a release.
//
// buf names its assets after `uname`, so the mapping is written out rather than
// derived: `arm64` is `arm64` on macOS and `aarch64` on Linux, and a table that
// guessed would be wrong on one of them.
func bufAsset() (string, error) {
	os_, ok := map[string]string{
		"darwin":  "Darwin",
		"linux":   "Linux",
		"windows": "Windows",
	}[runtime.GOOS]
	if !ok {
		return "", fmt.Errorf("no buf is published for %s; name one with %s", runtime.GOOS, BufEnv)
	}

	arch, ok := map[string]string{
		"amd64": "x86_64",
		"arm64": map[bool]string{true: "aarch64", false: "arm64"}[runtime.GOOS == "linux"],
	}[runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no buf is published for %s/%s; name one with %s", runtime.GOOS, runtime.GOARCH, BufEnv)
	}

	name := os_ + "-" + arch
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	return name, nil
}

// Buf is the buf to run, fetched into a cache the first time and found there
// after.
//
// [BufEnv] wins outright and is not verified against anything: somebody who
// named a buf has said which one they want, and a checksum on it would be this
// refusing the answer it asked for.
func Buf(ctx context.Context) (string, error) {
	if v := os.Getenv(BufEnv); v != "" {
		return v, nil
	}

	asset, err := bufAsset()
	if err != nil {
		return "", err
	}

	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("nowhere to keep buf: %w; name one with %s", err, BufEnv)
	}

	// By version, so that two checkouts on two payday versions do not take
	// turns overwriting one file -- and so that the answer to "which buf ran"
	// is in the path it ran from.
	dir = filepath.Join(dir, "payday", "buf", BufVersion)
	at := filepath.Join(dir, "buf")
	if runtime.GOOS == "windows" {
		at += ".exe"
	}
	if _, err := os.Stat(at); err == nil {
		return at, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := fetchBuf(ctx, asset, at); err != nil {
		return "", fmt.Errorf("fetch buf %s: %w\n\nname one with %s to skip this", BufVersion, err, BufEnv)
	}

	return at, nil
}

// fetchBuf downloads one asset and puts it at `at`, whole or not at all.
func fetchBuf(ctx context.Context, asset string, at string) error {
	sum, ok := bufSums[asset]
	if !ok {
		return fmt.Errorf("buf-%s is not one this payday knows the digest of", asset)
	}

	url := fmt.Sprintf("https://github.com/bufbuild/buf/releases/download/v%s/buf-%s", BufVersion, asset)

	return fetchBufFrom(ctx, url, sum, at)
}

// bufBackoff is how long a fetch waits before each attempt after the first,
// so it makes one more attempt than this has entries: five, fifteen seconds
// of waiting in all.
//
// GitHub serves a release through a gateway that answers 504 now and then for
// a file that is there. In one run of payday's own CI, on 2026-11-08, it did
// to a `pd gen` and to the tests that fetch buf, and each failed on its one
// attempt: the red build nobody caused that pinning buf set out to end.
// Doubling from a second rides out a gateway that is back within seconds, and
// still gives up on one that is not while whoever ran it is waiting. A var so
// that a test does not wait for real.
var bufBackoff = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// bufWaitAtMost is the longest a fetch waits because a server asked it to
// (Retry-After). A rate limit may ask for an hour, and nothing is printed
// while a fetch waits, so a wait that long is a `pd gen` nobody can tell from
// a hung one. A server that asks for more is asked again after this, until
// the attempts are spent, and its last answer is the error.
var bufWaitAtMost = 30 * time.Second

// fetchBufFrom is [fetchBuf] from `url`, asked again while what fails is the
// way to it: a 5xx, a 429, a connection that could not be made or was cut.
//
// What would be the same answer asked again is the answer at once -- any
// other 4xx, which is a release or an asset that does not exist; a file
// whose digest is not `sum`, which is a wrong file and not a slow one; and a
// caller that has gone, which is not waited on at all.
func fetchBufFrom(ctx context.Context, url string, sum string, at string) error {
	for i := 0; ; i++ {
		err := fetchBufOnce(ctx, url, sum, at)
		if err == nil {
			return nil
		}

		var t *transient
		if !errors.As(err, &t) {
			return err
		}
		if i == len(bufBackoff) {
			return fmt.Errorf("gave up after %d attempts: %w", i+1, err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("%w, waiting to try again after %w", ctx.Err(), err)
		case <-time.After(max(bufBackoff[i], min(t.after, bufWaitAtMost))):
		}
	}
}

// fetchBufOnce is one attempt, into a temporary file of its own, so that an
// attempt that died leaves nothing the next one has to know about.
func fetchBufOnce(ctx context.Context, url string, sum string, at string) error {
	// Into a temporary file beside the destination and renamed onto it, so that
	// two generations running at once cannot read half of what the other is
	// writing -- and so that a download that died leaves nothing that looks
	// like a buf.
	f, err := os.CreateTemp(filepath.Dir(at), ".buf-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())

	h := sha256.New()
	if err := download(ctx, url, io.MultiWriter(f, h)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return fmt.Errorf("digest is %s and this payday pins %s", got, sum)
	}
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		return err
	}

	return os.Rename(f.Name(), at)
}

// download writes what `url` answers into `w`. What fails on the way to it --
// a 5xx, a 429, a request or a body the network failed -- is [transient]; a
// write that fails is not.
func download(ctx context.Context, url string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return cut(ctx, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		err := fmt.Errorf("%s: %s", url, res.Status)
		if res.StatusCode/100 != 5 && res.StatusCode != http.StatusTooManyRequests {
			return err
		}

		return &transient{err: err, after: retryAfter(res.Header.Get("Retry-After"), time.Now())}
	}

	body := &downloading{r: res.Body}
	if _, err := io.Copy(w, body); err != nil {
		if body.err != nil {
			return cut(ctx, fmt.Errorf("%s: %w", url, err))
		}
		return err
	}

	return nil
}

// transient is an attempt that failed in a way the next one may not, and how
// long the server asked to be left before it, when it said.
type transient struct {
	err   error
	after time.Duration
}

func (e *transient) Error() string { return e.err.Error() }
func (e *transient) Unwrap() error { return e.err }

// cut is a request the network failed -- refused, reset, timed out, ended
// early -- which the next attempt may get through. Unless the caller is the
// one that ended it: that is not the network, and is not asked again.
func cut(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}

	return &transient{err: err}
}

// downloading is a body that remembers whether it stopped because it could
// not be read. That is the network, and worth another attempt; a write that
// failed is this machine's -- a full disk -- and is not, and io.Copy answers
// both with nothing but the error.
type downloading struct {
	r   io.Reader
	err error
}

func (v *downloading) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		v.err = err
	}

	return n, err
}

// retryAfter is how long a Retry-After asks to be waited: a number of seconds,
// or a date (RFC 9110, 10.2.3). Anything else asks for nothing.
//
// A number too large to read is as long as one can be, which bufWaitAtMost
// then cuts down, rather than nothing: it is a server asking for longer than
// anybody will wait, not one that did not ask.
func retryAfter(v string, now time.Time) time.Duration {
	if s, err := strconv.ParseUint(v, 10, 32); err == nil || errors.Is(err, strconv.ErrRange) {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0)
	}

	return 0
}
