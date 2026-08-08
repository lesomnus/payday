#!/usr/bin/env bash
# Everything, which is more than `go test ./...` reaches.
#
# `internal/apptest` is a Go module of its own -- it owns the ent dependency
# payday itself does not have -- so `./...` at the root does not see it. That is
# not a detail: the app is where nearly every claim payday makes is actually
# demonstrated, and a green run at the root while its tests do not compile is a
# green run that means very little.
#
# CI runs this rather than `go test ./...` for that reason.
set -o errexit
set -o pipefail

__root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${__root}"

echo "== payday"
go build ./...
go vet ./...
go test "$@" ./...

echo "== the app it is tried against"
cd internal/apptest
go build ./...
go vet ./...
go test "$@" ./...
