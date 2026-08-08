#!/usr/bin/env bash
# Regenerate the app payday is tried against.
#
# It is what `pd gen` will be. Written out here so that the order is visible
# while it is still moving: the service contracts are generated from the
# entities, merged with whatever an app wrote by hand, and everything else is
# generated from the result.
set -o errexit
set -o pipefail

__root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${__root}"

APP="internal/apptest"
MODULE="github.com/lesomnus/payday/internal/apptest"

# 0. payday's own entities, merged with whatever this app added to them.
#
# They are copied into the app rather than imported because everything
# generated from them -- the messages, the ent schema, the servers -- has to be
# one set of types in one module. `go_package` is rewritten for the same
# reason: it is the app's module that all of this lands in.
#
# An overlay may only add. `pd gen` will refuse one that redeclares a number
# payday owns, since protobuf-merge takes the overlay's word and `alias` would
# quietly become whatever it said.
PD_SCHEMA="$(go list -m -f '{{.Dir}}' github.com/lesomnus/payday 2>/dev/null || echo .)/schema/payday"
mkdir -p "${APP}/proto/payday"
for f in "${PD_SCHEMA}"/{tenant,holder,audit}.proto; do
	name="$(basename "$f")"
	ext="${APP}/proto.ext/payday/${name%.proto}.ext.proto"
	out="${APP}/proto/payday/${name}"

	if [ -f "${ext}" ]; then
		echo "merge : payday/${name}  +  $(basename "${ext}")"
		go tool protobuf-merge "$f" "${ext}" >"${out}"
	else
		cp "$f" "${out}"
	fi

	sed -i "s|^option go_package = .*|option go_package = \"${MODULE}\";|" "${out}"
done

# 1. Service contracts from the entities, and payday's additions to them.
rm -rf "${APP}/proto.svc" "${APP}/proto.pd" "${APP}"/proto/app/*_svc.proto "${APP}"/proto/payday/*_svc.proto
buf generate --template buf.gen.apptest.svc.yaml

# 2. Merge each contract with what payday adds and with whatever the app wrote
#    by hand, in that order: payday's is generated from the declaration, the
#    app's is the last word.
for f in $(find "${APP}/proto.svc" -name '*_svc.g.proto'); do
	rel="${f#"${APP}"/proto.svc/}"
	out="${APP}/proto/${rel%.g.proto}.proto"
	pd="${APP}/proto.pd/${rel%_svc.g.proto}_svc.pd.proto"
	ext="${APP}/proto.ext/${rel%_svc.g.proto}_svc.ext.proto"

	tmp="$(mktemp)"
	cp "$f" "$tmp"
	for overlay in "$pd" "$ext"; do
		if [ -f "$overlay" ]; then
			echo "merge : ${rel}  +  $(basename "$overlay")"
			go tool protobuf-merge "$tmp" "$overlay" >"${tmp}.next"
			mv "${tmp}.next" "$tmp"
		fi
	done

	sed -E 's|(import ")([^"]*)_svc\.g\.proto(";)|\1\2_svc.proto\3|' "$tmp" >"$out"
	rm -f "$tmp"
done

# 3. Messages, stubs, query helpers, ent schema, the servers, and what payday
#    makes of the declarations.
rm -rf .gen
buf generate --template buf.gen.apptest.yaml

rm -rf "${APP}/ent/schema" "${APP}/server/bare" "${APP}/server/pd"
find "${APP}" -maxdepth 1 -name '*.g.go' -delete
find "${APP}" -maxdepth 1 -name '*.pb.go' -delete
find "${APP}/ent" -maxdepth 1 -name '*.g.go' -delete 2>/dev/null || true
cp -r .gen/. "${APP}/"
rm -rf .gen

# 4. The ent runtime for the schema that just changed.
cd "${APP}"
go tool ent generate ./ent/schema \
	--target ./ent \
	--feature sql/modifier \
	--feature sql/versioned-migration

echo "Done."
