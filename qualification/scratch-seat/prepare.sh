#!/bin/sh
# Prepare the owner-attended proof of real lead replies: the scratch config and
# the files it names, in a folder of the proof's own, for the daemon that
# serves the scratch leads and the seat gate on the phone trial's state folder.
# Read docs/scratch-seat-proof.md first: it has every step, in order, and says
# which steps are the owner's alone.
#
#   prepare.sh --dry   [--state DIR] [--root DIR] [--gate IP:PORT]
#                      [--claude FILE --model NAME] [--bin DIR] [--daemon FILE]
#                      [--pasta FILE] [--bwrap FILE]
#   prepare.sh --write [the same]
#   prepare.sh --self-test gate IP:PORT
#   prepare.sh --self-test config STATE ROOT [MODEL]
#   prepare.sh --self-test settings ROOT
#   prepare.sh --self-test commands STATE ROOT IP:PORT [live]
#
# A value is taken from its flag, else from the environment: OWNER_STATE_DIR
# (--state: the phone trial's state folder, which becomes the scratch state
# root, so that the gate's key, its seat pins and the paired phones' keys stay
# the trial's), ROOT (--root: a new or empty folder for the proof's own files),
# SEAT_GATE (--gate: the gate's address, IP:PORT), CLAUDE_FILE and
# CLAUDE_MODEL (--claude, --model: the tested claude, one regular file, and the
# model; without them the config has the stand-in lead only), and TRIAL_UNIT
# (the trial's user unit, only printed). No value is written into the
# repository and none has a default that names a place of the owner's.
#
# --dry prints what --write would make and the owner's steps, and writes
# nothing. --write makes ROOT (mode 0700) and writes in it only: the scratch
# config scratch.json (0600), the owner channel's settings owner-scratch.json
# (0600) for the owner to put in place by hand, the managed settings and the
# no-op guard hook, the replay screens of the checkout, copies of the stand-in,
# the probe, pasta when it is a link and the claude file, a throwaway source
# repository (git, one commit), and the folders owner-channel/, its drop/ and
# evidence/. It never starts a unit, a daemon, a sandbox or an agent, never
# runs the claude it copies, never answers a prompt and never deletes
# anything. It never writes in the trial's state folder: it only looks there
# for the gate's key and the trial's settings. Moving the trial's settings
# aside, putting the proof's settings in their place, scratch-init and every
# start are the owner's commands, which it prints.
set -eu
umask 077

NAME=prepare.sh
case $0 in
  */*) _d=${0%/*} ;;
  *) _d=. ;;
esac
[ -n "$_d" ] || _d=/
_d=$(cd -P -- "$_d" && pwd -P)
REPO=$(cd -P -- "$_d/../.." && pwd -P)

# The tested version of claude, the one the recorded screens are of.
RUNTIME_VERSION=2.1.325
# The keys of the two leads, short enough for the seat (daemon:KEY).
KEY_STANDIN=standin
KEY_LIVE=live
# The binaries of build/canary the proof uses.
BINARIES='ataniq-holder ataniq-canary ataniq-canary-standin ataniq-console ataniq-probe'

say() { printf '%s\n' "$*"; }

die() {
  printf '%s: %s\n' "$NAME" "$*" >&2
  exit 1
}

usage() {
  printf '%s\n' 'usage: prepare.sh --dry|--write [--state DIR] [--root DIR] [--gate IP:PORT]' \
    '                  [--claude FILE --model NAME] [--bin DIR] [--daemon FILE] [--pasta FILE] [--bwrap FILE]' \
    '       --dry          print what --write makes and your steps; write nothing' \
    '       --write        make ROOT and write the proof'"'"'s files in it (nothing else)' \
    '       --state DIR    the phone trial'"'"'s state folder (or OWNER_STATE_DIR)' \
    '       --root DIR     a new or empty folder for the proof (or ROOT), outside /tmp and the checkout' \
    '       --gate IP:PORT the seat gate'"'"'s address (or SEAT_GATE)' \
    '       --claude FILE  the tested claude, one regular file (or CLAUDE_FILE); it is copied, never run' \
    '       --model NAME   the model of the live lead (or CLAUDE_MODEL); needed with --claude' \
    '       --bin DIR      the canary binaries (default: build/canary of the checkout)' \
    '       --daemon FILE  the daemon of both tags (default: build/scratch-seat/ataniqd of the checkout)' \
    '       --pasta FILE   pasta (default: /usr/bin/pasta; a link is copied)' \
    '       --bwrap FILE   bubblewrap (default: the one on PATH)' \
    '       prepare.sh --self-test gate IP:PORT | config STATE ROOT [MODEL] | settings ROOT' \
    '                  | commands STATE ROOT IP:PORT [live]   (computes and prints only)' >&2
  exit 2
}

# safe_path NAME VALUE refuses a path that is not absolute and clean, or that
# holds a character the JSON or a printed command would have to escape.
safe_path() {
  case $2 in
    /*) ;;
    *) die "$1 is not an absolute path: $2" ;;
  esac
  case $2 in
    *[!A-Za-z0-9._/+-]*) die "$1 has a character outside A-Z a-z 0-9 . _ / + -: $2" ;;
  esac
  case $2/ in
    */./* | */../* | *//*) die "$1 has a . or .. part or an empty part: $2" ;;
  esac
  [ "$2" = / ] && die "$1 is /"
  case $2 in
    */) die "$1 ends with /: $2" ;;
  esac
  return 0
}

# inside PATH FOLDER succeeds when PATH is FOLDER or lies below it.
inside() {
  case $1 in
    "$2" | "$2"/*) return 0 ;;
  esac
  return 1
}

# check_gate VALUE refuses an address that is not one IP address and a port:
# a.b.c.d:port or [v6]:port, no wildcard and no port 0. The daemon checks it
# again when it starts.
check_gate() {
  case $1 in
    \[*\]:*)
      _h=${1%]:*}
      _h=${_h#\[}
      _p=${1##*]:}
      case $_h in
        '' | *[!0-9a-fA-F:.]*) die "the gate's address is not an IP address and a port: $1" ;;
      esac
      case $_h in
        :: | ::0 | 0::0) die "the gate's address is a wildcard: $1" ;;
      esac
      ;;
    *:*)
      _h=${1%:*}
      _p=${1##*:}
      case $_h in
        *[!0-9.]* | *..* | .* | *.) die "the gate's address is not an IP address and a port: $1" ;;
      esac
      _n=0
      _rest=$_h.
      while [ -n "$_rest" ]; do
        _o=${_rest%%.*}
        _rest=${_rest#*.}
        _n=$((_n + 1))
        case $_o in
          '' | ???? | 0?*) die "the gate's address is not an IP address and a port: $1" ;;
        esac
        [ "$_o" -le 255 ] || die "the gate's address is not an IP address and a port: $1"
      done
      [ "$_n" -eq 4 ] || die "the gate's address is not an IP address and a port: $1"
      [ "$_h" != 0.0.0.0 ] || die "the gate's address is a wildcard: $1"
      ;;
    *) die "the gate's address is not IP:PORT: $1" ;;
  esac
  case $_p in
    '' | *[!0-9]* | 0*) die "the gate's port is not a number from 1 to 65535: $1" ;;
  esac
  [ "${#_p}" -le 5 ] && [ "$_p" -le 65535 ] || die "the gate's port is not a number from 1 to 65535: $1"
}

# check_model NAME refuses a model name the agent command could misread.
check_model() {
  case $1 in
    '' | -* | *[!A-Za-z0-9._:+-]*) die "--model is a model name of letters, digits and . _ : + - that does not start with -: $1" ;;
  esac
  [ "${#1}" -le 64 ] || die '--model is longer than 64 characters'
}

# The values every computation uses. set_paths STATE ROOT sets the names of
# the proof's files.
set_paths() {
  S=$1
  ROOT=$2
  CONFIG=$ROOT/scratch.json
  SETTINGS=$ROOT/owner-scratch.json
  RUNTIME=$ROOT/runtime
  OC=$ROOT/owner-channel
  EVIDENCE=$ROOT/evidence
}

# never_mounted prints the paths no bind source of a lead may touch: the
# checkout, the owner's own folders of credentials and the owner folder of
# the state root, which holds the gate's key and the phones' keys.
never_mounted() {
  _nm="\"$REPO\""
  case ${HOME:-} in
    /*)
      case $HOME in
        *[!A-Za-z0-9._/+-]*) ;;
        *) _nm="$_nm, \"$HOME/.ssh\", \"$HOME/.claude\", \"$HOME/.codex\"" ;;
      esac
      ;;
  esac
  printf '%s' "$_nm, \"$S/owner\""
}

# lead_json KEY KIND ARGV ENV prints one lead of the scratch config.
lead_json() {
  cat <<EOF
    {
      "key": "$1",
      "source": "$ROOT/upstream",
      "upstream_branch": "develop",
      "agent": {
        "kind": "$2",
        "runtime": "claude",
        "runtime_version": "$RUNTIME_VERSION",
        "footer_mode": "manual",
        "argv": $3,
        "env": $4
      },
      "inject": true,
      "limits": {"memory_max_bytes": 4294967296, "swap_max_bytes": 0, "tasks_max": 512, "memory_floor_bytes": 1073741824, "memory_share_percent": 50}
    }
EOF
}

# config_json [MODEL] prints the scratch config: the trial's state folder as
# the state root, ROOT/runtime as the runtime root, one lead running at a
# time, the stand-in lead and, with a model, the live lead.
config_json() {
  cat <<EOF
{
  "version": 1,
  "state_root": "$S",
  "runtime_root": "$RUNTIME",
  "max_running": 1,
  "holder": {"executable": "$BIN/ataniq-holder", "cols": 120, "rows": 40},
  "sandbox": {
    "agent_install": "$ROOT/agent-install",
    "pasta": "$PASTA_USE",
    "bwrap": "$BWRAP",
    "probe": "$ROOT/bin/ataniq-probe",
    "managed_marker": "guard-hook",
    "never_mounted": [$(never_mounted)],
    "provider_host": "api.anthropic.com",
    "terminal": {"term": "xterm-256color", "colorterm": "truecolor", "lang": "C.UTF-8"},
    "time_limit_seconds": 60
  },
  "provision": {
    "git_name": "Sample Lead",
    "git_email": "lead@example.invalid",
    "managed_settings": "$ROOT/conf/managed-settings.json",
    "guard_hook": "$ROOT/conf/guard-hook",
    "replay_screens": "$ROOT/conf/replay-screens"
  },
  "leads": [
EOF
  if [ -n "${1:-}" ]; then
    lead_json "$KEY_STANDIN" stand-in '["/ataniq/agent/ataniq-canary-standin"]' '[]' | sed '$ s/}$/},/'
    lead_json "$KEY_LIVE" live "[\"/ataniq/agent/claude\", \"--model\", \"$1\"]" '["DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_NO_FLICKER=1"]'
  else
    lead_json "$KEY_STANDIN" stand-in '["/ataniq/agent/ataniq-canary-standin"]' '[]'
  fi
  cat <<EOF
  ]
}
EOF
}

# settings_json prints the owner channel's non-trial settings: the stand-in
# door, one sample project whose mirror, drop folder and work folder lie in
# ROOT/owner-channel, and a sandbox template that is never written, so that
# no request can be verified and no suite sandbox can start.
settings_json() {
  cat <<EOF
{
  "project": "proof-project",
  "mirror": "$OC/mirror.git",
  "role": "proof-lead",
  "target": "refs/heads/develop",
  "identity": {"name": "Sample Host", "email": "host@example.invalid"},
  "gates": [{"name": "suite", "argv": ["make", "test"]}],
  "drop": "$OC/drop",
  "seat": {"epoch": 1, "seat_rev": 1},
  "sandbox": "$OC/no-template.json",
  "work": "$OC/work",
  "door": "stand-in",
  "roots": ["$OC"],
  "kinds": ["platform-passkey", "vault-passkey", "passkey"]
}
EOF
}

# commands GATE [live] prints the owner's steps, with the values filled in.
# Every step that starts a real process says OWNER ONLY. No step deletes
# anything: a file is moved only to a name that is free.
commands() {
  _g=$1
  _unit=${TRIAL_UNIT:-'<TRIAL_UNIT>'}
  _daemon=$DAEMON
  _chat="$_daemon chat set"
  cat <<EOF
Your steps, in order (docs/scratch-seat-proof.md says what each proves).
Before the sitting:
  c. OWNER ONLY (runs the claude copy): env -i HOME="\$(mktemp -d)" $ROOT/agent-install/claude --version
     must name $RUNTIME_VERSION.
  d. Check that your firewall admits the gate's port of $_g from the authority host only.
  e. Read the pins the gate will use: $_daemon seat-gate pins --state-dir $S
Switch from the trial to the proof:
  1. OWNER ONLY: systemctl --user stop $_unit
     then: systemctl --user is-active $_unit   (inactive)
  2. $BIN/ataniq-canary scratch-init --config $CONFIG
  3. test ! -e $S/owner/scratch.json.trial && mv $S/owner/scratch.json $S/owner/scratch.json.trial
  4. test ! -e $S/owner/scratch.json && cp $SETTINGS $S/owner/scratch.json && chmod 0600 $S/owner/scratch.json
  5. OWNER ONLY (terminal A): $_daemon run --scratch $CONFIG --owner-channel --seat-gate $_g 2>&1 | tee -ai $EVIDENCE/daemon.log
  6. OWNER ONLY (terminal B): $BIN/ataniq-console --daemon $RUNTIME   then L
The stand-in lead:
  7. d on $KEY_STANDIN, then: $_chat $KEY_STANDIN read --config $CONFIG
  8. OWNER ONLY: s on $KEY_STANDIN; m: canary-token: proof-standin-0001
     then w, paste that line into the pane, Enter once, and leave with the return key
  9. Phone and desktop: Leads, daemon:$KEY_STANDIN, the stand-in's reply; then x and y on $KEY_STANDIN
EOF
  if [ "${2:-}" = live ]; then
    cat <<EOF
The live lead (same sitting, only if all was well):
 10. Note the login rows of the provider's account page. d on $KEY_LIVE, then: $_chat $KEY_LIVE read --config $CONFIG
 11. OWNER ONLY: s on $KEY_LIVE; w, a fresh login in the pane; back; m with the message of the guide
 12. Phone and desktop: daemon:$KEY_LIVE and its reply; at most 30 minutes or 10 turns; then x and y on $KEY_LIVE
     and revoke the login made in 11
EOF
  fi
  cat <<EOF
Off, restart, stop and back to the trial:
 13. $_chat KEY off --config $CONFIG   (the page then says Chat is off)
 14. OWNER ONLY: Ctrl+C in terminal A, the daemon again as in 5 (every lead is off), Ctrl+C again
 15. test ! -e $S/owner/scratch.json.leads && mv $S/owner/scratch.json $S/owner/scratch.json.leads
 16. test ! -e $S/owner/scratch.json && mv $S/owner/scratch.json.trial $S/owner/scratch.json
 17. OWNER ONLY: systemctl --user start $_unit
EOF
}

# self_check is --self-test with no name: it computes the config, the
# settings and the owner's steps for sample values and checks what the guide
# promises of them, and prints one line. It reads no file and writes nothing.
self_check() {
  _n=0
  _fail() {
    printf '%s: self-test: %s\n' "$NAME" "$*" >&2
    exit 1
  }
  for _a in 192.0.2.10:7443 '[2001:db8::10]:7443' 10.0.0.1:1; do
    (check_gate "$_a") 2>/dev/null || _fail "the gate address $_a is refused"
    _n=$((_n + 1))
  done
  for _a in 0.0.0.0:7443 '[::]:7443' gate.example:7443 192.0.2.10:0 192.0.2.10 192.0.2.300:7443 192.0.2.10:70000; do
    if (check_gate "$_a") 2>/dev/null; then _fail "the gate address $_a is taken"; fi
    _n=$((_n + 1))
  done
  set_paths /srv/sample/trial-state /srv/sample/proof
  for _m in '' sample-model; do
    _c=$(config_json "$_m")
    for _w in '"state_root": "/srv/sample/trial-state"' '"runtime_root": "/srv/sample/proof/runtime"' '"max_running": 1' \
      '"/srv/sample/trial-state/owner"' '"key": "standin"' '"inject": true'; do
      case $_c in *"$_w"*) ;; *) _fail "the config lacks $_w" ;; esac
      _n=$((_n + 1))
    done
    case $_c in
      *'"key": "live"'*) [ -n "$_m" ] || _fail 'the config has a live lead without a model' ;;
      *) [ -z "$_m" ] || _fail 'the config has no live lead with a model' ;;
    esac
    _n=$((_n + 1))
  done
  _s=$(settings_json)
  for _w in '"door": "stand-in"' '"roots": ["/srv/sample/proof/owner-channel"]' '"sandbox": "/srv/sample/proof/owner-channel/no-template.json"'; do
    case $_s in *"$_w"*) ;; *) _fail "the settings lack $_w" ;; esac
    _n=$((_n + 1))
  done
  case $_s in *'"trial"'* | */srv/sample/trial-state*) _fail 'the settings name the trial or the state folder' ;; esac
  _n=$((_n + 1))
  _o=$(commands 192.0.2.10:7443 live)
  _nl='
'
  # The name of the make target that builds the daemon of both tags, made
  # here so that this file never holds it: no step names it.
  _tgt=scratch-seat-
  _tgt=${_tgt}build
  _rest=$_o$_nl
  while [ -n "$_rest" ]; do
    _l=${_rest%%"$_nl"*}
    _rest=${_rest#*"$_nl"}
    case $_l in
      *' rm '* | *'rm -'* | *"$_tgt"*) _fail "a step deletes or builds: $_l" ;;
      *' mv '*) case $_l in *'test ! -e '*'&& mv '*) ;; *) _fail "a move is not guarded: $_l" ;; esac ;;
    esac
    case $_l in
      *'systemctl --user st'* | *' run --scratch '* | *'ataniq-console '* | *' --version'*)
        case $_l in *'OWNER ONLY'*) ;; *) _fail "a step that starts a process is not marked OWNER ONLY: $_l" ;; esac ;;
    esac
    _n=$((_n + 1))
  done
  say "$NAME self-test: $_n checks passed; nothing was read, written or run"
}

# --self-test NAME VALUE...: the test entry. It computes and prints only: it
# reads no file, writes nothing and runs nothing.
if [ "${1:-}" = --self-test ]; then
  BIN=$REPO/build/canary
  DAEMON=$REPO/build/scratch-seat/ataniqd
  PASTA_USE=/usr/bin/pasta
  BWRAP=/usr/bin/bwrap
  case ${2:-} in
    '')
      [ "$#" -eq 1 ] || usage
      self_check
      exit 0
      ;;
    gate)
      [ "$#" -eq 3 ] || usage
      check_gate "$3"
      say "gate $3"
      exit 0
      ;;
    config)
      [ "$#" -eq 4 ] || [ "$#" -eq 5 ] || usage
      safe_path --state "$3"
      safe_path --root "$4"
      if [ "$#" -eq 5 ]; then check_model "$5"; fi
      set_paths "$3" "$4"
      config_json "${5:-}"
      exit 0
      ;;
    settings)
      [ "$#" -eq 3 ] || usage
      safe_path --root "$3"
      set_paths /state "$3"
      settings_json
      exit 0
      ;;
    commands)
      [ "$#" -eq 5 ] || { [ "$#" -eq 6 ] && [ "$6" = live ]; } || usage
      safe_path --state "$3"
      safe_path --root "$4"
      check_gate "$5"
      set_paths "$3" "$4"
      commands "$5" "${6:-}"
      exit 0
      ;;
  esac
  usage
fi

MODE=
S=${OWNER_STATE_DIR:-}
ROOT=${ROOT:-}
GATE=${SEAT_GATE:-}
CLAUDE_SRC=${CLAUDE_FILE:-}
MODEL=${CLAUDE_MODEL:-}
BIN=$REPO/build/canary
DAEMON=$REPO/build/scratch-seat/ataniqd
PASTA=
BWRAP=
while [ "$#" -gt 0 ]; do
  case $1 in
    --dry) [ -z "$MODE" ] || usage; MODE='dry'; shift ;;
    --write) [ -z "$MODE" ] || usage; MODE='write'; shift ;;
    --state) [ "$#" -ge 2 ] || usage; S=$2; shift 2 ;;
    --root) [ "$#" -ge 2 ] || usage; ROOT=$2; shift 2 ;;
    --gate) [ "$#" -ge 2 ] || usage; GATE=$2; shift 2 ;;
    --claude) [ "$#" -ge 2 ] || usage; CLAUDE_SRC=$2; shift 2 ;;
    --model) [ "$#" -ge 2 ] || usage; MODEL=$2; shift 2 ;;
    --bin) [ "$#" -ge 2 ] || usage; BIN=$2; shift 2 ;;
    --daemon) [ "$#" -ge 2 ] || usage; DAEMON=$2; shift 2 ;;
    --pasta) [ "$#" -ge 2 ] || usage; PASTA=$2; shift 2 ;;
    --bwrap) [ "$#" -ge 2 ] || usage; BWRAP=$2; shift 2 ;;
    *) usage ;;
  esac
done
# No default action: --dry or --write is always said.
[ -n "$MODE" ] || usage

[ -n "$S" ] || die 'no state folder: give --state DIR or OWNER_STATE_DIR, the phone trial'"'"'s state folder'
[ -n "$ROOT" ] || die 'no root: give --root DIR or ROOT, a new or empty folder for the proof'
[ -n "$GATE" ] || die 'no gate address: give --gate IP:PORT or SEAT_GATE'
safe_path 'the state folder' "$S"
safe_path 'the root' "$ROOT"
safe_path 'the checkout' "$REPO"
safe_path --bin "$BIN"
safe_path --daemon "$DAEMON"
check_gate "$GATE"
if [ -n "$CLAUDE_SRC$MODEL" ]; then
  [ -n "$CLAUDE_SRC" ] || die '--model needs --claude FILE, the tested claude'
  [ -n "$MODEL" ] || die '--claude needs --model NAME: the live lead runs the model you name (the cheapest)'
  check_model "$MODEL"
  safe_path --claude "$CLAUDE_SRC"
  if [ -L "$CLAUDE_SRC" ]; then die "--claude is a link (the version manager's shim or a link to a version): give the file itself: $CLAUDE_SRC"; fi
  if [ ! -f "$CLAUDE_SRC" ] || [ ! -x "$CLAUDE_SRC" ]; then die "--claude is not an executable regular file: $CLAUDE_SRC"; fi
fi

# The trial's state folder: a real private folder that holds the gate's key
# and pins. It is only looked at.
if [ -L "$S" ] || [ ! -d "$S" ]; then die "the state folder is not a folder (a link is refused): $S"; fi
for _f in owner/gate/cert.pem owner/gate/key.pem owner/gate/seat-pins; do
  [ -f "$S/$_f" ] || die "the state folder has no $_f: run the phone trial's set-up first (docs/seat-gate.md, Phone trial)"
done
TRIAL_SETTINGS=no
if [ -f "$S/owner/scratch.json" ]; then TRIAL_SETTINGS=yes; fi
if [ -e "$S/owner/scratch.json.trial" ]; then
  die "$S/owner/scratch.json.trial is there already: the trial's settings may be set aside from an earlier proof; put them back (step 16) before you prepare again"
fi

# The root: not in /tmp, not in or around the checkout or the state folder,
# new or empty.
if [ -L "$ROOT" ]; then die "the root is a link: $ROOT"; fi
if [ -e "$ROOT" ] && [ ! -d "$ROOT" ]; then die "the root is not a folder: $ROOT"; fi
for _t in /tmp /var/tmp; do
  if inside "$ROOT" "$_t"; then die "the root is in $_t, which the host clears: choose a folder in your cache folder"; fi
done
if inside "$ROOT" "$REPO" || inside "$REPO" "$ROOT"; then die "the root and the checkout lie one in the other: $ROOT"; fi
if inside "$ROOT" "$S" || inside "$S" "$ROOT"; then die "the root and the state folder lie one in the other: $ROOT"; fi
if [ -d "$ROOT" ] && [ -n "$(find "$ROOT" -mindepth 1 -maxdepth 1 2>/dev/null)" ]; then
  die "the root is not empty: $ROOT (prepare writes a new set-up; choose another folder)"
fi

# The binaries.
_missing=
for _b in $BINARIES; do
  if [ ! -f "$BIN/$_b" ] || [ -L "$BIN/$_b" ] || [ ! -x "$BIN/$_b" ]; then _missing="$_missing $_b"; fi
done
[ -z "$_missing" ] || die "not built in $BIN:$_missing (docs/scratch-seat-proof.md, Before the sitting, says how to build them)"
if [ ! -f "$DAEMON" ] || [ ! -x "$DAEMON" ]; then
  die "no daemon of both tags at $DAEMON (docs/scratch-seat-proof.md, Before the sitting, says how to build it)"
fi

# pasta: a regular file is used as it is; a link is copied into ROOT/bin.
PASTA_COPY=
if [ -n "$PASTA" ]; then
  safe_path --pasta "$PASTA"
  if [ -L "$PASTA" ] || [ ! -f "$PASTA" ] || [ ! -x "$PASTA" ]; then die "--pasta is not an executable regular file (a link is refused): $PASTA"; fi
  PASTA_USE=$PASTA
elif [ -L /usr/bin/pasta ]; then
  PASTA_COPY=$(readlink -f -- /usr/bin/pasta) || die 'the link /usr/bin/pasta cannot be resolved; give --pasta FILE'
  if [ ! -f "$PASTA_COPY" ] || [ ! -x "$PASTA_COPY" ]; then die '/usr/bin/pasta does not lead to an executable regular file; give --pasta FILE'; fi
  PASTA_USE=$ROOT/bin/pasta
elif [ -f /usr/bin/pasta ] && [ -x /usr/bin/pasta ]; then
  PASTA_USE=/usr/bin/pasta
else
  die 'no /usr/bin/pasta; give --pasta FILE'
fi
if [ -z "$BWRAP" ]; then BWRAP=$(command -v bwrap 2>/dev/null) || BWRAP=/usr/bin/bwrap; fi
safe_path 'bubblewrap' "$BWRAP"

set_paths "$S" "$ROOT"
SCREENS=$REPO/internal/inject/screen/testdata/claude-$RUNTIME_VERSION
LIVE=
if [ -n "$MODEL" ]; then LIVE=live; fi

if [ "$MODE" = dry ]; then
  say "[dry] prepare.sh would make, and writes nothing now:"
  say "[dry]   folder $ROOT (0700) with conf/, agent-install/, bin/, upstream/, evidence/, owner-channel/ and owner-channel/drop/ (0700)"
  say "[dry]   $CONFIG (0600): the scratch config; state_root $S (the trial's state folder), runtime_root $RUNTIME, max_running 1"
  say "[dry]     lead $KEY_STANDIN: the stand-in, runtime claude $RUNTIME_VERSION, footer_mode manual, inject true, limits 4 GiB, no swap, 512 tasks"
  if [ -n "$LIVE" ]; then
    say "[dry]     lead $KEY_LIVE: live claude $RUNTIME_VERSION, argv [\"/ataniq/agent/claude\", \"--model\", \"$MODEL\"], env DISABLE_AUTOUPDATER=1 CLAUDE_CODE_NO_FLICKER=1, footer_mode manual, inject true, the same limits"
  else
    say "[dry]     no live lead: give --claude FILE and --model NAME for it"
  fi
  say "[dry]     never_mounted: $(never_mounted)"
  say "[dry]   $SETTINGS (0600): the owner channel's settings (stand-in door, proof-project, roots $OC, no sandbox template), for you to put in place at step 4"
  say "[dry]   $ROOT/conf/managed-settings.json, a no-op $ROOT/conf/guard-hook and $ROOT/conf/replay-screens (from $SCREENS)"
  say "[dry]   $ROOT/agent-install/ataniq-canary-standin, a copy of $BIN/ataniq-canary-standin"
  if [ -n "$LIVE" ]; then say "[dry]   $ROOT/agent-install/claude (0700), a copy of $CLAUDE_SRC; it is not run"; fi
  say "[dry]   $ROOT/bin/ataniq-probe (0700), a copy of $BIN/ataniq-probe"
  if [ -n "$PASTA_COPY" ]; then say "[dry]   $PASTA_USE, a copy of $PASTA_COPY"; else say "[dry]   pasta: $PASTA_USE (used as it is)"; fi
  say "[dry]   $ROOT/upstream: a throwaway repository, one commit on develop, author example.invalid"
  say "[dry]   nothing in $S: it is only looked at (the gate's key and pins are there; trial settings in owner/scratch.json: $TRIAL_SETTINGS)"
  say ""
  commands "$GATE" "$LIVE"
  exit 0
fi

# --write. From here a failure leaves a half-made root, which is never
# deleted: the last line says so.
half_made() {
  _rc=$?
  if [ "$_rc" -ne 0 ] && [ -n "${ROOT_MADE:-}" ]; then
    printf '%s\n' "$NAME: failed; $ROOT is half made: look at it, then remove it by hand or prepare again with another --root" >&2
  fi
  exit "$_rc"
}
# shellcheck disable=SC2174  # the parents get 0700 from the umask
if [ ! -d "$ROOT" ]; then mkdir -p -m 0700 -- "$ROOT"; fi
chmod 0700 -- "$ROOT"
ROOT_MADE=1
trap half_made EXIT
say "making $ROOT"
mkdir -m 0700 -- "$ROOT/conf" "$ROOT/agent-install" "$ROOT/bin" "$ROOT/upstream" "$EVIDENCE" "$OC" "$OC/drop"

# The managed settings name the guard hook, bound at /ataniq/qargi/guard-hook,
# and hold the marker text guard-hook, as the canary run kit's do.
printf '%s\n' '{"permissions":{"defaultMode":"default"},"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/ataniq/qargi/guard-hook"}]}]}}' >"$ROOT/conf/managed-settings.json"
printf '%s\n' '#!/bin/sh' 'exit 0' >"$ROOT/conf/guard-hook"
chmod 0600 -- "$ROOT/conf/managed-settings.json"
chmod 0700 -- "$ROOT/conf/guard-hook"
mkdir -m 0700 -- "$ROOT/conf/replay-screens"
for _x in json txt; do
  [ -f "$SCREENS/idle-before-paste.$_x" ] || die "the recorded screens are missing in the checkout: $SCREENS"
  cp -- "$SCREENS/idle-before-paste.$_x" "$ROOT/conf/replay-screens/1.$_x"
  cp -- "$SCREENS/collapsed-paste-footer-c.$_x" "$ROOT/conf/replay-screens/2.$_x"
done
printf '%s\n' 'state=idle:app=claude-code' >"$ROOT/conf/replay-screens/report.txt"
chmod 0600 -- "$ROOT/conf/replay-screens/"*

cp -- "$BIN/ataniq-canary-standin" "$ROOT/agent-install/ataniq-canary-standin"
chmod 0700 -- "$ROOT/agent-install/ataniq-canary-standin"
if [ -n "$LIVE" ]; then
  cp -- "$CLAUDE_SRC" "$ROOT/agent-install/claude"
  chmod 0700 -- "$ROOT/agent-install/claude"
fi
cp -- "$BIN/ataniq-probe" "$ROOT/bin/ataniq-probe"
chmod 0700 -- "$ROOT/bin/ataniq-probe"
if [ -n "$PASTA_COPY" ]; then
  cp -- "$PASTA_COPY" "$PASTA_USE"
  chmod 0700 -- "$PASTA_USE"
fi

# The throwaway repository both leads' mirrors are made from.
env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 git init -q --template= "$ROOT/upstream"
env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "$ROOT/upstream" symbolic-ref HEAD refs/heads/develop
printf '%s\n' 'A throwaway repository for the attended proof of real replies.' >"$ROOT/upstream/README.txt"
env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "$ROOT/upstream" add README.txt
env GIT_AUTHOR_NAME='Sample Author' GIT_AUTHOR_EMAIL=author@example.invalid \
  GIT_COMMITTER_NAME='Sample Author' GIT_COMMITTER_EMAIL=author@example.invalid \
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 \
  git -C "$ROOT/upstream" -c commit.gpgsign=false commit -q -m 'Initial throwaway commit'

config_json "$MODEL" >"$CONFIG"
chmod 0600 -- "$CONFIG"
settings_json >"$SETTINGS"
chmod 0600 -- "$SETTINGS"

say "wrote $CONFIG and $SETTINGS; nothing was started, and nothing in $S was changed"
say ""
commands "$GATE" "$LIVE"
