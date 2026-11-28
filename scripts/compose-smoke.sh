#!/usr/bin/env bash
# Checks the compose hardening. First, with no containers, it fails when
# docker-compose.hub.yml drifts from docker-compose.yml on any hardening
# field. Then it starts docker-compose.yml with the given images and checks
# the running containers. CI runs it in the docker job; `make docker-smoke`
# runs it locally.
#
# Usage: compose-smoke.sh BACKEND_IMAGE FRONTEND_IMAGE
# Env:   SMOKE_BE_PORT (default 18082), SMOKE_FE_PORT (default 13002): host ports.
# Needs docker compose, jq and curl. Uses its own compose project, so it never
# touches the lofi-radio project, its volumes or its image tags. Removes only
# that project and the temp directory it creates; prints the project logs when
# a check fails.
set -Eeuo pipefail

usage() {
  echo "usage: $0 BACKEND_IMAGE FRONTEND_IMAGE" >&2
  exit 2
}

[[ $# -eq 2 ]] || usage
readonly BE_IMAGE="$1" FE_IMAGE="$2"
readonly BE_PORT="${SMOKE_BE_PORT:-18082}" FE_PORT="${SMOKE_FE_PORT:-13002}"
readonly PROJECT="lofi-compose-smoke-$$"

for port in "$BE_PORT" "$FE_PORT"; do
  [[ "$port" =~ ^[0-9]+$ ]] || { echo "compose-smoke: port '$port' is not a number" >&2; exit 2; }
done
for tool in docker jq curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "compose-smoke: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")/.."

tmp="$(mktemp -d)"
readonly tmp
mkdir "$tmp/music"
export MUSIC_DIR="$tmp/music" BACKEND_PORT="$BE_PORT" FRONTEND_PORT="$FE_PORT"
readonly OVERRIDE="$tmp/override.yml"
# --env-file /dev/null: a developer's .env must not leak into the checks.
compose=(docker compose --env-file /dev/null -p "$PROJECT" -f docker-compose.yml -f "$OVERRIDE")
started=false

cleanup() {
  local status=$?
  if [[ "$started" == true ]]; then
    if [[ $status -ne 0 ]]; then
      echo "--- docker compose logs ($PROJECT)" >&2
      "${compose[@]}" logs >&2 2>&1 || true
    fi
    "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  rm -rf -- "$tmp"
  exit "$status"
}
trap cleanup EXIT
trap 'echo "compose-smoke: check failed at line $LINENO" >&2' ERR

# hardening FILE: the hardening-relevant fields of each service, as sorted JSON.
hardening() {
  docker compose --env-file /dev/null -f "$1" config --format json | jq -S '.services | map_values({
    read_only, tmpfs, cap_drop, security_opt, user, restart,
    healthcheck: (.healthcheck | if . == null then null else {test, interval, timeout, retries, start_period} end),
    volumes: [.volumes[]? | {target, read_only}],
    env: ((.environment // {}) | keys),
    ports: [.ports[]? | .target]
  })'
}

check_parity() {
  hardening docker-compose.yml > "$tmp/local.json"
  hardening docker-compose.hub.yml > "$tmp/hub.json"
  if ! diff -u --label docker-compose.yml --label docker-compose.hub.yml "$tmp/local.json" "$tmp/hub.json"; then
    echo "compose-smoke: docker-compose.hub.yml hardening differs from docker-compose.yml (diff above); keep both in step" >&2
    exit 1
  fi
}

smoke_runtime() {
  local be fe id
  printf 'services:\n  backend:\n    image: "%s"\n  frontend:\n    image: "%s"\n' "$BE_IMAGE" "$FE_IMAGE" > "$OVERRIDE"
  started=true
  "${compose[@]}" up -d --no-build --pull never --wait --wait-timeout 120
  be="$("${compose[@]}" ps -q backend)"
  fe="$("${compose[@]}" ps -q frontend)"
  for id in "$be" "$fe"; do
    test "$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$id")" = true
    docker inspect -f '{{.HostConfig.CapDrop}}' "$id" | grep -w ALL
    test "$(docker inspect -f '{{.State.Health.Status}}' "$id")" = healthy
  done
  docker inspect -f '{{json .HostConfig.SecurityOpt}}' "$fe" | grep -F '"no-new-privileges:true"'
  curl -fsS -o /dev/null "http://127.0.0.1:$FE_PORT/config.js"
}

check_parity
smoke_runtime
echo "compose-smoke: all checks passed"
