#!/bin/sh
# Put a failed or finished attempt of the canary run back to a clean slate
# for a rerun. Read README.md beside this file first.
#
#   canary-reset.sh --root DIR [--bin DIR] [--dry]
#
# It deletes the provisioned state/ and runtime/ of a set-up that
# canary-prepare.sh made, and makes the two roots and the mirror again as
# canary-prepare.sh does (the code is shared in canary-lib.sh). It never
# deletes config.json, upstream, bin, conf, evidence or mirror-qual, and
# nothing outside the root. It refuses without a terminal on standard input,
# while an ataniq-* unit is loaded or an ataniqd or ataniq-holder of the root
# runs (named in its arguments, or running in the root), and for a root that does not carry the mark of its own prepare. It
# shows what it will delete and deletes only after the owner types the word
# reset: the script never answers that prompt. With --dry it prints the plan
# and deletes nothing. It starts nothing.
#
# --self-test NAME VALUE...: the test entry, which only computes and prints.
#   typed-word WORD         "go" when WORD is exactly reset, otherwise "stop"
#   procs-of ROOT PROCDIR   the numbers of the processes of PROCDIR (a copy of
#                           the shape of /proc) that are an ataniqd or an
#                           ataniq-holder of ROOT (named in its arguments or
#                           running in ROOT); a refusal when PROCDIR holds no
#                           process folder
set -eu
umask 077

CC_NAME=canary-reset.sh
_d=$(cd -P -- "${0%/*}" && pwd -P)
REPO=$(cd -P -- "$_d/../../.." && pwd -P)
# shellcheck source=canary-lib.sh
. "$_d/canary-lib.sh"

ROOT=
BIN=$REPO/build/canary
DRY=0
WORD=reset

usage() {
  printf '%s\n' 'usage: canary-reset.sh --root DIR [--bin DIR] [--dry]' \
    '       --root DIR  the folder canary-prepare.sh made' \
    '       --bin DIR   the built binaries (default: build/canary of the checkout)' \
    '       --dry       print the plan and delete nothing' \
    '       canary-reset.sh --self-test NAME VALUE...' \
    '         typed-word WORD         go when WORD is the word that confirms, else stop' \
    '         procs-of ROOT PROCDIR   the daemon and holder processes of ROOT in PROCDIR' >&2
  exit 2
}

# typed_word WORD prints go for the confirming word and stop for any other.
typed_word() {
  if [ "$1" = "$WORD" ]; then printf '%s\n' go; else printf '%s\n' stop; fi
}

# arg_in_root ROOT CWD WORD succeeds when the command-line word WORD names
# ROOT or a path below it, compared with every symbolic link resolved: a word
# that is not absolute is taken from CWD (nothing matches when CWD is empty).
arg_in_root() {
  case $3 in
    /*) _ap=$3 ;;
    *) [ -n "$2" ] || return 1; _ap=$2/$3 ;;
  esac
  _ap=$(readlink -f -- "$_ap" 2>/dev/null) || return 1
  cc_inside "$_ap" "$1"
}

# proc_is_ours PROCFOLDER succeeds when the process is run by this user, or
# when its owner cannot be read (it is then taken for ours).
proc_is_ours() {
  _pu=$(awk '$1 == "Uid:" { print $2 " " $3; exit }' "$1/status" 2>/dev/null) || _pu=
  [ -n "$_pu" ] || return 0
  case " $_pu " in
    *" $(id -u) "*) return 0 ;;
  esac
  return 1
}

# procs_of ROOT PROCDIR prints the process numbers of PROCDIR whose command is
# an ataniqd or an ataniq-holder that has to do with ROOT: a path below ROOT
# in its arguments as written, or resolved (a word that is not absolute is
# taken from the process's folder), or ROOT or a folder below it as its
# current folder. A process of this user whose current folder cannot be read
# is taken for a match, which refuses rather than risks a delete under a live
# daemon. It fails when PROCDIR holds no process folder (no /proc, or one that
# cannot be read): nothing can then be said.
procs_of() {
  _seen=0
  for _pd in "$2"/[0-9]*; do
    [ -e "$_pd" ] || continue
    _seen=1
    [ -r "$_pd/cmdline" ] || continue
    _cl=$(tr '\0' ' ' <"$_pd/cmdline" 2>/dev/null) || continue
    _first=${_cl%% *}
    case ${_first##*/} in
      ataniqd | ataniq-holder) ;;
      *) continue ;;
    esac
    case $_cl in
      *"$1/"* | *"$1 "*) printf '%s\n' "${_pd##*/}"; continue ;;
    esac
    _cwd=$(cd -P -- "$_pd/cwd" 2>/dev/null && pwd -P) || _cwd=
    if [ -n "$_cwd" ]; then
      if cc_inside "$_cwd" "$1"; then printf '%s\n' "${_pd##*/}"; continue; fi
    elif proc_is_ours "$_pd"; then
      printf '%s\n' "${_pd##*/}"
      continue
    fi
    _words=$(tr '\0' '\n' <"$_pd/cmdline" 2>/dev/null) || _words=
    _hit=0
    while IFS= read -r _w; do
      [ -n "$_w" ] || continue
      if arg_in_root "$1" "$_cwd" "$_w"; then _hit=1; break; fi
      case $_w in
        -*=*) if arg_in_root "$1" "$_cwd" "${_w#*=}"; then _hit=1; break; fi ;;
      esac
    done <<EOF
$_words
EOF
    if [ "$_hit" -eq 1 ]; then printf '%s\n' "${_pd##*/}"; fi
  done
  [ "$_seen" -eq 1 ]
}

if [ "${1:-}" = --self-test ]; then
  case ${2:-} in
    typed-word) [ "$#" -eq 3 ] || usage; typed_word "$3"; exit 0 ;;
    procs-of)
      [ "$#" -eq 4 ] || usage
      procs_of "$3" "$4" || {
        printf '%s\n' "refused: no process folder in $4 (no /proc, or one that cannot be read)"
        exit 1
      }
      exit 0
      ;;
  esac
  usage
fi

while [ "$#" -gt 0 ]; do
  case $1 in
    --root) [ "$#" -ge 2 ] || usage; ROOT=$2; shift 2 ;;
    --bin) [ "$#" -ge 2 ] || usage; BIN=$2; shift 2 ;;
    --dry) DRY=1; shift ;;
    *) usage ;;
  esac
done
[ -n "$ROOT" ] || usage

cc_check_root "$ROOT"
[ -d "$ROOT" ] || cc_die "no such folder: $ROOT"

# The mark of the root's own prepare, and what a prepare makes.
MARK=$ROOT/$CC_MARKER
if [ -L "$MARK" ] || [ ! -f "$MARK" ]; then
  cc_die "the root does not carry the mark of a canary-prepare.sh run ($CC_MARKER): $ROOT"
fi
read -r _first <"$MARK" || _first=
[ "$_first" = "$CC_MARKER_LINE" ] ||
  cc_die "the mark of the root is not the one canary-prepare.sh writes: $MARK"
for _k in config.json upstream; do
  if [ -L "$ROOT/$_k" ] || [ ! -e "$ROOT/$_k" ]; then cc_die "the root lacks $_k, which a prepare makes: $ROOT"; fi
done
[ -d "$ROOT/upstream" ] || cc_die "the root's upstream is not a folder: $ROOT/upstream"
cc_git -C "$ROOT/upstream" rev-parse --verify -q refs/heads/develop >/dev/null ||
  cc_die "the throwaway repository has no branch develop: $ROOT/upstream"

# Only the two roots go, each a folder that really lies in the root (a link,
# or a path that resolves elsewhere, is refused); one that is gone is nothing
# to delete.
for _k in state runtime; do
  if [ -L "$ROOT/$_k" ]; then cc_die "$ROOT/$_k is a symbolic link"; fi
  if [ -e "$ROOT/$_k" ]; then
    [ -d "$ROOT/$_k" ] || cc_die "$ROOT/$_k is not a folder"
    _got=$(cd -P -- "$ROOT/$_k" && pwd -P) || cc_die "$ROOT/$_k cannot be entered"
    [ "$_got" = "$ROOT/$_k" ] || cc_die "$ROOT/$_k resolves to another path"
  fi
done
[ -x "$BIN/ataniq-intake" ] && [ -f "$BIN/ataniq-intake" ] || cc_die "no ataniq-intake in $BIN (make canary-build builds it): the mirror could not be made again"

# Nothing of the attempt may still run.
_units=$(systemctl --user list-units --all --no-legend --plain 'ataniq-*') ||
  cc_die 'systemctl --user list-units failed: the loaded units cannot be read, so nothing is deleted'
_units=$(printf '%s\n' "$_units" | awk '$1 != "" && $1 !~ /\.slice$/ { print $1 }')
[ -z "$_units" ] || cc_die "an ataniq-* unit is loaded, so nothing is deleted (stop it first, README.md, Undo by hand): $(printf '%s' "$_units" | tr '\n' ' ')"
_pids=$(procs_of "$ROOT" /proc) ||
  cc_die 'the processes cannot be read (no /proc, or one that cannot be read), so nothing is deleted'
[ -z "$_pids" ] || cc_die "an ataniqd or ataniq-holder of this root runs (process $(printf '%s' "$_pids" | tr '\n' ' ')), so nothing is deleted"

# The plan: what goes, what stays, what is made again.
cc_say "Reset of $ROOT"
cc_say
cc_say 'It will DELETE these two folders and everything below them:'
for _k in state runtime; do
  if [ -d "$ROOT/$_k" ]; then
    cc_say "  $ROOT/$_k ($(find "$ROOT/$_k" -mindepth 1 | awk 'END { print NR }') entries below it)"
  else
    cc_say "  $ROOT/$_k (not there)"
  fi
done
cc_say 'The entries down to three levels:'
for _k in state runtime; do
  if [ -d "$ROOT/$_k" ]; then find "$ROOT/$_k" -mindepth 1 -maxdepth 3 | sort | sed 's/^/    /'; fi
done
cc_say
cc_say "It KEEPS config.json, upstream, bin, conf, evidence, mirror-qual and the rest of $ROOT."
cc_say "It then makes state/ and runtime/ again, empty (mode 0700), and the mirror with"
cc_say "  ataniq-intake init, with the branch develop of $ROOT/upstream pushed into it."
cc_say

if [ "$DRY" -eq 1 ]; then
  cc_say '[dry] nothing was deleted and nothing was made'
  exit 0
fi

if [ ! -t 0 ]; then
  cc_die 'standard input is not a terminal: the word that confirms is yours to type, so nothing is deleted'
fi

printf 'Type the word %s and press Enter to delete them (anything else stops): ' "$WORD"
ANSWER=
IFS= read -r ANSWER || {
  printf '\n'
  cc_die 'no answer could be read: nothing is deleted'
}
if [ "$(typed_word "$ANSWER")" != go ]; then
  cc_die 'the word was not typed: nothing is deleted'
fi

# The two folders may hold read-only entries (a tree of git objects).
for _k in state runtime; do
  if [ -d "$ROOT/$_k" ]; then
    chmod -R u+rwX -- "$ROOT/$_k"
    rm -rf -- "${ROOT:?}/$_k"
  fi
done
cc_say "deleted $ROOT/state and $ROOT/runtime"
cc_make_state "$ROOT" "$BIN"
cc_say "made $ROOT/state and $ROOT/runtime again, and the mirror with the branch develop"
cc_say 'Reset done. Nothing was started. The run script is typed as before.'
