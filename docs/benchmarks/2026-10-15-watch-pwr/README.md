# Common Watch: lazy Any evaluation with pwr

Measured on 2026-10-15. Implementation: payday
[`86176f1`](https://github.com/lesomnus/payday/commit/86176f1f7e728466a297810338a8861fb0051777),
against the standard CEL adapter used by
[`38ed2fd`](https://github.com/lesomnus/payday/commit/38ed2fde72a37a7a6cdf5feff601057bf0736418).
The pwr CEL module is pinned to
[`de464a5`](https://github.com/lesomnus/pwr/commit/de464a5ef1fadb082fc9f778a1b420a54b2c2372).

## What changed

`ResourceService.Watch` still authorizes and projects each event before evaluating
its CEL filter. The event envelope is already a Go protobuf value. pwr reads the
projected `Any` payload selectively, avoiding CEL's subsequent full payload
unpacking. The authorization decode and projected payload encoding still happen.
Existing generated resource-specific Watch APIs are unchanged.

This integration uses pwr's default duplicate-safe semantics: ordinary singular
fields retain last-value behavior. It does not enable the unique-scalar or
unchecked-UTF-8 assumptions. Whole-object equality and well-known types can still
materialize messages eagerly.

## Method

The reproducible benchmark is
[`resource/wire_benchmark_test.go`](../../../resource/wire_benchmark_test.go).
Both implementations use the same serving-path CEL cost and interrupt limits,
compiled before timing. The `native` environment reproduces the previous standard
CEL provider; `pwr` uses the current server environment. Each iteration allocates
an activation map as the Watch loop does.

The resource is a `payday.WatchEvent` with an ID, `method = "artifact"`, and a nested
`Any` containing a `BytesValue` of the indicated size. It is packed into the outer
Watch event once before timing. The payload sizes below are the blob sizes, not
the full serialized event sizes.

- `type`: `type(event.value) == payday.WatchEvent`.
- `field`: `has(event.value) && type(event.value) == payday.WatchEvent && event.value.method == "artifact"`.
- `filter`: CEL evaluation of an already packed event.
- `project_filter`: event cloning, full resource decode, an identity projector,
  re-encoding, and CEL evaluation. No database queries, broker delivery, or gRPC
  transport are simulated. Real projector work can reduce the relative gain.

Environment: Linux/amd64, AMD Ryzen 7 5825U, Go 1.27.1, `GOMAXPROCS=1`, no CPU
affinity. Each case ran three times for 150 ms, in Go benchmark order. Other build
and test commands completed before this run. The table gives medians; these short
local measurements have visible timing variation and are not confidence intervals
or production throughput predictions. Compare implementations within this run,
not absolute times from the earlier pwr experiments on other hosts.

```sh
GOMAXPROCS=1 go test ./resource -run '^$' -bench '^BenchmarkWatchFilter$' -benchmem -benchtime 150ms -count 3
```

## Results

Time is in microseconds per operation. Speedup is native time divided by pwr time;
a value below 1 means pwr was slower. Memory and allocations show native / pwr.
All 72 raw samples are in [`raw.txt`](raw.txt).

| Blob bytes | Filter | Stage | Native µs | pwr µs | Speedup | B/op | allocs/op |
| ---: | --- | --- | ---: | ---: | ---: | ---: | ---: |
| 0 | type | filter | 3.732 | 2.814 | 1.33x | 1,296 / 1,192 | 22 / 21 |
| 0 | type | project_filter | 5.836 | 5.502 | 1.06x | 2,152 / 2,048 | 35 / 34 |
| 0 | field | filter | 7.591 | 7.928 | 0.96x | 2,056 / 2,080 | 36 / 38 |
| 0 | field | project_filter | 10.777 | 10.202 | 1.06x | 2,912 / 2,936 | 49 / 51 |
| 8,192 | type | filter | 13.405 | 3.760 | 3.57x | 20,165 / 1,192 | 23 / 21 |
| 8,192 | type | project_filter | 26.873 | 21.960 | 1.22x | 49,284 / 30,311 | 37 / 35 |
| 8,192 | field | filter | 27.609 | 5.687 | 4.85x | 39,792 / 2,080 | 38 / 38 |
| 8,192 | field | project_filter | 49.773 | 20.061 | 2.48x | 68,906 / 31,200 | 52 / 52 |
| 262,144 | type | filter | 275.884 | 4.737 | 58.24x | 541,939 / 1,192 | 23 / 21 |
| 262,144 | type | project_filter | 709.326 | 469.559 | 1.51x | 1,353,706 / 813,083 | 38 / 37 |
| 262,144 | field | filter | 503.840 | 6.441 | 78.22x | 1,083,330 / 2,080 | 39 / 38 |
| 262,144 | field | project_filter | 799.618 | 404.851 | 1.98x | 1,895,105 / 813,975 | 53 / 54 |

For the field predicate, the 8 KiB and 256 KiB cases improved by approximately
2.48x and 1.98x respectively when projection was included. Their filter-only gains
were larger because the avoided payload unpacking dominated that isolated stage.
The small field-only case was about 4.4% slower with pwr and used two more
allocations; the timing difference is small relative to run-to-run variation.
Large field-only cases retained the same 2,080 B/op with pwr because the blob was
never requested. Traversing every field or materializing whole messages is a
different workload and is not demonstrated to improve by these results.

## Binary size

The same application (`internal/apptest/testsrv`) was built from the baseline and
updated commits using Go 1.27.1 with the following flags. Build VCS metadata was
disabled and paths were trimmed for both builds.

```sh
go build -C internal/apptest -trimpath -buildvcs=false -ldflags='-s -w' -o /tmp/testsrv ./testsrv
```

| Build | Bytes | SHA-256 |
| --- | ---: | --- |
| baseline | 41,242,887 | `4b39a41a77148057da1037032516548055f112561903f85d64d7f489c3ce9fd3` |
| updated | 41,369,863 | `ed8020b24571b389f38438456fe90f3507682b4588596ac27307570d35ffe8b7` |

Increase: **126,976 bytes (124 KiB, 0.308%)**. This measures the test application's
linked binary, not the production cr server or per-schema growth. Binaries are not
committed.

## Validation

- Full `scripts/test.sh`: root, application, and AWS KMS module build/vet/tests,
  formatting, and generated Go/TypeScript consistency checks.
- Resource and application Watch tests with the race detector.
- Root Wasm build.
- Standard-CEL differential tests, authorized projection before filtering,
  unchanged source events, lazy unread payloads, and sanitized deep-Any errors.
- The lazy-unread-payload test was also run against the old implementation and
  failed there, demonstrating that it detects the removed eager unpacking.

See the [resource guide](../../guide/resources.md) for the public Watch contract.
