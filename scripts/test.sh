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

# Formatting, which `go vet` does not read and so sat wrong for a while. It is
# first because it is the cheapest thing here and the only one whose answer is
# the same on every machine.
echo "== gofmt"
if v="$(gofmt -l . | grep -v node_modules || true)"; [ -n "${v}" ]; then
	echo "not gofmt'd:" >&2
	echo "${v}" >&2
	echo >&2
	echo "    gofmt -w ${v}" >&2
	exit 1
fi

# Each module's go.mod, read on its own. go.work makes the three one build, in
# which the highest version any of them asks for wins -- so a go.mod can name
# an old payday, old versions with known vulnerabilities, or no module at all
# for a package it imports, and every build below still passes. An app gets
# payday's go.mod alone, and a scanner reads each one alone; payday's lacked
# protobuf-patch, which `audit` imports, and apptest's named the payday of
# 1 September.
echo "== each go.mod, without go.work"
for m in . internal/apptest trail/trailawskms; do
	if ! (cd "${__root}/${m}" && GOWORK=off go mod tidy -diff); then
		echo "${m}/go.mod is not what that module builds with on its own:" >&2
		echo >&2
		echo "    (cd ${m} && GOWORK=off go mod tidy)" >&2
		exit 1
	fi
done

echo "== payday"
go build ./...
go vet ./...
go test "$@" ./...

echo "== the app it is tried against"
cd internal/apptest
go build ./...
go vet ./...
go test "$@" ./...

# And the KMS signer, a module of its own so that the AWS SDK is in no go.mod
# but its own (#52): `./...` at the root does not see it either.
echo "== the KMS signer"
cd "${__root}/trail/trailawskms"
go build ./...
go vet ./...
go test "$@" ./...

# And that the app's generated half is what its schema says.
#
# Here rather than only in CI, which is where it was and which is one push too
# late. A generated file that was not regenerated **compiles perfectly and is
# wrong**, so everything above passes while the thing this whole module exists
# to demonstrate is a schema behind -- twice in one afternoon, both times found
# by a red CI run after a green local one.
#
# `--ts` because that is what CI runs, and the check without it passes while
# `ts/gen` is a schema behind. It needs the plugin, so a checkout with no
# `node_modules` is told to install rather than told the TypeScript is fine.
cd "${__root}"
echo "== and its generated half"
if [ ! -x internal/apptest/ts/node_modules/.bin/protoc-gen-es ]; then
	echo "no protoc-gen-es, so the TypeScript half cannot be checked:" >&2
	echo >&2
	echo "    npm ci --prefix internal/apptest/ts" >&2
	exit 1
fi
go tool pd gen --check --ts internal/apptest
