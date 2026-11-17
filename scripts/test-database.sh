#!/bin/sh
# Run real PostgreSQL integration tests with ephemeral, project-owned resources.
set -eu
image=${1:?Usage: test-database.sh TEST_STAGE_IMAGE}
postgres_image=${GATEWAY_POSTGRES_IMAGE:-postgres@sha256:665dc7b37ac52a17e298511414c21d23ffa4e6a6ef6225b3e5d59c7afb35e994}
network="upstream-gateway-test-$$"
database="${network}-db"
private_dir=$(mktemp -d)
cleanup() {
  docker rm -f -v "$database" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  rm -rf "$private_dir"
}
trap cleanup EXIT HUP INT TERM
python3 - "$private_dir" <<'PY'
import pathlib, secrets, sys
p=pathlib.Path(sys.argv[1]); password=secrets.token_hex(32)
(p/'postgres.env').write_text('POSTGRES_DB=gateway_test\nPOSTGRES_USER=gateway_test\nPOSTGRES_PASSWORD='+password+'\n')
(p/'database-url').write_text('postgres://gateway_test:'+password+'@database:5432/gateway_test?sslmode=disable\n')
PY
docker network create --internal "$network" >/dev/null
docker run -d --name "$database" --network "$network" --network-alias database \
 --cpus=1 --memory=256m --memory-swap=256m \
 --env-file "$private_dir/postgres.env" --tmpfs /var/lib/postgresql/data:rw \
 "$postgres_image" >/dev/null
for attempt in 1 2 3 4 5 6 7 8 9 10; do
 if docker exec "$database" pg_isready -U gateway_test -d gateway_test >/dev/null 2>&1; then break; fi
 sleep 1
done
docker run --rm --network "$network" --cpus=2 --memory=3g --memory-swap=3g \
 --mount "type=bind,source=$private_dir/database-url,target=/run/test/database-url,readonly" \
 --env GATEWAY_TEST_DATABASE_URL_FILE=/run/test/database-url --env CGO_ENABLED=1 \
 "$image" go test -race -count=1 -timeout=120s ./internal/integration
