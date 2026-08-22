#!/usr/bin/env bash
# The suite, on the database an app is deployed on rather than the one it is
# tested on.
#
# Everything payday generates is SQL, and SQLite -- what a test gets with
# nothing in the environment -- is permissive in exactly the directions that
# hide a mistake. Twice now a column declared NOT NULL has been taking a nil
# `[]byte` since the day it was written, because the SQLite driver stores that
# as an empty blob and pgx sends SQL NULL: ten tests the first time, every write
# to a global entity the second. The long version is on `pdtest.Postgres`.
#
# Both times CI found it, which is one push later than a desk would have. What
# stood in the way of the desk was knowing a variable, a DSN, and where to get a
# server; this is those three. It provides a database and then runs whatever it
# was handed, so the run is the ordinary run:
#
#     ./scripts/with-postgres.sh                     # the gate, both modules
#     ./scripts/with-postgres.sh go test ./pdtest/...
#
# A second copy of `scripts/test.sh` with a DSN in front of it would have been
# shorter to write and would have started drifting from the first one the day
# after.
#
# `PDTEST_POSTGRES` already set is used as it stands -- somebody with a database
# of their own is not given one. Otherwise a container is started on whatever
# Docker engine is reachable and removed on the way out, and with no engine
# either this stops rather than falling back to SQLite, which would be the same
# green that proves nothing this exists to prevent.
#
# The engine is spoken to over its HTTP API rather than through the `docker`
# CLI, which is itself a client of that API: two ways to start a container is
# one way that gets run and one that rots, and the machine that most wants this
# -- a dev container against an engine outside it -- is the one with no CLI in
# it. The CLI is asked one question where it happens to be installed, which is
# where its engine is: a context (Desktop, colima, podman) points it somewhere
# `DOCKER_HOST` does not name. What the engine answers is JSON, and `python3`
# reads it, because a `grep` for a field is parsing that works until the field
# moves.
set -o errexit
set -o pipefail

__root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${__root}"

# The gate, because that is the thing worth running on a real database and it is
# the one command that reaches both modules. `internal/apptest` is a module of
# its own, so `go test ./...` at the root does not see it -- and the root module
# holds tests, `pdtest`'s own among them, that only mean something against a
# server. CI's postgres job runs both for the same reason; this is that, here.
if [ $# -eq 0 ]; then
	set -- ./scripts/test.sh
fi

if [ -n "${PDTEST_POSTGRES:-}" ]; then
	echo "== postgres, the one PDTEST_POSTGRES names"
	exec "$@"
fi

for __need in curl python3; do
	if ! command -v "${__need}" >/dev/null 2>&1; then
		echo "no ${__need}, so no engine can be asked for a database." >&2
		echo "name one instead:" >&2
		echo >&2
		echo "    PDTEST_POSTGRES='postgres://app:app@127.0.0.1:5432/app?sslmode=disable' $0 $*" >&2
		exit 1
	fi
done

# Where the engine is. `DOCKER_HOST` first because it is what overrides
# everything for the CLI too, then the CLI's own answer if it is here, then the
# socket that is there when nobody has said otherwise.
__endpoint="${DOCKER_HOST:-}"
if [ -z "${__endpoint}" ] && command -v docker >/dev/null 2>&1; then
	__endpoint="$(docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null || true)"
fi
if [ -z "${__endpoint}" ]; then
	__endpoint="unix:///var/run/docker.sock"
fi

# `__engine` is how curl reaches it and `__host` is where its published ports
# are, which are not the same question: an engine named by `DOCKER_HOST` is
# somewhere else, and a port published there is not on this machine's loopback.
# Getting that wrong is a connection refused at the first test rather than here.
__engine=()
case "${__endpoint}" in
unix://*)
	__engine=(--unix-socket "${__endpoint#unix://}")
	__api="http://docker"
	__host="127.0.0.1"
	;;
tcp://* | http://* | https://*)
	__authority="${__endpoint#*://}"
	__authority="${__authority%%/*}"
	case "${__endpoint}" in
	https://*) __api="https://${__authority}" ;;
	*) __api="http://${__authority}" ;;
	esac
	__host="${__authority%%:*}"
	;;
*)
	echo "DOCKER_HOST=${__endpoint} is a kind of endpoint this cannot speak to." >&2
	echo "name a database instead:" >&2
	echo >&2
	echo "    PDTEST_POSTGRES='postgres://app:app@127.0.0.1:5432/app?sslmode=disable' $0 $*" >&2
	exit 1
	;;
esac

__tmp="$(mktemp -d)"
__container=""

# Removed however this ends, including a failing run -- the engine is shared
# with whatever else is on this machine, and a container left behind holds its
# published port. `v=1` because the image declares a volume for its data
# directory, and a forgotten anonymous volume is the same leak more slowly.
__clean() {
	local status=$?
	if [ -n "${__container}" ]; then
		__request DELETE "/containers/${__container}?force=1&v=1" >/dev/null || true
	fi
	rm -rf "${__tmp}"
	return ${status}
}

# `curl` answers with the HTTP status and leaves the body in a file, because
# every call here wants both and an engine that says 404 says it with a body
# worth printing.
__request() {
	local method="$1" path="$2"
	shift 2
	curl "${__engine[@]}" \
		--silent --show-error --request "${method}" \
		--connect-timeout 5 --max-time 60 \
		--output "${__tmp}/body" --write-out '%{http_code}' \
		"${__api}${path}" "$@"
}

__body() {
	cat "${__tmp}/body"
}

trap '__clean' EXIT
# So that an interrupt leaves through the same door and the container goes with
# it; an untrapped signal kills the shell without running the EXIT trap.
trap 'exit 130' INT TERM

if ! __request GET /_ping >/dev/null 2>&1; then
	echo "no database, and no Docker engine at ${__endpoint} to start one on." >&2
	echo >&2
	echo "PostgreSQL is not optional here: on SQLite this suite passes without" >&2
	echo "ever issuing the statements it will issue in production. Name a server" >&2
	echo >&2
	echo "    PDTEST_POSTGRES='postgres://app:app@127.0.0.1:5432/app?sslmode=disable' $0 $*" >&2
	echo >&2
	echo "or make an engine reachable -- DOCKER_HOST, or /var/run/docker.sock --" >&2
	echo "and this starts one itself." >&2
	exit 1
fi

# Named for what it is and for the process that owns it, since the engine is
# shared: two runs at once are two databases, and one that outlives its run says
# which shell to blame.
__container="payday-pdtest-${$}"

# `postgres:17` is the image CI's postgres job uses, and matching it is the
# point of the exercise -- the alpine variant is a smaller pull whose initdb
# lands in the C locale, which sorts text differently from the en_US.utf8 this
# one is built with, so a disagreement about ORDER BY would be the image rather
# than the code.
#
# The durability settings are off because this database lives for one run and is
# deleted: a schema per test is DDL-heavy, and fsync buys a crash recovery
# nobody will ever perform.
#
# The health check is the whole reason this is not `docker run` and a guess: the
# container is running well before the database accepts, and without waiting the
# first test is what finds out. `-h 127.0.0.1` rather than a bare `pg_isready`,
# because the entrypoint runs an initdb pass with `listen_addresses=''` that a
# unix-socket probe answers for -- healthy while the port is still shut. Many
# retries, because the probe goes on failing for as long as initdb does and
# `unhealthy` is a verdict rather than a wait; the deadline below is the bound
# that matters.
#
# `Tty` so that the log tail printed when it never comes up is text; the engine
# multiplexes the stream otherwise, and a hexdump helps nobody.
cat >"${__tmp}/create.json" <<'JSON'
{
	"Image": "postgres:17",
	"Env": ["POSTGRES_USER=app", "POSTGRES_PASSWORD=app", "POSTGRES_DB=app"],
	"Cmd": ["postgres", "-c", "fsync=off", "-c", "synchronous_commit=off"],
	"Tty": true,
	"Healthcheck": {
		"Test": ["CMD-SHELL", "pg_isready -h 127.0.0.1 -U app -d app"],
		"Interval": 500000000,
		"Timeout": 2000000000,
		"Retries": 120
	},
	"HostConfig": {"PublishAllPorts": true}
}
JSON

__create() {
	__request POST "/containers/create?name=${__container}" \
		--header 'Content-Type: application/json' \
		--data-binary "@${__tmp}/create.json"
}

echo "== postgres in ${__container}"
__code="$(__create)"
if [ "${__code}" = 404 ]; then
	# The one call that is slow, and only ever on a machine that has not run
	# this before.
	echo "-- pulling postgres:17"
	__request POST "/images/create?fromImage=postgres&tag=17" --max-time 900 >/dev/null
	__code="$(__create)"
fi
if [ "${__code}" != 201 ]; then
	echo "create ${__container}: ${__code}" >&2
	__body >&2
	exit 1
fi

__code="$(__request POST "/containers/${__container}/start")"
if [ "${__code}" != 204 ]; then
	echo "start ${__container}: ${__code}" >&2
	__body >&2
	exit 1
fi

__died() {
	echo "${__container} never accepted connections: $1" >&2
	__request GET "/containers/${__container}/logs?stdout=1&stderr=1&tail=40" >/dev/null || true
	__body >&2
	exit 1
}

__deadline=$((SECONDS + 120))
while :; do
	__code="$(__request GET "/containers/${__container}/json")"
	if [ "${__code}" != 200 ]; then
		__died "the engine says ${__code}"
	fi

	# Both, because they fail differently: a container whose entrypoint
	# rejects its own configuration exits while its health is still
	# "starting", and reading only the health would wait out the deadline for
	# something that is already over.
	read -r __status __health < <(python3 -c 'import json,sys; s=json.load(sys.stdin)["State"]; print(s["Status"], s.get("Health", {}).get("Status", "unchecked"))' <"${__tmp}/body")
	if [ "${__status}" != running ]; then
		__died "it is ${__status}"
	fi
	case "${__health}" in
	healthy) break ;;
	starting) ;;
	*) __died "it is ${__health}" ;;
	esac

	if [ "${SECONDS}" -ge "${__deadline}" ]; then
		__died "gave up waiting"
	fi

	sleep 0.5
done

# Read back rather than asked for. A port chosen here is a port something else
# on a shared engine may already hold, and the engine is the only one that knows
# which one it gave out.
__port="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["NetworkSettings"]["Ports"]["5432/tcp"][0]["HostPort"])' <"${__tmp}/body")"

export PDTEST_POSTGRES="postgres://app:app@${__host}:${__port}/app?sslmode=disable"
echo "== ${PDTEST_POSTGRES}"

# Not `exec`, because the container has to be removed after it.
"$@"
