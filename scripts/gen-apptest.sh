#!/usr/bin/env bash
# Regenerate the app payday is tried against.
#
# It used to be the pipeline written out; it is `pd gen` now, and the pipeline
# is in `internal/pdcli`. What was gained by moving it is not fewer lines -- it
# is that the buf templates are payday's rather than the app's, so a decision
# like `strategy: all` is one an app cannot get wrong by not knowing about it.
#
# This is left as a script because it is what a person types here, and because
# CI runs `go tool pd gen --check internal/apptest` against the same code.
set -o errexit
set -o pipefail

__root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${__root}"

exec go tool pd gen "$@" internal/apptest
