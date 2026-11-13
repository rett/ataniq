#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
version=$(cat .go-version)
case "$version" in
  *[!0-9.]*|'') echo 'error: invalid Go toolchain pin' >&2; exit 1 ;;
esac
export GOENV=off GOWORK=off
export GOTOOLCHAIN="go${version}+auto"
case "${1-}" in
  doctor)
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
      actual=$(GOPROXY=off GOSUMDB=off go version 2>/dev/null) || actual=
      case "$actual" in
        "go version go${version} "*) printf 'ok: %s\n' "$actual" ;;
        *) printf 'fail: selected Go %s unavailable or mismatched\n' "$version"; status=1 ;;
      esac
    fi
    exit "$status"
    ;;
  bootstrap)
    command -v go >/dev/null 2>&1 || { echo 'missing: Go bootstrap driver' >&2; exit 1; }
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
