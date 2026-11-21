#!/bin/sh
# Look, before the attended run, at what the run would trip over, and say what
# to type to fix it. Read README.md beside this file first.
#
#   canary-preflight.sh --root DIR [--bin DIR] [--evidence DIR] [--mirror-report FILE]
#
# It is read-only. It changes nothing of the root, the checkout or the host,
# starts no unit, no daemon, no sandbox and no agent session, answers nothing,
# and needs no terminal. For a pass-B root (a live agent) it also runs the
# root's own claude copy once with `--version`, in an empty environment and a
# new empty home folder, as step 9 of the run does (and nothing else of it). The one thing it writes is a temporary copy of the list
# `qargi local list` prints, in a folder made with mktemp under TMPDIR and
# removed when it ends. It runs `--version` of the four tagged binaries,
# `ataniq-canary show`, `fields`, `profile-digest` and `check-inputs`,
# `systemctl --user list-units`, `loginctl show-user`, `qargi local list`, git,
# and the run script's own test entry (`canary-run.sh --self-test`, which only computes)
# to read the open runs as the run does.
#
# It prints one line per check, "ok:" or "FIX:" with the exact command to type
# on the same line (a check it cannot make for want of an earlier one is a
# "skip:" line, which the fix before it clears), and ends with "READY" or
# "NOT READY (n to fix)". When ready it prints the three things the owner
# types and the terminal of each: the daemon command (T2, started at step 10 after step 8
# provisions, which it keeps until Ctrl-C at step 26), the attach command with its ticket file, `ticket` in the
# holder's runtime folder (T3; T4 for the second console of step 14) and the
# run command (T1). The exit status is 0 when ready, 1 when not, 2 for a wrong
# command line.
#
# For the tests only, three variables name the places it looks at on the host:
# CANARY_PREFLIGHT_PROC (the folder of the processes, /proc),
# CANARY_PREFLIGHT_TIOCSTI (the file of the sysctl dev.tty.legacy_tiocsti) and
# CANARY_PREFLIGHT_ATANIQ (the path that must not exist, /ataniq).
set -eu
umask 077
# git must not even refresh the index of the checkout it looks at.
GIT_OPTIONAL_LOCKS=0
export GIT_OPTIONAL_LOCKS

# shellcheck disable=SC2034  # read by canary-lib.sh
CC_NAME=canary-preflight.sh
# The folder of this script, with no fork: a name with no slash (sh canary-x.sh
# in its own folder) is in the current folder, and "/name" is in /.
case $0 in
  */*) _d=${0%/*} ;;
  *) _d=. ;;
esac
[ -n "$_d" ] || _d=/
_d=$(cd -P -- "$_d" && pwd -P)
REPO=$(cd -P -- "$_d/../../.." && pwd -P)
# shellcheck source=canary-lib.sh
. "$_d/canary-lib.sh"

ROOT=
BIN=$REPO/build/canary
EVIDENCE=
MIRROR_REPORT=
RUNSH=$_d/canary-run.sh
TEMPLATE=$REPO/qualification/canary/templates/canary-intake.json
FIXES=0
PROC=${CANARY_PREFLIGHT_PROC:-/proc}
TIOCSTI=${CANARY_PREFLIGHT_TIOCSTI:-/proc/sys/dev/tty/legacy_tiocsti}
ATANIQ=${CANARY_PREFLIGHT_ATANIQ:-/ataniq}

usage() {
  printf '%s\n' 'usage: canary-preflight.sh --root DIR [--bin DIR] [--evidence DIR] [--mirror-report FILE]' \
    '       --root DIR           the folder canary-prepare.sh made' \
    '       --bin DIR            the built binaries (default: build/canary of the checkout)' \
    '       --evidence DIR       the folder the run writes its evidence in (default: DIR/evidence)' \
    '       --mirror-report FILE the report of qualify-mirror (default: the hint in DIR/answers.hint,' \
    '                            else DIR/mirror-qual/report.json)' >&2
  exit 2
}

while [ "$#" -gt 0 ]; do
  case $1 in
    --root) [ "$#" -ge 2 ] || usage; ROOT=$2; shift 2 ;;
    --bin) [ "$#" -ge 2 ] || usage; BIN=$2; shift 2 ;;
    --evidence) [ "$#" -ge 2 ] || usage; EVIDENCE=$2; shift 2 ;;
    --mirror-report) [ "$#" -ge 2 ] || usage; MIRROR_REPORT=$2; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$ROOT" ] || usage
case $BIN in /*) ;; *) cc_die "--bin is not an absolute path: $BIN" ;; esac
cc_check_root "$ROOT"
[ -n "$EVIDENCE" ] || EVIDENCE=$ROOT/evidence
case $EVIDENCE in /*) ;; *) cc_die "--evidence is not an absolute path: $EVIDENCE" ;; esac
CONFIG=$ROOT/config.json
INPUTS=$ROOT/conf/sandbox-inputs.json

# One line per check.
ok() { cc_say "ok:   $1"; }
skip() { cc_say "skip: $1"; }
fix() {
  FIXES=$((FIXES + 1))
  cc_say "FIX:  $1 -- type: $2"
}

TMPD=
VHOME=
cleanup() {
  if [ -n "$TMPD" ]; then rm -rf -- "$TMPD"; fi
  if [ -n "$VHOME" ]; then rm -rf -- "$VHOME"; fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP

# field FILE KEY prints the value of one path of a JSON file, through the
# tagged helper, and nothing when it cannot be read.
field() {
  "$BIN/ataniq-canary" fields --file "$1" 2>/dev/null |
    awk -v k="$2" 'index($0, k "=") == 1 { print substr($0, length(k) + 2); exit }'
}

BUILT=1

# ---------------------------------------------------------------- checks ---

# 1. The root is prepared.
PREPARED=0
check_prepared() {
  _mark=$ROOT/$CC_MARKER
  _first=
  if [ -f "$_mark" ] && [ ! -L "$_mark" ]; then read -r _first <"$_mark" || _first=; fi
  if [ "$_first" = "$CC_MARKER_LINE" ] && [ -f "$CONFIG" ] && [ ! -L "$CONFIG" ]; then
    PREPARED=1
    ok "the root is prepared ($CC_MARKER, config.json): $ROOT"
  elif [ -d "$ROOT" ] && [ -n "$(find "$ROOT" -mindepth 1 -maxdepth 1 2>/dev/null | sed -n 1p)" ]; then
    # canary-prepare.sh makes a new set-up and refuses a folder that holds
    # anything, so the same root cannot be prepared: a fresh one is.
    fix "the root is not prepared (no $CC_MARKER with its config.json), and it holds files, so canary-prepare.sh refuses it: $ROOT" \
      "sh $_d/canary-prepare.sh --root <a new or empty folder>   (use a fresh --root; remove $ROOT yourself if you do not need it)"
  else
    fix "the root is not prepared (no $CC_MARKER with its config.json): $ROOT" \
      "sh $_d/canary-prepare.sh --root $ROOT"
  fi
}

# 2. The binaries are built, and from the checkout's HEAD.
newest_code_time() {
  cc_git -C "$REPO" log -1 --format=%ct -- cmd internal go.mod go.sum third_party 2>/dev/null | awk 'NR == 1 { print $1 }'
}
check_built() {
  _missing=
  for _b in $CC_BINARIES; do
    if [ ! -f "$BIN/$_b" ] || [ -L "$BIN/$_b" ] || [ ! -x "$BIN/$_b" ]; then _missing="$_missing $_b"; fi
  done
  if [ -n "$_missing" ]; then
    BUILT=0
    fix "not built in $BIN:$_missing" "make -C $REPO canary-build"
    return 0
  fi
  for _b in ataniqd ataniq-holder ataniq-canary ataniq-canary-standin; do
    case $("$BIN/$_b" --version 2>/dev/null || :) in
      *ataniq_canary*) ;;
      *)
        BUILT=0
        fix "$_b --version does not name the build tag ataniq_canary" "make -C $REPO canary-build"
        return 0
        ;;
    esac
  done
  _head=$(cc_git -C "$REPO" rev-parse HEAD 2>/dev/null) || _head=
  if [ -z "$_head" ]; then
    BUILT=0
    fix "the checkout's HEAD commit cannot be read: $REPO" "git -C $REPO log -1"
    return 0
  fi
  # A binary that names the commit it was built from is compared by that
  # commit; one that does not is compared by the time of its file against the
  # newest commit that touches the code.
  _named=
  for _b in ataniqd ataniq-holder ataniq-canary ataniq-canary-standin; do
    _c=$("$BIN/$_b" --version 2>/dev/null | awk '{ for (i = 1; i <= NF; i++) if ($i ~ /^[0-9a-f]+$/ && length($i) == 40) { print $i; exit } }')
    if [ -n "$_c" ]; then
      _named=1
      if [ "$_c" != "$_head" ]; then
        BUILT=0
        fix "$_b was built from $_c, not from the HEAD commit $_head (by its --version)" "make -C $REPO canary-build"
        return 0
      fi
    fi
  done
  if [ -n "$_named" ]; then
    ok "the binaries were built from the HEAD commit $_head (by the commit their --version names)"
    return 0
  fi
  _newest=$(newest_code_time)
  case $_newest in
    '' | *[!0-9]*)
      BUILT=0
      fix "the newest commit that touches the code cannot be read: $REPO" "git -C $REPO log -1 -- cmd internal go.mod go.sum third_party"
      return 0
      ;;
  esac
  for _b in $CC_BINARIES; do
    _t=$(stat -c %Y -- "$BIN/$_b")
    if [ "$_t" -lt "$_newest" ]; then
      BUILT=0
      fix "$_b is older than the newest commit that touches the code (by file time: --version names no commit)" "make -C $REPO canary-build"
      return 0
    fi
  done
  ok "the binaries are newer than the newest commit that touches the code (by file time: --version names no commit; HEAD is $_head)"
}

# 3. The checkout is where step 3 of the run wants it: at develop, no tracked change.
check_checkout() {
  _head=$(cc_git -C "$REPO" rev-parse HEAD 2>/dev/null) || _head=
  _dev=$(cc_git -C "$REPO" rev-parse --verify 'develop^{commit}' 2>/dev/null) || _dev=
  if [ -z "$_dev" ]; then
    fix "the branch develop cannot be read in $REPO (step 3 compares HEAD with it)" "git -C $REPO branch -a"
    return 0
  fi
  if [ "$_head" != "$_dev" ]; then
    fix "the checkout's commit is not the commit of develop (step 3 refuses it)" "git -C $REPO checkout develop"
    return 0
  fi
  _changed=$(cc_git -C "$REPO" status --porcelain 2>/dev/null | awk 'substr($0, 1, 2) != "??" && $0 != ""' | sed -n 1p)
  if [ -n "$_changed" ]; then
    fix "a tracked file of the checkout has a change (step 3 refuses it): $_changed" "git -C $REPO status"
    return 0
  fi
  ok "the checkout is at develop and no tracked file has a change"
}

# 4. The holder the config names, and the pin of the profile digest.
check_holder_and_pin() {
  if [ "$PREPARED" -eq 0 ] || [ "$BUILT" -eq 0 ]; then
    skip "the holder and the profile digest pin (needs a prepared root and the built binaries)"
    return 0
  fi
  _holder=$(field "$CONFIG" holder.executable)
  if [ -z "$_holder" ]; then
    fix "config.json names no holder (holder.executable)" "sh $_d/canary-prepare.sh --root <a new folder>"
    return 0
  fi
  if [ ! -f "$_holder" ] || [ ! -x "$_holder" ]; then
    fix "the holder the config names is not an executable file: $_holder" "make -C $REPO canary-build"
    return 0
  fi
  if [ "$_holder" != "$BIN/ataniq-holder" ]; then
    fix "the holder the config names is not the one of $BIN (step 3 refuses it): $_holder" \
      "sh $_d/canary-prepare.sh --root <a new folder> --bin $BIN"
    return 0
  fi
  _pin=$(field "$CONFIG" holder.profile_digest)
  if ! _now=$("$BIN/ataniq-canary" profile-digest --config "$CONFIG" --inputs "$INPUTS" 2>/dev/null); then
    _cmd="$BIN/ataniq-canary profile-digest --config $CONFIG --inputs $INPUTS"
    fix "ataniq-canary cannot compute the profile digest from the config and the sandbox inputs" "$_cmd"
    return 0
  fi
  if [ "$_pin" != "$_now" ]; then
    # Prepare pins the digest and then runs check-inputs; the fix does both,
    # so that a pin made over inputs the config does not match is refused.
    fix "the profile digest pinned in config.json ($_pin) is not the one ataniq-canary computes now ($_now)" \
      "sed -i 's/\"profile_digest\": \"[0-9a-f]*\"/\"profile_digest\": \"$_now\"/' $CONFIG && $BIN/ataniq-canary check-inputs --config $CONFIG --inputs $INPUTS"
    return 0
  fi
  if ! "$BIN/ataniq-canary" show --config "$CONFIG" >/dev/null 2>&1; then
    _cmd="$BIN/ataniq-canary show --config $CONFIG"
    fix "ataniq-canary cannot read the config" "$_cmd"
    return 0
  fi
  PINNED=1
  ok "the holder is the built one and the profile digest pin is the one computed now"
}

# 4b. The sandbox inputs, as step 8 and the holder's start read them: the
# helper's own check against the config (the same ataniq-canary check-inputs
# that prepare ends with), and, for the runtime claude, the guard hook's marker:
# sources.managed_marker must be there (the holder's spec refuses a claude start
# without it) and the managed-settings file the config names must hold that text
# (assertion 8 searches it). Neither is in the profile digest, so the pin above
# cannot show a missing one. An inputs file from an earlier prepare may lack the
# marker; it cannot be mended by the owner's hand without changing the pinned
# managed settings, so the fix is a fresh root.
PINNED=0
check_inputs() {
  if [ "$PINNED" -eq 0 ]; then
    skip "the sandbox inputs (needs the holder and the profile digest pin)"
    return 0
  fi
  _fresh="sh $_d/canary-prepare.sh --root <a new or empty folder>   (an inputs file from an earlier prepare may lack what the holder now needs: use a fresh --root)"
  if ! _why=$("$BIN/ataniq-canary" check-inputs --config "$CONFIG" --inputs "$INPUTS" 2>&1); then
    # The first line of the refusal, with no control character, at most 200 bytes.
    _why=$(printf '%s\n' "$_why" | awk 'NR == 1 { gsub(/[[:cntrl:]]/, ""); print substr($0, 1, 200); exit }')
    fix "ataniq-canary check-inputs refuses the sandbox inputs (${_why:-no reason given}): $INPUTS" "$_fresh"
    return 0
  fi
  if [ "$(field "$INPUTS" runtime)" = claude ]; then
    _mm=$(field "$INPUTS" sources.managed_marker)
    if [ -z "$_mm" ]; then
      fix "the sandbox inputs have no sources.managed_marker, which the holder needs to start claude (validate-config refuses it at step 11): $INPUTS" "$_fresh"
      return 0
    fi
    _ms=$(field "$CONFIG" provision.managed_settings)
    if [ -z "$_ms" ] || [ ! -f "$_ms" ] || ! grep -qF -- "$_mm" "$_ms" 2>/dev/null; then
      fix "the managed-settings file ${_ms:-the config names} does not hold the marker text $_mm (assertion 8 searches it)" "$_fresh"
      return 0
    fi
    ok "ataniq-canary check-inputs accepts the sandbox inputs, which name the marker $_mm that the managed settings hold"
    return 0
  fi
  ok "ataniq-canary check-inputs accepts the sandbox inputs"
}

# 4c. A live agent (pass B), what prepare --pass B makes and a hand edit can
# break: the footer mode is one the recogniser names (the config's validation
# refuses anything else with inject on, this says it in words), the agent's
# environment names DISABLE_AUTOUPDATER and CLAUDE_CODE_NO_FLICKER (names only, from `ataniq-canary show`;
# a value is never read or printed), and the claude executable is there, one
# regular file under the install folder, and prints the tested version as step
# 9 of the run asks it: `--version` with an empty environment and a home that
# is a new empty folder. That is the one program of this script that is not the
# kit's own; it is the file the sandbox binds, run outside the sandbox.
show_field() {
  "$BIN/ataniq-canary" show --config "$CONFIG" 2>/dev/null |
    awk -v k="$1" 'index($0, k "=") == 1 { print substr($0, length(k) + 2); exit }'
}
check_live_agent() {
  if [ "$PINNED" -eq 0 ]; then return 0; fi
  [ "$(show_field agent_kind)" = live ] || return 0
  _fresh="sh $_d/canary-prepare.sh --pass B --claude <the tested claude file> --root <a new or empty folder>"
  _mode=$(show_field agent_footer_mode)
  case $_mode in
    manual | auto) ok "agent.footer_mode is $_mode, a name the screen recogniser reports" ;;
    *) fix "agent.footer_mode is \"$_mode\", not manual or auto (the recogniser's names of the mode, not the footer's text): every inject would end not-now" \
      "edit agent.footer_mode in $CONFIG to manual (it is not in the pin)" ;;
  esac
  case ",$(show_field agent_env_names)," in
    *,DISABLE_AUTOUPDATER,*) ok "agent.env names DISABLE_AUTOUPDATER (names only; no value is read)" ;;
    *) fix "agent.env does not name DISABLE_AUTOUPDATER: the agent may draw an update notice on the screen the fence reads, and the env is part of the pin" "$_fresh" ;;
  esac
  case ",$(show_field agent_env_names)," in
    *,CLAUDE_CODE_NO_FLICKER,*) ok "agent.env names CLAUDE_CODE_NO_FLICKER (names only; no value is read)" ;;
    *) fix "agent.env does not name CLAUDE_CODE_NO_FLICKER: the agent may draw on the main screen, where every inject ends not-now, and the env is part of the pin" "$_fresh" ;;
  esac
  _hasmodel=0
  _i=0
  while [ "$_i" -lt 64 ]; do
    _av=$(show_field "agent_argv_$_i")
    [ -n "$_av" ] || break
    [ "$_av" = --model ] && _hasmodel=1
    _i=$((_i + 1))
  done
  if [ "$_hasmodel" -eq 1 ]; then
    ok "agent.argv names --model (step 15 asks you to confirm the model)"
  else
    fix "agent.argv has no --model: the agent would start on its default model, and step 15 ends the pass when no model is shown" \
      "edit agent.argv in $CONFIG: add \"--model\", \"NAME\" (not in the pin), then run preflight again; or prepare a NEW root with --model NAME"
  fi
  _a0=$(show_field agent_argv_0)
  _rest=${_a0#/ataniq/agent/}
  if [ "$_rest" = "$_a0" ] || [ -z "$_rest" ]; then
    fix "agent.argv[0] is not a file under /ataniq/agent/ ($_a0): step 9 runs the file the install folder holds under that name" "$_fresh"
    return 0
  fi
  _inst=$(field "$INPUTS" sources.agent_install)
  _claude=$_inst/$_rest
  if [ -z "$_inst" ] || [ -L "$_claude" ] || [ ! -f "$_claude" ] || [ ! -x "$_claude" ]; then
    fix "the claude copy is not an executable regular file at $_claude (sources.agent_install and agent.argv[0] name it)" "$_fresh"
    return 0
  fi
  _want=$(show_field agent_runtime_version)
  VHOME=$(mktemp -d) || {
    fix "mktemp -d failed: no home for claude --version" "mktemp -d"
    return 0
  }
  _vrc=0
  _vout=$(env -i HOME="$VHOME" "$_claude" --version 2>&1) || _vrc=$?
  rmdir -- "$VHOME" 2>/dev/null && VHOME=
  if [ "$_vrc" -eq 0 ] && [ -n "$_want" ] && printf '%s\n' "$_vout" | grep -qwF -- "$_want"; then
    ok "the claude copy prints the tested version $_want (--version with an empty environment): $_claude"
  else
    _vout=$(printf '%s\n' "$_vout" | awk 'NR == 1 { gsub(/[[:cntrl:]]/, ""); print substr($0, 1, 100); exit }')
    fix "the claude copy $_claude does not print the tested version ${_want:-unknown} (exit status $_vrc, first line: ${_vout:-none})" "$_fresh"
  fi
}

# 5. state/ and runtime/ are clean.
check_clean_roots() {
  if [ "$PREPARED" -eq 0 ]; then
    skip "state/ and runtime/ (needs a prepared root)"
    return 0
  fi
  for _k in state runtime; do
    if [ -L "$ROOT/$_k" ] || [ ! -d "$ROOT/$_k" ]; then
      fix "$ROOT/$_k is not a folder of the root" "sh $_d/canary-reset.sh --root $ROOT"
      return 0
    fi
  done
  _extra=$(find "$ROOT/state" -mindepth 1 -maxdepth 1 ! -name mirrors | sed -n 1p)
  _run=$(find "$ROOT/runtime" -mindepth 1 -maxdepth 1 | sed -n 1p)
  if [ -n "$_extra" ] || [ -n "$_run" ]; then
    fix "state/ or runtime/ holds an earlier attempt (${_extra:-$_run}): the run provisions into empty folders" \
      "sh $_d/canary-reset.sh --root $ROOT   (run canary-reset.sh)"
    return 0
  fi
  if [ ! -d "$ROOT/state/mirrors/$CC_PROJECT.git" ]; then
    fix "the mirror is missing: $ROOT/state/mirrors/$CC_PROJECT.git" "sh $_d/canary-reset.sh --root $ROOT   (run canary-reset.sh)"
    return 0
  fi
  ok "state/ and runtime/ are empty (the mirror is made, nothing is provisioned)"
}

# 6. No unit is loaded and no daemon or holder runs.
check_quiet() {
  if ! _units=$(systemctl --user list-units --all --no-legend --plain 'ataniq-*' 2>/dev/null); then
    fix "systemctl --user cannot list the units (is the user manager running?)" "systemctl --user list-units --all 'ataniq-*'"
  else
    _loaded=$(printf '%s\n' "$_units" | awk '$1 != "" && $1 !~ /\.slice$/ { print $1 }' | tr '\n' ' ')
    if [ -n "$_loaded" ]; then
      fix "an ataniq-* unit is loaded: $_loaded" "systemctl --user stop $_loaded"
    else
      ok "no ataniq-* unit is loaded"
    fi
  fi
  _pids=
  _seen=0
  for _pd in "$PROC"/[0-9]*; do
    [ -e "$_pd" ] || continue
    _seen=1
    [ -r "$_pd/cmdline" ] || continue
    _first=$(tr '\0' '\n' <"$_pd/cmdline" 2>/dev/null | head -n 1) || continue
    case ${_first##*/} in
      ataniqd | ataniq-holder) _pids="$_pids ${_pd##*/}" ;;
    esac
  done
  if [ "$_seen" -eq 0 ]; then
    fix "$PROC cannot be read, so the processes cannot be listed" "ls $PROC"
  elif [ -n "$_pids" ]; then
    fix "an ataniqd or ataniq-holder runs (process$_pids)" "stop it with Ctrl-C in its terminal, or: kill -INT$_pids"
  else
    ok "no ataniqd and no ataniq-holder runs"
  fi
}

# 7. No Qargi run of this repository is open: the run script's own reading.
check_open_runs() {
  if ! command -v qargi >/dev/null 2>&1; then
    ok "qargi is not on PATH: the run asks the lead to confirm that no run is open"
    return 0
  fi
  TMPD=$(mktemp -d)
  _list=$TMPD/list
  if ! qargi local list >"$_list" 2>/dev/null; then
    ok "qargi local list failed: the run asks the lead to confirm that no run is open"
    return 0
  fi
  _scan=$(sh "$RUNSH" --self-test list-scan "$_list")
  case $_scan in
    *'header bad'* | *'other '[1-9]*)
      ok "the list of qargi local list cannot be read as the run reads it: the run asks the lead to confirm that no run is open"
      return 0
      ;;
  esac
  _open=$(sh "$RUNSH" --self-test open-runs "$_list" "$REPO" | tr '\n' ' ')
  if [ -n "$_open" ]; then
    fix "a Qargi run of this repository is open: $_open" "qargi local list   (verify and close the run)"
    return 0
  fi
  _unread=$(sh "$RUNSH" --self-test unread-runs "$_list" | tr '\n' ' ')
  if [ -n "$_unread" ]; then
    ok "no open run is known, but one has no readable repo.path ($_unread): the run asks the lead"
    return 0
  fi
  ok "no Qargi run of this repository is open"
}

# 8. /ataniq does not exist on the host (the note of the template).
check_no_ataniq_dir() {
  if [ -e "$ATANIQ" ] || [ -L "$ATANIQ" ]; then
    fix "$ATANIQ exists on the host (the template's note: it must not)" "ls -la $ATANIQ   (then remove it yourself)"
  else
    ok "$ATANIQ does not exist"
  fi
}

# 9. The report of qualify-mirror is there and is of this template.
mirror_report_path() {
  if [ -n "$MIRROR_REPORT" ]; then
    printf '%s\n' "$MIRROR_REPORT"
    return 0
  fi
  _hf=$ROOT/answers.hint
  if [ -f "$_hf" ] && [ ! -L "$_hf" ]; then
    while IFS= read -r _hl || [ -n "$_hl" ]; do
      case $_hl in
        mirror-report=/*)
          printf '%s\n' "${_hl#*=}"
          return 0
          ;;
      esac
    done <"$_hf"
  fi
  printf '%s\n' "$ROOT/mirror-qual/report.json"
}
check_mirror_report() {
  _rep=$(mirror_report_path)
  _cmd="edit $ROOT/mirror-qual/config.json yourself (i_run_this_on_purpose true), then: sh scripts/go-tool.sh build -o build/qualify-mirror ./qualification/leadmirror/cmd/qualify-mirror && build/qualify-mirror --config $ROOT/mirror-qual/config.json --out $_rep   (in $REPO)"
  if [ "$BUILT" -eq 0 ]; then
    skip "the report of qualify-mirror (needs the built binaries to read it)"
    return 0
  fi
  if [ ! -f "$_rep" ]; then
    fix "the report of qualify-mirror is not there: $_rep" "$_cmd"
    return 0
  fi
  if ! "$BIN/ataniq-canary" fields --file "$_rep" >/dev/null 2>&1; then
    fix "the report of qualify-mirror does not parse as one JSON object: $_rep" "$_cmd"
    return 0
  fi
  _want=$(sha256sum <"$TEMPLATE" | awk '{ print $1 }')
  _got=$(field "$_rep" template.sha256)
  if [ -z "$_got" ]; then
    fix "the report has no template.sha256: $_rep" "$_cmd"
  elif [ "$_got" != "$_want" ]; then
    fix "the report is of another template ($_got, the template is $_want): $_rep" "$_cmd"
  else
    ok "the report of qualify-mirror parses and names the template's hash: $_rep"
  fi
}

# 10. The tools step 3 runs and timeout, the sysctl and lingering it reads.
check_host() {
  _absent=
  _tools='systemctl loginctl git bwrap pasta'
  _steps='step 3 runs them'
  _list='systemctl, loginctl, git, bwrap and pasta'
  # ss is run only by step 6, which only pass B (a live agent) does.
  if [ "$(show_field agent_kind)" = live ]; then
    _tools="$_tools ss"
    _steps='steps 3 and 6 run them'
    _list='systemctl, loginctl, git, bwrap, pasta and ss'
  fi
  for _t in $_tools; do
    command -v "$_t" >/dev/null 2>&1 || _absent="$_absent $_t"
  done
  if [ -n "$_absent" ]; then
    fix "not on PATH ($_steps):$_absent" "put them on PATH (a copy of pasta made by canary-prepare.sh is in $ROOT/bin: PATH=$ROOT/bin:\$PATH)"
  else
    ok "$_list are on PATH"
  fi
  if command -v timeout >/dev/null 2>&1; then
    ok "timeout is on PATH (the run cuts off each ask of the daemon's status with it)"
  else
    fix "timeout is not on PATH (the run cuts off each ask of the daemon's status with it)" "install coreutils, or put timeout on PATH"
  fi
  _tiocsti=$(cat "$TIOCSTI" 2>/dev/null) || _tiocsti=
  if [ "$_tiocsti" = 0 ]; then
    ok "the sysctl dev.tty.legacy_tiocsti is 0"
  else
    fix "the sysctl dev.tty.legacy_tiocsti is \"${_tiocsti:-unreadable}\", not 0 (step 3 refuses it)" "sudo sysctl dev.tty.legacy_tiocsti=0"
  fi
  if _linger=$(loginctl show-user "$(id -un)" --property=Linger 2>/dev/null); then
    ok "lingering can be read ($_linger; the run records it and checks it is the same at the end)"
  else
    fix "lingering cannot be read with loginctl (step 3 needs it)" "loginctl show-user $(id -un) --property=Linger"
  fi
}

# 11. The evidence folder is writable.
check_evidence() {
  if [ -d "$EVIDENCE" ] && [ -w "$EVIDENCE" ] && [ -x "$EVIDENCE" ]; then
    ok "the evidence folder is writable: $EVIDENCE"
  else
    fix "the evidence folder is not a writable folder: $EVIDENCE" "mkdir -p -m 0700 $EVIDENCE   (or chmod u+rwx $EVIDENCE)"
  fi
}

check_prepared
check_built
check_checkout
check_holder_and_pin
check_inputs
check_live_agent
check_clean_roots
check_quiet
check_open_runs
check_no_ataniq_dir
check_mirror_report
check_host
check_evidence

cc_say
if [ "$FIXES" -gt 0 ]; then
  cc_say "NOT READY ($FIXES to fix)"
  exit 1
fi

# The three things the owner types, with the real paths.
HOLDER_RUN=$("$BIN/ataniq-canary" show --config "$CONFIG" 2>/dev/null | awk 'index($0, "holder_runtime_dir=") == 1 { print substr($0, 20); exit }')
[ -n "$HOLDER_RUN" ] || HOLDER_RUN='<the holder runtime folder of the config>'
cc_say "READY"
cc_say
cc_say 'What you type, and where. The run never starts the daemon or the console for you.'
cc_say
cc_say '1. At step 10, in a second terminal (T2), the daemon, started BEFORE you press Enter at step 10'
cc_say '   (wait for its listening line), and not earlier: step 8 provisions the roots and writes the'
cc_say '   marker file (canary-root.json) that the daemon needs, so a daemon started before step 8 has'
cc_say '   no marker to find. It stays in front of that terminal until you stop it with Ctrl-C at'
cc_say '   step 26, before that step'"'"'s Enter, so T2 is busy from step 10 on:'
cc_say "     $BIN/ataniqd run --canary $CONFIG"
cc_say
cc_say '2. At step 14 (and again in the second epoch), in a third terminal (T3), the console, started'
cc_say '   BEFORE you press Enter at step 14: it waits for a ticket, and the run mints one only after'
cc_say '   your Enter (a ticket lives 30 seconds). Step 14 asks for the same command once more in a'
cc_say '   fourth terminal (T4), in the first epoch, where the integrate command of step 20 goes too:'
cc_say "     $BIN/ataniq-console --attach $HOLDER_RUN --ticket-file $HOLDER_RUN/ticket"
cc_say '   (In the second epoch the run prints this command with --viewer added: use that one.)'
cc_say
cc_say '3. In this terminal (T1), the run (read its dry run first, add --dry):'
# The pass the root was made for: a stand-in agent is pass A, a live agent is
# pass B. A config that cannot say gets a placeholder, never a guess.
_kind=$("$BIN/ataniq-canary" show --config "$CONFIG" 2>/dev/null | awk 'index($0, "agent_kind=") == 1 { print substr($0, 12); exit }')
case $_kind in
  stand-in) _pass=A ;;
  live) _pass=B ;;
  *) _pass='<A or B>' ;;
esac
cc_say "     sh $_d/canary-run.sh --pass $_pass --config $CONFIG --evidence $EVIDENCE"
