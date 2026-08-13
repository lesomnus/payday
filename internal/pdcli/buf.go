package pdcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, res.Status)
	}

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
	if _, err := io.Copy(io.MultiWriter(f, h), res.Body); err != nil {
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
