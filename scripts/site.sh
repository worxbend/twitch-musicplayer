#!/usr/bin/env bash
# Builds and checks the product site the way .github/workflows/pages.yml does.
#   site.sh build OUT   assemble site/ plus the logo and screenshots into OUT (needs cwebp, dwebp)
#   site.sh check OUT   fail on a missing asset, an oversized site, a tuner.js syntax error,
#                       a tuner.js route with no frontend route, or scene radios out of sync
set -euo pipefail

usage() {
  echo "usage: $0 build|check OUT" >&2
  exit 2
}

[ "$#" -eq 2 ] || usage
case "$1" in
  build) mkdir -p "$2" ;;
  check) [ -d "$2" ] || { echo "site.sh: $2 is not a directory; run build first" >&2; exit 1; } ;;
  *) usage ;;
esac
cmd=$1
out=$(cd "$2" && pwd)
cd "$(dirname "$0")/.."

# The site reuses the README screenshots and logo instead of keeping copies.
# The source logo is 2048px; the page shows it at 40px, so ship an 80px copy.
build() {
  mkdir -p "$out/shots"
  cp site/index.html site/styles.css site/tuner.js "$out/"
  cwebp -quiet -lossless -resize 80 80 frontend/src/assets/worxbend-logo.png -o "$out/logo.webp"
  dwebp -quiet "$out/logo.webp" -o "$out/logo.png"
  for n in player spectrum bars mirror dots ribbon line lens console; do
    cp "docs/signal-observatory/$n-desktop.png" "$out/shots/"
    cwebp -quiet -q 80 -m 6 "$out/shots/$n-desktop.png" -o "$out/shots/$n-desktop.webp"
  done
}

# Prints "missing site asset" errors and returns 1 when any local reference is absent.
check_assets() {
  local missing=0 html_refs scene_refs ref path
  html_refs=$(grep -oE '(src|srcset|href)="[^"]*"' "$out/index.html" | cut -d'"' -f2)
  scene_refs=$(sed -nE 's/^  ([a-z]+): \{ route:.*/\1/p' "$out/tuner.js" |
    sed -E 's#.*#shots/&-desktop.png shots/&-desktop.webp#')
  for ref in $html_refs $scene_refs; do
    case "$ref" in http://*|https://*|'#'*) continue ;; esac
    path="${ref%%[?#]*}"
    case "$path" in */) path="${path}index.html" ;; esac
    if [ ! -f "$out/$path" ]; then
      echo "::error::missing site asset: $ref"
      missing=1
    fi
  done
  return "$missing"
}

check_size() {
  local size
  size=$(du -sb "$out" | cut -f1)
  echo "site size: $size bytes"
  # About 2.0 MB today (PNG fallbacks are 1.57 MB); the slack absorbs encoder drift.
  if [ "$size" -gt 2500000 ]; then
    echo "::error::site is $size bytes, over the 2500000 byte budget"
    return 1
  fi
}

# Every tuner.js route must open a real page: '/' is the player (FIXED key ''),
# '/<x>' is a scene slug in viz/scenes.ts or a FIXED key in routes.ts.
check_routes() {
  local bad=0 routes known route
  routes=$(sed -nE "s/^  [a-z]+: \{ route: '([^']*)'.*/\1/p" site/tuner.js)
  known=$( {
    sed -nE "s/^ *slug: '([^']+)',?$/\1/p" frontend/src/viz/scenes.ts
    sed -nE "/^const FIXED/,/^\};/s/^  '?([a-z-]*)'?: \{.*/\1/p" frontend/src/routes.ts
  } )
  if [ -z "$routes" ] || [ -z "$known" ]; then
    echo "::error::could not read routes from site/tuner.js, viz/scenes.ts or routes.ts"
    return 1
  fi
  for route in $routes; do
    if ! grep -qxF -- "${route#/}" <<<"$known"; then
      echo "::error::site/tuner.js route $route has no frontend route"
      bad=1
    fi
  done
  return "$bad"
}

# The tuner radios in index.html and the SCENES keys in tuner.js must name the same scenes.
check_radios() {
  local radios scenes only_html only_js
  radios=$(grep -oE 'name="scene" value="[^"]*"' site/index.html | cut -d'"' -f4 | sort -u)
  scenes=$(sed -nE 's/^  ([a-z]+): \{ route:.*/\1/p' site/tuner.js | sort -u)
  only_html=$(comm -23 <(echo "$radios") <(echo "$scenes") | paste -sd ' ' -)
  only_js=$(comm -13 <(echo "$radios") <(echo "$scenes") | paste -sd ' ' -)
  if [ -n "$only_html$only_js" ]; then
    echo "::error::scene radios and tuner.js SCENES differ; only in index.html: ${only_html:-none}; only in tuner.js: ${only_js:-none}"
    return 1
  fi
}

check() {
  local status=0
  check_assets || status=1
  check_size || status=1
  node --check site/tuner.js || status=1
  check_routes || status=1
  check_radios || status=1
  return "$status"
}

if [ "$cmd" = build ]; then build; else check; fi
