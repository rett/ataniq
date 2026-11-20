#!/bin/sh
# The attended run of the canary slice: pass A (the stand-in agent) or pass B
# (one live agent). It starts real processes and is run only by the owner's
# hand, with the lead present. Read README.md beside this file first.
#
#   canary-run.sh --pass A|B --config CONFIG [--dry] [--evidence DIR]
#
# It prints every step with its number, what it does, what is expected and the
# gate it serves, waits for Enter before every step that runs anything, and
# never answers a prompt itself. With --dry it prints everything and starts
# nothing: no command at all is run, no file is read or written, and the
# values are samples. --fail-at N (with --dry only) injects a failure at step
# N, so that the dry run prints the clean-up chain a real failure there runs.
# --self-test NAME [VALUE...] is the test entry: it gives sample values to one
# of the script's own functions, prints what the function answers and ends. It
# only computes: no step runs, nothing is started or written, and nothing is
# read but the file given to pass-a-record and has-line and, by wait-key,
# standard input; has-line runs the helper ataniq-canary fields of build/canary
# on each line of its file, and no other program.
#
# The steps and their numbers are the plan's table. The failure path: in pass
# B a failure from step 6 on runs steps 22, 24, 25 and 26, and 21 and 23 as
# well from step 15 on; in pass A a failure from step 11 on runs 22 and 26. A
# step of the chain that has nothing to act on says so; none can be skipped.
set -eu
umask 077

PASS=
CONFIG=
DRY=0
EVPARENT=
FAILAT=0
SELFTEST=0

usage() {
  printf '%s\n' 'usage: canary-run.sh --pass A|B --config CONFIG [--dry] [--evidence DIR]' \
    '       --dry          print every step and start nothing' \
    '       --evidence DIR the folder in which the dated evidence folder is made' \
    '       --fail-at N    with --dry only: inject a failure at step N (1 to 26)' \
    '       canary-run.sh --self-test NAME [VALUE...]' \
    '                      a test entry: compute and print only, start nothing' \
    '         balance CEILING SHARE FLOOR AVAILABLE' \
    '                      the balanced memory limit of step 11 (bytes, percent, bytes, kB)' \
    '         inject-action PASS OUTCOME REASON WRITTEN' \
    '                      what step 17 does with a result of the injection' \
    '         fields-hold FIELDS PATH=VALUE...' \
    '                      yes when the lines FIELDS hold every PATH=VALUE (step 12)' \
    '         wait-key' \
    '                      wait for one key as the clean-up chain does (reads standard input)' \
    '         pass-a-record FILE' \
    '                      what step 2 of pass B reads in a result.txt of pass A' \
    '         outside-roots FOLDER ROOT...' \
    '                      whether the evidence folder FOLDER lies outside every ROOT, links resolved' \
    '         counters-7501 FIELDS' \
    '                      the 7501 counters step 22 prints from the lines FIELDS' \
    '         unit-stopped STATE' \
    '                      whether that active state lets step 26 show the delete command' \
    '         open-runs LIST REPO | list-scan LIST | loaded-units FILE | tracked-changes FILE' \
    '         unit-facts FIELDS | plan-facts FIELDS | holder-start FIELDS VERSION DIGEST' \
    '         upstream-id FILE NAME | lead-id FILE ROLE BRANCH' \
    '                      what steps 1, 2, 3, 8, 12 and 20 read themselves in place of a word' >&2
  exit 2
}

while [ "$#" -gt 0 ]; do
  case $1 in
    --pass) [ "$#" -ge 2 ] || usage; PASS=$2; shift 2 ;;
    --config) [ "$#" -ge 2 ] || usage; CONFIG=$2; shift 2 ;;
    --evidence) [ "$#" -ge 2 ] || usage; EVPARENT=$2; shift 2 ;;
    --fail-at) [ "$#" -ge 2 ] || usage; FAILAT=$2; shift 2 ;;
    --dry) DRY=1; shift ;;
    --self-test)
      # The rest of the command line is the test entry's own.
      [ "$#" -ge 2 ] || usage
      SELFTEST=1
      shift
      break
      ;;
    *) usage ;;
  esac
done
if [ "$SELFTEST" -eq 0 ]; then
  case $PASS in A | B) ;; *) usage ;; esac
  [ -n "$CONFIG" ] || usage
fi
case $FAILAT in '' | *[!0-9]*) usage ;; esac
[ "$FAILAT" -le 26 ] || usage
if [ "$FAILAT" -ne 0 ] && [ "$DRY" -ne 1 ]; then
  printf '%s\n' 'canary-run.sh: --fail-at is a test hook and needs --dry' >&2
  exit 2
fi

# ---------------------------------------------------------------- state ---

CUR=0          # the step that is running
IN_CHAIN=0     # 1 while the clean-up chain runs
SIGNALLED=0    # 1 when a signal came while the chain read from the terminal
TTY_ASSUMED=0  # 1 in the test entry wait-key only: the input is taken for a terminal
CHAIN_RAN=0
FAIL_STEP=0
FAIL_WHY=
INJECTED=0     # --fail-at fires once
EPOCH_NO=1     # 1, or 2 in the second epoch of pass A
STARTED=0      # a start was asked of the daemon
STOPPED=0      # the last start was stopped with proof
START_EPOCH=   # the epoch the last start result named
PROVISIONED=0
DAEMON_UP=0
CAPPED=0       # pass B met the cap of 30 minutes or 10 turns
T0=0           # pass B: when the cap's timer started (seconds)
TURNS=0
FINDINGS=0
DONE_STEPS=' ' # the steps that ended, for the chain's "already done"
EV=
LOG=
LASTOUT=
LASTERR=
RC=0
ANSWER=
LINGER0=
PASSA=
TOKEN=
BODY=
STAMP=

# The words and names the daemon, the holder and the stand-in use.
TAG=ataniq_canary
WAIVER='assertion 10 waived: canary slice, no qualification record'
BRANCH=canary/one
AGENT_INSIDE=/ataniq/agent
# The mirror's place inside the sandbox (profile.MirrorInside): provisioning sets
# the tree's origin to it, not to the host path.
MIRROR_ROOT_INSIDE=/ataniq/mirror
CAP_SECONDS=1800
CAP_TURNS=10
PAGE=4096
PROFILE_VERSION=4

out() { printf '%s\n' "$*"; }

now() {
  if [ "$DRY" -eq 1 ]; then printf '%s' '<time>'; else date -u +%Y-%m-%dT%H:%M:%SZ; fi
}

# note appends one line to the evidence log, with the time.
note() {
  if [ "$DRY" -eq 1 ] || [ -z "$LOG" ]; then return 0; fi
  printf '%s step %s: %s\n' "$(now)" "$CUR" "$*" >>"$LOG"
}

dry() { out "   [dry] would $*"; }

# fail ends a step. Outside the chain it ends the run, and the exit trap runs
# the clean-up chain. Inside the chain it records the failure and returns: the
# caller then returns from its step, and the chain goes on.
fail() {
  out "   FAILED at step $CUR: $1"
  note "FAILED: $1"
  if [ "$IN_CHAIN" -eq 1 ]; then
    FINDINGS=$((FINDINGS + 1))
    return 0
  fi
  FAIL_STEP=$CUR
  FAIL_WHY=$1
  exit 1
}

# wait_key waits for the owner's Enter. Nothing answers it but the terminal.
wait_key() {
  if [ "$DRY" -eq 1 ]; then
    dry "wait for Enter: $1"
    return 0
  fi
  printf '   >> %s Press Enter: ' "$1"
  if read_answer; then
    note "key: $1"
  else
    out ''
    note "no key could be read: $1"
    if [ "$IN_CHAIN" -eq 0 ]; then fail 'no key could be read from the terminal'; fi
    return 1
  fi
}

# on_terminal reports whether standard input is a terminal. A real run has
# one from its start; the test entry, which has none, says so with
# TTY_ASSUMED.
on_terminal() { [ "$TTY_ASSUMED" -eq 1 ] || [ -t 0 ]; }

# chain_trap is the clean-up chain's answer to a signal: the chain goes on,
# and the signal is remembered, so that it cannot stand in for a key.
chain_trap() {
  trap 'SIGNALLED=1; out ""; out "   the clean-up chain cannot be skipped"' INT TERM HUP
}

# read_answer reads one line from the terminal into ANSWER. In the clean-up
# chain a signal (Ctrl-C) ends the read without a key; that is not the
# owner's key, so the read is repeated while standard input is a terminal.
# Only the end of the input ends it without a line.
read_answer() {
  while :; do
    SIGNALLED=0
    _rs=0
    IFS= read -r ANSWER || _rs=$?
    if [ "$_rs" -eq 0 ]; then return 0; fi
    if [ "$IN_CHAIN" -eq 1 ] && on_terminal && { [ "$SIGNALLED" -eq 1 ] || [ "$_rs" -gt 128 ]; }; then
      out '   a signal is not a key: this step still waits for yours'
      printf '   >> '
      continue
    fi
    return "$_rs"
  done
}

# ask puts a question to the owner or the lead and records the answer and the
# time. The answer is in ANSWER.
ask() {
  ANSWER=
  if [ "$DRY" -eq 1 ]; then
    dry "ask and record the answer and the time: $1"
    return 0
  fi
  printf '   ?? %s\n   > ' "$1"
  if ! read_answer; then
    ANSWER=
    out ''
  fi
  printf '%s\tstep %s\t%s\t%s\n' "$(now)" "$CUR" "$1" "$ANSWER" >>"$EV/answers.txt"
  note "answer recorded: $1"
}

# affirm asks and fails the step unless the answer is the word.
affirm() {
  ask "$1 (type: $2)"
  if [ "$DRY" -eq 1 ]; then return 0; fi
  if [ "$ANSWER" != "$2" ]; then
    fail "the answer was not \"$2\": $1"
    return 1
  fi
}

# runcap runs one command, keeps its output in the evidence and shows it. It
# sets RC, LASTOUT and LASTERR and does not judge the status.
runcap() {
  _name=$1
  shift
  if [ "$DRY" -eq 1 ]; then
    dry "run: $*"
    RC=0
    return 0
  fi
  LASTOUT=$EV/out/$(printf '%02d' "$CUR")-e$EPOCH_NO-$_name.out
  LASTERR=$EV/out/$(printf '%02d' "$CUR")-e$EPOCH_NO-$_name.err
  out "   run: $*"
  RC=0
  "$@" >"$LASTOUT" 2>"$LASTERR" || RC=$?
  sed 's/^/   | /' "$LASTOUT"
  sed 's/^/   ! /' "$LASTERR"
  note "ran $_name: exit $RC"
}

# check runs a test and fails the step when it does not hold.
check() {
  _desc=$1
  shift
  if [ "$DRY" -eq 1 ]; then
    dry "check: $_desc"
    return 0
  fi
  if "$@"; then
    out "   ok: $_desc"
    note "ok: $_desc"
    return 0
  fi
  fail "$_desc"
  return 1
}

is_eq() { [ "$1" = "$2" ]; }
# mirror_inside prints the mirror's place inside the sandbox for a project.
mirror_inside() { printf '%s/%s.git' "$MIRROR_ROOT_INSIDE" "$1"; }
# remote_is_mirror reports whether URL (with or without file://) is the
# mirror's place inside the sandbox for PROJECT.
remote_is_mirror() { [ "${1#file://}" = "$(mirror_inside "$2")" ]; }
is_rc() { [ "$RC" -eq "$1" ]; }
is_empty_file() { [ ! -s "$1" ]; }
is_absent() { [ ! -e "$1" ] && [ ! -L "$1" ]; }

# fld prints the value of one path of a JSON file, through the tagged helper.
fld() {
  "$BIN/ataniq-canary" fields --file "$1" |
    awk -v k="$2" 'index($0, k "=") == 1 { print substr($0, length(k) + 2); exit }'
}

# fld_all prints every line of a JSON file, through the tagged helper.
fld_all() { "$BIN/ataniq-canary" fields --file "$1"; }

# fields_hold reports whether the lines $1, which are what ataniq-canary
# fields prints for one JSON object, hold each further argument ("path=value")
# as a whole line.
fields_hold() {
  _flds=$1
  shift
  for _kv in "$@"; do
    case "
$_flds
" in
      *"
$_kv
"*) ;;
      *) return 1 ;;
    esac
  done
}

# counters_7501 prints, from the lines $1 (what ataniq-canary fields prints
# for the holder's final record), the counters named for 7501, and fails when
# there is none, so that the caller can say so.
counters_7501() {
  _rest=$1
  _none=1
  while [ -n "$_rest" ]; do
    _l=${_rest%%"
"*}
    case $_rest in
      *"
"*) _rest=${_rest#*"
"} ;;
      *) _rest= ;;
    esac
    case $_l in
      counters.*7501*)
        out "     $_l"
        _none=0
        ;;
    esac
  done
  return "$_none"
}

# has_line reports whether one line of the file $1, which holds one JSON
# object in a line, holds every further argument ("path=value"). Each line is
# read as JSON by the tagged helper, never matched as text; a line that is not
# a whole JSON object (one being written) is passed over.
has_line() {
  _file=$1
  shift
  [ -f "$_file" ] || return 1
  while IFS= read -r _line || [ -n "$_line" ]; do
    _got=$(printf '%s\n' "$_line" | "$BIN/ataniq-canary" fields --file - 2>/dev/null) || continue
    if fields_hold "$_got" "$@"; then return 0; fi
  done <"$_file"
  return 1
}

# fact appends the name, size, time and hash of a file to a list in the
# evidence. The content is not copied.
fact() {
  if [ "$DRY" -eq 1 ]; then
    dry "record the name, size, time and hash of ${2:-the file named} in $1 (no content)"
    return 0
  fi
  printf '%s %s %s\n' "$(sha256sum <"$2" | awk '{print $1}')" "$(stat -c '%s %Y' -- "$2")" "$2" >>"$EV/$1"
}

# manifest_of writes names, sizes, times and hashes of the files of a folder.
manifest_of() {
  if [ "$DRY" -eq 1 ]; then
    dry "write names, sizes, times and hashes of the files of $1 to $2 (no content)"
    return 0
  fi
  (
    cd "$1"
    find . ! -type d ! -name MANIFEST.txt | LC_ALL=C sort | while IFS= read -r _f; do
      if [ -f "$_f" ] && [ ! -L "$_f" ]; then
        printf '%s %s %s\n' "$(sha256sum <"$_f" | awk '{print $1}')" "$(stat -c '%s %Y' -- "$_f")" "$_f"
      else
        printf '%s %s %s\n' 'not-a-regular-file' "$(stat -c '%s %Y' -- "$_f")" "$_f"
      fi
    done
  ) >"$2"
}

# cfg prints one value of the config, as ataniq-canary show gave it.
cfg() {
  printf '%s\n' "$CFG_SHOW" | while IFS= read -r _l; do
    case $_l in "$1="*)
      printf '%s\n' "${_l#*=}"
      break
      ;;
    esac
  done
}

# norm prints a limit as systemd or the control group gives it, in bytes or a
# count: "infinity" and "max" become "max", a suffix K, M, G or T is expanded.
norm() {
  case $1 in
    infinity | max) out max ;;
    *K) out $((${1%K} * 1024)) ;;
    *M) out $((${1%M} * 1024 * 1024)) ;;
    *G) out $((${1%G} * 1024 * 1024 * 1024)) ;;
    *T) out $((${1%T} * 1024 * 1024 * 1024 * 1024)) ;;
    *) out "$1" ;;
  esac
}

# pagefloor rounds a number of bytes down to whole pages, as the kernel does
# when a memory limit is written.
pagefloor() {
  case $1 in max) out max ;; *) out $(($1 / PAGE * PAGE)) ;; esac
}

# balance is R1: the smaller of the ceiling and the share of the available
# memory, rounded down to whole pages. $1 available kB, $2 ceiling in bytes,
# $3 share in percent. It only lowers: the result is never above the ceiling.
balance() {
  _b=$(($1 * 1024 * $3 / 100))
  if [ "$_b" -gt "$2" ]; then _b=$2; fi
  out $((_b / PAGE * PAGE))
}

# below_floor is R1's other half: the balanced value $1 is under the floor $2,
# so the host is too busy, nothing is set and the unit is stopped.
below_floor() { [ "$1" -lt "$2" ]; }

# inject_action names what step 17 does with the result of an injection: $1 is
# the pass, $2 the outcome, $3 the reason (empty for none), $4 what was
# written. The answers:
#   none            nothing to do: the message was submitted whole
#   owner-enter     the owner presses Enter once in the pane (N3: this result
#                   and no other)
#   look            nothing is pressed; the owner looks at the pane and decides
#   owner-supplies  the fence wrote nothing, and the owner puts the message in
#                   the pane himself
#   decide          no key is pressed; the owner decides whether the pass goes on
#   wrong-config    pass A: the fence refused an untested version, so the
#                   config does not declare a tested one
#   unexpected      pass A: not the result the stand-in's unknown screen gives
# Pass A, first epoch (L6): the config declares a tested version and the
# stand-in draws a screen no recogniser knows. The fence then answers "not
# idle", which the wire carries as not-now, not-ready, nothing. "refused" is
# the wire's word for an untested version or a refused text, not for this.
inject_action() {
  case $1:$2/$3/$4 in
    A:not-now/not-ready/nothing) out owner-supplies ;;
    A:refused/untested/*) out wrong-config ;;
    A:*) out unexpected ;;
    B:submitted//whole) out none ;;
    B:uncertain/not-submitted/partial) out owner-enter ;;
    B:uncertain/died/*) out look ;;
    B:refused/*) out owner-supplies ;;
    *) out decide ;;
  esac
}

show_prop() { systemctl --user show "$UNIT" --property="$1" --value; }

# resolved prints the absolute path $1 as the kernel will follow it: the part
# of it that exists is resolved with cd -P, so no symbolic link is left in it,
# and the part that does not exist yet is kept as written (mkdir makes it as
# plain folders). It fails when the path is not absolute or its existing part
# cannot be entered.
resolved() {
  case $1 in /*) ;; *) return 1 ;; esac
  _rp=$1
  _tail=
  while [ "${_rp%/}" != "$_rp" ] && [ "$_rp" != / ]; do _rp=${_rp%/}; done
  while [ ! -d "$_rp" ] && [ "$_rp" != / ]; do
    _tail=/${_rp##*/}$_tail
    _rp=${_rp%/*}
    [ -n "$_rp" ] || _rp=/
  done
  _rp=$(CDPATH='' cd -P -- "$_rp" 2>/dev/null && pwd -P) || return 1
  case $_rp in
    /) out "${_tail:-/}" ;;
    *) out "$_rp$_tail" ;;
  esac
}

# lies_in reports whether the path $1 is the folder $2 or lies below it, as
# the two are written.
lies_in() {
  case $1/ in "$2"/*) return 0 ;; esac
  return 1
}

# outside_roots reports whether the folder $1, which need not exist yet, lies
# outside every further folder (the checkout and the canary's two roots).
# The paths are compared as written and as resolved, so a symbolic link on
# the way into one of the folders does not pass. A path with a "." or ".."
# part is not taken: what it names cannot be told before it exists.
outside_roots() {
  _ev=$1
  shift
  case $_ev/ in */../* | */./*) return 1 ;; esac
  _evp=$(resolved "$_ev") || return 1
  for _root in "$@"; do
    _rr=$(resolved "$_root") || _rr=$_root
    if lies_in "$_ev" "$_root" || lies_in "$_evp" "$_rr"; then return 1; fi
  done
}

# passa_record reads the result.txt of a pass A, $1, for step 2 of pass B. It
# prints "clean" when the pass completed with no finding, "findings N" when
# it completed with N findings, "unknown" when it completed and the number of
# findings is not there as a number, and "incomplete" in every other case (a
# failed pass, no file).
passa_record() {
  _complete=0
  _found=
  if [ -f "$1" ]; then
    while IFS= read -r _l || [ -n "$_l" ]; do
      case $_l in
        'pass A: complete') _complete=1 ;;
        'findings: '*) _found=${_l#findings: } ;;
      esac
    done <"$1"
  fi
  if [ "$_complete" -ne 1 ]; then
    out incomplete
  elif ! is_number "$_found"; then
    out unknown
  elif [ "$_found" -eq 0 ]; then
    out clean
  else
    out "findings $_found"
  fi
}

# unit_state prints the unit's active state as systemd names it, or nothing
# when systemd cannot be asked.
unit_state() { systemctl --user show "$UNIT" --property=ActiveState --value 2>/dev/null || :; }

# unit_stopped reports whether the active state $1 shows that nothing of the
# unit runs: "inactive" (which systemd also answers for a unit it does not
# know) or "failed". Every other answer, an empty one too, is not a proof.
unit_stopped() {
  case $1 in inactive | failed) return 0 ;; esac
  return 1
}

done_step() { DONE_STEPS="$DONE_STEPS$CUR "; }
was_done() { case $DONE_STEPS in *" $1 "*) return 0 ;; esac; return 1; }

# ---------------------------------------------- machine reads of the lead ---

# The helpers below are what the script reads itself in place of a word of the
# lead. Each takes the text of a program's output or of a report, and none runs
# a program.

# real_dir prints the resolved path of a folder (symbolic links followed, no
# trailing slash), or the argument itself when it cannot be entered.
real_dir() {
  [ -n "$1" ] || return 0
  (cd "$1" 2>/dev/null && pwd -P) || printf '%s\n' "$1"
}

# repo_roots REPO prints the resolved path of REPO and, when REPO is in a work
# tree whose repository has its main work tree in another folder (a linked
# work tree), the resolved path of that main work tree as the last line. It
# prints nothing for an empty argument. qargi records a run's repo.path as the
# resolved --repo folder the run was started in, which may be a linked work
# tree, so both sides of a comparison are taken to their main work tree.
repo_roots() {
  [ -n "$1" ] || return 0
  _rr=$(real_dir "$1")
  printf '%s\n' "$_rr"
  _gc=$(cd "$_rr" 2>/dev/null && _g=$(git rev-parse --git-common-dir 2>/dev/null) && [ -n "$_g" ] && cd "$_g" 2>/dev/null && pwd -P) || return 0
  case $_gc in
    */.git)
      _gc=${_gc%/.git}
      if [ "$_gc" != "$_rr" ]; then printf '%s\n' "$_gc"; fi
      ;;
  esac
}

# open_runs LIST REPO prints the folder of each run that "qargi local list"
# (the text in the file LIST) shows as open and whose repo.path is in the same
# repository as REPO: the main work tree of each side is compared, as resolved
# paths, so a run prepared from a linked work tree is seen from the main
# checkout and from another work tree. A run is open unless it is applied (or
# closed) and, when applied, merged. Residue lines and the rows of a run whose
# folder is gone ("directory gone") are not rows of an open run it can read;
# list_gone counts the second kind, and the caller says so.
open_runs() {
  _roots=$(repo_roots "$2" | tail -n 1)
  [ -n "$_roots" ] || return 0
  awk 'NF == 6 && substr($6, 1, 1) == "/" && ($4 == "no" || ($4 == "yes" && $5 != "yes")) { print $6 }' "$1" |
    while IFS= read -r _r; do
      _rp=$(repo_roots "$(cat "$_r/repo.path" 2>/dev/null || :)" | tail -n 1)
      [ -n "$_rp" ] || continue
      if [ "$_rp" = "$_roots" ]; then printf '%s\n' "$_r"; fi
    done
}

# list_scan LIST classifies the lines of the text of "qargi local list" in the
# file LIST, as "qargi local list" prints them: the header (ROLE SLUG AGE
# APPLIED MERGED RUN, exactly), a row of six fields whose APPLIED is no, yes or
# closed and whose RUN is a path, a residue line, a row whose run directory is
# gone ("directory gone"), and a blank line. It prints "header bad" when the
# header is another, "gone N" for the rows of a run whose directory is gone
# (their repo.path cannot be read), and "other N" for every line it does not
# know.
list_scan() {
  awk '
    NR == 1 { $1 = $1; if ($0 != "ROLE SLUG AGE APPLIED MERGED RUN") bad = 1; next }
    $0 ~ /^[ \t]*$/ { next }
    $0 ~ /^  residue: / { next }
    $0 ~ /  directory gone/ && substr($0, 1, 1) != " " { gone++; next }
    NF == 6 && substr($6, 1, 1) == "/" && ($4 == "no" || $4 == "yes" || $4 == "closed") { next }
    { other++ }
    END {
      if (NR == 0 || bad) print "header bad"
      print "gone " gone + 0
      print "other " other + 0
    }' "$1"
}

# list_looks_right reports whether the file is what "qargi local list" prints:
# the header is the one it prints, and every other line is one list_scan knows.
# A list that drifted (another column, a line of another kind) is not read.
list_looks_right() {
  case $(list_scan "$1") in
    *'header bad'*) return 1 ;;
    *'other 0') return 0 ;;
    *) return 1 ;;
  esac
}

# list_gone prints how many rows of the list are of a run whose directory is
# gone, which open_runs passes over because the run's repo.path is not there.
list_gone() { list_scan "$1" | awk '$1 == "gone" { print $2 }'; }

# loaded_units prints the unit names of the file (systemctl list-units
# --plain --no-legend), the slices left out: the slices of the earlier
# qualification are inert.
loaded_units() { awk '$1 != "" && $1 !~ /\.slice$/ { print $1 }' "$1"; }

# untracked_only and tracked_changes read the text of git status --porcelain:
# the first prints the untracked entries, the second every other line.
untracked_only() { awk 'substr($0, 1, 2) == "??"' "$1"; }
tracked_changes() { awk 'substr($0, 1, 2) != "??" && $0 != ""' "$1"; }

# unit_facts_verdict FIELDS reads the lines that ataniq-canary fields prints for
# the report of leadunit-qualify. It prints "ok TEXT" when the tool is
# leadunit-qualify, the verdict is pass, every entry of checks has the result
# pass, and the checks that a run with limits has (limits-show-read-back and
# limits-cgroup-read-back) are there and pass, and "bad REASON" otherwise. A
# report of a run without limits has none of those two checks.
unit_facts_verdict() {
  printf '%s\n' "$1" | awk '
    { v = substr($0, index($0, "=") + 1) }
    /^tool=/ { tool = v }
    /^verdict=/ { verdict = v; hv = 1 }
    /^checks\.[0-9]+\./ {
      split($0, a, "."); i = a[2]; seen[i] = 1
      if ($0 ~ /^checks\.[0-9]+\.name=/) nm[i] = v
      if ($0 ~ /^checks\.[0-9]+\.result=/) {
        hr[i] = 1; rs[i] = v
        if (v != "pass" && bad == "") bad = "check " i " (" nm[i] ") has the result " v ", not pass"
      }
    }
    END {
      if (tool != "leadunit-qualify") { print "bad the report is of the tool " tool ", not leadunit-qualify"; exit }
      if (!hv) { print "bad the report has no verdict"; exit }
      if (verdict != "pass") { print "bad the verdict is " verdict ", not pass"; exit }
      n = 0; miss = 0
      for (i in seen) { n++; if (!(i in hr)) miss++ }
      if (n == 0) { print "bad the report has no checks"; exit }
      if (bad != "") { print "bad " bad; exit }
      if (miss > 0) { print "bad " miss " check(s) have no result"; exit }
      m = split("limits-show-read-back limits-cgroup-read-back", want, " ")
      for (k = 1; k <= m; k++) {
        found = 0
        for (i in seen) if (nm[i] == want[k] && rs[i] == "pass") found = 1
        if (!found) { print "bad the report has no passing check " want[k] " (a run without limits?)"; exit }
      }
      print "ok verdict pass, " n " checks, every result pass, the limits read back"
    }'
}

# plan_facts_verdict FIELDS reads the lines for the summary.json of the
# launched plan. It prints "ok TEXT" when the plan was launched and exited
# (exit.launched and exit.exited are true, report_outcome is line-read), every
# step has the status ok or skipped, exit.code is 0 and every case result is ok
# or not-checked, and "bad REASON" otherwise. A summary of a plan that was never
# launched has exit.code 0 and passes everything else, so the launch is read
# first.
plan_facts_verdict() {
  printf '%s\n' "$1" | awk '
    { v = substr($0, index($0, "=") + 1) }
    /^steps\.[0-9]+\.name=/ { split($0, a, "."); sn[a[2]] = v }
    /^steps\.[0-9]+\.status=/ {
      split($0, a, "."); s++; st = v
      if (st != "ok" && st != "skipped" && bad == "") bad = "step " a[2] " (" sn[a[2]] ") has the status " st
    }
    /^cases\.[0-9]+\.results\.[0-9]+\.id=/ { split($0, a, "."); cid[a[2] "." a[4]] = v }
    /^cases\.[0-9]+\.results\.[0-9]+\.status=/ {
      split($0, a, "."); c++; st = v
      if (st != "ok" && st != "not-checked" && bad == "") bad = "case result " a[2] "." a[4] " (" cid[a[2] "." a[4]] ") has the status " st
    }
    /^exit\.code=/ { ec = v; he = 1 }
    /^exit\.launched=/ { el = v }
    /^exit\.exited=/ { ex = v }
    /^report_outcome=/ { ro = v }
    END {
      if (s == 0) { print "bad the summary has no step"; exit }
      if (el != "true") { print "bad the plan was not launched (exit.launched is " el ")"; exit }
      if (ex != "true") { print "bad the plan did not exit (exit.exited is " ex ")"; exit }
      if (ro != "line-read") { print "bad report_outcome is " ro ", not line-read"; exit }
      if (!he) { print "bad the summary has no exit.code"; exit }
      if (ec != "0") { print "bad exit.code is " ec ", not 0"; exit }
      if (c == 0) { print "bad the summary has no case result"; exit }
      if (bad != "") { print "bad " bad; exit }
      print "ok " s " steps ok or skipped, exit.code 0, " c " case results ok or not-checked"
    }'
}

# upstream_branch_id FILE NAME and lead_branch_id FILE ROLE BRANCH print the
# commit that the text of "ataniq-intake branches" (in FILE) names for the
# upstream branch NAME and for the lead's branch, and nothing when it names
# none. The rows are "  NAME  ID" under "Upstream branches:" and "  ROLE
# BRANCH  ID" before it.
upstream_branch_id() {
  awk -v n="$2" '
    $0 == "Upstream branches:" { up = 1; next }
    up && substr($0, 1, 1) == " " && $1 == n { print $2; exit }
    up && substr($0, 1, 1) != " " { up = 0 }' "$1"
}
lead_branch_id() {
  awk -v r="$2" -v b="$3" '
    $0 == "Upstream branches:" { exit }
    substr($0, 1, 1) == " " && $1 == r && $2 == b && NF >= 3 { print $3; exit }' "$1"
}
lists_upstream() { [ -n "$(upstream_branch_id "$1" "$2")" ]; }

# holder_start_verdict FIELDS VERSION DIGEST reads the lines for holder.json.
# It prints "ok TEXT" when the sandbox has started, the profile version is
# VERSION and the profile digest is DIGEST, and "bad REASON" otherwise.
holder_start_verdict() {
  _f=$1
  case "
$_f
" in
    *"
sandbox_started=true
"*) ;;
    *) printf '%s\n' 'bad holder.json does not say the sandbox started (sandbox_started is not true)'; return 0 ;;
  esac
  fields_hold "$_f" "profile_version=$2" || { printf '%s\n' "bad holder.json profile_version is not $2"; return 0; }
  [ -n "$3" ] || { printf '%s\n' 'bad the config names no profile digest'; return 0; }
  fields_hold "$_f" "profile_digest=$3" || { printf '%s\n' 'bad holder.json profile_digest is not the pinned digest of the config'; return 0; }
  printf '%s\n' "ok sandbox started, profile version $2, profile digest is the pinned digest"
}

# -------------------------------------------------------------- self-test ---

# is_number reports whether every argument is a number in plain digits.
is_number() {
  for _n in "$@"; do
    case $_n in '' | *[!0-9]*) return 1 ;; esac
  done
}

# self_test is the test entry: it calls one function of the script with the
# values given and prints its answer. It runs no step and no program but the
# helper has-line names.
self_test() {
  _name=$1
  shift
  case $_name in
    balance)
      # CEILING SHARE FLOOR AVAILABLE: bytes, percent, bytes, kB of MemAvailable.
      if [ "$#" -ne 4 ] || ! is_number "$@"; then usage; fi
      _v=$(balance "$4" "$1" "$2")
      out "value=$_v"
      if below_floor "$_v" "$3"; then out 'floor=below'; else out 'floor=ok'; fi
      ;;
    mirror-remote)
      # URL PROJECT: whether URL is the mirror's place inside for PROJECT.
      [ "$#" -eq 2 ] || usage
      if remote_is_mirror "$1" "$2"; then out yes; else out no; fi
      ;;
    inject-action)
      [ "$#" -eq 4 ] || usage
      inject_action "$1" "$2" "$3" "$4"
      ;;
    unit-stopped)
      # STATE: an active state as systemd names it; it may be empty.
      [ "$#" -eq 1 ] || usage
      if unit_stopped "$1"; then out stopped; else out not-stopped; fi
      ;;
    wait-key)
      # No value. It waits for one key as a step of the clean-up chain does,
      # with the chain's answer to a signal, and takes its standard input for
      # a terminal. It prints "key" when a line was read and "no-key" when
      # the input ended.
      [ "$#" -eq 0 ] || usage
      IN_CHAIN=1
      TTY_ASSUMED=1
      chain_trap
      if wait_key 'The test entry waits as the clean-up chain does.'; then out key; else out no-key; fi
      ;;
    pass-a-record)
      # FILE: a result.txt of pass A. The file is read and nothing else.
      [ "$#" -eq 1 ] || usage
      passa_record "$1"
      ;;
    outside-roots)
      # FOLDER ROOT...: absolute paths; FOLDER need not exist.
      [ "$#" -ge 2 ] || usage
      if outside_roots "$@"; then out outside; else out inside; fi
      ;;
    counters-7501)
      # FIELDS: what ataniq-canary fields prints for a final record.
      [ "$#" -eq 1 ] || usage
      counters_7501 "$1" || out '     (none is named for 7501)'
      ;;
    open-runs)
      # LIST REPO: the open runs of REPO in the text of qargi local list.
      [ "$#" -eq 2 ] || usage
      open_runs "$1" "$2"
      ;;
    list-scan)
      # LIST: how list_scan reads the text of qargi local list.
      [ "$#" -eq 1 ] || usage
      list_scan "$1"
      ;;
    loaded-units)
      # FILE: the loaded units, slices left out, in a list-units text.
      [ "$#" -eq 1 ] || usage
      loaded_units "$1"
      ;;
    tracked-changes)
      # FILE: the lines of a porcelain status that are not untracked.
      [ "$#" -eq 1 ] || usage
      tracked_changes "$1"
      ;;
    unit-facts)
      # FIELDS: what fields prints for the report of leadunit-qualify.
      [ "$#" -eq 1 ] || usage
      unit_facts_verdict "$1"
      ;;
    plan-facts)
      # FIELDS: what fields prints for the summary of the launched plan.
      [ "$#" -eq 1 ] || usage
      plan_facts_verdict "$1"
      ;;
    holder-start)
      # FIELDS VERSION DIGEST: what fields prints for holder.json.
      [ "$#" -eq 3 ] || usage
      holder_start_verdict "$1" "$2" "$3"
      ;;
    upstream-id)
      # FILE NAME: the commit of the upstream branch in a branches text.
      [ "$#" -eq 2 ] || usage
      upstream_branch_id "$1" "$2"
      ;;
    lead-id)
      # FILE ROLE BRANCH: the commit of the lead's branch in a branches text.
      [ "$#" -eq 3 ] || usage
      lead_branch_id "$1" "$2" "$3"
      ;;
    has-line)
      # FILE PATH=VALUE...: FILE holds one JSON object in each line. The
      # helper is the one of build/canary in the current folder.
      [ "$#" -ge 2 ] || usage
      BIN=build/canary
      if has_line "$@"; then out yes; else out no; fi
      ;;
    fields-hold)
      # FIELDS PATH=VALUE...: FIELDS is what ataniq-canary fields prints for
      # one line of a record, with its line feeds.
      [ "$#" -ge 2 ] || usage
      if fields_hold "$@"; then out yes; else out no; fi
      ;;
    *) usage ;;
  esac
}

if [ "$SELFTEST" -eq 1 ]; then
  self_test "$@"
  exit 0
fi

# -------------------------------------------------------------- headings ---

# step_head prints a step: number, title, what it does, the expected result, the
# gate it serves. In the chain the heading says so.
step_head() {
  CUR=$1
  out ''
  if [ "$IN_CHAIN" -eq 1 ]; then
    out "== clean-up step $1 [pass $PASS]: $2"
  elif [ "$EPOCH_NO" -eq 2 ]; then
    out "== step $1 [pass $PASS, second epoch]: $2"
  else
    out "== step $1 [pass $PASS]: $2"
  fi
  out "   does:     $3"
  out "   expected: $4"
  out "   serves:   $5"
  note "begin: $2"
  if [ "$IN_CHAIN" -eq 0 ] && [ "$INJECTED" -eq 0 ] && [ "$FAILAT" -eq "$1" ]; then
    INJECTED=1
    fail 'a failure injected by --fail-at'
  fi
}

# skip prints a step that this pass does not run.
skip() {
  CUR=$1
  out ''
  out "== step $1 [pass $PASS]: skipped in pass $PASS: $2"
  out "   serves:   $3"
  note "skipped in pass $PASS: $2"
}

result() {
  out "   result:   $1"
  note "result: $1"
}

# ------------------------------------------------------------------ setup ---

case $0 in */*) _d=${0%/*} ;; *) _d=. ;; esac

if [ "$DRY" -eq 1 ]; then
  REPO='<repository>'
  ME='<user>'
  BIN=build/canary
  STAMP='<date>'
  [ -n "$EVPARENT" ] || EVPARENT='<home>/ataniq-canary-evidence'
  # Sample values: a dry run reads no config and needs none.
  if [ "$PASS" = A ]; then _kind=stand-in; _agent=$AGENT_INSIDE/ataniq-canary-standin; else _kind=live; _agent=$AGENT_INSIDE/bin/sample-agent; fi
  CFG_SHOW="role=throwaway-one/lead
project=throwaway-one
layout_role=throwaway-one-lead
unit=ataniq-lead-throwaway-one.service
state_root=/sample/canary/state
runtime_root=/sample/canary/runtime
marker=/sample/canary/state/canary-root.json
holder_executable=/sample/checkout/build/canary/ataniq-holder
holder_state_dir=/sample/canary/state/leads/throwaway-one-lead/holder
holder_runtime_dir=/sample/canary/runtime/leads/throwaway-one-lead
evidence=/sample/canary/state/canary-evidence.jsonl
sandbox_inputs=/sample/canary/conf/sandbox-inputs.json
inject=true
limit_memory_max_bytes=4294967296
limit_swap_max_bytes=0
limit_tasks_max=512
limit_memory_floor_bytes=1073741824
limit_memory_share_percent=50
agent_kind=$_kind
agent_runtime=sample-runtime
agent_runtime_version=1.2.3
agent_argc=3
agent_argv_0=$_agent
agent_argv_1=--model
agent_argv_2=sample-small
mirror=/sample/canary/state/mirrors/throwaway-one.git
upstream_branch=develop
lead_home=/sample/canary/state/leads/throwaway-one-lead/home
lead_config_dir=/sample/canary/state/leads/throwaway-one-lead/home/.config/sample-runtime
lead_tree=/sample/canary/state/leads/throwaway-one-lead/tree
replay_dir=/sample/canary/state/leads/throwaway-one-lead/home/canary-replay
replay_switch=/sample/canary/state/leads/throwaway-one-lead/home/canary-replay/replay.on
source_managed_settings=/sample/canary/state/leads/throwaway-one-lead/managed-settings.json
source_lead_skills=/sample/canary/state/leads/throwaway-one-lead/skills
source_instruction_file=/sample/canary/state/leads/throwaway-one-lead/instructions.md
source_git_identity_dir=/sample/canary/state/leads/throwaway-one-lead/git-identity
source_qargi_install=/sample/canary/state/leads/throwaway-one-lead/qargi-install
source_assignment_dir=/sample/canary/state/leads/throwaway-one-lead/assignment
source_projection_dir=/sample/canary/state/leads/throwaway-one-lead/projection"
else
  if [ ! -t 0 ]; then
    printf '%s\n' 'canary-run.sh: the run is attended: standard input must be a terminal' >&2
    exit 2
  fi
  REPO=$(cd "$_d/../../.." && pwd)
  ME=$(id -un)
  BIN=$REPO/build/canary
  STAMP=$(date -u +%Y%m%dT%H%M%SZ)
  [ -n "$EVPARENT" ] || EVPARENT=$HOME/ataniq-canary-evidence
  case $EVPARENT in /*) ;; *)
    printf '%s\n' 'canary-run.sh: --evidence must be an absolute path' >&2
    exit 2
    ;;
  esac
  # The binaries are the tagged ones of make canary-build, before anything
  # else is done with them (step 3 records them).
  for _b in ataniqd ataniq-holder ataniq-canary ataniq-canary-standin; do
    if [ ! -x "$BIN/$_b" ]; then
      printf '%s\n' "canary-run.sh: $BIN/$_b is missing: run make canary-build" >&2
      exit 1
    fi
    case $("$BIN/$_b" --version 2>/dev/null || :) in
      *"$TAG"*) ;;
      *)
        printf '%s\n' "canary-run.sh: $BIN/$_b --version does not name the build tag: run make canary-build" >&2
        exit 1
        ;;
    esac
  done
  for _b in ataniq-console ataniq-intake ataniq-probe; do
    if [ ! -x "$BIN/$_b" ]; then
      printf '%s\n' "canary-run.sh: $BIN/$_b is missing: run make canary-build" >&2
      exit 1
    fi
  done
  CFG_SHOW=$("$BIN/ataniq-canary" show --config "$CONFIG")
fi

ROLE=$(cfg role)
PROJECT=$(cfg project)
LAYOUT=$(cfg layout_role)
UNIT=$(cfg unit)
STATE=$(cfg state_root)
RUNTIME=$(cfg runtime_root)
MARKER=$(cfg marker)
HOLDEREXE=$(cfg holder_executable)
HSTATE=$(cfg holder_state_dir)
HRUN=$(cfg holder_runtime_dir)
EVIDENCE=$(cfg evidence)
INPUTS=$(cfg sandbox_inputs)
MAXB=$(cfg limit_memory_max_bytes)
SWAPB=$(cfg limit_swap_max_bytes)
TASKS=$(cfg limit_tasks_max)
FLOORB=$(cfg limit_memory_floor_bytes)
SHARE=$(cfg limit_memory_share_percent)
KIND=$(cfg agent_kind)
RVERSION=$(cfg agent_runtime_version)
AGENT0=$(cfg agent_argv_0)
MIRROR=$(cfg mirror)
UPSTREAM=$(cfg upstream_branch)
LCONFIG=$(cfg lead_config_dir)
TREE=$(cfg lead_tree)
REPLAY=$(cfg replay_dir)
SWITCH=$(cfg replay_switch)
# The daemon's socket in the runtime root (the client protocol's fixed name).
SOCK=$RUNTIME/daemon.sock
TEMPLATE=$REPO/qualification/canary/templates/canary-intake.json
EV=$EVPARENT/canary-pass-$PASS-$STAMP

if [ "$DRY" -eq 0 ]; then
  for _v in "$ROLE" "$UNIT" "$STATE" "$RUNTIME" "$HRUN" "$INPUTS" "$MAXB" "$FLOORB" "$SHARE" "$KIND" "$AGENT0" "$MIRROR"; do
    if [ -z "$_v" ]; then
      printf '%s\n' 'canary-run.sh: ataniq-canary show did not print a value the script needs' >&2
      exit 1
    fi
  done
  case $PASS$KIND in Astand-in | Blive) ;; *)
    printf '%s\n' "canary-run.sh: pass $PASS does not fit the config's agent kind \"$KIND\" (A: stand-in, B: live)" >&2
    exit 1
    ;;
  esac
  if ! outside_roots "$EV" "$REPO" "$STATE" "$RUNTIME"; then
    printf '%s\n' 'canary-run.sh: the evidence folder must not lie in the checkout or in a root of the canary (symbolic links are resolved), and its path must hold no . or .. part' >&2
    exit 1
  fi
fi

# ------------------------------------------------------------- the steps ---

# no_open_runs checks, with qargi local list, that no run of this project is
# open (a run is the project's when its repo.path is this checkout). Without
# qargi, or with a list it cannot read, it asks the lead as before and says so.
no_open_runs() {
  if [ "$DRY" -eq 1 ]; then
    dry "check: no Qargi run of this project is open (qargi local list, the rows whose repo.path is $REPO that are not applied and merged); without qargi on PATH, or when the list cannot be read, ask the lead as before and say so in the log"
    return 0
  fi
  if ! command -v qargi >/dev/null 2>&1; then
    out '   qargi is not on PATH: the open runs cannot be read; the lead is asked'
    note 'qargi is not on PATH: the machine check of the open runs is not possible, the lead is asked'
    affirm 'Lead: no Qargi run is open' yes || return 1
    return 0
  fi
  runcap qargi-runs qargi local list
  if [ "$RC" -ne 0 ] || ! list_looks_right "$LASTOUT"; then
    out '   the output of qargi local list cannot be read: the lead is asked'
    note 'the output of qargi local list cannot be read: the machine check of the open runs is not possible, the lead is asked'
    affirm 'Lead: no Qargi run is open' yes || return 1
    return 0
  fi
  _gone=$(list_gone "$LASTOUT")
  if [ "${_gone:-0}" -gt 0 ]; then
    out "   $_gone run(s) in the list have a directory that is gone: their repo.path cannot be read, they are not counted"
    note "$_gone run(s) of qargi local list have a directory that is gone: not counted as open runs of this project"
  fi
  _open=$(open_runs "$LASTOUT" "$REPO")
  if [ -n "$_open" ]; then
    printf '%s\n' "$_open" | sed 's/^/   open run: /'
    note "open runs of this project: $(printf '%s' "$_open" | tr '\n' ' ')"
    fail 'a Qargi run of this project is open (see above): verify and close it first'
    return 1
  fi
  out '   ok: no Qargi run of this project is open'
  note "ok: no Qargi run of $REPO is open (qargi local list, $LASTOUT)"
}

step_1() {
  step_head 1 'Quiet window, and the evidence folder' \
    "make the evidence folder $EV (mode 0700); ask for the quiet window; list the user manager's ataniq units" \
    'the script reads that no Qargi run of this project is open and no ataniq unit is loaded; the lead confirms that no agent session was told about the slice; no ataniq-lead-* unit exists' 'G4, L10'
  wait_key 'Step 1 makes the evidence folder and lists units.'
  if [ "$DRY" -eq 1 ]; then
    dry "make $EV and $EV/out with mode 0700 (refused if it exists)"
  else
    mkdir -p -- "$EVPARENT"
    mkdir -m 0700 -- "$EV" "$EV/out" || fail 'the evidence folder cannot be made (it must not exist)' || return 0
    LOG=$EV/log.txt
    : >"$EV/answers.txt"
    note "pass $PASS begins; config $(sha256sum <"$CONFIG" | awk '{print $1}') (hash only)"
  fi
  out "   the evidence folder is $EV: it is never published"
  no_open_runs || return 0
  affirm 'Lead: no agent session is open that has been told about the slice' yes || return 0
  runcap lead-units systemctl --user list-units --all --no-legend --plain 'ataniq-lead-*'
  check 'the ataniq-lead-* units can be listed' is_rc 0 || return 0
  check 'no ataniq-lead-* unit exists' is_empty_file "$LASTOUT" || return 0
  runcap units-before systemctl --user list-units --all --no-legend --plain 'ataniq-*'
  check 'the ataniq units can be listed' is_rc 0 || return 0
  if [ "$DRY" -eq 0 ]; then
    awk '{print $1}' "$LASTOUT" | LC_ALL=C sort >"$EV/units-before.txt"
    _loaded=$(loaded_units "$LASTOUT")
    if [ -n "$_loaded" ]; then printf '%s\n' "$_loaded" | sed 's/^/   loaded: /'; fi
  else
    _loaded=
  fi
  check 'no ataniq-* unit is loaded (the slices of the earlier qualification are left out)' is_eq "$_loaded" '' || return 0
  runcap unit-files-before systemctl --user list-unit-files --no-legend 'ataniq-*'
  if [ "$DRY" -eq 0 ]; then awk '{print $1}' "$LASTOUT" | LC_ALL=C sort >"$EV/unit-files-before.txt"; fi
  result ok
  done_step
}

# facts_pass reads one fact report (KIND unit: the report of leadunit-qualify;
# plan: the summary of the launched plan) and fails the step unless it shows
# that everything passed. What was read is in the evidence.
facts_pass() {
  if [ "$DRY" -eq 1 ]; then
    if [ "$1" = unit ]; then
      dry 'check: the report parses, its verdict is pass and every entry of checks has the result pass'
    else
      dry 'check: the summary parses, every step has the status ok or skipped, exit.code is 0 and every case result is ok or not-checked'
    fi
    return 0
  fi
  _fl=$EV/out/02-facts-$1.fields
  if ! "$BIN/ataniq-canary" fields --file "$2" >"$_fl" 2>"$_fl.err"; then
    fail "the file cannot be read as one JSON object: $2"
    return 1
  fi
  _flds=$(cat "$_fl")
  if [ "$1" = unit ]; then _v=$(unit_facts_verdict "$_flds"); else _v=$(plan_facts_verdict "$_flds"); fi
  case $_v in
    'ok '*)
      out "   ok: $2: ${_v#ok }"
      note "ok: $1 facts $2 (fields read: $_fl): ${_v#ok }"
      ;;
    *)
      fail "the $1 facts do not show a pass: ${_v#bad }"
      return 1
      ;;
  esac
}

step_2() {
  step_head 2 'Facts on file' \
    'ask for the two fact reports (the unit manager with the limits, the launched plan) and, in pass B, for the evidence folder of pass A; record names, sizes, times and hashes' \
    'each is present and passed, which the script reads from the files; a miss stops the run' 'G4, G5, G2'
  wait_key 'Step 2 reads the reports you name.'
  for _kind in unit plan; do
    case $_kind in
      unit) _what='the report of leadunit-qualify with the limits (cs-facts-unit, second run)' ;;
      *) _what='the summary of the launched plan (cs-facts-plan)' ;;
    esac
    ask "Lead: the path of $_what"
    check "the file is present: $_what" test -f "$ANSWER" || return 0
    fact facts.txt "$ANSWER"
    facts_pass "$_kind" "$ANSWER" || return 0
  done
  if [ "$PASS" = B ]; then
    ask 'Lead: the evidence folder of pass A'
    PASSA=$ANSWER
    if [ "$DRY" -eq 1 ]; then
      dry 'check: the record of pass A (result.txt in that folder) says the pass completed'
      dry 'check: the record shows no finding (findings: 0); with findings, print the number and ask the lead to confirm that every finding has its fix merged, and record the answer; anything else stops the run'
    else
      _rec=$(passa_record "$PASSA/result.txt")
      out "   the record of pass A: $_rec"
      note "the record of pass A: $_rec"
      case $_rec in
        clean) out '   ok: pass A completed with no finding' ;;
        'findings '*)
          # A finding of pass A (a refusal in its second epoch) is for a fix
          # task before pass B: the lead's word that it is merged is recorded.
          out "   pass A completed with ${_rec#findings } finding(s): each needs its fix merged before pass B"
          affirm "Lead: every finding of pass A (${_rec#findings }) has its fix merged" yes || return 0
          ;;
        incomplete) fail 'the record of pass A does not say the pass completed'; return 0 ;;
        *) fail 'the record of pass A does not say how many findings it had'; return 0 ;;
      esac
    fi
    fact facts.txt "$PASSA/result.txt"
  fi
  result ok
  done_step
}

step_3() {
  step_head 3 'The host and the build' \
    "print the tool versions, the sysctl dev.tty.legacy_tiocsti, lingering and the checkout's commit; check that each binary in $BIN that holds canary mode names the build tag in --version; record every binary's hash" \
    'each value printed; a miss stops the run' 'G8'
  wait_key 'Step 3 runs version commands.'
  runcap systemd-version systemctl --version
  check 'systemctl answers' is_rc 0 || return 0
  runcap git-version git --version
  check 'git answers' is_rc 0 || return 0
  runcap bwrap-version bwrap --version
  check 'bubblewrap answers' is_rc 0 || return 0
  runcap pasta-version pasta --version
  check 'pasta answers' is_rc 0 || return 0
  runcap tiocsti cat /proc/sys/dev/tty/legacy_tiocsti
  if [ "$DRY" -eq 0 ]; then _v=$(cat "$LASTOUT"); else _v=0; fi
  check 'the sysctl dev.tty.legacy_tiocsti is 0' is_eq "$_v" 0 || return 0
  runcap linger loginctl show-user "$ME" --property=Linger
  check 'lingering can be read' is_rc 0 || return 0
  if [ "$DRY" -eq 0 ]; then LINGER0=$(cat "$LASTOUT"); fi
  runcap commit git -C "$REPO" rev-parse HEAD
  check 'the checkout has a commit' is_rc 0 || return 0
  if [ "$DRY" -eq 0 ]; then _head=$(cat "$LASTOUT"); else _head=; fi
  runcap branch git -C "$REPO" rev-parse --abbrev-ref HEAD
  runcap worktree git -C "$REPO" status --porcelain
  check 'the work tree can be read' is_rc 0 || return 0
  _wtf=$LASTOUT
  runcap develop git -C "$REPO" rev-parse --verify 'develop^{commit}'
  check 'the branch develop can be read' is_rc 0 || return 0
  if [ "$DRY" -eq 0 ]; then _dev=$(cat "$LASTOUT"); else _dev=; fi
  check "the checkout's commit is the commit of develop ($_dev)" is_eq "$_head" "$_dev" || return 0
  if [ "$DRY" -eq 0 ]; then
    _un=$(untracked_only "$_wtf")
    if [ -n "$_un" ]; then
      printf '%s\n' "$_un" | sed 's/^/   untracked (reported, not refused): /'
      note "untracked entries: $(printf '%s' "$_un" | tr '\n' ' ')"
    fi
    _tr=$(tracked_changes "$_wtf")
    if [ -n "$_tr" ]; then printf '%s\n' "$_tr" | sed 's/^/   changed: /'; fi
  else
    _tr=
  fi
  check 'the work tree has no change to a tracked file' is_eq "$_tr" '' || return 0
  for _b in ataniqd ataniq-holder ataniq-canary ataniq-canary-standin; do
    runcap "version-$_b" "$BIN/$_b" --version
    check "$_b names the build tag in --version" grep -q "$TAG" "$LASTOUT" || return 0
  done
  for _b in ataniqd ataniq-holder ataniq-canary ataniq-canary-standin ataniq-console ataniq-intake ataniq-probe; do
    fact binaries.txt "$BIN/$_b"
  done
  check "the config's holder is the built one" is_eq "$HOLDEREXE" "$BIN/ataniq-holder" || return 0
  result ok
  done_step
}

step_4() {
  if [ "$PASS" = A ]; then _does='hash the intake template; ask for the review on file, for the owner having read the template, and for the report of qualify-mirror, which the owner runs once himself with the lead present'
  else _does="hash the intake template and compare it with pass A's; reuse pass A's record when the hash is unchanged, else as in pass A"; fi
  step_head 4 'The intake template' "$_does" \
    "the run passes; pass B reuses the record if the template's hash is unchanged" 'G6'
  wait_key 'Step 4 hashes the template.'
  check 'no /ataniq exists on the host (the note of the template)' is_absent /ataniq || return 0
  if [ "$DRY" -eq 1 ]; then
    dry "write the hash of $TEMPLATE to $EV/template.sha256"
    if [ "$PASS" = B ]; then dry "compare it with template.sha256 in pass A's evidence folder; when equal, record the reuse and end the step"; fi
  else
    sha256sum <"$TEMPLATE" | awk '{print $1}' >"$EV/template.sha256"
    out "   template hash: $(cat "$EV/template.sha256")"
    if [ "$PASS" = B ] && [ -f "$PASSA/template.sha256" ] && cmp -s "$EV/template.sha256" "$PASSA/template.sha256"; then
      result "reused: the template's hash is the one of pass A"
      done_step
      return 0
    fi
  fi
  ask "Lead: the path of the template's review on file"
  check 'the review is present' test -f "$ANSWER" || return 0
  fact template-records.txt "$ANSWER"
  affirm 'Owner: you have read the template and its note' read || return 0
  out '   Owner: run qualify-mirror once now, yourself, in your own terminal, with the lead present'
  out '   (docs/qualification-lead-mirror.md), with this template and a new output file.'
  ask 'Owner: the path of the report qualify-mirror wrote'
  check 'the report is present' test -f "$ANSWER" || return 0
  fact template-records.txt "$ANSWER"
  affirm 'Lead: the qualify-mirror run passed' yes || return 0
  result ok
  done_step
}

step_5() {
  if [ "$PASS" = A ]; then
    skip 5 "Revocation: the owner confirms the provider's account page can revoke a login" 'G2'
    return 0
  fi
  step_head 5 'Revocation' \
    "the owner opens the provider's account page himself and confirms that it lists logins or sessions and can revoke one server-side, of the kind the sandbox login will make; the script opens no browser" \
    '"confirmed", with the time; anything else ends the pass here' 'G2'
  ask 'Owner: type confirmed when the account page lists logins and can revoke one server-side; anything else ends the pass'
  check 'the owner confirmed server-side revocation' is_eq "$ANSWER" confirmed || return 0
  result confirmed
  done_step
}

# listeners writes the non-loopback listening sockets, sorted, to a file.
listeners() {
  runcap "$1" ss -ltnu
  if [ "$DRY" -eq 1 ]; then
    dry "keep the sockets on non-loopback addresses, sorted, in $2"
    return 0
  fi
  awk 'NR > 1 && $5 !~ /^(127\.|\[::1\]|\[::ffff:127\.)/ { print $1, $5 }' "$LASTOUT" | LC_ALL=C sort >"$2"
}

step_6() {
  if [ "$PASS" = A ]; then
    skip 6 "Listeners: list the host's listening sockets; the owner stops the ones he marks" 'G7'
    return 0
  fi
  step_head 6 'Listeners' \
    "list the host's listening sockets on non-loopback addresses (ss -ltnu) into the evidence; the owner marks each as stop or keep and stops the marked ones himself; list again; write the departure line" \
    'the second list holds only those he kept; the stopped ones are noted for step 24; the departure line is in the evidence' 'G7'
  wait_key 'Step 6 lists the listening sockets.'
  listeners listeners-before "$EV/listeners-before.txt"
  if [ "$DRY" -eq 1 ]; then
    dry 'print the list with a number before each line'
  else
    awk '{ print "   " NR ": " $0 }' "$EV/listeners-before.txt"
  fi
  ask 'Owner: the numbers of the listeners you stop, with spaces between them, or none'
  if [ "$DRY" -eq 1 ]; then
    dry "write the marked lines to $EV/listeners-stopped.txt and the others to $EV/listeners-expected.txt"
  else
    _marked=" $ANSWER "
    : >"$EV/listeners-stopped.txt"
    : >"$EV/listeners-expected.txt"
    _n=0
    while IFS= read -r _l; do
      _n=$((_n + 1))
      case $_marked in
        *" $_n "*) printf '%s\n' "$_l" >>"$EV/listeners-stopped.txt" ;;
        *) printf '%s\n' "$_l" >>"$EV/listeners-expected.txt" ;;
      esac
    done <"$EV/listeners-before.txt"
  fi
  wait_key 'Owner: stop the listeners you marked, yourself. The script stops nothing.'
  listeners listeners-kept "$EV/listeners-kept.txt"
  check 'the second list holds only the listeners the owner kept' cmp -s "$EV/listeners-expected.txt" "$EV/listeners-kept.txt" || return 0
  _line='departure from L3: this pass runs on the open network of the profile as built; the owner accepted it under G7'
  if [ "$DRY" -eq 1 ]; then
    dry "write to $EV/departures.txt: $_line"
  else
    printf '%s %s\n' "$(now)" "$_line" >>"$EV/departures.txt"
  fi
  result ok
  done_step
}

step_7() {
  if [ "$PASS" = A ]; then
    skip 7 "Manifest, before: names, sizes, times and hashes of the live runtime's real config folder" 'G2'
    return 0
  fi
  step_head 7 'Manifest, before' \
    "ask for the live runtime's real config folder and write names, sizes, times and hashes of its files; no content is read into the evidence" \
    'written to the evidence' 'G2'
  wait_key 'Step 7 hashes the files of the folder you name.'
  ask "Owner: the path of the live runtime's real config folder"
  check 'the folder is present' test -d "$ANSWER" || return 0
  if [ "$DRY" -eq 0 ]; then printf '%s\n' "$ANSWER" >"$EV/live-config-folder.txt"; fi
  manifest_of "$ANSWER" "$EV/manifest-before.txt"
  result written
  done_step
}

step_8() {
  if [ "$PASS" = A ]; then _exp2='; the replay screens are in the lead'"'"'s home and the switch file is not'; else _exp2=; fi
  step_head 8 "Provision the lead's folders" \
    "check that the mirror exists; run ataniq-canary provision; check the tree's remote, the roots and the marker; check the sandbox inputs file with ataniq-canary check-inputs" \
    "the folders exist; the tree's only remote is the mirror's place inside the sandbox; the state and runtime roots are not the defaults; the marker is present; the project is throwaway-one$_exp2" 'G5'
  wait_key 'Step 8 provisions the folders.'
  check "the mirror exists (ataniq-intake init, then the upstream branch): $MIRROR" test -d "$MIRROR" || return 0
  runcap mirror-branches "$BIN/ataniq-intake" branches --state "$STATE" --project "$PROJECT"
  check 'the mirror can be listed' is_rc 0 || return 0
  check "the mirror lists the upstream branch $UPSTREAM of the throwaway repository" lists_upstream "$LASTOUT" "$UPSTREAM" || return 0
  PROVISIONED=1
  runcap provision "$BIN/ataniq-canary" provision --config "$CONFIG"
  check 'provisioning succeeded' is_rc 0 || return 0
  check 'the project is throwaway-one' is_eq "$PROJECT" throwaway-one || return 0
  check 'the marker is present' test -f "$MARKER" || return 0
  check 'the state root is not the default' test "$STATE" != "${XDG_STATE_HOME:-${HOME:-}/.local/state}/ataniq" || return 0
  check 'the runtime root is not the default' test "$RUNTIME" != "${XDG_RUNTIME_DIR:-}/ataniq" || return 0
  check "the holder's runtime folder exists" test -d "$HRUN" || return 0
  # No lead has run in the tree yet, so git may read it here and only here.
  runcap tree-remotes git -C "$TREE" remote
  if [ "$DRY" -eq 0 ]; then _n=$(awk 'END { print NR }' "$LASTOUT"); _r=$(cat "$LASTOUT"); else _n=1; _r=origin; fi
  check 'the tree has one remote' is_eq "$_n" 1 || return 0
  runcap tree-remote-url git -C "$TREE" remote get-url "$_r"
  if [ "$DRY" -eq 0 ]; then _u=$(cat "$LASTOUT"); else _u=$(mirror_inside "$PROJECT"); fi
  check "the tree's only remote is the mirror's place inside the sandbox ($(mirror_inside "$PROJECT"))" remote_is_mirror "$_u" "$PROJECT" || return 0
  if [ "$PASS" = A ]; then
    for _f in 1.json 1.txt 2.json 2.txt; do
      check "the replay screen $_f is in the lead's home" test -f "$REPLAY/$_f" || return 0
    done
    check 'the replay switch file is not there' is_absent "$SWITCH" || return 0
  fi
  out "   the sandbox inputs file is $INPUTS; the sources it must name:"
  printf '%s\n' "$CFG_SHOW" | while IFS= read -r _l; do
    case $_l in source_*) out "     $_l" ;; esac
  done
  wait_key 'Owner: write the sandbox inputs file now if it is not written (README.md has a filled example), mode 0600.'
  runcap check-inputs "$BIN/ataniq-canary" check-inputs --config "$CONFIG" --inputs "$INPUTS"
  check 'the sandbox inputs match the config' is_rc 0 || return 0
  for _k in executables.pasta executables.bwrap executables.probe; do
    if [ "$DRY" -eq 1 ]; then
      dry "record the name, size, time and hash of the inputs file's $_k"
    else
      fact sandbox-executables.txt "$(fld "$INPUTS" "$_k")"
    fi
  done
  result ok
  done_step
}

# agent_host prints the host path of the agent executable: the inputs file's
# install folder, and below it what the config's first argument names inside.
agent_host() {
  out "$(fld "$INPUTS" sources.agent_install)/${AGENT0#"$AGENT_INSIDE"/}"
}

step_9() {
  if [ "$PASS" = A ]; then
    step_head 9 'Version check: hash the stand-in' \
      "hash the agent executable in the inputs file's install folder and the stand-in of $BIN" \
      'the hash is the one make canary-build produced; both values are recorded; a mismatch refuses the start' 'G3, M9'
    wait_key 'Step 9 hashes the stand-in.'
    if [ "$DRY" -eq 1 ]; then
      dry "hash the agent executable (the install folder of $INPUTS, then ${AGENT0#"$AGENT_INSIDE"/}) and $BIN/ataniq-canary-standin, and record both"
      _a=x _b=x
    else
      _host=$(agent_host)
      check 'the agent executable is present' test -f "$_host" || return 0
      _a=$(sha256sum <"$_host" | awk '{print $1}')
      _b=$(sha256sum <"$BIN/ataniq-canary-standin" | awk '{print $1}')
      printf 'agent %s\nbuilt %s\n' "$_a" "$_b" >"$EV/standin-hash.txt"
      out "   agent: $_a"
      out "   built: $_b"
    fi
    check 'the agent is the stand-in that make canary-build produced' is_eq "$_a" "$_b" || return 0
  else
    step_head 9 'Version check: the bound agent executable' \
      'run the bound agent executable with --version, outside the sandbox, with an empty environment and a home that is an empty temporary folder' \
      "the output holds the config's version ($RVERSION) as a whole word, and the install folder is the canary's own; both values are recorded; a mismatch refuses the start (the fallback is the config change of G3)" 'G3, M9'
    wait_key 'Step 9 runs the agent executable with --version, outside the sandbox.'
    if [ "$DRY" -eq 1 ]; then
      dry "make an empty temporary folder as the home; run: env -i HOME=<that folder> <install folder of $INPUTS>/${AGENT0#"$AGENT_INSIDE"/} --version; remove the folder"
    else
      _host=$(agent_host)
      check 'the agent executable is present' test -f "$_host" || return 0
      _tmp=$(mktemp -d)
      runcap agent-version env -i HOME="$_tmp" "$_host" --version
      rmdir -- "$_tmp" 2>/dev/null || out "   note: the version check left files in $_tmp; look at them and remove the folder yourself"
      out "   install folder: ${_host%/"${AGENT0#"$AGENT_INSIDE"/}"}"
    fi
    check "the output holds $RVERSION as a whole word" grep -qwF -- "$RVERSION" "$LASTOUT" || return 0
    affirm "Owner: the install folder is the canary's own, not the one your sessions use" yes || return 0
  fi
  result ok
  done_step
}

step_10() {
  step_head 10 'The owner starts the daemon' \
    "the owner starts the daemon with --canary by hand, in his own terminal; the script then asks its status" \
    'it listens; status shows no role; hello lists the canary operations' 'G4'
  out '   Owner: in your own terminal, run:'
  out "     $BIN/ataniqd run --canary $CONFIG"
  wait_key 'Owner: start the daemon now, yourself; then come back here.'
  DAEMON_UP=1
  runcap daemon-status "$BIN/ataniqd" status --socket "$SOCK"
  check 'the daemon listens' is_rc 0 || return 0
  if [ "$DRY" -eq 1 ]; then
    dry 'check: the status shows no role (no line that starts with role=)'
  elif grep -q '^role=' "$LASTOUT"; then
    fail 'the status shows a role'
    return 0
  fi
  runcap daemon-status-json "$BIN/ataniqd" status --socket "$SOCK" --json
  affirm "Lead: the daemon's start line and hello list the canary operations (define, start, ticket, inject, stop)" yes || return 0
  result ok
  done_step
}

# read_limits reads the three limits both ways. It sets M1 S1 T1 (systemd's
# show, normalised) and M2 S2 T2 (the control group's files).
read_limits() {
  M1=$(norm "$(show_prop MemoryMax)")
  S1=$(norm "$(show_prop MemorySwapMax)")
  T1=$(norm "$(show_prop TasksMax)")
  CG=/sys/fs/cgroup$(show_prop ControlGroup)
  M2=$(norm "$(cat "$CG/memory.max")")
  S2=$(norm "$(cat "$CG/memory.swap.max")")
  T2=$(norm "$(cat "$CG/pids.max")")
  out "   limits by systemctl --user show: MemoryMax=$M1 MemorySwapMax=$S1 TasksMax=$T1"
  out "   limits in $CG: memory.max=$M2 memory.swap.max=$S2 pids.max=$T2"
  note "limits: show $M1 $S1 $T1; control group $M2 $S2 $T2"
}

# step_11 is one step with one key: define, start, the read-back of the three
# limits and the balancing of the memory limit follow each other with no pause.
step_11() {
  step_head 11 'define, start, read the limits back, balance the memory limit' \
    "define, then start; then at once, with no pause: read the unit's three limits back from systemctl --user show (normalised to bytes and counts) and from memory.max, memory.swap.max and pids.max of the unit's control group; set MemoryMax to the smaller of the ceiling ($MAXB) and $SHARE percent of MemAvailable, rounded down, with systemctl --user set-property --runtime; read it back both ways" \
    "the role is starting; the unit is active; the three limits equal the config's ($MAXB, $SWAPB, $TASKS) in both readings; the balanced value is at or below the ceiling and at or above the floor ($FLOORB), and both readings equal it; if any of this fails, the clean-up chain runs" 'G4, M6'
  wait_key "Step 11 starts the unit $UNIT. It is one step: there is no pause until the limit is balanced and read back."
  runcap define "$BIN/ataniqd" canary define --config "$CONFIG"
  if [ "$EPOCH_NO" -eq 2 ] && [ "$DRY" -eq 0 ] && [ "$RC" -eq 30 ]; then
    out '   the role is defined already: the second epoch keeps the definition'
    note 'define refused in the second epoch: the role is defined'
  else
    check 'define succeeded' is_rc 0 || return 0
  fi
  STARTED=1
  STOPPED=0
  runcap start "$BIN/ataniqd" canary start --config "$CONFIG"
  check 'start succeeded' is_rc 0 || return 0
  if [ "$DRY" -eq 1 ]; then
    dry 'check: the start result names the role state starting, this unit and the waiver'
    dry 'keep the epoch the start result names: step 12 reads the records of that epoch only'
    dry "run: systemctl --user show $UNIT --property=ActiveState --value"
    dry 'check: the unit is active'
    dry "read MemoryMax, MemorySwapMax, TasksMax and ControlGroup with systemctl --user show $UNIT, normalised to bytes and counts"
    dry "read memory.max, memory.swap.max and pids.max in the unit's control group folder under /sys/fs/cgroup"
    dry "check: the three limits equal the config's in both readings"
    dry 'read MemAvailable from /proc/meminfo, the memory in use in the slice and the number of other scopes in it'
    _sample=6291456
    dry "compute the balanced value: with $_sample kB available (a sample), min($MAXB, $SHARE% of available) rounded down = $(balance "$_sample" "$MAXB" "$SHARE") bytes"
    dry "go to the clean-up chain when the value is below the floor $FLOORB: the host is too busy"
    dry "run: systemctl --user set-property --runtime $UNIT MemoryMax=<bytes>"
    dry 'check: both readings of the memory limit equal the balanced value, which is at or below the ceiling and at or above the floor'
    result ok
    done_step
    return 0
  fi
  START_OUT=$LASTOUT
  _state=$(fld "$START_OUT" state)
  case $_state in
    starting | running) out "   ok: the role is $_state" ;;
    *) fail "the role is \"$_state\", not starting"; return 0 ;;
  esac
  check 'the start result names this unit' is_eq "$(fld "$START_OUT" unit)" "$UNIT" || return 0
  check 'the start result shows the waiver in full' is_eq "$(fld "$START_OUT" waiver)" "$WAIVER" || return 0
  # The epoch of this start: step 12 reads the records of this epoch only.
  START_EPOCH=$(fld "$START_OUT" epoch)
  check 'the start result names its epoch' is_number "$START_EPOCH" || return 0
  _active=$(show_prop ActiveState)
  case $_active in
    active | activating) out "   ok: the unit is $_active" ;;
    *) fail "the unit is \"$_active\", not active"; return 0 ;;
  esac
  read_limits
  check 'MemoryMax is the config, by systemd' is_eq "$M1" "$MAXB" || return 0
  check 'memory.max is the config, in the control group' is_eq "$M2" "$(pagefloor "$MAXB")" || return 0
  check 'MemorySwapMax is the config, by systemd' is_eq "$S1" "$SWAPB" || return 0
  check 'memory.swap.max is the config, in the control group' is_eq "$S2" "$(pagefloor "$SWAPB")" || return 0
  check 'TasksMax is the config, by systemd' is_eq "$T1" "$TASKS" || return 0
  check 'pids.max is the config, in the control group' is_eq "$T2" "$TASKS" || return 0
  _avail=$(awk '$1 == "MemAvailable:" { print $2 }' /proc/meminfo)
  case $_avail in '' | *[!0-9]*) fail 'MemAvailable cannot be read'; return 0 ;; esac
  _slice=${CG%/*}
  _used=$(cat "$_slice/memory.current" 2>/dev/null || out unknown)
  _others=$(find "$_slice" -mindepth 1 -maxdepth 1 -type d -name '*.scope' | awk 'END { print NR }')
  BAL=$(balance "$_avail" "$MAXB" "$SHARE")
  out "   balancing: MemAvailable=${_avail} kB, in use in the slice=$_used, other scopes in the slice=$_others, ceiling=$MAXB, floor=$FLOORB, share=$SHARE%, value=$BAL"
  note "balancing: available_kb=$_avail slice_current=$_used other_scopes=$_others ceiling=$MAXB floor=$FLOORB share=$SHARE value=$BAL"
  if below_floor "$BAL" "$FLOORB"; then
    fail "the balanced value $BAL is below the floor $FLOORB: the host is too busy; the unit is stopped by the clean-up chain and the pass ends"
    return 0
  fi
  check 'the balanced value is at or below the ceiling' test "$BAL" -le "$MAXB" || return 0
  runcap set-memory systemctl --user set-property --runtime "$UNIT" "MemoryMax=$BAL"
  check 'set-property succeeded' is_rc 0 || return 0
  read_limits
  check 'MemoryMax is the balanced value, by systemd' is_eq "$M1" "$BAL" || return 0
  check 'memory.max is the balanced value, in the control group' is_eq "$M2" "$BAL" || return 0
  result "ok: MemoryMax=$BAL"
  done_step
}

step_12() {
  step_head 12 "Read the holder's start record" \
    "read holder.json and the event log in the holder's state folder, and the start line of the daemon's evidence file" \
    'the probe reported once; every assertion is ok except 10, which is waived by name; the profile is version 4 with the pinned digest; the waiver shows in holder.json, the event log, the evidence and the status line' 'G5'
  wait_key "Step 12 reads the holder's files."
  if [ "$DRY" -eq 1 ]; then
    dry "wait until $HSTATE/holder.json names the epoch of the start of step 11 (the folder still holds the last epoch's file until the new holder writes its own); fail when it never does"
    dry "print the values of $HSTATE/holder.json with ataniq-canary fields"
    dry "check: its field waiver is exactly: $WAIVER"
    dry "check: a line of $HSTATE/events.jsonl with that epoch holds the state sandbox-started with the reason assertion-10-waived"
    dry "check: the start line of $EVIDENCE with that epoch holds the waiver"
    dry 'check: the result of start (step 11) showed the waiver'
  else
    # The state folder is the same in every epoch, so each record is matched
    # by the epoch of this start: a file or a line of an earlier epoch does
    # not pass for this one.
    _try=0
    while [ "$(fld "$HSTATE/holder.json" epoch 2>/dev/null)" != "$START_EPOCH" ] && [ "$_try" -lt 30 ]; do
      _try=$((_try + 1))
      sleep 1
    done
    runcap holder-json "$BIN/ataniq-canary" fields --file "$HSTATE/holder.json"
    check 'holder.json can be read' is_rc 0 || return 0
    check "holder.json is the one of this start: its epoch is $START_EPOCH" is_eq "$(fld "$HSTATE/holder.json" epoch)" "$START_EPOCH" || return 0
    check 'holder.json shows the waiver in full' is_eq "$(fld "$HSTATE/holder.json" waiver)" "$WAIVER" || return 0
    _try=0
    while ! has_line "$HSTATE/events.jsonl" "epoch=$START_EPOCH" 'state=sandbox-started' && [ "$_try" -lt 30 ]; do
      _try=$((_try + 1))
      sleep 1
    done
    check "the event log shows the sandbox started in epoch $START_EPOCH with assertion 10 waived" \
      has_line "$HSTATE/events.jsonl" "epoch=$START_EPOCH" 'state=sandbox-started' 'reason=assertion-10-waived' || return 0
    check "the daemon's evidence shows the waiver on the start line of epoch $START_EPOCH" \
      has_line "$EVIDENCE" "epoch=$START_EPOCH" 'event=start' "waiver=$WAIVER" || return 0
  fi
  if [ "$DRY" -eq 1 ]; then
    dry "check: holder.json says the sandbox started, has profile_version $PROFILE_VERSION and has the profile_digest of the config's holder.profile_digest (the pinned digest)"
  else
    _hv=$(holder_start_verdict "$(fld_all "$HSTATE/holder.json")" "$PROFILE_VERSION" "$(fld "$CONFIG" holder.profile_digest)")
    case $_hv in
      'ok '*)
        out "   ok: ${_hv#ok }"
        note "ok: holder.json: ${_hv#ok }"
        ;;
      *)
        fail "${_hv#bad }"
        return 0
        ;;
    esac
  fi
  # What the check above covers: the holder writes sandbox_started=true only
  # after its own judgement of the probe's report passed with every assertion
  # required (a result that is failed, not checked or missing is a problem; only
  # assertion 10, and only in canary mode, is excused, with the waiver). What it
  # does not cover is the report itself: holder.json keeps no result, and this
  # script reads no file that shows the probe reported once. The lead's word
  # stays as a reading of the probe's report, a second one next to the holder's.
  affirm 'Lead: the probe reported once and every assertion is ok except 10' yes || return 0
  result ok
  done_step
}

step_13() {
  if [ "$PASS" = B ]; then
    skip 13 'Try a second start' 'none'
    return 0
  fi
  step_head 13 'Try a second start' 'run ataniqd canary start again' 'refused at the registry (exit status 30)' 'none'
  wait_key 'Step 13 asks for a second start, which must be refused.'
  runcap second-start "$BIN/ataniqd" canary start --config "$CONFIG"
  check 'the second start is refused (exit 30)' is_rc 30 || return 0
  result refused
  done_step
}

step_14() {
  step_head 14 'Mint a writer ticket and attach the console' \
    'run ataniqd canary ticket for a writer; the owner attaches the console himself in his own terminal, then tries the used ticket once more' \
    'the pane shows the agent; a used ticket is refused' 'none'
  wait_key 'Step 14 mints a writer ticket.'
  runcap ticket "$BIN/ataniqd" canary ticket --config "$CONFIG" --attach-role writer
  check 'the ticket was minted' is_rc 0 || return 0
  if [ "$DRY" -eq 1 ]; then _tf=$HRUN/ticket; else _tf=$(fld "$LASTOUT" ticket_file); fi
  out '   Owner: in your own terminal, attach the console as the writer:'
  out "     $BIN/ataniq-console --attach $HRUN --ticket-file $_tf"
  affirm 'Owner: the pane shows the agent' yes || return 0
  out '   Owner: in a third terminal, run the same command once more: the ticket is used.'
  affirm 'Owner: the used ticket was refused' yes || return 0
  result ok
  done_step
}

# over_cap reports whether pass B met its cap.
over_cap() {
  if [ "$PASS" != B ] || [ "$DRY" -eq 1 ] || [ "$T0" -eq 0 ]; then return 1; fi
  if [ $(($(date +%s) - T0)) -ge "$CAP_SECONDS" ] || [ "$TURNS" -ge "$CAP_TURNS" ]; then
    CAPPED=1
    out "   the cap is met ($TURNS turns, $(($(date +%s) - T0)) s): the pass goes to step 21"
    note "cap met: turns=$TURNS"
    return 0
  fi
  return 1
}

step_15() {
  if [ "$PASS" = A ]; then
    skip 15 'Model, cap and login: the owner confirms the model and logs in inside the sandbox' 'G2'
    return 0
  fi
  step_head 15 'Model, cap and login' \
    "print the agent's arguments for the owner to confirm the model is the cheapest; start the 30-minute timer; the owner logs in inside the sandbox, in the pane; list the files that changed in the canary's config folder, by name" \
    "the login writes only the canary's config folder: the script lists the files that changed there, by name" 'G2'
  out "   the agent's arguments, from the config:"
  printf '%s\n' "$CFG_SHOW" | while IFS= read -r _l; do
    case $_l in agent_argv_*) out "     $_l" ;; esac
  done
  affirm 'Owner: the model argument is the cheapest model' yes || return 0
  manifest_of "$LCONFIG" "$EV/canary-config-before.txt"
  if [ "$DRY" -eq 1 ]; then
    dry "start the timer of $CAP_SECONDS s and the tally of $CAP_TURNS turns"
  else
    T0=$(date +%s)
    note "the cap's timer starts"
  fi
  out '   Owner: log in inside the sandbox now, in the pane, yourself. The script holds no credential and opens no browser.'
  wait_key 'Owner: press Enter here when the login is done.'
  manifest_of "$LCONFIG" "$EV/canary-config-after.txt"
  if [ "$DRY" -eq 1 ]; then
    dry "list by name the files of $LCONFIG that the login changed (names only)"
  else
    out "   files the login changed in $LCONFIG, by name:"
    awk 'NR == FNR { before[$0] = 1; next } !($0 in before) { $1 = $2 = $3 = ""; sub(/^ +/, ""); print "     " $0 }' \
      "$EV/canary-config-before.txt" "$EV/canary-config-after.txt"
  fi
  result ok
  done_step
}

step_16() {
  if [ "$PASS" = A ]; then
    skip 16 'Wait for the idle composer, and look at the report path' 'B1'
    return 0
  fi
  step_head 16 'Wait for the idle composer, and look at the report path' \
    "ask the daemon's status; the lead reads the holder's status and its 7501 counters in the console and the script records what he reads: while the holder runs the counters are only in its status answer, which no command of the kit reads, and the event log holds none; the script prints them itself from final.json after the child ends (step 22)" \
    "the holder's idle field reads unknown in this feature, and the answer is recorded, not judged (the fence judges idle itself inside a delivery); the OSC 7501 query was answered before the DA1 reply, and at least one report was tracked; if no report ever arrives, that is recorded: the fence will then end uncertain, and the reason is known; an update notice on the screen is recorded if it shows (L9)" 'B1'
  wait_key 'Step 16 reads the status.'
  runcap role-status "$BIN/ataniqd" status --socket "$SOCK"
  if [ "$DRY" -eq 1 ]; then
    dry "say plainly where the 7501 counters are read: while the holder runs, only in its status answer, which no command of the kit reads (the event log holds no counters), so the lead records what the console shows; after the child ends, in $HSTATE/final.json, which step 22 prints"
  else
    out '   Where the 7501 counters (queries, reports, unreadable payloads) are read:'
    out '   - while the holder runs they are only in its status answer. No command of the kit reads'
    out '     that answer, and the event log holds no counters: the script cannot show them now.'
    out '     Lead: read them in the console and answer below; the script records what you read.'
    out "   - after the child ends they are in $HSTATE/final.json: step 22 prints them from there."
    note 'the 7501 counters are not read by the script at this step: the lead records what the console shows'
  fi
  ask "Lead: what does the holder's idle field read (the holder reports unknown in this feature; the fence judges idle itself inside a delivery)"
  note "holder idle field: $ANSWER"
  ask 'Lead: was the OSC 7501 query answered before the DA1 reply (yes, no, unknown)'
  ask 'Lead: how many reports were tracked (a number, or none: then the fence will end uncertain and the reason is known)'
  ask 'Lead: does the screen show an update notice (no, or what it says in your words)'
  result ok
  done_step
}

# make_message writes the body of the one message of an epoch: one named file
# with one token, committed on one named branch.
make_message() {
  if [ "$DRY" -eq 1 ]; then
    TOKEN='canary-<date>-<epoch>'
    BODY=$EV/message-$EPOCH_NO.txt
    MSGID='canary-<pass>-<epoch>-<date>'
    dry "write the body to $BODY: the file canary-token.txt, the branch $BRANCH, and the line \"canary-token: $TOKEN\""
    return 0
  fi
  _low=$(printf '%s' "$STAMP" | tr '[:upper:]' '[:lower:]')
  TOKEN=canary-$_low-$EPOCH_NO
  MSGID=canary-$(printf '%s' "$PASS" | tr '[:upper:]' '[:lower:]')-$EPOCH_NO-$_low
  BODY=$EV/message-$EPOCH_NO.txt
  printf '%s\n%s\n' "Write the token below and a line feed to the file canary-token.txt at the top of the tree, and commit that one file on the branch $BRANCH. Do nothing else." \
    "canary-token: $TOKEN" >"$BODY"
}

# paste_message tells the owner how to put the message into the pane himself.
# It must arrive as one bracketed paste followed by one Enter: the stand-in
# commits only from a paste, and the body has two lines, so typed line by line
# the first Enter would submit half of it to a live agent.
paste_message() {
  out '   Owner: put the message into the pane yourself. Do NOT type it. Copy the whole text of this file:'
  out "     $BODY"
  out '   and paste it into the pane with your terminal'"'"'s paste, so that it arrives as ONE paste'
  out '   (a bracketed paste; typed line by line, the first Enter would send half of it).'
  out '   Then press Enter in the pane ONCE. The text of the file, to compare with what you paste:'
  sed 's/^/     | /' "$BODY"
}

step_17() {
  if [ "$PASS" = A ]; then
    _exp='the config declares the tested version and the stand-in draws a screen no recogniser knows, so the fence answers not-now with the reason not-ready and writes nothing (L6); the owner then pastes the message into the pane as one paste and presses Enter once; a refusal by the daemon (exit status 30) or by the fence (refused, untested: a wrong config) fails the step'
  else
    _exp='a refusal by the daemon (exit status 30) fails the step; otherwise the result is recorded in the neutral words; exactly one result gets the owner'"'"'s Enter: uncertain with the reason not-submitted and written partial, recorded as its own line owner-enter; on uncertain with the reason died nothing is pressed, the owner looks at the pane and decides; on the G3 fallback the fence refuses an untested version and the owner pastes the message as one paste'
  fi
  step_head 17 'inject one message' \
    "inject one message: make one named file with one token, commit it on the branch $BRANCH" "$_exp" 'G1, G3'
  if over_cap; then result 'not run: the cap'; return 0; fi
  make_message
  wait_key 'Step 17 injects the one message of this epoch.'
  runcap inject "$BIN/ataniqd" canary inject --config "$CONFIG" --message-id "$MSGID" --body-file "$BODY"
  if [ "$DRY" -eq 1 ]; then
    dry 'read outcome, reason and written of the result with ataniq-canary fields, and record them'
    if [ "$PASS" = A ]; then
      dry 'check: the result is not-now, not-ready, written nothing'
      dry 'fail the step on exit status 30 (the daemon refused the message) and on refused, untested (a wrong config: the version is not a tested one)'
      dry "print the file to paste from, $BODY, and its text; the owner pastes it into the pane with the terminal's paste, as one paste, and presses Enter once: he does not type it; wait"
    else
      dry 'fail the step on exit status 30 (the daemon refused the message: the fence was not asked)'
      dry 'on uncertain, not-submitted, partial and on nothing else: the owner presses Enter once in the pane; record the line owner-enter with the time'
      dry 'on uncertain, died: press nothing (the input may hold an open paste); the owner looks at the pane; record what he saw and his decision'
      dry "on any refusal: print the file to paste from, $BODY; the owner pastes the message himself, as one paste, and presses Enter once: he does not type it; that is recorded"
    fi
    result ok
    done_step
    return 0
  fi
  _o=''
  _r=''
  _w=''
  if [ "$RC" -eq 0 ]; then
    _o=$(fld "$LASTOUT" result.outcome)
    _r=$(fld "$LASTOUT" result.reason)
    _w=$(fld "$LASTOUT" result.written)
    out "   result: outcome=$_o reason=${_r:-none} written=$_w"
    note "inject result: outcome=$_o reason=${_r:-none} written=$_w"
  elif [ "$RC" -eq 30 ]; then
    # In both passes the daemon's refusal (admission, epoch or role) is not
    # the fence's answer, and must not read as one.
    fail 'the daemon refused the message (exit 30): the fence was not asked, so this is not the fence'"'"'s answer'
    return 0
  else
    fail "inject ended with exit status $RC"
    return 0
  fi
  _act=$(inject_action "$PASS" "$_o" "$_r" "$_w")
  if [ "$PASS" = A ]; then
    if [ "$_act" = wrong-config ]; then
      note 'wrong config: the fence refused an untested version'
      fail "a wrong config: the fence refused an untested version, so the config's agent.runtime_version is not a tested one (pass A declares a tested version)"
      return 0
    fi
    check 'the fence answered not-now, not-ready and wrote nothing (L6: an unknown screen)' is_eq "$_act" owner-supplies || return 0
    paste_message
    wait_key 'Owner: press Enter here when you have pasted and submitted the message.'
    note 'the owner pasted the message'
    result 'not-now, not-ready, nothing: the owner supplied the message'
    done_step
    return 0
  fi
  case $_act in
    none)
      result 'submitted, whole'
      ;;
    owner-enter)
      out '   Owner: press Enter ONCE in the pane now. This is the one result that gets your Enter.'
      wait_key 'Owner: press Enter here after you pressed Enter in the pane.'
      printf '%s owner-enter\n' "$(now)" >>"$EV/owner-enter.txt"
      note 'owner-enter'
      result 'uncertain, not-submitted, partial: owner-enter'
      ;;
    look)
      out '   Press NOTHING in the pane: the input may hold an open paste.'
      ask 'Owner: look at the pane. What do you see, in your words'
      ask 'Owner: what happens next is your decision: type go-on to continue the pass, anything else ends it and runs the clean-up chain'
      if [ "$ANSWER" != go-on ]; then
        fail 'the injection died and the owner ended the pass'
        return 0
      fi
      result 'uncertain, died: no key was pressed'
      ;;
    owner-supplies)
      out '   The fence refused. On the G3 fallback (the config states the observed version) that is correct.'
      paste_message
      ask 'Owner: type pasted here after you pasted the message in the pane and pressed Enter once; anything else ends the pass and runs the clean-up chain'
      if [ "$ANSWER" != pasted ]; then
        fail 'the fence refused and the owner ended the pass'
        return 0
      fi
      result "refused (${_r:-no reason}): the owner pasted the message"
      ;;
    *)
      out '   No key is pressed for this result.'
      ask 'Owner: type go-on to continue the pass, anything else ends it and runs the clean-up chain'
      if [ "$ANSWER" != go-on ]; then
        fail "the injection ended $_o/${_r:-none}/$_w and the owner ended the pass"
        return 0
      fi
      result "$_o, ${_r:-no reason}, $_w"
      ;;
  esac
  done_step
}

step_18() {
  if [ "$PASS" = A ]; then
    step_head 18 'The agent acts: the stand-in' \
      'the stand-in takes the token of the pasted message and commits; the owner looks at the pane' \
      'one commit on the branch in the clone (step 20 fetches it)' 'G2'
    affirm 'Owner: the pane shows that the stand-in committed' yes || return 0
    result ok
    done_step
    return 0
  fi
  step_head 18 'The agent acts' \
    "the owner answers any approval prompt himself, as the writer; the lead tallies each turn; the cap is $CAP_TURNS turns or $((CAP_SECONDS / 60)) minutes" \
    'one commit on the branch in the clone; at 10 turns or 30 minutes, go to step 21' 'G2'
  if over_cap; then result 'not run: the cap'; return 0; fi
  if [ "$DRY" -eq 1 ]; then
    dry 'ask after each turn: the lead types turn for one more turn, or done when the agent has committed; the script answers no prompt of the agent'
    dry "go to step 21 at $CAP_TURNS turns or $CAP_SECONDS s"
    result ok
    done_step
    return 0
  fi
  while :; do
    ask "Lead: type turn after each turn of the agent ($TURNS so far), or done when the agent has committed"
    case $ANSWER in
      turn) TURNS=$((TURNS + 1)) ;;
      done) break ;;
      *) out '   type turn or done' ;;
    esac
    if over_cap; then result "the cap: $TURNS turns"; return 0; fi
  done
  result "ok: $TURNS turns"
  done_step
}

step_19() {
  step_head 19 'Try a second inject' 'run ataniqd canary inject again, with a new message id' 'refused: one message per epoch (exit status 30)' 'G1'
  if [ "$CAPPED" -eq 1 ]; then result 'not run: the cap sent the pass to step 21'; return 0; fi
  wait_key 'Step 19 asks for a second message, which must be refused.'
  if [ "$DRY" -eq 1 ]; then _id2='<a second message id>'; else _id2=$MSGID-second; fi
  runcap second-inject "$BIN/ataniqd" canary inject --config "$CONFIG" --message-id "$_id2" --body-file "$BODY"
  check 'the second message is refused (exit 30)' is_rc 30 || return 0
  result refused
  done_step
}

step_20() {
  step_head 20 'The intake: fetch, review, verify, integrate' \
    "run ataniq-intake fetch, review and verify (the gate only checks that the file holds the token) with the canary's template; the owner signs off by typing integrate himself" \
    'the upstream branch moves, fast-forward only, to the commit; the records exist' 'G6'
  if [ "$CAPPED" -eq 1 ]; then result 'not run: the cap sent the pass to step 21'; return 0; fi
  wait_key 'Step 20 fetches from the lead'"'"'s clone in the sandbox of the template.'
  _rec=$EV/intake-records
  _work=$EV/intake-work
  if [ "$DRY" -eq 1 ]; then
    dry "make $_rec and $_work with mode 0700"
  else
    mkdir -m 0700 -- "$_rec" "$_work"
  fi
  check 'no /ataniq exists on the host (the note of the template)' is_absent /ataniq || return 0
  runcap fetch "$BIN/ataniq-intake" fetch --state "$STATE" --project "$PROJECT" --role "$LAYOUT" --sandbox "$TEMPLATE"
  check 'the fetch succeeded' is_rc 0 || return 0
  runcap branches "$BIN/ataniq-intake" branches --state "$STATE" --project "$PROJECT" --role "$LAYOUT"
  runcap review "$BIN/ataniq-intake" review --state "$STATE" --project "$PROJECT" --role "$LAYOUT" --branch "$BRANCH" --target "$UPSTREAM" --records "$_rec"
  check 'the review succeeded' is_rc 0 || return 0
  runcap verify "$BIN/ataniq-intake" verify --state "$STATE" --project "$PROJECT" --role "$LAYOUT" --branch "$BRANCH" \
    --gate "token=grep -qxF $TOKEN canary-token.txt" --sandbox "$TEMPLATE" --records "$_rec" --work "$_work"
  check 'the gate passed: the file holds the token' is_rc 0 || return 0
  out '   Owner: the sign-off is yours. In your own terminal, with the tip and the digest that review printed above:'
  out "     $BIN/ataniq-intake integrate --state $STATE --project $PROJECT --role $LAYOUT --branch $BRANCH --target $UPSTREAM \\"
  out "       --tip <TIP> --digest <DIGEST> --accept-change --records $_rec"
  out '   and one --accept-path for each path review listed under "Needs your sign-off".'
  affirm 'Owner: integrate succeeded' yes || return 0
  runcap branches-after "$BIN/ataniq-intake" branches --state "$STATE" --project "$PROJECT" --role "$LAYOUT"
  if [ "$DRY" -eq 0 ]; then
    _lead=$(lead_branch_id "$LASTOUT" "$LAYOUT" "$BRANCH")
    _up=$(upstream_branch_id "$LASTOUT" "$UPSTREAM")
  else
    _lead=
    _up=
  fi
  runcap upstream-tip git --git-dir "$MIRROR" rev-parse --verify "refs/heads/$UPSTREAM^{commit}"
  check "git reads the upstream branch $UPSTREAM in the mirror" is_rc 0 || return 0
  if [ "$DRY" -eq 0 ]; then _tip=$(cat "$LASTOUT"); else _tip=; fi
  check "the intake names the lead's branch $BRANCH" test -n "$_lead" || return 0
  check "the upstream branch $UPSTREAM is at the lead's commit ($_lead), by the intake's list" is_eq "$_up" "$_lead" || return 0
  check "the upstream branch $UPSTREAM is at the lead's commit, by git on the mirror" is_eq "$_tip" "$_lead" || return 0
  if [ "$DRY" -eq 1 ]; then
    dry "check: $_rec holds a verification record and an intake record"
  else
    out '   the records:'
    find "$_rec" -type f | LC_ALL=C sort | sed 's/^/     /'
    check 'a verification record exists' test -n "$(find "$_rec" -type f -name '*verification*' | sed -n 1p)" || return 0
  fi
  result ok
  done_step
}

step_21() {
  if [ "$PASS" = A ]; then
    skip 21 "Log out inside the sandbox, with the agent's own logout, typed by the owner" 'G2'
    return 0
  fi
  step_head 21 'Log out' \
    "the owner types the agent's own logout inside the sandbox, in the pane" 'the agent shows it is logged out' 'G2'
  if [ "$IN_CHAIN" -eq 1 ] && was_done 21; then
    wait_key 'This step ended earlier in this run.'
    result 'nothing to act on: the logout was done at step 21 of this run'
    return 0
  fi
  if [ "$IN_CHAIN" -eq 1 ] && [ "$STARTED" -eq 1 ] && [ "$STOPPED" -eq 1 ]; then
    wait_key 'The unit is stopped: there is no pane to type in.'
    result 'nothing to act on: the unit was stopped before a logout could be typed; step 23 revokes the login'
    return 0
  fi
  if [ "$DRY" -eq 1 ] && [ "$IN_CHAIN" -eq 1 ]; then dry 'say so and record it when the logout was done already, or the unit is stopped and there is no pane: nothing to act on'; fi
  ask "Owner: type the agent's own logout in the pane; then type logged-out here, or what you see instead"
  if [ "$DRY" -eq 0 ] && [ "$ANSWER" != logged-out ]; then
    fail "the agent does not show it is logged out: \"$ANSWER\" (step 23 revokes the login whatever this shows)"
    return 0
  fi
  result 'logged out'
  done_step
}

# stop_role stops the role and shows the proof. It is step 22's first half and
# the stop of the second epoch.
stop_role() {
  runcap stop "$BIN/ataniqd" canary stop --config "$CONFIG"
  check 'stop succeeded' is_rc 0 || return 1
  if [ "$DRY" -eq 1 ]; then
    dry 'check: the result is stopped, with a proof text naming the invocation and its empty control group'
    return 0
  fi
  check 'the role is stopped' is_eq "$(fld "$LASTOUT" state)" stopped || return 1
  _proof=$(fld "$LASTOUT" proof)
  out "   proof: $_proof"
  check 'the stop has a proof text' test -n "$_proof" || return 1
  STOPPED=1
}

step_22() {
  step_head 22 'stop, revert, and list what is left' \
    "run ataniqd canary stop; then systemctl --user revert on the unit; list the user manager's units and unit files for ataniq-*; list the unit's name under the user manager's transient/ and user.control/ runtime folders" \
    'stopped, with a proof text naming the invocation and its empty control group; no unit, no unit file and no drop-in remains, in either folder; lingering is as it was' 'G4'
  wait_key 'Step 22 stops the unit and lists what is left.'
  if [ "$STARTED" -eq 0 ] && [ "$DRY" -eq 0 ]; then
    out '   nothing to stop: no start was asked in this run. The lists below are still read.'
    note 'nothing to act on: no unit was started'
  elif [ "$STOPPED" -eq 1 ] && [ "$DRY" -eq 0 ]; then
    out '   nothing to stop: the role was stopped with proof already. The lists below are still read.'
    note 'nothing to act on: the role is stopped'
  else
    if [ "$DRY" -eq 1 ] && [ "$IN_CHAIN" -eq 1 ]; then dry 'say so and record it when no start was asked, or the role is stopped already: nothing to stop'; fi
    stop_role || :
  fi
  runcap revert systemctl --user revert "$UNIT"
  runcap units-after systemctl --user list-units --all --no-legend --plain 'ataniq-*'
  if [ "$DRY" -eq 0 ]; then awk '{print $1}' "$LASTOUT" | LC_ALL=C sort >"$EV/units-after-$EPOCH_NO.txt"; fi
  check 'no unit remains: the units are those of step 1' cmp -s "$EV/units-before.txt" "$EV/units-after-$EPOCH_NO.txt" || :
  runcap unit-files-after systemctl --user list-unit-files --no-legend 'ataniq-*'
  if [ "$DRY" -eq 0 ]; then awk '{print $1}' "$LASTOUT" | LC_ALL=C sort >"$EV/unit-files-after-$EPOCH_NO.txt"; fi
  check 'no unit file remains: the unit files are those of step 1' cmp -s "$EV/unit-files-before.txt" "$EV/unit-files-after-$EPOCH_NO.txt" || :
  for _folder in transient user.control; do
    _dir=${XDG_RUNTIME_DIR:-/nonexistent}/systemd/$_folder
    if [ "$DRY" -eq 1 ]; then
      dry "check: nothing named $UNIT or $UNIT.d is in $_dir"
    else
      check "nothing of the unit is in $_dir" test -z "$(find "$_dir" -maxdepth 1 -name "$UNIT*" 2>/dev/null | sed -n 1p)" || :
    fi
  done
  runcap linger-after loginctl show-user "$ME" --property=Linger
  if [ "$DRY" -eq 0 ]; then _l=$(cat "$LASTOUT"); else _l=$LINGER0; fi
  check 'lingering is as it was' is_eq "$_l" "$LINGER0" || :
  if [ "$DRY" -eq 1 ]; then
    dry "print the 7501 counters of the holder's final record, $HSTATE/final.json"
  elif [ -f "$HSTATE/final.json" ]; then
    out "   the 7501 counters of the holder's final record:"
    _fin=$("$BIN/ataniq-canary" fields --file "$HSTATE/final.json" 2>/dev/null || :)
    counters_7501 "$_fin" || out '     (none is named for 7501)'
  else
    out "   the holder left no final record ($HSTATE/final.json): there are no 7501 counters to print"
  fi
  result stopped
  done_step
  EPOCH_NO=1
}

step_23() {
  if [ "$PASS" = A ]; then
    skip 23 "Revoke: the owner revokes the login on the provider's account page" 'G2'
    return 0
  fi
  step_head 23 'Revoke' \
    "the owner revokes the login on the provider's account page, whatever the logout showed, and confirms it is gone from the list; the credential is treated as exposed" \
    '"revoked", with the time' 'G2'
  if [ "$IN_CHAIN" -eq 1 ] && was_done 23; then
    wait_key 'This step ended earlier in this run.'
    result 'nothing to act on: the login was revoked at step 23 of this run'
    return 0
  fi
  if [ "$DRY" -eq 1 ] && [ "$IN_CHAIN" -eq 1 ]; then dry 'say so and record it when the login was revoked already, or the owner answers that no login was made: nothing to act on'; fi
  ask 'Owner: revoke the login on the account page yourself; type revoked when it is gone from the list, or no-login when no login was ever made in this pass'
  case $ANSWER in
    revoked) result revoked ;;
    no-login) result 'nothing to act on: no login was made' ;;
    *)
      if [ "$DRY" -eq 1 ]; then
        result revoked
      else
        fail "the login is not confirmed revoked: \"$ANSWER\""
        return 0
      fi
      ;;
  esac
  done_step
}

step_24() {
  if [ "$PASS" = A ]; then
    skip 24 'Listeners, after: the owner restarts what he stopped; list again' 'G7'
    return 0
  fi
  step_head 24 'Listeners, after' \
    'the owner restarts what he stopped at step 6, himself; list the listening sockets again' \
    'the list equals the first list of step 6' 'G7'
  if [ "$DRY" -eq 0 ] && [ ! -f "$EV/listeners-before.txt" ]; then
    wait_key 'Step 6 did not list the listeners in this run.'
    result 'nothing to act on: no listener was listed or stopped in this run'
    return 0
  fi
  if [ "$DRY" -eq 1 ]; then
    if [ "$IN_CHAIN" -eq 1 ]; then dry 'say so and record it when step 6 listed nothing in this run: nothing to restore'; fi
    dry "print the listeners noted at step 6 ($EV/listeners-stopped.txt)"
  else
    out '   stopped at step 6:'
    sed 's/^/     /' "$EV/listeners-stopped.txt" 2>/dev/null || out '     (the list was not written: look at what you stopped)'
  fi
  wait_key 'Owner: restart what you stopped, yourself; then press Enter.'
  listeners listeners-after "$EV/listeners-after.txt"
  check 'the list equals the first list of step 6' cmp -s "$EV/listeners-before.txt" "$EV/listeners-after.txt" || :
  result restored
  done_step
}

step_25() {
  if [ "$PASS" = A ]; then
    skip 25 'Manifest, after: the same folder as step 7' 'G2'
    return 0
  fi
  step_head 25 'Manifest, after' \
    'write names, sizes, times and hashes of the same folder as step 7 and compare the two manifests' \
    'no difference; a difference is reported as it is; nothing is repaired by the script' 'G2'
  if [ "$DRY" -eq 0 ] && [ ! -f "$EV/manifest-before.txt" ]; then
    wait_key 'Step 7 wrote no manifest in this run.'
    result 'nothing to act on: no manifest was written before'
    return 0
  fi
  wait_key 'Step 25 hashes the files of the folder of step 7.'
  if [ "$DRY" -eq 1 ]; then
    if [ "$IN_CHAIN" -eq 1 ]; then dry 'say so and record it when step 7 wrote no manifest in this run: nothing to compare'; fi
    manifest_of '<the folder of step 7>' "$EV/manifest-after.txt"
    dry 'compare the two manifests; print the lines that differ (hash, size, time, name: no content); repair nothing'
    result 'no difference'
    done_step
    return 0
  fi
  manifest_of "$(cat "$EV/live-config-folder.txt")" "$EV/manifest-after.txt"
  if cmp -s "$EV/manifest-before.txt" "$EV/manifest-after.txt"; then
    result 'no difference'
  else
    out '   the manifests differ (hash, size, time, name). Nothing is repaired:'
    diff "$EV/manifest-before.txt" "$EV/manifest-after.txt" | sed 's/^/   | /' || :
    FINDINGS=$((FINDINGS + 1))
    result 'a difference, reported as it is'
  fi
  done_step
}

# keep copies one file of the canary's roots into the evidence, if it exists.
keep() {
  if [ -f "$1" ] && [ ! -L "$1" ]; then
    cp -p -- "$1" "$EV/kept/$2"
    out "   kept: $2"
  fi
}

step_26() {
  if [ "$PASS" = A ]; then _extra=; else _extra='; under H4, the owner logs out the two logins of the spike and deletes its home'; fi
  step_head 26 'Clean up' \
    "copy the records into the evidence (the registry journal, the holder's event log, state file and final record, the daemon's evidence file, the stand-in's files); scrub: replace the last screen in the final record by its hash and search the evidence for a login URL or code (L8); the owner stops the daemon and deletes the canary's two roots himself$_extra; write the manifest of the evidence" \
    'the evidence holds no screen text from the login; the folders are gone' 'G2'
  wait_key 'Step 26 copies the records and scrubs them.'
  if [ "$DRY" -eq 1 ]; then
    if [ "$IN_CHAIN" -eq 1 ]; then dry 'say so and record it for each part that has nothing to act on: no folder was provisioned, no daemon was started'; fi
    dry "copy, when present: $STATE/registry/registry.jsonl; $HSTATE/events.jsonl, events.jsonl.1, holder.json, final.json, final.json.previous; $EVIDENCE; $REPLAY/order.txt and result.txt"
    dry 'run ataniq-canary scrub-screen on each copy of a final record: the screen text is replaced by its hash'
    dry 'search the evidence for a login URL or code; print file names and line numbers only; the owner decides what to remove'
    dry "remove the scratch folder of the intake's verify"
    dry "wait for Enter: Owner: stop the daemon yourself (Ctrl-C in its terminal)"
    dry "run: $BIN/ataniqd status --socket $SOCK"
    dry 'check: no daemon answers (exit 20)'
    dry "run: systemctl --user show $UNIT --property=ActiveState --value"
    dry "when the unit is not stopped (its state is neither inactive nor failed): print systemctl --user stop $UNIT for the owner, wait for Enter, and read the state again"
    dry 'fail the step when the unit is still not stopped: the command that deletes the roots is then not shown'
    dry "wait for Enter: Owner: delete the canary's two roots yourself: rm -rf -- $STATE $RUNTIME"
    dry 'check: both roots are gone'
    if [ "$PASS" = B ]; then dry 'ask and record the answer and the time: Owner: under H4, the two logins of the spike are logged out and its home is deleted'; fi
    dry "write names, sizes, times and hashes of every file of the evidence to $EV/MANIFEST.txt"
    result ok
    done_step
    return 0
  fi
  [ -d "$EV/kept" ] || mkdir -m 0700 -- "$EV/kept"
  keep "$STATE/registry/registry.jsonl" registry.jsonl
  keep "$HSTATE/events.jsonl" events.jsonl
  keep "$HSTATE/events.jsonl.1" events.jsonl.1
  keep "$HSTATE/holder.json" holder.json
  keep "$HSTATE/final.json" final.json
  keep "$HSTATE/final.json.previous" final.json.previous
  keep "$EVIDENCE" canary-evidence.jsonl
  keep "$REPLAY/order.txt" replay-order.txt
  keep "$REPLAY/result.txt" replay-result.txt
  for _f in "$EV/kept/final.json" "$EV/kept/final.json.previous"; do
    if [ -f "$_f" ]; then
      runcap "scrub-${_f##*/}" "$BIN/ataniq-canary" scrub-screen --file "$_f"
      if [ "$RC" -ne 0 ]; then
        rm -f -- "$_f"
        fail "the final record ${_f##*/} could not be scrubbed: the copy is removed from the evidence"
      fi
    fi
  done
  rm -rf -- "$EV/intake-work"
  out '   lines of the evidence that look like a login URL or a code (file and line number only):'
  grep -rnIE 'https?://|[A-Z0-9]{4}-[A-Z0-9]{4}' "$EV" 2>/dev/null | cut -d: -f1,2 | sed 's/^/     /' || :
  ask 'Owner and lead: look at the lines listed, and remove from the evidence what holds a login URL or code; type clean when the evidence holds none'
  if [ "$ANSWER" != clean ]; then fail "the evidence is not confirmed clean of a login URL or code: \"$ANSWER\""; fi
  if [ "$DAEMON_UP" -eq 0 ]; then
    out '   nothing to act on: this run did not ask for the daemon to be started. The status is still asked.'
  fi
  wait_key 'Owner: stop the daemon yourself (Ctrl-C in its terminal), if it runs.'
  runcap daemon-gone "$BIN/ataniqd" status --socket "$SOCK"
  if [ "$RC" -ne 20 ]; then fail "a daemon still answers at $SOCK (exit $RC, want 20)"; fi
  if [ "$PROVISIONED" -eq 0 ] && [ ! -e "$STATE" ] && [ ! -e "$RUNTIME" ]; then
    out "   nothing to act on: neither root exists and this run provisioned nothing"
    note 'nothing to act on: no folder to delete'
  else
    # The undo's order: the unit is stopped before the roots are deleted. Step
    # 22 may not have stopped it (the daemon gone, the stop interrupted), and
    # the holder and the agent then still run with these folders bound.
    _ust=$(unit_state)
    if ! unit_stopped "$_ust"; then
      out "   the unit $UNIT is \"$_ust\", not stopped: the roots are not deleted while it may run."
      out '   Owner: stop it first, yourself, in your own terminal (README.md, undo by hand, 1 and 2):'
      out "     systemctl --user stop $UNIT"
      wait_key 'Owner: press Enter when the unit is stopped.'
      _ust=$(unit_state)
    fi
    if ! unit_stopped "$_ust"; then
      fail "the unit $UNIT is still \"$_ust\": the command that deletes the roots is not shown; stop the unit and then delete the roots by hand (README.md, undo by hand)"
    else
      note "the unit is $_ust"
      out "   Owner: delete the canary's two roots yourself, in your own terminal. The script deletes neither:"
      out "     rm -rf -- $STATE $RUNTIME"
      wait_key 'Owner: press Enter when both are deleted.'
      if [ -e "$STATE" ] || [ -e "$RUNTIME" ]; then fail 'a root of the canary still exists'; fi
    fi
  fi
  if [ "$PASS" = B ]; then
    ask 'Owner: under H4, the two logins of the spike are logged out and its home is deleted (type done, or not-applicable)'
  fi
  if [ "$FAIL_STEP" -eq 0 ]; then
    printf 'pass %s: complete\nfindings: %s\n' "$PASS" "$FINDINGS" >"$EV/result.txt"
  else
    printf 'pass %s: failed at step %s: %s\nfindings: %s\n' "$PASS" "$FAIL_STEP" "$FAIL_WHY" "$FINDINGS" >"$EV/result.txt"
  fi
  manifest_of "$EV" "$EV/MANIFEST.txt"
  out "   the evidence is in $EV; MANIFEST.txt holds names, sizes, times and hashes"
  result ok
  done_step
}

# second_epoch is R6: after step 20 of pass A, a new epoch with the same
# config in which the stand-in replays the recorded screens, so the fence can
# accept inside the real sandbox.
second_epoch() {
  CUR=20
  out ''
  out '== second epoch of pass A (after step 20): the stand-in replays the recorded screens'
  out '   does:     stop with proof; write the replay switch file; steps 11, 12 and 14 again; inject once; then step 22 in full'
  out '   expected: the fence reaches submitted, no reason, whole, inside the real sandbox; order.txt says 7501-first; the stand-in committed. A refusal is recorded with the detail and is a finding for a fix task before pass B'
  out '   serves:   G1, B1'
  wait_key 'The second epoch begins: the role is stopped with proof (the stop of step 22, without the clean-up).'
  CUR=22
  stop_role || return 0
  CUR=20
  wait_key 'Write the replay switch file.'
  runcap replay-on "$BIN/ataniq-canary" replay-on --config "$CONFIG"
  check 'the switch file was written' is_rc 0 || return 0
  EPOCH_NO=2
  step_11
  step_12
  step_14
  CUR=17
  out ''
  out '== step 17 [pass A, second epoch]: inject once'
  out '   does:     inject one message; read the result, the stand-in'"'"'s order.txt and result.txt'
  out '   expected: submitted, no reason, whole; order.txt says 7501-first; result.txt says committed'
  out '   serves:   G1, G3'
  make_message
  wait_key 'Inject the one message of the second epoch.'
  runcap inject "$BIN/ataniqd" canary inject --config "$CONFIG" --message-id "$MSGID" --body-file "$BODY"
  if [ "$DRY" -eq 1 ]; then
    dry 'check: the result is submitted, no reason, whole; anything else is recorded with its detail as a finding, and the pass goes on to step 22'
    dry "read $REPLAY/order.txt and $REPLAY/result.txt; a file that is empty for a moment is read again"
    dry 'check: order.txt says 7501-first'
    dry 'check: result.txt says committed'
  else
    if [ "$RC" -eq 0 ]; then
      _o=$(fld "$LASTOUT" result.outcome)
      _r=$(fld "$LASTOUT" result.reason)
      _w=$(fld "$LASTOUT" result.written)
      _dt=$(fld "$LASTOUT" result.detail)
    else
      _o="exit $RC"
      _r=''
      _w=''
      _dt=''
    fi
    out "   result: outcome=$_o reason=${_r:-none} written=$_w"
    if [ "$_o/$_r/$_w" = submitted//whole ]; then
      note 'second epoch: submitted, no reason, whole'
      for _pair in "order.txt:7501-first" "result.txt:committed"; do
        _file=$REPLAY/${_pair%%:*}
        _want=${_pair#*:}
        _try=0
        while [ ! -s "$_file" ] && [ "$_try" -lt 30 ]; do
          _try=$((_try + 1))
          sleep 1
        done
        _got=$(sed -n 1p "$_file" 2>/dev/null || :)
        out "   ${_pair%%:*}: $_got"
        case $_got in
          "$_want" | "$_want "*) note "ok: ${_pair%%:*} says $_want" ;;
          *)
            FINDINGS=$((FINDINGS + 1))
            out "   FINDING: ${_pair%%:*} does not say $_want"
            note "FINDING: ${_pair%%:*} says \"$_got\", want $_want"
            ;;
        esac
      done
    else
      FINDINGS=$((FINDINGS + 1))
      out "   FINDING for a fix task before pass B: the fence did not accept: $_o/${_r:-none}/$_w detail=$_dt"
      note "FINDING: second epoch: $_o/${_r:-none}/$_w detail=$_dt"
    fi
  fi
  out '   the second epoch ends with step 22 in full, which follows'
}

# ------------------------------------------------------------- the chain ---

run_step() {
  case $1 in
    21) step_21 ;; 22) step_22 ;; 23) step_23 ;; 24) step_24 ;; 25) step_25 ;; 26) step_26 ;;
  esac
}

# chain_steps prints the clean-up steps for a failure at step $1.
chain_steps() {
  if [ "$PASS" = A ]; then
    if [ "$1" -ge 11 ]; then out '22 26'; fi
  elif [ "$1" -ge 15 ]; then
    out '21 22 23 24 25 26'
  elif [ "$1" -ge 6 ]; then
    out '22 24 25 26'
  fi
}

on_exit() {
  _st=$?
  trap - EXIT
  if [ "$_st" -eq 0 ] || [ "$CHAIN_RAN" -eq 1 ]; then exit "$_st"; fi
  set +e
  CHAIN_RAN=1
  if [ "$FAIL_STEP" -eq 0 ]; then
    FAIL_STEP=$CUR
    FAIL_WHY='the script ended unexpectedly or was interrupted'
  fi
  _steps=$(chain_steps "$FAIL_STEP")
  out ''
  if [ -z "$_steps" ]; then
    out "== no clean-up chain: the failure at step $FAIL_STEP is before the chain begins (pass A: step 11; pass B: step 6)"
    out '   undo by hand whatever an earlier step left: see the undo in README.md'
    CUR=$FAIL_STEP
    note 'no clean-up chain'
    if [ "$DRY" -eq 0 ] && [ -n "$LOG" ]; then
      printf 'pass %s: failed at step %s: %s\n' "$PASS" "$FAIL_STEP" "$FAIL_WHY" >"$EV/result.txt"
    fi
    exit "$_st"
  fi
  out "== clean-up chain after the failure at step $FAIL_STEP [pass $PASS]: steps $_steps"
  out '   no step of the chain can be skipped; each waits for its key and records its result'
  IN_CHAIN=1
  EPOCH_NO=1
  chain_trap
  for _s in $_steps; do
    run_step "$_s"
  done
  out ''
  out "== the pass failed at step $FAIL_STEP: $FAIL_WHY"
  exit "$_st"
}
trap on_exit EXIT
trap 'exit 130' INT TERM HUP

# --------------------------------------------------------------- the pass ---

out "canary run, pass $PASS$([ "$DRY" -eq 1 ] && printf ' (dry: nothing is started, the values are samples)' || :)"
out "role $ROLE, unit $UNIT"
out "limits: memory ceiling $MAXB, swap $SWAPB, tasks $TASKS, floor $FLOORB, share $SHARE%"
out "evidence: $EV"

step_1
step_2
step_3
step_4
step_5
step_6
step_7
step_8
step_9
step_10
step_11
step_12
step_13
step_14
step_15
step_16
step_17
step_18
step_19
step_20
if [ "$PASS" = A ]; then second_epoch; fi
step_21
step_22
step_23
step_24
step_25
step_26

out ''
out "== pass $PASS is complete, with $FINDINGS finding(s)"
