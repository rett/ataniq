#!/bin/sh
# host-bootstrap.sh: prepare the authority host to receive releases. Run ON the
# host, as root, once; running it again changes nothing that is already right.
#
#   host-bootstrap.sh [--dry | --apply] --switch-bin FILE --switch-sha256 HEX
#                     [--link-caddy]
#   host-bootstrap.sh --self-test
#
# --dry (the default) prints every step and changes nothing. --apply does the
# steps. Every check runs first, in both modes: a refused step stops the
# script before anything is changed.
#
# What it does (see deploy/authority-host/README.md, "Layout"):
#   - creates /opt/authority-host, /opt/authority-host/releases,
#     /etc/authority-host and /var/lib/authority-host (0755, root:root);
#     /srv/authority/public is not touched;
#   - installs /etc/authority-host/stop.list (0644, root:root) with the one
#     line /usr/bin/true when the file is missing (a list that is there,
#     the owner's, is kept as it is);
#   - does NOT write /etc/authority-host/switch-policy.json: without it the
#     switch runs in manual mode, which the first scratch deploys may use;
#   - installs FILE as /usr/local/sbin/ah-switch (0755, root:root), only when
#     its SHA-256 is the one given with --switch-sha256;
#   - with --link-caddy, makes /etc/caddy/Caddyfile a link to
#     /opt/authority-host/current/etc/caddy/Caddyfile, keeping a regular
#     Caddyfile once as /etc/caddy/Caddyfile.pre-authority-host.
#
# It never starts, restarts, enables or reloads a service, and it opens no
# network connection. Every folder above what it touches must be a real
# folder owned by root and not writable by its group or by others, or it
# refuses. The environment variable ROOT_PREFIX, when set, is put before every
# absolute path (tests use a scratch folder); the folders of the user running
# the script are then accepted as owners, as ah-switch accepts them in tests.
#
# POSIX sh only. Tools: ls, id, mkdir, chmod, chown, cp, mv, ln, rm, cmp, cat,
# readlink, tr, sha256sum (or shasum).
#
# Exit status: 0 done (or dry run complete), 1 refused or failed, 2 usage.

set -eu
umask 022
LC_ALL=C
export LC_ALL

HB_NAME=host-bootstrap.sh
PHASE=
MADE=' '
TMPFILES=

say() { printf '%s\n' "$*"; }
out() { [ "$PHASE" = check ] || say "$*"; }
die() {
  printf '%s: %s\n' "$HB_NAME" "$*" >&2
  exit 1
}
usage() {
  cat >&2 <<EOF
usage: $HB_NAME [--dry | --apply] --switch-bin FILE --switch-sha256 HEX [--link-caddy]
       $HB_NAME --self-test
EOF
  exit 2
}

cleanup() {
  for _f in $TMPFILES; do
    rm -f -- "$_f"
  done
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

# --- pure helpers (the self-test covers these) -------------------------------

# valid_sha256 TEXT succeeds for 64 hex digits.
valid_sha256() {
  case $1 in
    *[!0-9a-fA-F]*) return 1 ;;
  esac
  [ "${#1}" -eq 64 ]
}

# mode_loose MODE succeeds when the mode string of ls -l (drwxr-xr-x) shows a
# write bit for the group or for others.
mode_loose() {
  case $1 in
    ?????w* | ????????w*) return 0 ;;
  esac
  return 1
}

# mode_exec_file MODE succeeds for exactly a regular file with mode 0755.
mode_exec_file() {
  case $1 in
    -rwxr-xr-x*) return 0 ;;
  esac
  return 1
}

stop_list_text() {
  cat <<'EOF'
# The authority host's own emergency stop list, run by ah-switch when no
# healthy release is left. One command per line; no shell runs it.
# This static host has no decision service yet, so the list only succeeds.
# The owner replaces this line when one exists.
/usr/bin/true
EOF
}

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    _h=$(sha256sum -- "$1") || die "cannot hash $1"
  elif command -v shasum >/dev/null 2>&1; then
    _h=$(shasum -a 256 -- "$1") || die "cannot hash $1"
  else
    die "missing: sha256sum"
  fi
  printf '%s\n' "${_h%% *}"
}

# --- inspection ---------------------------------------------------------------

# check_one PATH TYPE: PATH must be of TYPE (d folder, - regular file; a
# symbolic link is never accepted), owned by root (or, under ROOT_PREFIX, by
# the user running this) and not writable by its group or by others.
check_one() {
  _l=$(ls -ldn -- "$1" 2>/dev/null) || die "cannot inspect $1"
  IFS=' ' read -r _m _lk _u _g _rest <<EOF
$_l
EOF
  case $_m in
    l*) die "refused: $1 is a symbolic link" ;;
    "$2"*) ;;
    *) die "refused: $1 is not of the expected kind ($2)" ;;
  esac
  [ "$_u" = 0 ] || { [ -n "$R" ] && [ "$_u" = "$ME" ]; } ||
    die "refused: $1 is not owned by root (owner id $_u)"
  if mode_loose "$_m"; then
    die "refused: $1 is writable by its group or by others ($_m)"
  fi
}

# trust_chain DIR: DIR and every folder above it, up to the root (or up to
# ROOT_PREFIX), pass check_one.
trust_chain() {
  _p=$1
  while :; do
    check_one "$_p" d
    [ "$_p" != "${R:-/}" ] || break
    _p=${_p%/*}
    [ -n "$_p" ] || _p=/
  done
}

# require_dir DIR: an existing, trusted folder.
require_dir() {
  [ ! -L "$1" ] || die "refused: $1 is a symbolic link"
  [ -d "$1" ] || die "refused: $1 must exist as a real folder"
  trust_chain "$1"
}

# --- steps --------------------------------------------------------------------

act() {
  _d=$1
  shift
  case $PHASE in
    check) return 0 ;;
    dry) say "would: $_d" ;;
    do)
      say "do: $_d"
      "$@"
      ;;
  esac
}

chown_root() {
  if [ "$ME" = 0 ]; then
    chown 0:0 -- "$1"
  else
    say "note: owner of $1 left as it is (not run as root, root prefix set)"
  fi
}

do_mkdir() {
  mkdir -- "$1"
  chmod 0755 -- "$1"
  chown_root "$1"
}

# ensure_dir DIR: DIR is a trusted folder, or is made (0755, root:root).
ensure_dir() {
  if [ -e "$1" ] || [ -L "$1" ]; then
    trust_chain "$1"
    out "ok: $1 exists"
    return 0
  fi
  _parent=${1%/*}
  case $MADE in
    *" $_parent "*) ;;
    *) require_dir "$_parent" ;;
  esac
  MADE="$MADE$1 "
  act "create $1 (0755, root:root)" do_mkdir "$1"
}

do_stop_list() {
  _tmp=$ETC/.stop.list.new.$$
  TMPFILES="$TMPFILES $_tmp"
  (
    umask 077
    stop_list_text >"$_tmp"
  )
  chmod 0644 -- "$_tmp"
  chown_root "$_tmp"
  mv -f -- "$_tmp" "$ETC/stop.list"
}

step_stop_list() {
  _f=$ETC/stop.list
  if [ -e "$_f" ] || [ -L "$_f" ]; then
    check_one "$_f" -
    if [ "$(cat -- "$_f")" = "$(stop_list_text)" ]; then
      out "ok: $_f is the default list"
    else
      out "kept: $_f differs from the default; the owner's list stays as it is"
    fi
    return 0
  fi
  act "install $_f (0644, root:root) with the single command /usr/bin/true" do_stop_list
}

do_switch() {
  _tmp=$SBIN/.ah-switch.new.$$
  TMPFILES="$TMPFILES $_tmp"
  cp -- "$SWITCH_BIN" "$_tmp"
  _got=$(hash_file "$_tmp")
  if [ "$_got" != "$SWITCH_SHA" ]; then
    die "refused: the copy of the switch binary has sha256 $_got, not $SWITCH_SHA"
  fi
  chmod 0755 -- "$_tmp"
  chown_root "$_tmp"
  mv -f -- "$_tmp" "$SBIN/ah-switch"
}

step_switch() {
  _dest=$SBIN/ah-switch
  require_dir "$SBIN"
  if [ -e "$_dest" ] || [ -L "$_dest" ]; then
    check_one "$_dest" -
    _l=$(ls -ldn -- "$_dest")
    if [ "$(hash_file "$_dest")" = "$SWITCH_SHA" ] && mode_exec_file "$_l"; then
      out "ok: $_dest is already installed with this sha256"
      return 0
    fi
  fi
  act "install $SWITCH_BIN as $_dest (0755, root:root), sha256 $SWITCH_SHA" do_switch
}

do_caddy() {
  _tmp=$CADDY_DIR/.Caddyfile.new.$$
  TMPFILES="$TMPFILES $_tmp"
  if [ -f "$CADDYFILE" ] && [ ! -L "$CADDYFILE" ]; then
    cp -p -- "$CADDYFILE" "$CADDY_BACKUP"
    cmp -s -- "$CADDYFILE" "$CADDY_BACKUP" || die "the backup of $CADDYFILE differs from it"
  fi
  ln -s -- "$CADDY_TARGET" "$_tmp"
  mv -f -- "$_tmp" "$CADDYFILE"
}

step_caddy() {
  require_dir "$CADDY_DIR"
  if [ -L "$CADDYFILE" ]; then
    _t=$(readlink -- "$CADDYFILE")
    if [ "$_t" = "$CADDY_TARGET" ]; then
      out "ok: $CADDYFILE already points at $CADDY_TARGET"
      return 0
    fi
    die "refused: $CADDYFILE is a link to $_t; move it away by hand"
  fi
  if [ -e "$CADDYFILE" ]; then
    [ -f "$CADDYFILE" ] || die "refused: $CADDYFILE is not a regular file"
    if [ -e "$CADDY_BACKUP" ] || [ -L "$CADDY_BACKUP" ]; then
      die "refused: $CADDY_BACKUP exists and is never overwritten; move it away by hand"
    fi
    act "back up $CADDYFILE to $CADDY_BACKUP, once" true
  fi
  act "make $CADDYFILE a link to $CADDY_TARGET" do_caddy
}

# plan runs every step in the current PHASE.
plan() {
  MADE=' '
  _d=$(hash_file "$SWITCH_BIN")
  out "switch binary: $SWITCH_BIN"
  out "switch binary sha256: $_d"
  [ "$_d" = "$SWITCH_SHA" ] ||
    die "refused: sha256 of the switch binary is $_d, not the $SWITCH_SHA given"
  out "ok: the sha256 matches --switch-sha256"

  ensure_dir "$OPT"
  ensure_dir "$OPT/releases"
  ensure_dir "$ETC"
  ensure_dir "$VAR"
  out "untouched: $R/srv/authority/public"

  step_stop_list

  if [ -e "$ETC/switch-policy.json" ] || [ -L "$ETC/switch-policy.json" ]; then
    out "left as it is: $ETC/switch-policy.json"
  else
    out "note: $ETC/switch-policy.json is not written. Without it ah-switch runs in"
    out "      manual mode (unsigned releases are accepted), which the first scratch"
    out "      deploys may use. Signed mode needs the owner's allowed signers file and"
    out "      switch-policy.json, both written by the owner (see DEPLOY.md)."
  fi

  step_switch

  if [ "$LINK_CADDY" = 1 ]; then
    step_caddy
    out "note: $OPT/current does not exist until the first ah-switch makes it. Until then the"
    out "      Caddyfile link dangles: switch to a release first, then restart caddy."
    out "      This script never restarts, reloads or enables a service."
  else
    out "skipped: $R/etc/caddy/Caddyfile is not linked (pass --link-caddy to do it)"
  fi
}

# --- self-test ----------------------------------------------------------------

self_test() {
  _n=0
  expect() {
    _n=$((_n + 1))
    if "$@"; then say "self-test $_n: ok"; else
      say "self-test $_n: FAILED: $*"
      exit 1
    fi
  }
  refuse() {
    _n=$((_n + 1))
    if "$@"; then
      say "self-test $_n: FAILED (should fail): $*"
      exit 1
    else say "self-test $_n: ok"; fi
  }
  expect valid_sha256 0000000000000000000000000000000000000000000000000000000000000000
  expect valid_sha256 ABCDEF0123456789abcdef0123456789ABCDEF0123456789abcdef0123456789
  refuse valid_sha256 abc
  refuse valid_sha256 000000000000000000000000000000000000000000000000000000000000000g
  expect mode_loose drwxrwxr-x
  expect mode_loose drwxr-xrwx
  expect mode_loose drwxrwxrwt
  refuse mode_loose drwxr-xr-x
  refuse mode_loose drwxr-xr-t
  expect mode_exec_file -rwxr-xr-x
  refuse mode_exec_file -rwxrwxr-x
  refuse mode_exec_file lrwxrwxrwx
  expect test "$(stop_list_text | grep -vc '^#')" = 1
  say "self-test: ok, $_n checks, nothing was run or changed"
}

# --- arguments ----------------------------------------------------------------

MODE=
SWITCH_BIN=
SWITCH_SHA=
LINK_CADDY=0
SELF=0
while [ $# -gt 0 ]; do
  case $1 in
    --dry | --apply)
      [ -z "$MODE" ] || [ "$MODE" = "${1#--}" ] || usage
      MODE=${1#--}
      ;;
    --self-test) SELF=1 ;;
    --link-caddy) LINK_CADDY=1 ;;
    --switch-bin)
      [ $# -ge 2 ] || usage
      SWITCH_BIN=$2
      shift
      ;;
    --switch-sha256)
      [ $# -ge 2 ] || usage
      SWITCH_SHA=$2
      shift
      ;;
    *) usage ;;
  esac
  shift
done

if [ "$SELF" = 1 ]; then
  [ -z "$MODE" ] && [ -z "$SWITCH_BIN" ] && [ -z "$SWITCH_SHA" ] && [ "$LINK_CADDY" = 0 ] || usage
  self_test
  exit 0
fi

MODE=${MODE:-dry}
[ -n "$SWITCH_BIN" ] || die "--switch-bin FILE is required"
[ -n "$SWITCH_SHA" ] || die "--switch-sha256 HEX is required"
valid_sha256 "$SWITCH_SHA" || die "--switch-sha256 must be 64 hexadecimal digits"
SWITCH_SHA=$(printf '%s' "$SWITCH_SHA" | tr 'A-F' 'a-f')
[ -f "$SWITCH_BIN" ] && [ ! -L "$SWITCH_BIN" ] ||
  die "--switch-bin must name a regular file, not a link"

R=${ROOT_PREFIX-}
while [ "${R%/}" != "$R" ]; do R=${R%/}; done
case $R in
  '' | /*) ;;
  *) die "ROOT_PREFIX must be an absolute path" ;;
esac
ME=$(id -u)
if [ "$MODE" = apply ] && [ "$ME" != 0 ] && [ -z "$R" ]; then
  die "--apply must run as root"
fi

OPT=$R/opt/authority-host
ETC=$R/etc/authority-host
VAR=$R/var/lib/authority-host
SBIN=$R/usr/local/sbin
CADDY_DIR=$R/etc/caddy
CADDYFILE=$CADDY_DIR/Caddyfile
CADDY_BACKUP=$CADDY_DIR/Caddyfile.pre-authority-host
CADDY_TARGET=/opt/authority-host/current/etc/caddy/Caddyfile

say "host-bootstrap: mode $MODE"
[ -z "$R" ] || say "root prefix: $R (every path below is under it)"

if [ "$MODE" = apply ]; then
  PHASE=check
  plan
  PHASE='do'
else
  PHASE=dry
fi
plan

if [ "$MODE" = apply ]; then
  say "host-bootstrap: done"
else
  say "host-bootstrap: dry run, nothing was changed; run again with --apply to do it"
fi
