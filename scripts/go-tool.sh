#!/bin/sh
set -eu
CDPATH=
export CDPATH
cd "$(dirname "$0")/.."
version=$(cat .go-version)
# Accept only a released Go 1.x patch version, with a toolchain-aware minor.
# Without grep the pin cannot be checked: doctor reports that and stops before
# it runs Go; every other action refuses to continue.
if command -v grep >/dev/null 2>&1; then
  if ! printf '%s\n' "$version" | grep -Eq '^1\.(2[1-9]|[3-9][0-9]|[1-9][0-9]{2,})\.(0|[1-9][0-9]*)$' ||
     [ "$(printf '%s\n' "$version" | wc -l)" -ne 1 ]; then
    echo 'error: invalid Go toolchain pin' >&2
    exit 1
  fi
elif [ "${1-}" != doctor ]; then
  echo 'missing: grep' >&2
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
show_diag() {
  # Downloading is impossible offline, so Go's progress lines only mislead.
  shown=$(printf '%s\n' "$1" | grep -v '^go: downloading ' || :)
  if [ -z "$shown" ]; then
    printf '  (no diagnostic from go)\n'
    return 0
  fi
  printf '%s\n' "$shown" |
    while IFS= read -r line; do printf '  %s\n' "$line"; done || :
}
case "${1-}" in
  doctor)
    status=0
    for tool in git make sh grep go; do
      if command -v "$tool" >/dev/null 2>&1; then
        printf 'ok: %s available\n' "$tool"
      else
        printf 'missing: %s\n' "$tool"
        status=1
      fi
    done
    if command -v go >/dev/null 2>&1 && command -v grep >/dev/null 2>&1; then
      if select_offline; then
        printf 'ok: %s\n' "$actual"
        # Every package and test of this module must load offline: each module
        # they need is in the module cache and its hash matches go.sum.
        # A cached module must also be unchanged since it was downloaded.
        # The Go diagnostic is shown, indented, so the module is named.
        if ! diag=$("$selected" list -deps -test ./... 2>&1 >/dev/null); then
          show_diag "$diag"
          printf 'fail: packages or module dependencies do not load offline (diagnostic above); for a missing module run make bootstrap, for a checksum mismatch restore go.sum, or run chmod -R u+w on the named module directory in the module cache, remove it and run make bootstrap; otherwise fix the package named above\n'; status=1
        elif ! diag=$("$selected" mod verify 2>&1); then
          show_diag "$diag"
          printf 'fail: cached module dependencies modified; remove the named module from the module cache (its files are read-only; run chmod -R u+w on it first, or use go clean -modcache), then run make bootstrap\n'; status=1
        else
          printf 'ok: module dependencies available and unmodified offline\n'
        fi
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
