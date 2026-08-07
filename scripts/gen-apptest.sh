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

# 1. Service contracts from the entities.
rm -rf "${APP}/proto.svc" "${APP}"/proto/app/*_svc.proto
buf generate --template buf.gen.apptest.svc.yaml

# 2. Merge each with its overlay, if it has one. Nothing here has one yet, so
#    this is a copy with the imports pointed at the merged names.
for f in $(find "${APP}/proto.svc" -name '*_svc.g.proto'); do
	out="${APP}/proto/${f#"${APP}"/proto.svc/}"
	out="${out%.g.proto}.proto"
	sed -E 's|(import ")([^"]*)_svc\.g\.proto(";)|\1\2_svc.proto\3|' "$f" >"$out"
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
