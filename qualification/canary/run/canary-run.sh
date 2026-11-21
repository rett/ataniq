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
# on each line of its file, and wait-daemon and stop-daemon run the ataniqd
# status of build/canary, review-verdict, mirror-verdict and ask-file run the
# ataniq-canary fields helper of build/canary on the file they are given;
# stop-daemon, roots-deleted, ask-path, ask-file and affirm-word read keys from
# standard input, and ask-path and ask-file read the hint file they are given;
# no other program is run. count-lines runs the helper ataniq-canary fields of
# build/canary on each line of the file it is given, as has-line does, and
# journal-save runs journalctl (the one on PATH, a stub in the tests) and writes
# its lines to EVIDENCE/out, a folder the test makes.
#
# At a path prompt (the two fact reports, the template's review, the report of
# qualify-mirror) the script shows "(Enter for: PATH)" when answers.hint beside
# the config names a path for it, and takes an empty line as that path; a typed
# path wins. The file is still checked and recorded, and the log says whether
# the answer was a hint or typed. No hint is ever read for a word (yes, read,
# integrate, confirmed, reset or an attestation): those are only typed.
#
# At those path prompts the file is also looked at before anything is recorded:
# the two fact reports as before (already parsed), the template's review must
# not be a qualify-mirror report and must hold the template's hash or the word
# verdict, and the report of qualify-mirror must be JSON whose template.sha256
# is the hash step 4 just computed. A refused file is said, with the reason,
# and the question is asked again, up to 3 more times, before the step fails.
#
# A step that waits for something the owner starts or stops in another
# terminal (the daemon at step 10 and step 26, the console's ticket at step 14,
# the listeners at steps 6 and 24) asks for it again every second for up to 60
# seconds instead of failing at once. Each ask of the daemon's status is cut
# off by timeout(1) after 5 seconds (a socket that accepts and never answers
# would otherwise hold each ask for about 10 seconds, the binary's own limit),
# and the wait as a whole ends after the 60 seconds plus one such ask, so the
# longest of these waits is about 65 seconds (step 12's wait for the holder's
# record asks the unit, not the daemon, 1.5 times as often: about 90 seconds). The three variables CANARY_RUN_WAIT_TRIES,
# CANARY_RUN_WAIT_INTERVAL and CANARY_RUN_STATUS_TIMEOUT change that for the
# test entry --self-test only: a real run (and --dry) ignores them, so that
# nothing in the environment can shorten what the script waits for. The script
# still answers nothing and starts nothing: it only watches.
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
    '         counters-alt FIELDS' \
    '                      the alternate-screen counters (alt_screen_on, alt_screen_off) step 22 prints from FIELDS' \
    '         unit-stopped STATE' \
    '                      whether that active state lets step 26 show the delete command' \
    '         review-verdict FILE HASH | mirror-verdict FILE HASH' \
    '                      what step 4 makes of a file given as the review or the report (build/canary helper)' \
    '         ask-file HINTFILE KEY review|mirror HASH' \
    '                      a path prompt of step 4 with its refusals and asking again (paths from standard input)' \
    '         ask-path HINTFILE KEY | affirm-word HINTFILE WORD' \
    '                      a path prompt with its hint, and a typed word with the hint file in place' \
    '         wait-daemon | stop-daemon | roots-deleted STATE RUNTIME' \
    '                      the polling and the asking again of steps 10 and 26 (stub ataniqd in build/canary)' \
    '         journal-save EVIDENCE START UNIT' \
    '                      what step 22 keeps of the journal of the unit (journalctl on PATH; writes EVIDENCE/out only)' \
    '         count-lines FILE PATH=VALUE...' \
    '                      how many lines of FILE hold every PATH=VALUE (step 14 counts attach events)' \
    '         ticket-taken FILE' \
    '                      whether step 14 takes the ticket file for taken (an empty name never is)' \
    '         open-runs LIST REPO | unread-runs LIST | list-scan LIST | loaded-units FILE | tracked-changes FILE' \
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

# How long a step waits for what the owner starts or stops in another terminal:
# WAIT_TRIES asks, WAIT_INTERVAL seconds apart (60 asks, one second apart), and
# an ask of the daemon's status is cut off after STATUS_TIMEOUT seconds. The
# three variables are read by the test entry (--self-test) only, which makes
# the wait short; a real run keeps the values here whatever the environment
# holds.
WAIT_TRIES=60
WAIT_INTERVAL=1
STATUS_TIMEOUT=5
if [ "$SELFTEST" -eq 1 ]; then
  WAIT_TRIES=${CANARY_RUN_WAIT_TRIES:-60}
  WAIT_INTERVAL=${CANARY_RUN_WAIT_INTERVAL:-1}
  STATUS_TIMEOUT=${CANARY_RUN_STATUS_TIMEOUT:-5}
fi
case $WAIT_TRIES in '' | *[!0-9]*)
  printf '%s\n' 'canary-run.sh: CANARY_RUN_WAIT_TRIES is not a number' >&2
  exit 2
  ;;
esac
case $WAIT_INTERVAL in '' | *[!0-9.]* | *.*.* | .)
  printf '%s\n' 'canary-run.sh: CANARY_RUN_WAIT_INTERVAL is not a number of seconds' >&2
  exit 2
  ;;
esac
case $STATUS_TIMEOUT in '' | *[!0-9]* | 0)
  printf '%s\n' 'canary-run.sh: CANARY_RUN_STATUS_TIMEOUT is not a number of seconds above 0' >&2
  exit 2
  ;;
esac
# After a refusal the owner is asked again this many more times: at step 26
# (the daemon, the two roots) and, for a path whose file is refused, at the
# path prompts of steps 2 and 4.
ASK_AGAIN=5
ASK_AGAIN_PATH=3

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
LOGIN_AT=      # pass B: the time the owner said the login was done
RESAMPLE_SECONDS=5 # step 6: the wait before the listeners are listed once more
TURNS=0
FINDINGS=0
DONE_STEPS=' ' # the steps that ended, for the chain's "already done"
EV=
LOG=
LASTOUT=
LASTERR=
RC=0
ANSWER=
ANSWER_SRC=typed # how the last answer came: typed, or hint (a path prompt, an empty line)
HINTS=           # the hint file next to the config, a real run only
LINGER0=
T_START=       # the local time of the first start of the unit: the journal is read from it
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

# ask puts a question to the owner or the lead and records the answer, how it
# came (typed, or hint) and the time. The answer is in ANSWER. The optional
# second argument is a hint, a suggested path: only ask_path gives one. It is
# shown as "(Enter for: PATH)" and an empty line, an Enter, takes it; a typed
# answer always wins, and the end of the input takes nothing.
ask() {
  ANSWER=
  ANSWER_SRC=typed
  if [ "$DRY" -eq 1 ]; then
    dry "ask and record the answer and the time: $1"
    return 0
  fi
  printf '   ?? %s\n' "$1"
  if [ -n "${2:-}" ]; then printf '   (Enter for: %s)\n' "$2"; fi
  printf '   > '
  _askok=1
  if ! read_answer; then
    ANSWER=
    _askok=0
    out ''
  fi
  if [ "$_askok" -eq 1 ] && [ -z "$ANSWER" ] && [ -n "${2:-}" ]; then
    ANSWER=$2
    ANSWER_SRC=hint
    out "   (the hint is taken: $ANSWER)"
  fi
  if [ -n "$EV" ]; then
    printf '%s\tstep %s\t%s\t%s\t%s\n' "$(now)" "$CUR" "$1" "$ANSWER" "$ANSWER_SRC" >>"$EV/answers.txt"
  fi
  note "answer recorded ($ANSWER_SRC): $1"
}

# hint_for KEY prints the path the hint file suggests for KEY (a line
# "KEY=/path"), or nothing: no file, no line, a path that is not absolute or
# holds a control character. The file is a list of suggested paths and holds
# nothing else; it is never read for a word that the owner types.
hint_for() {
  [ -n "$HINTS" ] && [ -f "$HINTS" ] && [ ! -L "$HINTS" ] || return 0
  while IFS= read -r _hl || [ -n "$_hl" ]; do
    case $_hl in
      "$1="/*)
        _hv=${_hl#*=}
        case $_hv in
          *[[:cntrl:]]*) return 0 ;;
        esac
        printf '%s\n' "$_hv"
        return 0
        ;;
    esac
  done <"$HINTS"
}

# ask_path asks for a path: $1 is the question, $2 the key of the hint. This
# and no other question takes a hint, so a word (yes, read, integrate,
# confirmed, reset) is always typed. The file is still checked and recorded by
# the step, a hint or not.
ask_path() {
  if [ "$DRY" -eq 1 ]; then
    dry "when answers.hint next to the config names a path for $2, show \"(Enter for: PATH)\" and take an empty answer as that path; a typed path always wins"
    ask "$1"
    return 0
  fi
  ask "$1" "$(hint_for "$2")"
}

# ask_file KEY QUESTION DESC VERIFIER [ARG...] asks for the path of a file as
# ask_path does (KEY names the hint) and looks at the file before the step
# records anything: it must be there, and VERIFIER FILE [ARG...] must print
# "ok TEXT" for it ("bad REASON" otherwise; DESC says in a dry run what it
# looks at). A refused file is said, with the reason, and the question is asked
# again, up to ASK_AGAIN_PATH more times; the step fails only after that. The
# accepted path is in ANSWER.
ask_file() {
  _afk=$1
  _afq=$2
  _afd=$3
  _afv=$4
  shift 4
  if [ "$DRY" -eq 1 ]; then
    ask_path "$_afq" "$_afk"
    dry "check: the file is present"
    dry "check: $_afd"
    dry "when the file is refused, say why and ask again, up to $ASK_AGAIN_PATH more times; fail only after that"
    return 0
  fi
  _afn=0
  while :; do
    ask_path "$_afq" "$_afk"
    if [ -z "$ANSWER" ]; then
      _afr='bad no path was given'
    elif [ ! -f "$ANSWER" ]; then
      _afr="bad the file is not present: $ANSWER"
    else
      _afr=$("$_afv" "$ANSWER" "$@") || _afr='bad the check of the file could not run'
    fi
    case $_afr in
      'ok '*)
        out "   ok: $ANSWER: ${_afr#ok }"
        note "ok: $ANSWER (${ANSWER_SRC}): ${_afr#ok }"
        return 0
        ;;
    esac
    out "   refused: ${_afr#bad }"
    note "the file was refused (ask $((_afn + 1)) of $((ASK_AGAIN_PATH + 1))): ${_afr#bad }"
    if [ "$_afn" -ge "$ASK_AGAIN_PATH" ]; then
      fail "the file was refused $((ASK_AGAIN_PATH + 1)) times: ${_afr#bad }"
      return 1
    fi
    _afn=$((_afn + 1))
    out "   ask again ($_afn of $ASK_AGAIN_PATH): a path you type is taken as it is"
  done
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
  if [ -n "$EV" ]; then
    LASTOUT=$EV/out/$(printf '%02d' "$CUR")-e$EPOCH_NO-$_name.out
    LASTERR=$EV/out/$(printf '%02d' "$CUR")-e$EPOCH_NO-$_name.err
    _csfx=$(chain_sfx "$LASTOUT")
    LASTOUT=${LASTOUT%.out}$_csfx.out
    LASTERR=${LASTERR%.err}$_csfx.err
  else
    # The test entry keeps no evidence.
    LASTOUT=/dev/null
    LASTERR=/dev/null
  fi
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
# is_under_agent_bind succeeds when the path is a file name under /ataniq/agent/:
# step 9 strips that prefix and runs the file of the install folder, so a live
# agent's first argument must be such a name (the executable itself, no wrapper).
is_under_agent_bind() { [ "${1#"$AGENT_INSIDE"/}" != "$1" ] && [ -n "${1#"$AGENT_INSIDE"/}" ]; }
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

# counters_like prints, from the lines $1 (what ataniq-canary fields prints
# for the holder's final record), the lines that match the pattern $2, and
# fails when there is none, so that the caller can say so.
# counters_7501 prints the counters named for 7501; counters_alt prints the
# two alternate-screen counters of the holder's pane (alt_screen_on and
# alt_screen_off): alt_screen_on above 0 means the agent drew with the
# fullscreen renderer, which the screen recogniser requires.
counters_7501() { counters_like "$1" 'counters.*7501*'; }
counters_alt() { counters_like "$1" 'counters.alt_screen_*'; }
counters_like() {
  _rest=$1
  _pat=$2
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
      $_pat)
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

# count_lines FILE PATH=VALUE... prints how many lines of FILE, which holds one
# JSON object in each line, hold every PATH=VALUE, each line read as JSON by the
# tagged helper as has_line does (a line that is not a whole JSON object is
# passed over). A file that is not there counts 0.
count_lines() {
  _cf=$1
  shift
  _cn=0
  if [ -f "$_cf" ]; then
    while IFS= read -r _cl || [ -n "$_cl" ]; do
      _cg=$(printf '%s\n' "$_cl" | "$BIN/ataniq-canary" fields --file - 2>/dev/null) || continue
      if fields_hold "$_cg" "$@"; then _cn=$((_cn + 1)); fi
    done <"$_cf"
  fi
  printf '%s\n' "$_cn"
}

# unit_journal LINES prints the last LINES lines of the unit's journal since
# the first start of this run. The holder runs as the unit's main process and
# says why it ended on its standard error, which is the journal: the event log
# and the evidence hold only a code ("sandbox-start-failed"), so a holder that
# exits at its start leaves its reason nowhere else. The journal can name the
# canary's folders: it goes into the evidence folder, which is never published,
# and step 26's scrub looks through it.
unit_journal() {
  journalctl --user --unit "$UNIT" --since "$T_START" --no-pager --output=short-iso --lines="$1"
}

# show_unit_journal prints the tail of the journal, for a step that fails
# because the unit is not what it should be.
show_unit_journal() {
  if [ -z "$T_START" ]; then return 0; fi
  out "   the journal of the unit since $T_START (the last 40 lines): the holder says why it ended there"
  runcap unit-journal-tail unit_journal 40
}

# chain_sfx FILE prints -chain when the clean-up chain is about to write a file
# that the pass itself wrote already (step 22 runs again in the chain, with the
# epoch number set to 1, and must not overwrite the proof of the stop between
# the epochs), and nothing otherwise.
chain_sfx() {
  if [ "$IN_CHAIN" -eq 1 ] && [ -e "$1" ]; then printf '%s' -chain; fi
}

# save_unit_journal keeps the journal in the evidence (step 22), whatever
# became of the unit.
save_unit_journal() {
  if [ "$DRY" -eq 1 ]; then
    dry "run: journalctl --user --unit $UNIT --since <the time of the first start> --no-pager --output=short-iso --lines=500, into $EV/out/22-e1-unit-journal.out (what the holder said on its standard error; when no start was asked there is nothing to read)"
    return 0
  fi
  if [ -z "$T_START" ]; then return 0; fi
  _jf=$EV/out/$(printf '%02d' "$CUR")-e$EPOCH_NO-unit-journal.out
  _jf=${_jf%.out}$(chain_sfx "$_jf").out
  if unit_journal 500 >"$_jf" 2>"$_jf.err"; then
    out "   the unit's journal since $T_START, as the holder wrote it on its standard error, is in $_jf ($(awk 'END { print NR }' "$_jf") lines)"
    note "kept the journal of the unit in $_jf"
  else
    out "   the unit's journal could not be read (journalctl failed: $_jf.err); the holder's reason for an early end may be missing from the evidence"
    note 'the journal of the unit could not be read'
  fi
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
#   owner-supplies  the fence wrote nothing (refused, or not-now with written
#                   nothing: the screen was not ready), and the owner puts the
#                   message in the pane himself, as in pass A's first epoch
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
    # Nothing was written, so the owner may supply the message himself, as in
    # pass A's first epoch. It is recorded as the fence's not-now.
    B:not-now/*/nothing) out owner-supplies ;;
    *) out decide ;;
  esac
}

show_prop() { systemctl --user show "$UNIT" --property="$1" --value; }

# holder_wait waits for the holder's own record of the start of this epoch. The
# holder writes holder.json twice: before the start sequence, with the epoch and
# without sandbox_started, and after it, with the waiver and sandbox_started
# true. The first write names the epoch already, so the wait ends only when the
# file names the epoch AND says sandbox_started is true, or when the unit is no
# longer active or activating (then it ends at once: the journal says why; an
# empty answer, as a failed systemctl gives, is asked again, within the same
# tries, and is never taken for the end), or
# after 1.5 times WAIT_TRIES asks, WAIT_INTERVAL seconds apart (90 asks, one
# second apart: more than the 60 seconds the inputs' report limit allows). It
# sets HW_END to started, ended or timeout.
holder_wait() {
  _hwn=0
  _hwmax=$((WAIT_TRIES * 3 / 2))
  HW_END=
  while :; do
    if [ "$(fld "$HSTATE/holder.json" epoch 2>/dev/null)" = "$START_EPOCH" ] &&
      [ "$(fld "$HSTATE/holder.json" sandbox_started 2>/dev/null)" = true ]; then
      HW_END=started
      return 0
    fi
    case $(show_prop ActiveState 2>/dev/null) in
      active | activating | '') ;;
      *)
        HW_END=ended
        return 0
        ;;
    esac
    if [ "$_hwn" -ge "$_hwmax" ]; then
      HW_END=timeout
      return 0
    fi
    _hwn=$((_hwn + 1))
    sleep "$WAIT_INTERVAL"
  done
}

# holder_started is the wait of step 12: it fails the step, with the unit's
# journal on the screen first, when the wait ends without the holder's record
# of a started sandbox.
holder_started() {
  holder_wait
  case $HW_END in
    started) return 0 ;;
    ended)
      show_unit_journal
      fail "the unit ended before $HSTATE/holder.json named epoch $START_EPOCH with sandbox_started true: the holder did not finish the start (the journal above says why)"
      return 1
      ;;
    *)
      show_unit_journal
      fail "$HSTATE/holder.json did not name epoch $START_EPOCH with sandbox_started true within $((WAIT_TRIES * 3 / 2)) asks: the holder is still starting or wrote no record (the journal above says what it said)"
      return 1
      ;;
  esac
}

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

# ------------------------------------------- what the owner does elsewhere ---

# The owner starts the daemon, attaches the console, stops the listeners and
# stops the daemon in terminals of his own, and each takes a moment. The
# script only watches: it asks quietly, answers nothing and starts nothing.

# poll_until WHAT COMMAND [ARG...] runs COMMAND (output dropped) until it
# succeeds, for at most WAIT_TRIES more asks, WAIT_INTERVAL seconds apart, and
# no longer than WAIT_TRIES * WAIT_INTERVAL seconds (rounded up) plus the one
# ask's own limit, whatever the asks take: a command that hangs is cut off by
# its caller (daemon_answers, daemon_gone), and the clock ends the wait. It says
# "waiting for WHAT..." once, and only when the first ask did not succeed, then
# prints a dot for every 5 asks. It returns 0 as soon as COMMAND succeeds and 1
# after the last ask; the caller decides what a failure is.
poll_until() {
  _puw=$1
  shift
  _pul=$(awk -v t="$WAIT_TRIES" -v i="$WAIT_INTERVAL" -v c="$STATUS_TIMEOUT" 'BEGIN { x = t * i; n = int(x); if (n < x) n++; print n + c }')
  _pus=$(date +%s)
  if "$@" >/dev/null 2>&1; then return 0; fi
  out "   waiting for $_puw..."
  _pun=0
  _pud=0
  while [ "$_pun" -lt "$WAIT_TRIES" ] && [ $(($(date +%s) - _pus)) -le "$_pul" ]; do
    sleep "$WAIT_INTERVAL"
    _pun=$((_pun + 1))
    if "$@" >/dev/null 2>&1; then
      if [ "$_pud" -eq 1 ]; then out ''; fi
      note "waited for $_puw: it came at try $_pun of $WAIT_TRIES"
      return 0
    fi
    if [ $((_pun % 5)) -eq 0 ]; then
      printf '.'
      _pud=1
    fi
  done
  if [ "$_pud" -eq 1 ]; then out ''; fi
  note "gave up waiting for $_puw after $_pun tries, $WAIT_INTERVAL s apart"
  return 1
}

# daemon_answers succeeds while a daemon answers at the socket, and
# daemon_gone when none does (the status's exit status 20). Both leave the
# status's exit status in DAEMON_RC. Each ask is run by timeout(1) (coreutils,
# as sha256sum and stat are) and cut off after STATUS_TIMEOUT seconds, which
# gives the exit status 124: a socket that accepts and never answers is then
# neither an answering daemon nor a gone one.
daemon_status() {
  DAEMON_RC=0
  timeout -k 1 "$STATUS_TIMEOUT" "$BIN/ataniqd" status --socket "$SOCK" >/dev/null 2>&1 || DAEMON_RC=$?
}
daemon_answers() {
  daemon_status
  [ "$DAEMON_RC" -eq 0 ]
}
daemon_gone() {
  daemon_status
  [ "$DAEMON_RC" -eq 20 ]
}

# ticket_taken succeeds when the ticket file $1 is absent or empty: the
# console empties it when it takes the single-use ticket. Nothing is read. An
# empty name is no file, so it never counts as taken.
ticket_taken() { [ -n "$1" ] && [ ! -s "$1" ]; }

# root_present reports whether the path $1 is there, a dangling link too.
root_present() { [ -e "$1" ] || [ -L "$1" ]; }

# daemon_no_key and roots_no_key are what step 26 does when no key can be read
# (the input ended: Ctrl-D, or the terminal hung up). In the clean-up chain
# wait_key then records nothing, so the step looks once itself and fails when
# a daemon still answers or a root still exists: a missing key must not hide
# either. The caller returns after it, as it does after any failure.
daemon_no_key() {
  daemon_gone || fail "a daemon still answers at $SOCK (exit $DAEMON_RC, want 20); no key could be read"
}
roots_no_key() {
  if root_present "$STATE" || root_present "$RUNTIME"; then
    fail 'a root of the canary still exists; no key could be read'
  fi
}

# daemon_stopped_by_owner is the daemon's half of step 26. The owner stops the
# daemon himself, in its terminal, and presses Enter. The script then asks the
# daemon's status every second for up to a minute (a daemon takes a moment to
# end, and he may still be pressing Ctrl-C). One that still answers after that
# is not a failure of the pass: it says so and asks again, up to ASK_AGAIN
# more times, and records each ask. It fails only after that.
daemon_stopped_by_owner() {
  _again=0
  if ! wait_key 'Owner: stop the daemon yourself (Ctrl-C in its terminal), if it runs.'; then
    daemon_no_key
    return 1
  fi
  while :; do
    if poll_until 'the daemon to stop' daemon_gone; then
      note "the daemon is gone (round $((_again + 1)))"
      return 0
    fi
    note "the daemon still answers (exit $DAEMON_RC), round $((_again + 1)) of $((ASK_AGAIN + 1))"
    if [ "$_again" -ge "$ASK_AGAIN" ]; then
      fail "a daemon still answers at $SOCK (exit $DAEMON_RC, want 20) after $((ASK_AGAIN + 1)) rounds"
      return 1
    fi
    _again=$((_again + 1))
    out '   the daemon still answers: stop it with Ctrl-C in its terminal, then press Enter'
    if ! wait_key 'Owner: press Enter after you stopped the daemon.'; then
      daemon_no_key
      return 1
    fi
  done
}

# roots_deleted_by_owner is the roots' half of step 26: the owner deletes the
# two roots himself and presses Enter. A root that is still there is said, and
# he is asked again, up to ASK_AGAIN more times, each ask recorded.
roots_deleted_by_owner() {
  _again=0
  if ! wait_key 'Owner: press Enter when both are deleted.'; then
    roots_no_key
    return 1
  fi
  while root_present "$STATE" || root_present "$RUNTIME"; do
    for _rt in "$STATE" "$RUNTIME"; do
      if root_present "$_rt"; then out "   still there: $_rt"; fi
    done
    note "a root still exists, round $((_again + 1)) of $((ASK_AGAIN + 1))"
    if [ "$_again" -ge "$ASK_AGAIN" ]; then
      fail "a root of the canary still exists after $((ASK_AGAIN + 1)) rounds"
      return 1
    fi
    _again=$((_again + 1))
    out '   a root of the canary still exists: delete it as shown above, then press Enter'
    if ! wait_key 'Owner: press Enter when both are deleted.'; then
      roots_no_key
      return 1
    fi
  done
}

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

# open_rows LIST prints the folder of each run that "qargi local list" (the
# text in the file LIST) shows as open: a run is open unless it is applied (or
# closed) and, when applied, merged. Residue lines and the rows of a run whose
# folder is gone ("directory gone") are not rows of an open run it can read;
# list_gone counts the second kind, and the caller says so.
open_rows() {
  awk 'NF == 6 && substr($6, 1, 1) == "/" && ($4 == "no" || ($4 == "yes" && $5 != "yes")) { print $6 }' "$1"
}

# open_runs LIST REPO prints the folder of each open run (open_rows) whose
# repo.path is in the same repository as REPO: the main work tree of each side
# is compared, as resolved paths, so a run prepared from a linked work tree is
# seen from the main checkout and from another work tree. An open run whose
# repo.path cannot be read is not printed here: open_unread names it, and the
# caller does not take the list as proof.
open_runs() {
  _roots=$(repo_roots "$2" | tail -n 1)
  [ -n "$_roots" ] || return 0
  open_rows "$1" |
    while IFS= read -r _r; do
      _rp=$(repo_roots "$(cat "$_r/repo.path" 2>/dev/null || :)" | tail -n 1)
      [ -n "$_rp" ] || continue
      if [ "$_rp" = "$_roots" ]; then printf '%s\n' "$_r"; fi
    done
}

# open_unread LIST prints the folder of each open run (open_rows) whose
# repo.path cannot be read: the file is missing, unreadable or empty. Whose
# repository such a run belongs to is not known, so the check of the open runs
# does not pass on this list.
open_unread() {
  open_rows "$1" |
    while IFS= read -r _r; do
      _rp=$(cat "$_r/repo.path" 2>/dev/null || :)
      case $_rp in
        '') printf '%s\n' "$_r" ;;
      esac
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
    NF == 8 && substr($6, 1, 1) == "/" && $7 == "directory" && $8 == "gone" && substr($0, length($0) - length($6) - 15) == $6 "  directory gone" { gone++; next }
    NF == 16 && substr($6, 1, 1) == "/" && $7 == "directory" && $8 == "gone," && $9 == "saved" && $10 == "copy" && $12 == "(qargi" && $15 == "--from-saved" && index($0, $6 "  directory gone, saved copy ") { gone++; next }
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

# review_verdict FILE HASH reads the file named as the template's review and
# prints "ok TEXT" or "bad REASON". It is a cheap look, not a judgement of the
# review: a report of qualify-mirror (a JSON file with cases and a template) is
# the other file step 4 asks for, so it is refused here, as is any JSON file or
# a file that is not text; the review must hold the template's hash or the word
# verdict.
review_verdict() {
  [ -s "$1" ] || { printf '%s\n' 'bad the file is empty'; return 0; }
  _rvfirst=$(awk 'NF { s = $0; sub(/^[ \t]+/, "", s); print substr(s, 1, 1); exit }' "$1")
  case $_rvfirst in
    '{')
      if _rvf=$("$BIN/ataniq-canary" fields --file "$1" 2>/dev/null) &&
        printf '%s\n' "$_rvf" | awk '
          index($0, "cases.") == 1 || index($0, "cases=") == 1 { c = 1 }
          index($0, "template.") == 1 || index($0, "template=") == 1 { t = 1 }
          END { exit !(c && t) }'; then
        printf '%s\n' 'bad this is a qualify-mirror report (it has cases and a template), not the template'"'"'s review: the report is asked for next'
      else
        printf '%s\n' 'bad this is a JSON file: the review is a text or markdown file'
      fi
      return 0
      ;;
  esac
  # shellcheck disable=SC2094  # cmp only reads the file
  if ! tr -d '\000' <"$1" | cmp -s - "$1"; then
    printf '%s\n' 'bad this is not a text file (it holds a NUL byte)'
    return 0
  fi
  if grep -qF -- "$2" "$1"; then
    printf '%s\n' "ok the review holds the template's hash"
  elif grep -qiw -- verdict "$1"; then
    printf '%s\n' 'ok the review holds the word verdict'
  else
    printf '%s\n' "bad the file neither holds the template's hash nor the word verdict, so it does not look like the review"
  fi
}

# mirror_verdict FILE HASH reads the file named as the report of qualify-mirror
# and prints "ok TEXT" or "bad REASON": it must be one JSON object whose
# template.sha256 is the hash step 4 just computed, so a report made with
# another template, or a file that is not a report, is refused.
mirror_verdict() {
  if ! _mvf=$("$BIN/ataniq-canary" fields --file "$1" 2>/dev/null); then
    printf '%s\n' 'bad the file is not one JSON object: the report of qualify-mirror is JSON'
    return 0
  fi
  _mvh=$(printf '%s\n' "$_mvf" | awk 'index($0, "template.sha256=") == 1 { print substr($0, 17); exit }')
  if [ -z "$_mvh" ]; then
    printf '%s\n' 'bad the file has no template.sha256: it is not the report of qualify-mirror'
  elif [ "$_mvh" != "$2" ]; then
    printf '%s\n' "bad the report's template.sha256 ($_mvh) is not the hash of the template ($2): it was made with another template"
  else
    printf '%s\n' "ok the report names the template's hash"
  fi
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
    journal-save)
      # EVIDENCE START UNIT: what step 22 keeps of the unit's journal: the
      # journalctl on PATH (a stub in the tests) is asked for the unit since
      # START, and the lines go to EVIDENCE/out, a folder that exists.
      [ "$#" -eq 3 ] || usage
      EV=$1
      T_START=$2
      UNIT=$3
      CUR=22
      EPOCH_NO=1
      save_unit_journal
      ;;
    count-lines)
      # FILE PATH=VALUE...: how many lines of FILE hold every PATH=VALUE, as
      # step 14 counts the attach events. The helper is the one of build/canary
      # in the current folder.
      [ "$#" -ge 2 ] || usage
      BIN=build/canary
      count_lines "$@"
      ;;
    ticket-taken)
      # FILE (which may be an empty string): whether step 14 takes the ticket
      # file for taken. It looks at the size of the file and reads nothing.
      [ "$#" -eq 1 ] || usage
      if ticket_taken "$1"; then out taken; else out waiting; fi
      ;;
    wait-daemon)
      # No value. It asks the ataniqd of build/canary in the current folder
      # for its status as step 10 does, with the polling, and prints "listens"
      # or "silent". The socket is a sample path.
      [ "$#" -eq 0 ] || usage
      BIN=build/canary
      SOCK=/sample/canary/runtime/daemon.sock
      CUR=10
      if poll_until 'the daemon to listen' daemon_answers; then out listens; else out silent; fi
      ;;
    stop-daemon)
      # No value. The daemon's half of step 26 as it runs there, with the
      # keys read from standard input and the failure that ends the step
      # printed and returned (as in the clean-up chain), not ending the run.
      [ "$#" -eq 0 ] || usage
      BIN=build/canary
      SOCK=/sample/canary/runtime/daemon.sock
      CUR=26
      IN_CHAIN=1
      if daemon_stopped_by_owner; then out stopped; else out failed; fi
      ;;
    roots-deleted)
      # STATE RUNTIME: the roots' half of step 26, as above.
      [ "$#" -eq 2 ] || usage
      STATE=$1
      RUNTIME=$2
      CUR=26
      IN_CHAIN=1
      if roots_deleted_by_owner; then out deleted; else out failed; fi
      ;;
    review-verdict)
      # FILE HASH: what step 4 makes of FILE as the template's review. The
      # helper is the one of build/canary in the current folder.
      [ "$#" -eq 2 ] || usage
      BIN=build/canary
      review_verdict "$1" "$2"
      ;;
    mirror-verdict)
      # FILE HASH: what step 4 makes of FILE as the report of qualify-mirror.
      [ "$#" -eq 2 ] || usage
      BIN=build/canary
      mirror_verdict "$1" "$2"
      ;;
    ask-file)
      # HINTFILE KEY KIND HASH: asks for a path as step 4 does (KIND review or
      # mirror) with the paths read from standard input, and prints
      # "accepted PATH" or "failed". The failure that ends the step is printed
      # and returned, not ending the run.
      [ "$#" -eq 4 ] || usage
      HINTS=$1
      BIN=build/canary
      IN_CHAIN=1
      CUR=4
      case $3 in
        review) _verifier=review_verdict ;;
        mirror) _verifier=mirror_verdict ;;
        *) usage ;;
      esac
      if ask_file "$2" 'Lead: a path' 'the file is looked at' "$_verifier" "$4"; then out "accepted $ANSWER"; else out failed; fi
      ;;
    ask-path)
      # HINTFILE KEY: asks a path prompt with the hint of KEY in HINTFILE
      # (which may not exist) and prints what the answer was and how it came.
      # The answer is read from standard input.
      [ "$#" -eq 2 ] || usage
      HINTS=$1
      ask_path 'Lead: a path' "$2"
      out "answer=$ANSWER"
      out "source=$ANSWER_SRC"
      ;;
    affirm-word)
      # HINTFILE WORD: asks for the typed word WORD with the hint file in
      # place, as every word of the run is asked, and prints accepted or
      # refused. The answer is read from standard input.
      [ "$#" -eq 2 ] || usage
      HINTS=$1
      IN_CHAIN=1
      if affirm 'Lead: a word' "$2"; then out accepted; else out refused; fi
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
    counters-alt)
      # FIELDS: what ataniq-canary fields prints for a final record.
      [ "$#" -eq 1 ] || usage
      counters_alt "$1" || out '     (alt_screen_on and alt_screen_off are not in the record)'
      ;;
    open-runs)
      # LIST REPO: the open runs of REPO in the text of qargi local list.
      [ "$#" -eq 2 ] || usage
      open_runs "$1" "$2"
      ;;
    unread-runs)
      # LIST: the open runs of qargi local list whose repo.path cannot be read.
      [ "$#" -eq 1 ] || usage
      open_unread "$1"
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
    holder-wait)
      # HSTATE EPOCH UNIT: step 12's wait for holder.json, as the script asks
      # it (the helper of build/canary and the unit tool on PATH are stubs in
      # the tests). It prints "ok" or "failed", and HW_END.
      [ "$#" -eq 3 ] || usage
      BIN=build/canary
      HSTATE=$1
      START_EPOCH=$2
      UNIT=$3
      T_START='2026-11-19 12:00:00'
      CUR=12
      IN_CHAIN=1
      if holder_started; then out "result=ok end=$HW_END"; else out "result=failed end=$HW_END"; fi
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
agent_footer_mode=manual
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
  # The hint file lies beside the config: a list of suggested paths for the
  # path prompts, written by canary-prepare.sh. It is optional.
  case $CONFIG in */*) HINTS=${CONFIG%/*}/answers.hint ;; *) HINTS=./answers.hint ;; esac
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
    dry "check: no Qargi run of this project is open (qargi local list, the rows whose repo.path is $REPO that are not applied and merged); without qargi on PATH, when the list cannot be read, or when an open run has no readable repo.path, ask the lead as before and say so in the log"
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
  _unread=$(open_unread "$LASTOUT")
  if [ -n "$_unread" ]; then
    printf '%s\n' "$_unread" | sed 's/^/   open run without a readable repo.path: /'
    out '   the repository of the run(s) above is not known: the lead is asked'
    note "open runs of qargi local list whose repo.path cannot be read: $(printf '%s' "$_unread" | tr '\n' ' '): the machine check of the open runs is not possible, the lead is asked"
    affirm 'Lead: no Qargi run is open' yes || return 1
    return 0
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

# facts_verdict FILE KIND reads one fact report (KIND unit: the report of
# leadunit-qualify; plan: the summary of the launched plan) and prints "ok TEXT"
# when it shows that everything passed and "bad REASON" otherwise. What was read
# is in the evidence. ask_file calls it before step 2 records the file.
facts_verdict() {
  _fl=$EV/out/02-facts-$2.fields
  if ! "$BIN/ataniq-canary" fields --file "$1" >"$_fl" 2>"$_fl.err"; then
    printf '%s\n' "bad the file cannot be read as one JSON object: $1"
    return 0
  fi
  _flds=$(cat "$_fl")
  if [ "$2" = unit ]; then _v=$(unit_facts_verdict "$_flds"); else _v=$(plan_facts_verdict "$_flds"); fi
  case $_v in
    'ok '*) printf '%s\n' "ok ${_v#ok } (fields read: $_fl)" ;;
    *) printf '%s\n' "bad the $2 facts do not show a pass: ${_v#bad }" ;;
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
    if [ "$_kind" = unit ]; then
      _hk=unit-report
      _look='the report parses, its verdict is pass and every entry of checks has the result pass'
    else
      _hk=plan-summary
      _look='the summary parses, every step has the status ok or skipped, exit.code is 0 and every case result is ok or not-checked'
    fi
    ask_file "$_hk" "Lead: the path of $_what" "$_look" facts_verdict "$_kind" || return 0
    fact facts.txt "$ANSWER"
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
  if [ "$DRY" -eq 1 ]; then _th='<hash>'; else _th=$(cat "$EV/template.sha256"); fi
  ask_file template-review "Lead: the path of the template's review on file" \
    "the review is a text or markdown file that is not a qualify-mirror report (no JSON file with cases and a template) and holds the template's hash ($_th) or the word verdict" \
    review_verdict "$_th" || return 0
  fact template-records.txt "$ANSWER"
  affirm 'Owner: you have read the template and its note' read || return 0
  out '   Owner: run qualify-mirror once now, yourself, in your own terminal, with the lead present'
  out '   (docs/qualification-lead-mirror.md), with this template and a new output file.'
  ask_file mirror-report 'Owner: the path of the report qualify-mirror wrote' \
    "the report is one JSON object whose template.sha256 is the hash computed above ($_th)" \
    mirror_verdict "$_th" || return 0
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
    "the owner opens the account page of the account the sandbox login will use and confirms that it lists the login as its own entry that can be revoked on its own, or that a separate throwaway account is used for the login; the script opens no browser" \
    '"confirmed", with the time, only when the login will be an entry of its own or the account is a throwaway; otherwise "no", which ends the pass here' 'G2'
  out '   Pass B run 1 showed that the provider lists Claude Code logins as rows named Unknown, with no entry that can be'
  out '   identified as the login made in the pass: a page that lists logins is not enough, so the question below is narrower.'
  ask 'Owner: type confirmed only if the account used for the sandbox login lists that login as its own entry that can be revoked on its own, OR if a separate throwaway account is used for it; otherwise type no (the pass ends)'
  check 'the owner confirmed an identifiable revocable login, or a throwaway account' is_eq "$ANSWER" confirmed || return 0
  result confirmed
  done_step
}

# listener_lines reads the text of ss -ltnu on standard input and prints the
# non-loopback listening sockets, sorted.
listener_lines() {
  awk 'NR > 1 && $5 !~ /^(127\.|\[::1\]|\[::ffff:127\.)/ { print $1, $5 }' | LC_ALL=C sort
}

# listeners writes the non-loopback listening sockets, sorted, to a file.
listeners() {
  runcap "$1" ss -ltnu
  if [ "$DRY" -eq 1 ]; then
    dry "keep the sockets on non-loopback addresses, sorted, in $2"
    return 0
  fi
  listener_lines <"$LASTOUT" >"$2"
}

# listeners_are FILE succeeds when the sockets that listen now are the ones in
# FILE. It asks quietly and keeps nothing: the owner stops and restarts his
# listeners in a terminal of his own, which takes a moment.
listeners_are() { ss -ltnu 2>/dev/null | listener_lines | cmp -s - "$1"; }

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
  if [ "$DRY" -eq 1 ]; then
    dry 'ask ss -ltnu every second for up to 60 s until the list is the one of the listeners he kept; say "waiting for the listeners to stop..." once'
  else
    poll_until 'the listeners to stop' listeners_are "$EV/listeners-expected.txt" || :
  fi
  listeners listeners-kept "$EV/listeners-kept.txt"
  if [ "$DRY" -eq 1 ]; then
    dry "when the second list is not the one expected, wait $RESAMPLE_SECONDS s and list once more before the step fails; print the sockets that appeared and the ones that are gone"
  elif ! cmp -s "$EV/listeners-expected.txt" "$EV/listeners-kept.txt"; then
    out "   the second list is not the one expected; listing once more in $RESAMPLE_SECONDS s (a socket may come and go, as a DHCP renewal or a browser makes one)..."
    note 'the second list differed: listing once more'
    sleep "$RESAMPLE_SECONDS"
    listeners listeners-kept-again "$EV/listeners-kept.txt"
    if ! cmp -s "$EV/listeners-expected.txt" "$EV/listeners-kept.txt"; then
      out '   appeared since the list of the owner'"'"'s kept listeners (+) and gone (-):'
      LC_ALL=C comm -13 "$EV/listeners-expected.txt" "$EV/listeners-kept.txt" | sed 's/^/     + /'
      LC_ALL=C comm -23 "$EV/listeners-expected.txt" "$EV/listeners-kept.txt" | sed 's/^/     - /'
    fi
  fi
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
    step_head 9 'Version check: hash the stand-in and the probe' \
      "hash the agent executable in the inputs file's install folder and the stand-in of $BIN, and the probe the inputs file names and the probe of $BIN" \
      'each hash is the one make canary-build produced; the values are recorded; a mismatch refuses the start' 'G3, M9'
    wait_key 'Step 9 hashes the stand-in and the probe.'
    if [ "$DRY" -eq 1 ]; then
      dry "hash the agent executable (the install folder of $INPUTS, then ${AGENT0#"$AGENT_INSIDE"/}) and $BIN/ataniq-canary-standin, and record both"
      dry "hash the probe the inputs file's executables.probe names (the copy prepare makes in the root) and $BIN/ataniq-probe, and record both"
      _a=x _b=x _pa=x _pb=x
    else
      _host=$(agent_host)
      check 'the agent executable is present' test -f "$_host" || return 0
      _a=$(sha256sum <"$_host" | awk '{print $1}')
      _b=$(sha256sum <"$BIN/ataniq-canary-standin" | awk '{print $1}')
      printf 'agent %s\nbuilt %s\n' "$_a" "$_b" >"$EV/standin-hash.txt"
      out "   agent: $_a"
      out "   built: $_b"
      _probe=$(fld "$INPUTS" executables.probe)
      check 'the probe the inputs name is present' test -f "$_probe" || return 0
      _pa=$(sha256sum <"$_probe" | awk '{print $1}')
      _pb=$(sha256sum <"$BIN/ataniq-probe" | awk '{print $1}')
      printf 'probe %s\nprobe-built %s\n' "$_pa" "$_pb" >>"$EV/standin-hash.txt"
      out "   probe: $_pa"
      out "   built probe: $_pb"
    fi
    check 'the agent is the stand-in that make canary-build produced' is_eq "$_a" "$_b" || return 0
    check 'the probe is the one that make canary-build produced' is_eq "$_pa" "$_pb" || return 0
  else
    step_head 9 'Version check: the bound agent executable' \
      'run the bound agent executable with --version, outside the sandbox, with an empty environment and a home that is an empty temporary folder' \
      "the output holds the config's version ($RVERSION) as a whole word, and the install folder is the canary's own; both values are recorded; a mismatch refuses the start (the fallback is the config change of G3)" 'G3, M9'
    wait_key 'Step 9 runs the agent executable with --version, outside the sandbox.'
    if [ "$DRY" -eq 1 ]; then
      dry "make an empty temporary folder as the home; run: env -i HOME=<that folder> <install folder of $INPUTS>/${AGENT0#"$AGENT_INSIDE"/} --version; remove the folder; first, the check that argv[0] is a file under /ataniq/agent"
    else
      check "the agent command's first element is a file under $AGENT_INSIDE/ (the executable itself, no wrapper)" is_under_agent_bind "$AGENT0" || return 0
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
  out '   Owner: in a second terminal (T2), run it; it stays in front of that terminal until you stop it'
  out '   with Ctrl-C at step 26, so the console and the intake need terminals of their own:'
  out "     $BIN/ataniqd run --canary $CONFIG"
  out '   Start it first and wait for its listening line, then press Enter here. If it is not up yet'
  out '   when you press Enter, this step asks for it every second for up to a minute.'
  wait_key 'Owner: start the daemon now, yourself; then come back here.'
  DAEMON_UP=1
  if [ "$DRY" -eq 1 ]; then
    dry "ask $BIN/ataniqd status --socket $SOCK every second for up to 60 s (each ask cut off by timeout after 5 s, so about 65 s at most), say \"waiting for the daemon to listen...\" once and print a dot every 5 s; go on as soon as it answers"
  else
    poll_until 'the daemon to listen' daemon_answers || :
  fi
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
  # The journal is read from the first start of the run, local time as
  # journalctl --since takes it.
  if [ "$DRY" -eq 0 ] && [ -z "$T_START" ]; then T_START=$(date '+%Y-%m-%d %H:%M:%S'); fi
  runcap start "$BIN/ataniqd" canary start --config "$CONFIG"
  check 'start succeeded' is_rc 0 || return 0
  if [ "$DRY" -eq 1 ]; then
    dry 'check: the start result names the role state starting, this unit and the waiver'
    dry 'keep the epoch the start result names: step 12 reads the records of that epoch only'
    dry "run: systemctl --user show $UNIT --property=ActiveState --value"
    dry "when the unit is not active (the holder ended at its start), print the last 40 lines of its journal, journalctl --user --unit $UNIT --since <the time of the start>, before the step fails: the holder's reason is only there"
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
    *) show_unit_journal; fail "the role is \"$_state\", not starting"; return 0 ;;
  esac
  check 'the start result names this unit' is_eq "$(fld "$START_OUT" unit)" "$UNIT" || return 0
  check 'the start result shows the waiver in full' is_eq "$(fld "$START_OUT" waiver)" "$WAIVER" || return 0
  # The epoch of this start: step 12 reads the records of this epoch only.
  START_EPOCH=$(fld "$START_OUT" epoch)
  check 'the start result names its epoch' is_number "$START_EPOCH" || return 0
  _active=$(show_prop ActiveState)
  case $_active in
    active | activating) out "   ok: the unit is $_active" ;;
    *) show_unit_journal; fail "the unit is \"$_active\", not active"; return 0 ;;
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
    dry "wait until $HSTATE/holder.json names the epoch of the start of step 11 and says sandbox_started is true (the holder writes the file twice, before and after its start; the folder still holds the last epoch's file until the new holder writes its own), for up to 90 s, or until the unit is no longer active; fail when it never does, with the unit's journal printed first"
    dry "print the values of $HSTATE/holder.json with ataniq-canary fields"
    dry "check: its field waiver is exactly: $WAIVER"
    dry "check: a line of $HSTATE/events.jsonl with that epoch holds the state sandbox-started with the reason assertion-10-waived"
    dry "check: the start line of $EVIDENCE with that epoch holds the waiver"
    dry 'check: the result of start (step 11) showed the waiver'
  else
    # The state folder is the same in every epoch, so each record is matched
    # by the epoch of this start: a file or a line of an earlier epoch does
    # not pass for this one.
    holder_started || return 0
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

# step_14: the console reads its ticket from a file and, while the file is
# empty or absent, only says "waiting for a ticket"; a ticket lives 30 seconds
# from its mint (serve.DefaultTicketLifetime). So the owner starts the console
# first and the script mints after his Enter: the command is printed before the
# mint, with its folder and ticket file known beforehand. The folder is the
# holder's runtime folder of the config (what the daemon's ticket command
# writes in), and the ticket file is the file named "ticket" in it
# (leaddaemon.TicketFileName); the mint's own answer is then checked against
# the file printed, and a step whose ticket went to another file fails.
#
# What the script can read of the console is only the ticket file's size (the
# console empties it when it takes the ticket) and the holder's event log (one
# attach event for each attach). A console that is started again finds the file
# empty and attaches nothing: that is what the second console shows, and the
# script reads that the file is empty and that the number of attach events has
# not changed. The kit has no command that presents a used ticket to the
# holder, so it does not claim that a used ticket is refused. The test is made
# in the first epoch only. The owner's yes at the end of each part is the
# attestation; the script answers nothing.
step_14() {
  # The first epoch's console is the writer: the owner pastes there, and the
  # writer's Hello sets the size of the session. In the second epoch the fence
  # writes and the owner only watches, so the ticket is a viewer's: a viewer's
  # Hello does not set the size, and the screen the stand-in drew once at its
  # start stays the size it was recorded at (the fence recognises no other).
  if [ "$EPOCH_NO" -eq 2 ]; then _role=viewer; else _role=writer; fi
  if [ "$_role" = viewer ]; then
    step_head 14 'Attach the console, then mint a viewer ticket' \
      'the owner starts the console first, in his own terminal, with the command printed here, and it waits for a ticket; after his Enter the script runs ataniqd canary ticket for a viewer (the ticket lives 30 seconds) and watches the ticket file; in this epoch the console only watches (read-only: no key, no paste and no size is sent), because the fence writes the message' \
      'the console attaches read-only and the pane shows the agent; the owner pastes nothing in this epoch' 'none'
  else
    step_head 14 'Attach the console, then mint a writer ticket' \
      'the owner starts the console first, in his own terminal, with the command printed here, and it waits for a ticket; after his Enter the script runs ataniqd canary ticket for a writer (the ticket lives 30 seconds) and watches the ticket file; in the first epoch the owner then starts the same console once more in a fourth terminal: it finds the ticket file empty and does not attach' \
      'the console attaches and the pane shows the agent; in the first epoch the second console does not attach and says it waits for a ticket, the ticket file is empty and the event log shows no second attach' 'none'
  fi
  check "the holder's runtime folder is known" test -n "$HRUN" || return 0
  _tf=$HRUN/ticket
  if [ "$EPOCH_NO" -eq 2 ]; then
    out '   Owner: the console of the first epoch must be closed (Ctrl+\ leaves the session pane, then q): one left open waits on the same ticket file and takes this epoch'"'"'s ticket for itself.'
    out '   In this epoch the console attaches as a read-only VIEWER: you only watch, you paste nothing, and the size of'
    out '   the window does not matter.'
  else
    out '   Owner: this console is the WRITER: you paste into it at step 17. Its window must be at least 100 columns by 30 rows'
    out '   (below that the console drops a paste). The size of its session pane becomes the agent'"'"'s terminal size.'
    if [ "$PASS" = B ]; then
      out '   The fence recognises only screens shaped like the recorded ones (120 columns by 36 rows); that holds for pass A'"'"'s'
      out '   replay. The live agent has never been read at another size: keep the window large enough that the composer'
      out '   and the footer row both show. A resize redraws the agent; do not resize it between step 16 and step 17.'
      out '   pasta may write two lines on the agent'"'"'s pane before the agent'"'"'s first screen (about an AVX2 build and a'
      out '   nameserver address): that is expected, not a fault.'
    else
      out '   pasta may write two lines on the agent'"'"'s pane above the stand-in'"'"'s prompt (about an AVX2 build and a'
      out '   nameserver address): that is expected, not a fault.'
    fi
  fi
  out '   Owner: in a third terminal (T3), start the console now, with this command. It waits for a ticket and'
  out '   says "waiting for a ticket". Do not start it later than your Enter below: a ticket lives 30 seconds.'
  _viewer=
  if [ "$EPOCH_NO" -eq 2 ]; then _viewer=' --viewer'; fi
  out "     $BIN/ataniq-console --attach $HRUN --ticket-file $_tf$_viewer"
  wait_key 'Owner: press Enter when the console is up and says it waits for a ticket. The ticket is minted then.'
  runcap ticket "$BIN/ataniqd" canary ticket --config "$CONFIG" --attach-role "$_role"
  check 'the ticket was minted' is_rc 0 || return 0
  if [ "$DRY" -eq 1 ]; then
    dry "check: the ticket file the mint names is the one printed above, $_tf (the console was told that file)"
    dry "watch the ticket file $_tf every second for up to 60 s: the console empties it when it takes the ticket (no content is read); say what was seen and go on to the question either way"
  else
    check "the ticket file the mint names is the one printed above ($_tf)" is_eq "$(fld "$LASTOUT" ticket_file)" "$_tf" || return 0
    out '   The script now watches the ticket file, which the console empties when it takes the ticket.'
    if poll_until 'the console to take the ticket' ticket_taken "$_tf"; then
      out '   ok: the ticket was taken'
      note 'the console took the ticket (the ticket file is empty)'
    else
      out '   the ticket file still holds a ticket: the console has not taken it, and a ticket lives 30 seconds (the question below is still yours to answer)'
      note 'the ticket file still held a ticket after the wait'
    fi
  fi
  affirm 'Owner: the pane shows the agent' yes || return 0
  if [ "$EPOCH_NO" -eq 1 ]; then
    if [ "$DRY" -eq 1 ]; then
      dry "count the attach events of $HSTATE/events.jsonl now (each line read as JSON by ataniq-canary fields)"
    else
      _att0=$(count_lines "$HSTATE/events.jsonl" event=attach)
      note "attach events before the second console: $_att0"
    fi
    out '   Owner: in a fourth terminal (T4), run the same console command once more. The first console emptied the'
    out '   ticket file when it took the ticket, so this one finds none: it does NOT attach. All it shows is its'
    out '   status "waiting for a ticket". Read that line, then quit it (q). Leave the first console open: the'
    out '   message of step 17 is pasted into it.'
    affirm 'Owner: the second console did not attach and shows "waiting for a ticket"' yes || return 0
    if [ "$DRY" -eq 1 ]; then
      dry "check: the ticket file $_tf is still empty, and the attach events of $HSTATE/events.jsonl are as many as before: no second attach happened"
    else
      check 'the ticket file is still empty: no second ticket is waiting' ticket_taken "$_tf" || return 0
      _att1=$(count_lines "$HSTATE/events.jsonl" event=attach)
      note "attach events after the second console: $_att1"
      check "the event log shows no second attach ($_att1 attach events, $_att0 before)" is_eq "$_att1" "$_att0" || return 0
    fi
  else
    out '   (the second console is tried in the first epoch only)'
  fi
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
    "print the agent's arguments for the owner to confirm the model is the cheapest; start the 30-minute timer; the owner logs in inside the sandbox, in the pane, and answers logged-in or failed (failed ends the pass); list the files that changed in the canary's config folder, by name" \
    "the login writes only the canary's config folder: the script lists the files that changed there, by name" 'G2'
  out "   the agent's arguments, from the config:"
  printf '%s\n' "$CFG_SHOW" | while IFS= read -r _l; do
    case $_l in agent_argv_*) out "     $_l" ;; esac
  done
  affirm 'Owner: the arguments above name the cheapest model (no --model shown: answer no, then prepare a new root with --model NAME)' yes || return 0
  manifest_of "$LCONFIG" "$EV/canary-config-before.txt"
  if [ "$DRY" -eq 1 ]; then
    dry "start the timer of $CAP_SECONDS s and the tally of $CAP_TURNS turns"
  else
    T0=$(date +%s)
    note "the cap's timer starts"
  fi
  out '   Owner: log in inside the sandbox now, in the pane, yourself. The script holds no credential and opens no browser.'
  out '   The login URL is long and wraps over several rows. It has to leave the pane by hand:'
  out '   - the agent'"'"'s own copy key does nothing (the session'"'"'s clipboard requests are dropped) and a click on the'
  out '     link does nothing (hyperlinks are not drawn);'
  out '   - while the agent holds the mouse, the console makes no selection in session focus: leave the session pane'
  out '     first (Ctrl+\), then drag over all the rows of the URL, and the console copies on release;'
  out '   - the console copies a wrapped line as two lines: paste it into the browser'"'"'s address bar and remove the'
  out '     line break (and any space) it left in the URL;'
  out '   - the copy needs your outer terminal to accept the console'"'"'s clipboard write, which the console cannot tell:'
  out '     if nothing arrives, read the URL off the pane and join its rows by hand.'
  out '   Paste the code back with the session pane in focus (the window at least 100 by 30). Paste nothing of the'
  out '   login into THIS terminal. If the login fails, start it again in the pane (the agent'"'"'s own login command).'
  out '   The cap'"'"'s timer has started already: the minutes of the login are part of the 30.'
  ask 'Owner: type logged-in when the login is done in the pane, or failed to give up (the pass then ends and the clean-up chain runs)'
  while [ "$DRY" -eq 0 ] && [ "$ANSWER" != logged-in ] && [ "$ANSWER" != failed ] && [ "${_askok:-1}" -eq 1 ]; do
    out '   ask again: only logged-in or failed is taken here (a wrong word must not spend the login)'
    ask 'Owner: type logged-in when the login is done in the pane, or failed to give up'
  done
  if [ "$DRY" -eq 0 ] && [ "$ANSWER" != logged-in ]; then
    fail "the login did not succeed (answer \"$ANSWER\")"
    return 0
  fi
  LOGIN_AT=$(now)
  note "the owner said the login is done at $LOGIN_AT"
  manifest_of "$LCONFIG" "$EV/canary-config-after.txt"
  if [ "$DRY" -eq 1 ]; then
    dry "list by name the files of $LCONFIG that the login changed (names only)"
  else
    out "   files the login changed in $LCONFIG, by name:"
    awk 'FILENAME == ARGV[1] { before[$0] = 1; next } !($0 in before) { $1 = $2 = $3 = ""; sub(/^ +/, ""); print "     " $0 }' \
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
    "ask the daemon's status; the lead reads the holder's status and its 7501 counters in the console and the script records what he reads: while the holder runs the counters are only in its status answer, which no command of the kit reads, and the event log holds none; the script prints them itself from final.json after the child ends (step 22); the lead also reads alt_screen_on in the status: alt_screen_on above 0 means the agent drew with the fullscreen renderer, which the fence requires; at 0 the agent drew on the main screen and every inject ends not-now; then the owner looks at the pane before step 17 (the empty composer, no dialog, the expected mode on the last row, no notice) and answers yes"  \
    "the holder's idle field reads unknown in this feature, and the answer is recorded, not judged (the fence judges idle itself inside a delivery); the OSC 7501 query was answered before the DA1 reply, and at least one report was tracked; if no report ever arrives, that is recorded: the fence will then end uncertain, and the reason is known; an update notice on the screen is recorded if it shows (L9); the owner's look at the pane is answered yes" 'B1'
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
    out '   Also read alt_screen_on (and alt_screen_off) in the same status: alt_screen_on above 0 means the'
    out '   agent drew with the fullscreen renderer (CLAUDE_CODE_NO_FLICKER=1), which the fence reads;'
    out '   0 means it drew on the main screen, and every inject will end not-now.'
    note 'the 7501 counters are not read by the script at this step: the lead records what the console shows'
  fi
  ask "Lead: what does the holder's idle field read (the holder reports unknown in this feature; the fence judges idle itself inside a delivery)"
  note "holder idle field: $ANSWER"
  ask 'Lead: was the OSC 7501 query answered before the DA1 reply (yes, no, unknown)'
  ask 'Lead: how many reports were tracked (a number, or none: then the fence will end uncertain and the reason is known)'
  ask 'Lead: does the screen show an update notice (no, or what it says in your words)'
  out '   Owner: before step 17 the pane must show the EMPTY composer and nothing else:'
  out '   - no dialog (answer the trust question and "press Enter to continue" yourself);'
  out "   - the LAST row reads exactly the mode the config expects ($(cfg agent_footer_mode)) and the usual hints,"
  out '     with no other text on that row (an update or install notice there means not-now);'
  out '     if it shows auto mode, press Shift+Tab until it shows the expected mode;'
  out '   - the console window is large enough that the composer and the footer both show (a resize redraws the'
  out '     agent; the fence has never read a live screen at another size);'
  out '   - then leave the session pane (Ctrl+\) and touch neither key nor mouse there for 3 seconds.'
  ask 'Owner: does the pane show the empty composer, the expected mode and no notice (yes; or go-on: the fence will then answer not-now and you paste the message at step 17; anything else ends the pass)'
  case $ANSWER in
    yes) ;;
    go-on) note 'the pane was not ready; the owner goes on to the paste path' ;;
    *) [ "$DRY" -eq 1 ] || { fail "the pane was not made ready: \"$ANSWER\""; return 0; } ;;
  esac
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
  # A live agent must write the value only: the label line is the stand-in's
  # and the daemon's admission's, and the gate in step 20 is an exact line.
  printf '%s\n%s\n' "Create the file canary-token.txt in the top folder of this repository. It must hold exactly one line: the value after the label on the next line of this message (the value only, without the label), then one line feed. Create the branch $BRANCH, commit that one file on it, and do nothing else." \
    "canary-token: $TOKEN" >"$BODY"
}

# paste_message tells the owner how to put the message into the pane himself.
# It must arrive as one bracketed paste followed by one Enter: the stand-in
# commits only from a paste, and the body has two lines, so typed line by line
# the first Enter would submit half of it to a live agent.
paste_message() {
  out '   Owner: first give the session pane the focus: select the session terminal and press Enter. The focus bar'
  out '   then names the session (Ctrl+\ leaves it). A paste while the console has the focus itself is dropped'
  out '   without a word, and the first Enter only moves the focus.'
  out '   Then put the message into the pane yourself. Do NOT type it. Copy the whole text of this file:'
  out "     $BODY"
  out '   and paste it into the pane with your terminal'"'"'s paste, so that it arrives as ONE paste'
  out '   (a bracketed paste; typed line by line, the first Enter would send half of it).'
  out '   Copy the file'"'"'s text (cat the file), not the lines of this screen: the stand-in takes the token only'
  out '   from a line that starts with "canary-token: ". "no token" in the pane means the paste was not the'
  out '   file'"'"'s text: paste again.'
  out '   Then press Enter in the pane ONCE. The text of the file, to compare with what you paste:'
  out "----- begin of ${BODY##*/} -----"
  cat "$BODY"
  out '----- end -----'
}

step_17() {
  if [ "$PASS" = A ]; then
    _exp='the config declares the tested version and the stand-in draws a screen no recogniser knows, so the fence answers not-now with the reason not-ready and writes nothing (L6); the owner then pastes the message into the pane as one paste and presses Enter once; a refusal by the daemon (exit status 30) or by the fence (refused, untested: a wrong config) fails the step'
  else
    _exp='a refusal by the daemon (exit status 30) fails the step; otherwise the result is recorded in the neutral words; exactly one result gets the owner'"'"'s Enter: uncertain with the reason not-submitted and written partial, recorded as its own line owner-enter; on uncertain with the reason died nothing is pressed, the owner looks at the pane and decides; on the G3 fallback the fence refuses an untested version, and on not-now with written nothing (the screen was not ready) the owner clears the pane and pastes the message as one paste'
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
      dry "print the file to paste from, $BODY, and its text; the owner pastes it into the pane with the terminal's paste, as one paste, and presses Enter once: he does not type it; he first gives the session pane the focus, because a paste into an unfocused console is dropped silently; wait"
    else
      dry 'fail the step on exit status 30 (the daemon refused the message: the fence was not asked)'
      dry 'on uncertain, not-submitted, partial and on nothing else: the owner presses Enter once in the pane; record the line owner-enter with the time'
      dry 'on uncertain, died: press nothing (the input may hold an open paste); the owner looks at the pane; record what he saw and his decision'
      dry "on any refusal, and on not-now with written nothing: say what to clear first, print the file to paste from, $BODY; the owner pastes the message himself, as one paste, and presses Enter once: he does not type it; he first gives the session pane the focus; then ask for the word pasted (an empty line is asked again, up to 3 times; any other word ends the pass); that is recorded"
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
      out "   The fence wrote nothing ($_o, ${_r:-no reason}). On refused/untested that is the G3 fallback (the config states the observed version)."
      out '   On not-now the screen was not ready: first clear what the pane shows (a dialog, an update or install notice,'
      out '   text in the composer) so that the composer is empty, then paste.'
      paste_message
      ask 'Owner: type pasted here after you pasted the message in the pane and pressed Enter once; anything else ends the pass and runs the clean-up chain'
      _re=0
      while [ -z "$ANSWER" ] && [ "${_askok:-1}" -eq 1 ] && [ "$_re" -lt 3 ]; do
        _re=$((_re + 1))
        out '   ask again: an empty line is not an answer (the login is spent: type pasted, or anything else to end the pass)'
        ask 'Owner: type pasted after you pasted the message in the pane and pressed Enter once; anything else ends the pass'
      done
      if [ "$ANSWER" != pasted ]; then
        fail "the fence wrote nothing ($_o) and the owner ended the pass"
        return 0
      fi
      result "$_o (${_r:-no reason}): the owner pasted the message"
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

# show_token_file prints what canary-token.txt holds in the lead's folder, the
# first 3 lines and nothing else, after the gate failed, so that the owner and
# the lead see which wrong reading happened (the label too, a blank line, a
# comment). The gate itself is not changed: it stays an exact line. The agent
# wrote the file, so the text is cut to 200 bytes and every byte that is not
# printable becomes a question mark before it reaches the terminal. The file is
# read as a plain file, never with git (the lead has run in the tree).
show_token_file() {
  _stf=$TREE/canary-token.txt
  # The agent may still run and may swap the file: the file is opened once,
  # under a timeout (a FIFO would hang), and the opened file is checked (a plain
  # file whose real path is the path, so no symlink), never the name before.
  _stp=$(realpath -- "$TREE" 2>/dev/null) && _stp=$_stp/canary-token.txt || _stp=
  if [ -n "$_stp" ] && _stc=$(timeout 2 sh -c 'exec 3<"$1" || exit 1; [ "$(readlink /proc/self/fd/3)" = "$1" ] && [ -f /proc/self/fd/3 ] || exit 1; head -c 200 <&3' sh "$_stp" 2>/dev/null); then
    out '   the gate failed. canary-token.txt, the working file (not the committed one), in the lead'"'"'s folder holds (first 3 lines, nothing else):'
    printf '%s\n' "$_stc" | sed -n '1,3p' | tr -c '[:print:]\n' '?' | sed 's/^/     | /'
  else
    out '   the gate failed. There is no canary-token.txt, as a plain file, at the top of the lead'"'"'s folder'
    out "   ($_stf): the agent wrote it elsewhere, or not at all."
  fi
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
  _revout=$LASTOUT
  runcap verify "$BIN/ataniq-intake" verify --state "$STATE" --project "$PROJECT" --role "$LAYOUT" --branch "$BRANCH" \
    --gate "token=grep -qxF $TOKEN canary-token.txt" --sandbox "$TEMPLATE" --records "$_rec" --work "$_work"
  if [ "$DRY" -eq 0 ] && [ "$RC" -ne 0 ]; then show_token_file; fi
  check 'the gate passed: the file holds the token' is_rc 0 || return 0
  out '   Owner: the sign-off is yours. In your own terminal, with the tip and the digest that review printed:'
  if [ "$DRY" -eq 1 ]; then
    dry "print the review's Digest: and Tip: lines, read from $EV/out/20-e1-review.out"
  else
    out "   the review's lines (from $_revout):"
    # The text comes from the agent's review: at most 4 lines of 300 bytes, control characters replaced.
    _dt=$(grep -E '^(Digest|Tip):' "$_revout" 2>/dev/null | sed -n '1,4p' | LC_ALL=C tr -c '[:print:]\n' '?' | LC_ALL=C cut -b1-300 || :)
    if [ -n "$_dt" ]; then
      printf '%s\n' "$_dt" | sed 's/^/     /'
    else
      out '     (no Digest line in the review output: read the review output above)'
    fi
  fi
  out "   Use the review's digest, not the verify output's plan digest: integrate refuses the plan digest."
  out "     $BIN/ataniq-intake integrate --state $STATE --project $PROJECT --role $LAYOUT --branch $BRANCH --target $UPSTREAM \\"
  out "       --tip <TIP> --digest <DIGEST> --accept-change --records $_rec"
  out '   and one --accept-path for each path review listed under "Needs your sign-off".'
  ask 'Owner: integrate succeeded (type: yes)'
  _re=0
  while [ "$DRY" -eq 0 ] && [ "$ANSWER" != yes ] && [ "${_askok:-1}" -eq 1 ] && [ "$_re" -lt 2 ]; do
    _re=$((_re + 1))
    out "   ask again ($_re of 2): only the word yes is taken; type it once integrate has succeeded, or anything else to end the pass"
    ask 'Owner: integrate succeeded (type: yes)'
  done
  if [ "$DRY" -eq 1 ]; then
    dry 'ask again when the answer is not exactly yes, up to 2 more times (3 answers in all); fail only after the third wrong answer or at the end of the input'
  elif [ "$ANSWER" != yes ]; then
    fail 'the answer was not "yes": Owner: integrate succeeded'
    return 0
  fi
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
    "run ataniqd canary stop; then systemctl --user revert on the unit; keep the unit's journal (what the holder said on its standard error) in the evidence; list the user manager's units and unit files for ataniq-*; list the unit's name under the user manager's transient/ and user.control/ runtime folders" \
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
  save_unit_journal
  runcap units-after systemctl --user list-units --all --no-legend --plain 'ataniq-*'
  _ua=$EV/units-after-$EPOCH_NO$(chain_sfx "$EV/units-after-$EPOCH_NO.txt").txt
  if [ "$DRY" -eq 0 ]; then awk '{print $1}' "$LASTOUT" | LC_ALL=C sort >"$_ua"; fi
  check 'no unit remains: the units are those of step 1' cmp -s "$EV/units-before.txt" "$_ua" || :
  runcap unit-files-after systemctl --user list-unit-files --no-legend 'ataniq-*'
  _ufa=$EV/unit-files-after-$EPOCH_NO$(chain_sfx "$EV/unit-files-after-$EPOCH_NO.txt").txt
  if [ "$DRY" -eq 0 ]; then awk '{print $1}' "$LASTOUT" | LC_ALL=C sort >"$_ufa"; fi
  check 'no unit file remains: the unit files are those of step 1' cmp -s "$EV/unit-files-before.txt" "$_ufa" || :
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
    dry "print the 7501 counters and the alternate-screen counters (alt_screen_on, alt_screen_off) of the holder's final record, $HSTATE/final.json"
  elif [ -f "$HSTATE/final.json" ]; then
    out "   the 7501 counters of the holder's final record:"
    _fin=$("$BIN/ataniq-canary" fields --file "$HSTATE/final.json" 2>/dev/null || :)
    counters_7501 "$_fin" || out '     (none is named for 7501)'
    out "   the alternate-screen counters of the final record (alt_screen_on above 0: the fullscreen renderer):"
    counters_alt "$_fin" || out '     (alt_screen_on and alt_screen_off are not in the record)'
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
  if [ "$DRY" -eq 1 ]; then
    dry 'print the time of this pass'"'"'s start and of its login, about, in UTC and in local time, and the warning that only the login made at that time may be revoked'
  else
    out "   This pass started at $STAMP (UTC); the login was reported done at about ${LOGIN_AT:-(no login time was recorded)} (UTC)."
    if [ -n "${LOGIN_AT:-}" ]; then
      out "   In your local time that is about $(date -d "$LOGIN_AT" '+%Y-%m-%d %H:%M:%S %Z' 2>/dev/null || echo '(the local time could not be worked out)'): the account page shows its own zone."
    fi
  fi
  out '   Identify the login as a new row whose last-active time falls during this run, and revoke ONLY that row, no other, and not'
  out '   "log out of all devices" on an account that holds your own sessions: the lead'"'"'s among them use logins of the same account.'
  out '   On a separate throwaway account used for this login only, "log out of all devices" revokes it completely and is the way.'
  out '   When no row can be identified (the provider lists only Unknown rows), do not guess: type cannot-identify: and what you see.'
  ask 'Owner: revoke the login on the account page yourself; type revoked when it is gone from the list, no-login when no login was ever made in this pass, or cannot-identify: <what you see> when no row can be identified (a finding)'
  case $ANSWER in
    revoked) result revoked ;;
    no-login) result 'nothing to act on: no login was made' ;;
    cannot-identify:*[![:space:]]*)
      # Accepted only with some text after the colon. Accepted, but a finding: the login is still live and the credential stays exposed.
      FINDINGS=$((FINDINGS + 1))
      note "finding: the login could not be identified on the account page: ${ANSWER#cannot-identify:}"
      out '   accepted, and recorded as a finding: the login could not be revoked on its own; the credential stays exposed until it is.'
      result "cannot-identify (a finding):${ANSWER#cannot-identify:}"
      ;;
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
  if [ "$DRY" -eq 1 ]; then
    dry 'ask ss -ltnu every second for up to 60 s until the list is the first list of step 6; say "waiting for the listeners to come back..." once'
  else
    poll_until 'the listeners to come back' listeners_are "$EV/listeners-before.txt" || :
  fi
  listeners listeners-after "$EV/listeners-after.txt"
  check 'the list equals the first list of step 6' cmp -s "$EV/listeners-before.txt" "$EV/listeners-after.txt" || :
  result restored
  done_step
}

# manifest_changes BEFORE AFTER prints, tab separated and sorted by path, one
# line for each file that is only in AFTER (added), only in BEFORE (removed) or
# in both with another hash, size or time (changed). A manifest line is hash,
# size, time and then the path, which may hold spaces.
manifest_changes() {
  awk '
    { k = $1 " " $2 " " $3; p = $0; sub(/^[^ ]+ [^ ]+ [^ ]+ /, "", p) }
    FILENAME == ARGV[1] { b[p] = k; next }
    { a[p] = k }
    END {
      for (p in a) { if (!(p in b)) print "added\t" p; else if (a[p] != b[p]) print "changed\t" p }
      for (p in b) if (!(p in a)) print "removed\t" p
    }' "$1" "$2" | LC_ALL=C sort -t "$(printf '\t')" -k2
}

# meaningful_path succeeds for the path of a file the login writes: a file at
# the top of the folder (./NAME) whose name is a credential, a token, a secret
# or an authorisation file, or the runtime's state file or a settings file.
# Anything deeper (transcripts, caches, per-session files) is not judged: the
# running sessions of the owner write those, except a project folder named for
# the sandbox's working folder (./projects/-work-*): only the canary's agent
# works in /work, so its transcripts there mean the sandbox touched this folder.
meaningful_path() {
  case $1 in
    ./projects/-work-*) return 0 ;;
    ./*/*) return 1 ;;
    ./*[Cc]redential* | ./*[Tt]oken* | ./*[Ss]ecret* | ./*[Oo]auth* | ./*[Aa]uth* | ./.claude.json* | ./settings*.json) return 0 ;;
  esac
  return 1
}

# canary_owns succeeds when the file at path $1 in the after-manifest of step 25
# has a hash that the canary's own config manifests (step 15, and step 25 for a
# token the canary refreshed since) hold too: the
# canary's own credential, proven, and not another session's refresh. The hash
# of an empty file and the mark of a non-regular file never count.
canary_owns() {
  _co_h=$(awk -v p="$1" '{ q = $0; sub(/^[^ ]+ [^ ]+ [^ ]+ /, "", q) } q == p { print $1; exit }' "$EV/manifest-after.txt" 2>/dev/null)
  case $_co_h in
    '' | not-a-regular-file | e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855) return 1 ;;
  esac
  # cat goes on past a missing file (no manifest of step 15); awk would stop.
  cat "$EV/canary-config-after.txt" "$EV/canary-config-now.txt" 2>/dev/null |
    awk -v h="$_co_h" -v p="$1" '{ q = $0; sub(/^[^ ]+ [^ ]+ [^ ]+ /, "", q) } $1 == h && q == p { f = 1 } END { exit !f }'
}

# canary_wrote_login succeeds when the canary's own login added or changed a
# file of the login's kind in the canary's config folder between step 15's two
# manifests. When it did not, nothing can prove a file in the real folder to be
# the canary's, and the isolation of the config folder is suspect.
canary_wrote_login() {
  [ -f "$EV/canary-config-after.txt" ] || return 1
  [ -f "$EV/canary-config-before.txt" ] || : >"$EV/canary-config-before.txt"
  manifest_changes "$EV/canary-config-before.txt" "$EV/canary-config-after.txt" >"$EV/canary-config-changes.txt"
  # Only a credential proves the login stayed in the canary's folder: the state
  # and settings files are written by any start of the runtime.
  while IFS="$(printf '\t')" read -r _cwk _cwp <&5; do
    [ "$_cwk" != removed ] || continue
    case $_cwp in
      ./*/*) ;;
      ./*[Cc]redential* | ./*[Tt]oken* | ./*[Ss]ecret* | ./*[Oo]auth* | ./*[Aa]uth*) return 0 ;;
    esac
  done 5<"$EV/canary-config-changes.txt"
  return 1
}

step_25() {
  if [ "$PASS" = A ]; then
    skip 25 'Manifest, after: the same folder as step 7' 'G2'
    return 0
  fi
  step_head 25 'Manifest, after' \
    'write names, sizes, times and hashes of the same folder as step 7 and compare the two manifests' \
    'no difference; the owner'"'"'s own running sessions write the folder, so every difference is counted and only the files of the login'"'"'s own kind (the credential and the runtime'"'"'s state and settings files, at the top of the folder) are judged, and a difference in one of those is a finding only when its new hash is one that the canary'"'"'s own config folder holds (otherwise it is a question to the owner and the lead, but a finding without the question when the canary'"'"'s own login wrote no file of its kind in its own folder, and the files the owner explained are named in the result), and so is a new path under projects/-work-; nothing is repaired by the script' 'G2'
  if [ "$DRY" -eq 0 ] && [ ! -f "$EV/manifest-before.txt" ]; then
    wait_key 'Step 7 wrote no manifest in this run.'
    result 'nothing to act on: no manifest was written before'
    return 0
  fi
  wait_key 'Step 25 hashes the files of the folder of step 7.'
  if [ "$DRY" -eq 1 ]; then
    if [ "$IN_CHAIN" -eq 1 ]; then dry 'say so and record it when step 7 wrote no manifest in this run: nothing to compare'; fi
    manifest_of '<the folder of step 7>' "$EV/manifest-after.txt"
    manifest_of '<the canary'"'"'s own config folder>' "$EV/canary-config-now.txt"
    dry 'compare the two manifests; count the files added, removed and changed; print the top-level names that differ; a difference in a credential, token, secret, authorisation, state or settings file at the top of the folder is a finding; every other path is only counted (no content); repair nothing'
    result 'no difference'
    done_step
    return 0
  fi
  manifest_of "$(cat "$EV/live-config-folder.txt")" "$EV/manifest-after.txt"
  # The canary's own folder as it is now: a token it refreshed after step 15.
  if [ -d "$LCONFIG" ]; then manifest_of "$LCONFIG" "$EV/canary-config-now.txt"; else : >"$EV/canary-config-now.txt"; fi
  manifest_changes "$EV/manifest-before.txt" "$EV/manifest-after.txt" >"$EV/manifest-changes.txt"
  if [ ! -s "$EV/manifest-changes.txt" ]; then
    result 'no difference'
  else
    _added=$(awk -F'\t' '$1 == "added"' "$EV/manifest-changes.txt" | awk 'END { print NR }')
    _gone=$(awk -F'\t' '$1 == "removed"' "$EV/manifest-changes.txt" | awk 'END { print NR }')
    _chg=$(awk -F'\t' '$1 == "changed"' "$EV/manifest-changes.txt" | awk 'END { print NR }')
    out "   the manifests differ: $_added added, $_gone removed, $_chg changed. The owner's own running sessions of the"
    out '   runtime (the lead'"'"'s among them) write this folder all the time, so a count alone is not a leak. What would'
    out '   be one: a change in the login'"'"'s own files, the credential and the runtime'"'"'s state and settings files at the'
    out '   top of the folder. Those are judged here; every other path is only counted. Nothing is repaired.'
    out '   files at the top of the folder that differ (names only):'
    awk -F'\t' '$2 ~ /^\.\/[^\/]*$/ { print "     " $1 " " $2 }' "$EV/manifest-changes.txt"
    _mean=0
    _expl=
    _cw=1
    # No manifest of step 15: no login was completed, so nothing of the
    # canary's can be in the folder; a difference is a question, not a finding.
    if [ -f "$EV/canary-config-after.txt" ]; then canary_wrote_login || _cw=0; fi
    # The loop reads the changes file on fd 4: ask reads the owner from stdin.
    while IFS="$(printf '\t')" read -r _k _p <&4; do
      if meaningful_path "$_p"; then
        if [ "${_p#./projects/-work-}" != "$_p" ] || { [ "$_k" != removed ] && canary_owns "$_p"; }; then
          _mean=$((_mean + 1))
          out "   MEANINGFUL: $_k $_p"
        elif [ "$_cw" -eq 0 ]; then
          _mean=$((_mean + 1))
          out "   MEANINGFUL (the canary's login wrote no file of its kind in its own folder, so nothing proves this one is another session's): $_k $_p"
        else
          out "   changed, not the canary's file (a running session's refresh looks so): $_k $_p"
          ask "Owner and lead: is the change of $_p explained by your own running sessions, one that ran during this pass (type yes, or say what is not)"
          if [ "$ANSWER" = yes ]; then
            note "the change of $_p is explained by the owner's own sessions"
            _expl="$_expl $_p"
          else
            _mean=$((_mean + 1))
          fi
        fi
      fi
    done 4<"$EV/manifest-changes.txt"
    if [ "$_mean" -gt 0 ]; then
      out "   $_mean file(s) of the login's kind differ (the canary's own credential, a name the owner and the lead did not"
      out '   explain, or a transcript of the sandbox'"'"'s working folder); the evidence holds both manifests and the changes.'
      FINDINGS=$((FINDINGS + 1))
      if [ -n "$_expl" ]; then
        result "a difference in $_mean file(s) of the login's kind, reported as it is (explained by the owner as his own sessions:$_expl)"
      else
        result "a difference in $_mean file(s) of the login's kind, reported as it is"
      fi
    else
      note "the real config folder: $_added added, $_gone removed, $_chg changed, none of the login's kind"
      if [ -n "$_expl" ]; then
        result "no unexplained file of the login's kind differs ($_added added, $_gone removed, $_chg changed; explained by the owner as his own sessions:$_expl)"
      else
        result "no file of the login's kind differs ($_added added, $_gone removed, $_chg changed: the owner's own sessions)"
      fi
    fi
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
    dry 'ask and record the answer and the time: Owner and lead: look at the lines listed, and remove from the evidence what holds a login URL or code; type clean when the evidence holds none'
    dry "remove the scratch folder of the intake's verify"
    dry "wait for Enter: Owner: stop the daemon yourself (Ctrl-C in its terminal)"
    dry "ask $BIN/ataniqd status --socket $SOCK every second for up to 60 s (each ask cut off by timeout after 5 s, so about 65 s at most), say \"waiting for the daemon to stop...\" once and print a dot every 5 s; go on as soon as no daemon answers"
    dry "run: $BIN/ataniqd status --socket $SOCK"
    dry 'check: no daemon answers (exit 20); when one still answers do not fail: say "the daemon still answers: stop it with Ctrl-C in its terminal, then press Enter", wait for Enter and ask again, up to 5 more times, recording each ask; fail only after that'
    dry "run: systemctl --user show $UNIT --property=ActiveState --value"
    dry "when the unit is not stopped (its state is neither inactive nor failed): print systemctl --user stop $UNIT for the owner, wait for Enter, and read the state again"
    dry 'fail the step when the unit is still not stopped: the command that deletes the roots is then not shown'
    dry "wait for Enter: Owner: delete the canary's two roots yourself: rm -rf -- $STATE $RUNTIME"
    dry 'check: both roots are gone; when one still exists say so, wait for Enter and ask again, up to 5 more times, recording each ask; fail only after that'
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
  grep -rnIE 'https?://|[A-Z0-9]{4}-[A-Z0-9]{4}|sk-ant-|[?&]code=|[A-Za-z0-9_-]{20,}#[A-Za-z0-9_-]{20,}' "$EV" 2>/dev/null | cut -d: -f1,2 | sed 's/^/     /' || :
  ask 'Owner and lead: look at the lines listed, and remove from the evidence what holds a login URL or code; type clean when the evidence holds none'
  if [ "$ANSWER" != clean ]; then fail "the evidence is not confirmed clean of a login URL or code: \"$ANSWER\""; fi
  if [ "$DAEMON_UP" -eq 0 ]; then
    out '   nothing to act on: this run did not ask for the daemon to be started. The status is still asked.'
  fi
  daemon_stopped_by_owner || :
  runcap daemon-gone "$BIN/ataniqd" status --socket "$SOCK"
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
      roots_deleted_by_owner || :
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
  out '   does:     stop with proof; write the replay switch file; steps 11, 12 and 14 again (the console attaches as a read-only viewer); inject once; then step 22 in full'
  out '   expected: the fence reaches submitted, no reason, whole, inside the real sandbox; order.txt says 7501-first; the stand-in committed. A refusal is recorded with the detail and is a finding for a fix task before pass B'
  out '   serves:   G1, B1'
  out '   Owner: close the console of the first epoch now (Ctrl+\ leaves the session pane, then q).'
  out '   One left open waits on the same ticket file and would take the ticket of the second epoch.'
  out '   In this epoch you only WATCH: the console of step 14 is a read-only viewer and you paste nothing, the fence writes the message.'
  wait_key 'The second epoch begins: the role is stopped with proof (the stop of step 22, without the clean-up). Close the first console first.'
  CUR=22
  stop_role || return 0
  # Step 22 reverts the unit at the end of the pass; the second start must not
  # load a drop-in of the first one (step 11 sets MemoryMax with --runtime), so
  # the unit is reverted here too, between the epochs.
  runcap revert-between-epochs systemctl --user revert "$UNIT"
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
  out '   Owner: nothing to paste or press here: watch the pane in the viewer console while the fence writes.'
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
