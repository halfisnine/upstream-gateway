#!/bin/sh
set -eu
image=${1:?Usage: smoke-image.sh IMAGE}
container_id=
cleanup() {
  if [ -n "$container_id" ]; then docker rm -f "$container_id" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT HUP INT TERM
docker run --rm --network=none --read-only --cap-drop ALL "$image" --version
container_id=$(docker run -d --read-only --cap-drop ALL --security-opt no-new-privileges \
  --memory=64m --cpus=1 -e GATEWAY_ADMIN_LISTEN=0.0.0.0:9090 \
  -p 127.0.0.1::9090 "$image")
port=$(docker inspect --format '{{(index (index .NetworkSettings.Ports "9090/tcp") 0).HostPort}}' "$container_id")
base="http://127.0.0.1:$port"
ready=false
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if curl -fsS --max-time 2 "$base/healthz" >/dev/null; then ready=true; break; fi
  sleep 1
done
if [ "$ready" != true ]; then docker logs "$container_id"; exit 1; fi
test "$(curl -sS --max-time 3 -o /dev/null -w '%{http_code}' "$base/readyz")" = 503
test "$(curl -sS --max-time 3 -X POST -o /dev/null -w '%{http_code}' "$base/v1/chat/completions")" = 404
docker stop --time 10 "$container_id" >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$container_id")" = 0
printf 'Image smoke check passed: liveness, not-ready, no model route, graceful shutdown.\n'
