#!/usr/bin/env bash
# Fails when the Docker build images or CI drift from the pinned toolchains:
# Go major.minor (backend/go.mod toolchain vs backend/Dockerfile and every CI
# setup-go version) and Node major (frontend/.nvmrc vs frontend/Dockerfile,
# @types/node and package.json engines.node). Versions named in the README and
# developer docs must match too, as must the README TypeScript badge.
# Patch versions may differ so Dependabot patch bumps stay green.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

status=0

# first_from FILE: image reference of the first FROM line, skipping flags such
# as --platform=$BUILDPLATFORM.
first_from() {
  { grep -m1 -E '^FROM[[:space:]]' "$1" || true; } \
    | sed -E 's/^FROM[[:space:]]+(--[^[:space:]]+[[:space:]]+)*([^[:space:]]+).*/\2/'
}

# compare LABEL LEFT_FILE LEFT_VALUE RIGHT_FILE RIGHT_VALUE
compare() {
  if [[ -z "$3" || -z "$5" || "$3" != "$5" ]]; then
    echo "MISMATCH $1: $2: '$3' vs $4: '$5'" >&2
    status=1
  else
    echo "ok $1: $2 $3 = $4 $5"
  fi
}

# check_prose LABEL WANT_FILE WANT REGEX FILE...: every REGEX match in each FILE
# must end in WANT. REGEX matches "<name><sep><version>"; the version is the
# text after the last space or hyphen. A FILE with no match fails, so rewording
# the docs cannot silently skip the check.
check_prose() {
  local label=$1 want_file=$2 want=$3 regex=$4 file line_no match found
  shift 4
  for file in "$@"; do
    found=0
    while IFS=: read -r line_no match; do
      [[ -n "$line_no" ]] || continue
      found=1
      compare "$label" "$want_file" "$want" "$file:$line_no" "${match##*[ -]}"
    done < <(grep -noE "$regex" "$file" || true)
    if (( found == 0 )); then
      echo "MISMATCH $label: $file: no version found" >&2
      status=1
    fi
  done
}

go_mod=$(grep -m1 -E '^toolchain go[0-9]' backend/go.mod \
  | sed -E 's/^toolchain go([0-9]+\.[0-9]+).*/\1/' || true)
go_image=$(first_from backend/Dockerfile \
  | sed -nE 's/^golang:([0-9]+\.[0-9]+).*/\1/p')
compare "go major.minor" backend/go.mod "$go_mod" backend/Dockerfile "$go_image"

# Every setup-go in CI (the backend and lint jobs each pin one).
ci_go_lines=$(grep -n -E '^[[:space:]]*go-version:' .github/workflows/ci.yml || true)
if [[ -z "$ci_go_lines" ]]; then
  echo "MISMATCH go major.minor: no go-version: line in .github/workflows/ci.yml" >&2
  status=1
fi
while IFS=: read -r line_no line; do
  [[ -n "$line_no" ]] || continue
  ci_go=$(sed -E "s/.*go-version:[[:space:]]*['\"]?([0-9]+\.[0-9]+).*/\1/" <<<"$line")
  compare "go major.minor" backend/go.mod "$go_mod" ".github/workflows/ci.yml:$line_no" "$ci_go"
done <<<"$ci_go_lines"

nvmrc=$(sed -nE '1s/^v?([0-9]+).*/\1/p' frontend/.nvmrc)
node_image=$(first_from frontend/Dockerfile \
  | sed -nE 's/^node:([0-9]+).*/\1/p')
types_node=$(grep -m1 -E '"@types/node"' frontend/package.json \
  | sed -E 's/.*"@types\/node"[[:space:]]*:[[:space:]]*"[~^]?([0-9]+).*/\1/' || true)
engines_node=$(grep -m1 -E '"node"[[:space:]]*:' frontend/package.json \
  | sed -E 's/.*"node"[[:space:]]*:[[:space:]]*"[^0-9]*([0-9]+).*/\1/' || true)
compare "node major" frontend/.nvmrc "$nvmrc" frontend/Dockerfile "$node_image"
compare "node major" frontend/.nvmrc "$nvmrc" frontend/package.json@types/node "$types_node"
compare "node major" frontend/.nvmrc "$nvmrc" frontend/package.json:engines.node "$engines_node"

check_prose "go major.minor" backend/go.mod "$go_mod" '\bGo[ -]1\.[0-9]+' \
  README.md docs/developer-guide.md backend/README.md
# \b skips AnalyserNode; the separator skips `node --test`.
check_prose "node major" frontend/.nvmrc "$nvmrc" '\bNode(\.js)?[ -][0-9]+' \
  README.md docs/developer-guide.md

typescript=$(grep -m1 -E '"typescript"[[:space:]]*:' frontend/package.json \
  | sed -E 's/.*"typescript"[[:space:]]*:[[:space:]]*"[^0-9]*([0-9]+).*/\1/' || true)
check_prose "typescript major" frontend/package.json "$typescript" '\bTypeScript-[0-9]+' \
  README.md

exit "$status"
