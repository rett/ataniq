#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
version=$(cat .go-version)
# Accept only a released Go 1.x patch version, with a toolchain-aware minor.
if ! printf '%s\n' "$version" | grep -Eq '^1\.(2[1-9]|[3-9][0-9]|[1-9][0-9]{2,})\.(0|[1-9][0-9]*)$' ||
   [ "$(printf '%s\n' "$version" | wc -l)" -ne 1 ]; then
  echo 'error: invalid Go toolchain pin' >&2
  exit 1
fi
export GOENV=off GOWORK=off GOFLAGS=
export GOPRIVATE= GONOPROXY=none GONOSUMDB=none
export GOTOOLCHAIN="go${version}"
case "${1-}" in
  doctor)
    export GOPROXY=off GOSUMDB=off
    status=0
    for tool in git make sh go; do
      if command -v "$tool" >/dev/null 2>&1; then
        printf 'ok: %s available\n' "$tool"
      else
        printf 'missing: %s\n' "$tool"
        status=1
      fi
    done
    if command -v go >/dev/null 2>&1; then
      actual=$(go version 2>/dev/null) || actual=
      case "$actual" in
        "go version go${version} "*) printf 'ok: %s\n' "$actual" ;;
        *) printf 'fail: selected Go %s unavailable or mismatched\n' "$version"; status=1 ;;
      esac
    fi
    exit "$status"
    ;;
  bootstrap)
    command -v go >/dev/null 2>&1 || { echo 'missing: Go bootstrap driver' >&2; exit 1; }
    export GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org
    go version
    go mod download
    ;;
  test)
    shift
    export GOPROXY=off GOSUMDB=off
    exec go test "$@"
    ;;
  *) echo 'error: unknown tool action' >&2; exit 1 ;;
esac
