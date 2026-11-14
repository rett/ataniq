#!/bin/sh
set -eu
CDPATH=
export CDPATH
cd "$(dirname "$0")/.."
version=$(cat .go-version)
# Accept only a released Go 1.x patch version, with a toolchain-aware minor.
if ! printf '%s\n' "$version" | grep -Eq '^1\.(2[1-9]|[3-9][0-9]|[1-9][0-9]{2,})\.(0|[1-9][0-9]*)$' ||
   [ "$(printf '%s\n' "$version" | wc -l)" -ne 1 ]; then
  echo 'error: invalid Go toolchain pin' >&2
  exit 1
fi
export GOENV=off GOWORK=off GOFLAGS= GO111MODULE=on
export GOPRIVATE= GONOPROXY=none GONOSUMDB=none
record=build/go-toolchain
exact_version() {
  actual=$("$1" version 2>/dev/null) || return 1
  case "$actual" in "go version go${version} "*) return 0 ;; *) return 1 ;; esac
}
select_offline() {
  export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
  selected=$(command -v go 2>/dev/null) || selected=
  if [ -n "$selected" ] && exact_version "$selected"; then return 0; fi
  selected=
  if [ -f "$record" ]; then
    {
      IFS= read -r recorded_pin && IFS= read -r candidate &&
      ! IFS= read -r extra && [ -z "${extra-}" ]
    } < "$record" || return 1
    [ "$recorded_pin" = "$version" ] || return 1
    case "$candidate" in /*/bin/go) ;; *) return 1 ;; esac
    [ -x "$candidate" ] && exact_version "$candidate" || return 1
    selected=$candidate
    return 0
  fi
  return 1
}
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
      if select_offline; then
        printf 'ok: %s\n' "$actual"
      else
        printf 'fail: selected Go %s unavailable or mismatched; run make bootstrap\n' "$version"; status=1
      fi
    fi
    exit "$status"
    ;;
  bootstrap)
    command -v go >/dev/null 2>&1 || { echo 'missing: Go bootstrap driver' >&2; exit 1; }
    export GOTOOLCHAIN="go${version}" GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org
    goroot=$(go env GOROOT)
    case "$goroot" in /*) ;; *) echo 'error: invalid selected GOROOT' >&2; exit 1 ;; esac
    candidate=$goroot/bin/go
    export GOTOOLCHAIN=local
    exact_version "$candidate" || { echo 'error: resolved Go version mismatched' >&2; exit 1; }
    printf '%s\n' "$actual"
    "$candidate" mod download
    mkdir -p build
    tmp=$(mktemp build/.go-toolchain.XXXXXX)
    trap 'rm -f "$tmp"' 0 HUP INT TERM
    printf '%s\n%s\n' "$version" "$candidate" > "$tmp"
    mv -f "$tmp" "$record"
    trap - 0 HUP INT TERM
    ;;
  test|build)
    action=$1
    shift
    select_offline || { echo 'fail: selected Go unavailable or mismatched; run make bootstrap' >&2; exit 1; }
    exec "$selected" "$action" "$@"
    ;;
  *) echo 'error: unknown tool action' >&2; exit 1 ;;
esac
