#!/usr/bin/env bash
# Lints GitHub workflows and shell scripts. Needs go and shellcheck 0.11.0 on
# PATH; actionlint also runs shellcheck over every workflow `run:` block.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
  echo "lint: go is required to run actionlint" >&2
  exit 1
fi
if ! command -v shellcheck >/dev/null 2>&1; then
  echo "lint: shellcheck 0.11.0 is required; install it from your package manager or https://github.com/koalaman/shellcheck/releases" >&2
  exit 1
fi

shellcheck -S warning scripts/*.sh
# The nginx image runs this hook under /bin/sh.
shellcheck -S warning -s sh frontend/docker-entrypoint.d/*.sh
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -color
