# The code canary-prepare.sh and canary-reset.sh share. It is sourced, never
# run: it defines names and runs nothing. The caller sets CC_NAME (its own
# name, for messages) and REPO (the checkout the kit lies in, resolved).
#
# POSIX sh only. It uses the tools canary-run.sh uses (mkdir, rm, cp, find,
# sort, tr, sha256sum, awk, sed, git, env, id) and, besides them, chmod,
# readlink and ls.

# shellcheck disable=SC2034  # the names are used by the scripts that source this file
CC_PROJECT=throwaway-one
CC_MARKER=.canary-prepared
CC_MARKER_LINE='ataniq-canary-prepare 1'
# The binaries the pass needs from build/canary, by what they are for.
CC_BINARIES='ataniqd ataniq-holder ataniq-canary ataniq-canary-standin ataniq-console ataniq-intake ataniq-probe'

cc_say() { printf '%s\n' "$*"; }

# cc_die ends the script with a message on standard error.
cc_die() {
  printf '%s: %s\n' "$CC_NAME" "$*" >&2
  exit 1
}

# cc_resolve PATH prints the absolute path with every symbolic link resolved
# in the part that exists; the part that does not exist is appended as
# written. It fails when the existing part is not a folder.
cc_resolve() {
  _rp=$1
  _rt=
  while [ ! -e "$_rp" ] && [ ! -L "$_rp" ]; do
    _rt=/${_rp##*/}$_rt
    _rp=${_rp%/*}
    [ -n "$_rp" ] || _rp=/
  done
  _rd=$(cd -P -- "$_rp" 2>/dev/null && pwd -P) || return 1
  [ "$_rd" != / ] || _rd=
  printf '%s%s\n' "$_rd" "$_rt"
}

# cc_inside PATH FOLDER succeeds when PATH is FOLDER or lies below it.
cc_inside() {
  case $1 in
    "$2" | "$2"/*) return 0 ;;
  esac
  return 1
}

# cc_check_root ROOT checks the folder that holds the whole canary set-up and
# sets ROOT to its resolved path. It refuses a path that is not absolute, has
# a . or .. part or a character the config's JSON would need to escape, a
# symbolic link, a folder in /tmp or /var/tmp, and a folder inside the
# repository checkout (or one that holds it). It makes nothing.
cc_check_root() {
  case $1 in
    /*) ;;
    *) cc_die "the root is not an absolute path: $1" ;;
  esac
  case $1 in
    *[!A-Za-z0-9._/+-]*) cc_die 'the root has a character outside A-Z a-z 0-9 . _ / + -' ;;
  esac
  case $1/ in
    */./* | */../* | *//*) cc_die "the root has a . or .. part or an empty part: $1" ;;
  esac
  if [ -L "$1" ]; then cc_die "the root is a symbolic link: $1"; fi
  if [ -e "$1" ] && [ ! -d "$1" ]; then cc_die "the root is not a folder: $1"; fi
  _r=$(cc_resolve "$1") || cc_die "the root cannot be resolved: $1"
  if cc_inside "$_r" "$REPO" || cc_inside "$REPO" "$_r"; then
    cc_die "the root and the repository checkout lie one in the other: $1"
  fi
  for _t in /tmp /var/tmp; do
    _tr=$(cc_resolve "$_t") || _tr=$_t
    if cc_inside "$_r" "$_t" || cc_inside "$_r" "$_tr"; then
      cc_die "the root is in $_t, which is cleared by the host: choose a folder in your cache folder"
    fi
  done
  # shellcheck disable=SC2034  # read by the caller
  ROOT=$_r
}

# cc_check_bin BIN checks that BIN is a folder that holds every binary of the
# kit as an executable regular file, and names the ones that are missing.
cc_check_bin() {
  [ -d "$1" ] || cc_die "no binary folder: $1 (make canary-build builds it; or give --build)"
  _missing=
  for _b in $CC_BINARIES; do
    if [ ! -f "$1/$_b" ] || [ -L "$1/$_b" ] || [ ! -x "$1/$_b" ]; then _missing="$_missing $_b"; fi
  done
  [ -z "$_missing" ] || cc_die "not built in $1:$_missing (make canary-build builds them; or give --build)"
}

# cc_git runs git with no global or system config and no templates.
cc_git() {
  env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 git "$@"
}

# cc_same_dir FOLDER succeeds when the folder the script is in is still the
# folder the name FOLDER leads to now. It asks the kernel each time: the inode
# number of "." against that of "FOLDER/.", each from its own ls (ls sorts
# several operands, so one call could not tell them apart), and FOLDER must
# resolve to itself, which refuses a link (an inode number alone could meet the
# same number on another file system). Comparing the output of pwd -P with
# FOLDER is no such check: dash prints the path it cached at the last cd -P
# and does not ask the kernel again, so it stays true after FOLDER was moved
# away and another folder put at its name. A folder that cannot be read is a
# refusal.
cc_same_dir() {
  # shellcheck disable=SC2012  # ls -di is the POSIX way to read an inode number
  _sa=$(ls -di -- . 2>/dev/null | awk 'NR == 1 { print $1 }')
  # shellcheck disable=SC2012  # same
  _sb=$(ls -di -- "$1/." 2>/dev/null | awk 'NR == 1 { print $1 }')
  [ -n "$_sa" ] && [ "$_sa" = "$_sb" ] || return 1
  [ "$(cd -P -- "$1" 2>/dev/null && pwd -P)" = "$1" ]
}

# cc_make_state ROOT BIN [pinned] makes the two roots, ROOT/state and
# ROOT/runtime, with mode 0700, and the mirror of the throwaway repository
# with ataniq-intake init, and puts the upstream branch develop of
# ROOT/upstream in it. Prepare and reset both make the roots with it.
#
# Prepare gives two arguments and the names go through ROOT. The reset gives
# the word pinned and has entered ROOT with cd -P: then the mkdir, the chmod
# and the push name their folders relative to that folder, so a ROOT swapped
# for a link meanwhile does not lead them anywhere. ataniq-intake init needs an
# absolute --state, so it cannot be pinned: ROOT/state and the folder entered
# are checked right before the call, and a swap in the few milliseconds
# between that check and the call is not stopped. What the call makes is the
# folders of an empty mirror; nothing is deleted.
cc_make_state() {
  _p=$1
  _m=$1/state/mirrors/$CC_PROJECT.git
  if [ "${3:-}" = pinned ]; then
    _p=.
    _m=../state/mirrors/$CC_PROJECT.git
  fi
  mkdir -m 0700 -- "$_p/state" "$_p/runtime"
  chmod 0700 -- "$_p/state" "$_p/runtime"
  if [ "${3:-}" = pinned ]; then
    [ "$(cd -P -- "$1/state" && pwd -P)" = "$1/state" ] ||
      cc_die "$1 was replaced while the folders were made, so nothing more is made"
    cc_same_dir "$1" ||
      cc_die "$1 was replaced while the folders were made, so nothing more is made"
  fi
  "$2/ataniq-intake" init --state "$1/state" --project "$CC_PROJECT" >/dev/null ||
    cc_die 'ataniq-intake init failed'
  cc_git -C "$_p/upstream" push -q "$_m" refs/heads/develop:refs/heads/develop ||
    cc_die 'the upstream branch develop could not be put in the mirror'
}
