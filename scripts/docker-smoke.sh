#!/usr/bin/env bash
# Smoke-tests built backend and frontend images with the compose hardening
# (read-only root, no capabilities, no-new-privileges). CI runs it in the
# docker and docker-arm64 jobs; `make docker-smoke` runs it locally.
#
# Usage: docker-smoke.sh BACKEND_IMAGE FRONTEND_IMAGE
# Env:   SMOKE_BE_PORT (default 8080), SMOKE_FE_PORT (default 3000): host ports.
# Needs docker, curl and node. Removes only the containers, volume and temp
# directory it creates; prints both container logs when a check fails.
set -Eeuo pipefail

usage() {
  echo "usage: $0 BACKEND_IMAGE FRONTEND_IMAGE" >&2
  exit 2
}

[[ $# -eq 2 ]] || usage
readonly BE_IMAGE="$1" FE_IMAGE="$2"
readonly BE_PORT="${SMOKE_BE_PORT:-8080}" FE_PORT="${SMOKE_FE_PORT:-3000}"
readonly BE_URL="http://127.0.0.1:$BE_PORT" FE_URL="http://127.0.0.1:$FE_PORT"
readonly BE_NAME="lofi-smoke-be-$$" FE_NAME="lofi-smoke-fe-$$"
readonly DATA_VOLUME="lofi-smoke-data-$$"

for port in "$BE_PORT" "$FE_PORT"; do
  [[ "$port" =~ ^[0-9]+$ ]] || { echo "docker-smoke: port '$port' is not a number" >&2; exit 2; }
done
for tool in docker curl node; do
  command -v "$tool" >/dev/null 2>&1 \
    || { echo "docker-smoke: $tool is required (node runs the config.js check)" >&2; exit 1; }
done

tmp="$(mktemp -d)"
readonly tmp

cleanup() {
  local status=$?
  if [[ $status -ne 0 ]]; then
    for name in "$BE_NAME" "$BE_NAME-port" "$FE_NAME"; do
      echo "--- docker logs $name" >&2
      docker logs "$name" >&2 2>&1 || true
    done
  fi
  docker rm -f "$BE_NAME" "$BE_NAME-port" "$FE_NAME" >/dev/null 2>&1 || true
  docker volume rm "$DATA_VOLUME" >/dev/null 2>&1 || true
  rm -rf -- "$tmp"
  exit "$status"
}
trap cleanup EXIT
trap 'echo "docker-smoke: check failed at line $LINENO" >&2' ERR

# wait_healthy NAME: the first health probe runs only after the 15s interval.
wait_healthy() {
  for _ in $(seq 60); do
    test "$(docker inspect -f '{{.State.Health.Status}}' "$1")" = healthy && break
    sleep 1
  done
  test "$(docker inspect -f '{{.State.Health.Status}}' "$1")" = healthy
}

smoke_backend() {
  docker volume create "$DATA_VOLUME" >/dev/null
  # Same hardening as the compose files: read-only root, no capabilities.
  docker run -d --name "$BE_NAME" -p "$BE_PORT:8080" -e BASE_URL="$BE_URL" \
    --read-only --tmpfs /tmp --tmpfs /music -v "$DATA_VOLUME:/data" \
    --cap-drop ALL --security-opt no-new-privileges:true "$BE_IMAGE" >/dev/null
  for _ in $(seq 30); do curl -fsS "$BE_URL/health" && break; sleep 1; done
  curl -fsS "$BE_URL/health"
  test "$(docker exec "$BE_NAME" id -u)" = 10001
  # The Bloom autosave target must be writable by the non-root user.
  docker exec "$BE_NAME" touch /data/probe
  # Every response, errors included, must forbid content sniffing.
  for p in /health /openapi.yaml /no-such-path; do
    curl -sS -D h.txt -o /dev/null "$BE_URL$p"
    grep -i '^x-content-type-options: nosniff' h.txt
  done
  wait_healthy "$BE_NAME"
}

# The image healthcheck must follow PORT, not a hardcoded 8080. No host port:
# only the in-container probe matters here.
smoke_backend_port() {
  docker run -d --name "$BE_NAME-port" -e PORT=9090 -e BASE_URL="$BE_URL" \
    --health-interval 1s --health-start-period 0s \
    --read-only --tmpfs /tmp --tmpfs /music -v "$DATA_VOLUME:/data" \
    --cap-drop ALL --security-opt no-new-privileges:true "$BE_IMAGE" >/dev/null
  wait_healthy "$BE_NAME-port"
  echo "docker-smoke: backend with PORT=9090 is healthy"
}

# Hostile values must stop the hook before nginx starts. Only the hook runs
# here, so a regression fails instead of hanging on nginx.
smoke_frontend_config_hook() {
  local bad
  for bad in "$(printf 'https://x\n";alert(1)//')" 'https://x";alert(1)//' 'https://a</script>' 'radio.example'; do
    if docker run --rm -e BACKEND_URL="$bad" --entrypoint /docker-entrypoint.d/20-lofi-config.sh "$FE_IMAGE" 2>err.txt; then
      echo "accepted hostile BACKEND_URL"
      exit 1
    fi
    grep -F 'invalid BACKEND_URL' err.txt
  done
  test "$(docker run --rm --entrypoint sh "$FE_IMAGE" -c '/docker-entrypoint.d/20-lofi-config.sh >/dev/null && cat /tmp/lofi/config.js')" = 'window.__LOFI_CONFIG__ = {};'
}

smoke_frontend_start() {
  # Same hardening as the compose files. No capabilities: nginx must bind
  # port 80 as uid 101 without them.
  docker run -d --name "$FE_NAME" -p "$FE_PORT:80" -e BACKEND_URL=https://radio.example \
    --read-only --tmpfs /tmp \
    --cap-drop ALL --security-opt no-new-privileges:true "$FE_IMAGE" >/dev/null
  for _ in $(seq 30); do curl -fsS -o /dev/null "$FE_URL/" && break; sleep 1; done
  test "$(docker exec "$FE_NAME" id -u)" = 101
  # The nginx master (pid 1) must not run as root either.
  test "$(docker exec "$FE_NAME" awk '/^Uid:/{print $2}' /proc/1/status)" = 101
  curl -fsS "$FE_URL/" > index.html
  curl -fsS -D headers.txt "$FE_URL/config.js" > config.js
  grep -F 'backendUrl: "https://radio.example"' config.js
  node -e 'const vm=require("vm"),fs=require("fs");const c={window:{}};vm.runInNewContext(fs.readFileSync("config.js","utf8"),c);if(c.window.__LOFI_CONFIG__.backendUrl!=="https://radio.example")process.exit(1)'
  grep -i '^cache-control:.*no-store' headers.txt
}

smoke_frontend_routes() {
  local path asset code
  curl -fsS "$FE_URL/admin" > admin.html
  grep -F '/assets/admin-' admin.html
  if cmp -s index.html admin.html; then echo "/admin served the SPA index"; exit 1; fi
  curl -fsSL "$FE_URL/admin/" > admin-slash.html
  grep -F '/assets/admin-' admin-slash.html
  curl -fsS -o /dev/null "$FE_URL/now-playing"
  # HTML entry points must revalidate; hashed assets stay long-cached.
  for path in / /admin /admin.html /now-playing; do
    curl -fsS -D html-headers.txt -o /dev/null "$FE_URL$path"
    grep -i '^cache-control:.*no-cache' html-headers.txt
  done
  asset="$(grep -o '/assets/[^"]*\.js' index.html | head -n1)"
  test -n "$asset"
  curl -fsS -D asset-headers.txt -o /dev/null "$FE_URL$asset"
  grep -i '^cache-control:.*immutable' asset-headers.txt
  # Every location must forbid content sniffing, including the /admin/
  # redirect itself (no -L).
  for path in / /config.js /admin /admin.html "$asset" /admin/; do
    curl -sS -D nosniff-headers.txt -o /dev/null "$FE_URL$path"
    grep -i '^x-content-type-options: nosniff' nosniff-headers.txt
  done
  # API paths on the frontend port must fail as JSON, not serve the SPA,
  # so a chat bot pointed at the wrong port gets an error.
  for path in /v1 /v1/ /v1/now-playing.txt /ws /ws/ /ws/x; do
    code="$(curl -sS -D api-headers.txt -o api-body.json -w '%{http_code}' "$FE_URL$path")"
    test "$code" = 404
    grep -i '^content-type:.*application/json' api-headers.txt
    grep -i '^x-content-type-options: nosniff' api-headers.txt
    grep -F '"error":"not_found"' api-body.json
  done
  # /wsx only shares a prefix with /ws, so it stays an SPA route.
  for path in / /wsx; do
    curl -fsS -D root-headers.txt -o /dev/null "$FE_URL$path"
    grep -i '^content-type:.*text/html' root-headers.txt
  done
}

cd "$tmp"
smoke_backend
smoke_backend_port
smoke_frontend_start
smoke_frontend_config_hook
smoke_frontend_routes
wait_healthy "$FE_NAME"
echo "docker-smoke: all checks passed"
