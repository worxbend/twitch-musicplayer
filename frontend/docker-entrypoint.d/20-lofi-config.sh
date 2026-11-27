#!/bin/sh
# Writes the runtime backend configuration read by index.html and admin.html.
#
# Vite inlines VITE_* variables at build time, so a published image would
# otherwise be permanently pinned to whatever backend URL it was built against.
# Generating this file at container start lets one image serve any deployment.
#
# BACKEND_URL unset or empty is the normal case: the page then falls back to the
# host that served it on port 8080, which is what the Compose layout provides.
#
# A non-empty BACKEND_URL must be an absolute http(s) URL made of RFC 3986
# characters only. Anything else is rejected and no file is written, so the
# container fails fast instead of serving a config.js that does not parse or
# carries injected JavaScript. LOFI_CONFIG_TARGET exists for tests.
#
# The default target sits under /tmp so the container can run with a read-only
# root filesystem and a /tmp tmpfs; nginx serves /config.js from there.
set -eu

target="${LOFI_CONFIG_TARGET:-/tmp/lofi/config.js}"
backend_url="${BACKEND_URL:-}"

# Allow-list check: with no quote, backslash, angle bracket, whitespace or
# control character possible, the value is safe inside a JS string literal.
# The pattern lives in a variable because busybox ash mishandles backslash
# escapes inside a literal bracket expression; `]` first and `-` last are the
# POSIX way to make them literal.
non_url_char='*[!]A-Za-z0-9._~:/?#@!$&()*+,;=%[-]*'
valid_backend_url() {
    case "$1" in
        http://?*|https://?*) ;;
        *) return 1 ;;
    esac
    # shellcheck disable=SC2254 # unquoted on purpose: it is a pattern
    case "$1" in
        $non_url_char) return 1 ;;
    esac
}

if [ -n "$backend_url" ] && ! valid_backend_url "$backend_url"; then
    echo 'lofi-config: invalid BACKEND_URL (must be an absolute http(s) URL without quotes, spaces, angle brackets or control characters)' >&2
    exit 1
fi

mkdir -p "$(dirname "$target")"
if [ -n "$backend_url" ]; then
    printf 'window.__LOFI_CONFIG__ = { backendUrl: "%s" };\n' "$backend_url" > "$target"
    echo "lofi-config: backend URL set to $backend_url"
else
    printf 'window.__LOFI_CONFIG__ = {};\n' > "$target"
    echo "lofi-config: no BACKEND_URL set, falling back to <page host>:8080"
fi
