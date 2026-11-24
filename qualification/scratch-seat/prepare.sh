#!/bin/sh
# Prepare the owner-attended proof of real lead replies: the scratch config and
# the files it names, in a folder of the proof's own, for the daemon that
# serves the scratch leads and the seat gate on the phone trial's state folder.
# Read docs/scratch-seat-proof.md first: it has every step, in order, and says
# which steps are the owner's alone.
#
#   prepare.sh --dry      VALUES [--claude FILE --model NAME] [--bin DIR] [--daemon FILE]
#                                [--pasta FILE] [--bwrap FILE]
#   prepare.sh --write    the same
#   prepare.sh --status   --state DIR --root DIR
#   prepare.sh --record-trial --state DIR --root DIR --unit NAME
#   prepare.sh --swap-in  VALUES
#   prepare.sh --swap-out --state DIR --root DIR --gate IP:PORT --unit NAME
#   prepare.sh --self-test [gate IP:PORT | config STATE ROOT SITTING [MODEL]
#                          | settings ROOT | commands STATE ROOT IP:PORT SITTING [live]]
#
# VALUES are --state DIR --root DIR --gate IP:PORT --sitting N --unit NAME. A
# value is taken from its flag, else from the environment: OWNER_STATE_DIR
# (--state: the phone trial's state folder, which becomes the scratch state
# root, so that the gate's key, its seat pins and the paired phones' keys stay
# the trial's), ROOT (--root: a new or empty folder for the proof's own files,
# whose parent exists), SEAT_GATE (--gate: the gate's IPv4 address and port),
# SITTING (--sitting: the sitting's number, 1 to 999, which makes the leads'
# keys standin-N and live-N fresh for this sitting), TRIAL_UNIT (--unit: the
# trial's user unit, one NAME.service; printed commands name it as
# "$TRIAL_UNIT", never by its value), CLAUDE_FILE and CLAUDE_MODEL (--claude,
# --model: the tested claude, one regular file, and the model; without them the
# config has the stand-in lead only). No value is written into the repository
# and none has a default that names a place of the owner's.
#
# --dry prints what --write would make and the owner's steps from the values
# alone: it reads no folder of the owner's and writes nothing, and what
# depends on the file system is printed as a condition. --write checks the
# state folder (the trial's private settings, the gate's key and pins, no
# earlier set-aside and nothing of this sitting's leads), the tools and ROOT
# (its parent exists; it is new or empty; physically outside the state folder,
# the checkout, /tmp, /var/tmp and $TMPDIR), then makes ROOT (mode 0700, only
# that folder) and writes in it only: the scratch config scratch.json (0600),
# the owner channel's settings owner-scratch.json (0600), the managed settings
# and the guard hook (a no-op the holder's marker check needs; it guards
# nothing), the replay screens of the checkout, copies of the stand-in, the
# probe, pasta when it is a link and the claude file, a throwaway source
# repository (git, one commit, in a clean environment), netcheck.sh (the
# owner's network check, never run by prepare) and the folders owner-channel/,
# its drop/ and evidence/. It never starts a unit, a daemon, a sandbox or an
# agent, never runs the claude it copies, never answers a prompt and never
# deletes anything. It never writes in the trial's state folder.
#
# --record-trial, --status, --swap-in and --swap-out are the owner's.
# --record-trial (step f, while the trial runs, before step 1) reads the
# trial's unit from the user manager (systemctl show only) and, for a
# transient unit, the command line of its main process
# (/proc/MAINPID/cmdline), and writes in ROOT only: trial-unit (the unit's
# kind, transient or a unit file, which the later modes read),
# evidence/trial-unit-before.txt and, for a transient unit, trial-restart.sh
# (0700), the systemd-run command that re-creates it, every argument
# single-quoted; prepare never runs it. It refuses, writing nothing, a unit
# that does not run, a MainPID of 0, a command line it cannot read or that
# holds a newline, and a program that is not ExecStart's. PREPARE_PROC, for
# the tests only, names a folder that stands for /proc.
# --status reads only: it names the state of owner/scratch.json, the trial's
# set-aside owner/scratch.json.trial and ROOT/owner-scratch.json as trial in
# place, swapped in, or interrupted or ambiguous, with their identities and
# digests, and the recorded kind of the trial's unit, and prints one next
# step. --swap-in (the trial's unit recorded and inactive; a transient one
# gone, a unit file masked or, when the runtime mask cannot take, without an
# activation source, see trial_guard; the gate's port free; the leads' units
# unknown to the user manager) adds what it found to
# ROOT/evidence/trial-unit-guard.txt, records the trial's settings in
# ROOT/swap-record, renames them within
# owner/ to scratch.json.trial and puts a copy of the proof's settings at
# owner/scratch.json (written under a dot name in owner/, then renamed).
# --swap-out (the unit inactive, the port free) renames the proof's settings
# within owner/ to scratch.json.leads-N and the recorded trial settings back
# to owner/scratch.json. Every rename refuses a name that is taken (a dangling
# link included), and each is checked after it is made; nothing is deleted. A
# layout that is not one of the three states is refused with the step that
# recovers it.
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
PREP=$REPO/qualification/scratch-seat/prepare.sh

# The tested version of claude, the one the recorded screens are of.
RUNTIME_VERSION=2.1.325
# The binaries of build/canary the proof uses.
BINARIES='ataniq-holder ataniq-canary ataniq-canary-standin ataniq-console ataniq-probe'
# The token the owner gives the stand-in.
TOKEN='canary-token: proof-standin-0001'

say() { printf '%s\n' "$*"; }

die() {
  printf '%s: %s\n' "$NAME" "$*" >&2
  exit 1
}

usage() {
  printf '%s\n' 'usage: prepare.sh --dry|--write|--swap-in --state DIR --root DIR --gate IP:PORT --sitting N --unit NAME' \
    '                  [--claude FILE --model NAME] [--bin DIR] [--daemon FILE] [--pasta FILE] [--bwrap FILE]' \
    '       prepare.sh --status --state DIR --root DIR' \
    '       prepare.sh --record-trial --state DIR --root DIR --unit NAME' \
    '       prepare.sh --swap-out --state DIR --root DIR --gate IP:PORT --unit NAME' \
    '       --dry          print what --write makes and your steps, from the values alone; read and write nothing' \
    '       --write        check, make ROOT and write the proof'"'"'s files in it (nothing else)' \
    '       --record-trial read the running trial'"'"'s unit and write its record in ROOT (yours; it runs nothing)' \
    '       --status       read the trial'"'"'s and the proof'"'"'s settings and print one next step (yours)' \
    '       --swap-in      put the proof'"'"'s settings in place of the trial'"'"'s, within owner/ (yours)' \
    '       --swap-out     put the trial'"'"'s settings back, within owner/ (yours)' \
    '       --state DIR    the phone trial'"'"'s state folder (or OWNER_STATE_DIR)' \
    '       --root DIR     a new or empty folder for the proof (or ROOT); its parent exists; outside /tmp and the checkout' \
    '       --gate IP:PORT the seat gate'"'"'s IPv4 address and port (or SEAT_GATE)' \
    '       --sitting N    the sitting'"'"'s number, 1 to 999 (or SITTING): the leads are standin-N and live-N' \
    '       --unit NAME    the trial'"'"'s user unit, NAME.service (or TRIAL_UNIT)' \
    '       --claude FILE  the tested claude, one regular file (or CLAUDE_FILE); it is copied, never run' \
    '       --model NAME   the model of the live lead (or CLAUDE_MODEL); needed with --claude' \
    '       --bin DIR      the canary binaries (default: build/canary of the checkout)' \
    '       --daemon FILE  the daemon of both tags (default: build/scratch-seat/ataniqd of the checkout)' \
    '       --pasta FILE   pasta (default: /usr/bin/pasta; a link is copied)' \
    '       --bwrap FILE   bubblewrap (default: the one on PATH at --write)' \
    '       prepare.sh --self-test [gate IP:PORT | config STATE ROOT SITTING [MODEL] | settings ROOT' \
    '                  | commands STATE ROOT IP:PORT SITTING [live]]   (computes and prints only)' >&2
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

# check_gate VALUE refuses an address that is not one IPv4 address of a host
# and a port: four decimal numbers from 0 to 255 without a leading zero, not
# 0.0.0.0, not multicast (224 to 239 first), not 255.255.255.255, and a port
# from 1 to 65535 without a leading zero. IPv6 is refused for the proof. The
# daemon checks the address again when it starts, and a test holds the two
# checks to one table.
check_gate() {
  case $1 in
    \[*) die "the proof takes an IPv4 gate address only; an IPv6 address is refused: $1" ;;
    *:*:*) die "the proof takes an IPv4 gate address only; an IPv6 address is refused: $1" ;;
    *:*) ;;
    *) die "the gate's address is not IPv4:PORT: $1" ;;
  esac
  _h=${1%:*}
  _p=${1##*:}
  case $_h in
    '' | *[!0-9.]* | *..* | .* | *.) die "the gate's address is not an IPv4 address and a port: $1" ;;
  esac
  _n=0
  _first=
  _rest=$_h.
  while [ -n "$_rest" ]; do
    _o=${_rest%%.*}
    _rest=${_rest#*.}
    _n=$((_n + 1))
    case $_o in
      '' | ???? | 0?*) die "the gate's address is not an IPv4 address and a port: $1" ;;
    esac
    [ "$_o" -le 255 ] || die "the gate's address is not an IPv4 address and a port: $1"
    [ -n "$_first" ] || _first=$_o
  done
  [ "$_n" -eq 4 ] || die "the gate's address is not an IPv4 address and a port: $1"
  [ "$_h" != 0.0.0.0 ] || die "the gate's address is a wildcard, not one address of this host: $1"
  [ "$_h" != 255.255.255.255 ] || die "the gate's address is the broadcast address, not one address of this host: $1"
  if [ "$_first" -ge 224 ] && [ "$_first" -le 239 ]; then die "the gate's address is a multicast address, not one address of this host: $1"; fi
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

# check_unit NAME refuses anything but one unit name NAME.service: no option,
# no pattern, no space, no character a shell would read, and not a lead's
# unit.
check_unit() {
  case $1 in
    '') die 'no trial unit: give --unit NAME or TRIAL_UNIT, the user unit that runs the phone trial (NAME.service)' ;;
    -*) die "the trial unit starts with -: $1" ;;
    *[!A-Za-z0-9:_.@-]*) die "the trial unit is one unit name of letters, digits and : _ . @ -, ending in .service: $1" ;;
    ataniq-lead-*) die "the trial unit is a lead's unit: $1" ;;
    ?*.service) ;;
    *) die "the trial unit is not a NAME.service: $1" ;;
  esac
  [ "${#1}" -le 255 ] || die 'the trial unit is longer than 255 characters'
}

# check_sitting N refuses a sitting number that is not 1 to 999.
check_sitting() {
  case $1 in
    '') die 'no sitting: give --sitting N or SITTING, the number of this sitting (1 to 999), which names its leads' ;;
    [1-9] | [1-9][0-9] | [1-9][0-9][0-9]) ;;
    *) die "the sitting is a number from 1 to 999 without a leading zero: $1" ;;
  esac
}

# check_home refuses a HOME that cannot give the never-mounted paths the
# guide promises (~/.ssh, ~/.claude and ~/.codex).
check_home() {
  _home=${HOME:-}
  [ -n "$_home" ] || die 'HOME is not set, so ~/.ssh, ~/.claude and ~/.codex cannot be listed as never mounted'
  case $_home in
    /*) ;;
    *) die "HOME is not an absolute path, so ~/.ssh, ~/.claude and ~/.codex cannot be listed as never mounted: $_home" ;;
  esac
  case $_home in
    *[!A-Za-z0-9._/+-]*) die "HOME has a character outside A-Z a-z 0-9 . _ / + -, so ~/.ssh, ~/.claude and ~/.codex cannot be listed as never mounted: $_home" ;;
  esac
  case $_home/ in
    */./* | */../* | *//*) die "HOME has a . or .. part or an empty part: $_home" ;;
  esac
  case $_home in
    / | */) die "HOME is / or ends with /: $_home" ;;
  esac
}

# sq VALUE prints VALUE single-quoted for sh, each ' written as '\''. The
# callers refuse a value with a newline before (a command substitution would
# drop a trailing one).
sq() {
  printf "'%s'" "$(printf '%s\n' "$1" | LC_ALL=C sed "s/'/'\\\\''/g")"
}

# phys DIR prints the physical path of an existing folder, or fails.
phys() { (cd -P -- "$1" 2>/dev/null && pwd -P); }

# The values every computation uses. set_paths STATE ROOT sets the names of
# the proof's files; set_keys SITTING the leads' keys.
set_paths() {
  S=$1
  ROOT=$2
  CONFIG=$ROOT/scratch.json
  SETTINGS=$ROOT/owner-scratch.json
  RUNTIME=$ROOT/runtime
  OC=$ROOT/owner-channel
  EVIDENCE=$ROOT/evidence
  RECORD=$ROOT/swap-record
  KINDREC=$ROOT/trial-unit
  BEFORE=$EVIDENCE/trial-unit-before.txt
  RESTART=$ROOT/trial-restart.sh
  OWNER=$S/owner
  CANON=$OWNER/scratch.json
  ASIDE=$OWNER/scratch.json.trial
}
set_keys() {
  SITTING=$1
  KEY_STANDIN=standin-$1
  KEY_LIVE=live-$1
  ARCHIVE_NAME=scratch.json.leads-$1
}

# never_mounted prints the paths no bind source of a lead may touch: the
# checkout, the owner's own folders of credentials and the owner folder of
# the state root, which holds the gate's key and the phones' keys. check_home
# has refused a HOME that cannot give them.
never_mounted() {
  printf '%s' "\"$REPO\", \"$HOME/.ssh\", \"$HOME/.claude\", \"$HOME/.codex\", \"$S/owner\""
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
# Every step that starts a real process, changes a unit or defines a lead says
# OWNER ONLY. No printed command deletes anything or moves a file by hand: the
# swaps are prepare's own modes. The trial's unit is named "$TRIAL_UNIT", a
# reference the owner's shell fills in, never by its value; no printed line
# holds a placeholder in angle brackets.
commands() {
  _g=$1
  _live=${2:-}
  _c="$DAEMON chat"
  _vals="--state $S --root $ROOT"
  _lu=ataniq-lead-scratch-$KEY_LIVE.service
  _su=ataniq-lead-scratch-$KEY_STANDIN.service
  cat <<EOF
Your steps, in order (docs/scratch-seat-proof.md says what each proves and what to do when one fails).
Three terminals in the checkout, with TRIAL_UNIT set: A for the daemon, B for the console, C for the desk.
Before the sitting, with the trial running (terminal C):
EOF
  if [ -n "$_live" ]; then
    cat <<EOF
  c. OWNER ONLY (runs the claude copy): env -i HOME="\$(mktemp -d)" $ROOT/agent-install/claude --version
     must name $RUNTIME_VERSION.
EOF
  fi
  cat <<EOF
  d. Check that your firewall admits TCP to $_g from the authority host only.
  e. OWNER ONLY (starts pasta, bubblewrap and curl): sh $ROOT/netcheck.sh | tee -a $EVIDENCE/netcheck.txt
     It must say PASS for the gate; write in $EVIDENCE/notes.txt whether you accept the open network it lists.
  f. $DAEMON seat-gate pins --state-dir $S
     sh $PREP --record-trial $_vals --unit "\$TRIAL_UNIT"   (reads the unit and its command line; writes $KINDREC, $BEFORE and, for a transient unit, its restart script)
     journalctl --user --unit "\$TRIAL_UNIT" -I | grep 'key fingerprint' | tail -n 1
  g. sh $PREP --status $_vals   (trial in place; it names the kind of the trial's unit)
Switch from the trial to the proof:
  1. OWNER ONLY: systemctl --user stop -- "\$TRIAL_UNIT"
     a unit file only (step g names the kind): OWNER ONLY: systemctl --user mask --runtime -- "\$TRIAL_UNIT"
     then: systemctl --user show -p ActiveState -p LoadState -- "\$TRIAL_UNIT"   (inactive; not-found for a transient unit, masked for a unit file)
     a unit file still loaded (the mask did not take): OWNER ONLY: systemctl --user unmask --runtime -- "\$TRIAL_UNIT"   (step 3 then checks that nothing can start it)
  2. $BIN/ataniq-canary scratch-init --config $CONFIG
  3. sh $PREP --swap-in $_vals --gate $_g --sitting $SITTING --unit "\$TRIAL_UNIT"
  4. OWNER ONLY (terminal A): sh -c 'trap : INT; "\$@" 2>&1; echo "ataniqd: exit status \$?"' sh $DAEMON run --scratch $CONFIG --owner-channel --seat-gate $_g | tee -ai $EVIDENCE/daemon.log
  5. OWNER ONLY (terminal B): $BIN/ataniq-console --daemon $RUNTIME   then L
The stand-in lead:
  6. OWNER ONLY (the daemon runs git to define it): d on $KEY_STANDIN
     then (terminal C): $_c set $KEY_STANDIN read --config $CONFIG
  7. OWNER ONLY: s on $KEY_STANDIN; m: $TOKEN
     Only after NOT delivered (nothing was written): w, Enter, look at the input line, paste that line, Enter once; Ctrl+\\ then q.
     Any other answer, or one you are not sure of: no paste; stop and reconcile as the guide says.
  8. Phone and desktop: Leads, daemon:$KEY_STANDIN and its reply; then x and y on $KEY_STANDIN
EOF
  if [ -n "$_live" ]; then
    cat <<EOF
The live lead (same sitting, only if all was well):
  9. Note the login rows of the provider's account page.
     OWNER ONLY (the daemon runs git to define it): d on $KEY_LIVE
     then (terminal C): $_c set $KEY_LIVE read --config $CONFIG
 10. OWNER ONLY: s on $KEY_LIVE; then (terminal C): systemctl --user status -- $_lu
     must show /ataniq/agent/claude --model $MODEL. Note the time.
     w, Enter, a fresh login in the pane; approve only the write of the one reply into its outbox; Ctrl+\\ then q;
     m with the message of the guide (a paste only after NOT delivered, as in 7)
 11. Phone and desktop: daemon:$KEY_LIVE and its reply; at most 30 minutes or 10 turns, by your clock; then x and y on $KEY_LIVE;
     revoke the login made in 10 and confirm it is gone
Off, restart, stop and back to the trial:
 12. (terminal C) $_c set $KEY_LIVE off --config $CONFIG   (its page says Chat is off; $KEY_STANDIN stays on)
     $_c list --config $CONFIG >>$EVIDENCE/chat-list.jsonl
EOF
  else
    cat <<EOF
Off, restart, stop and back to the trial:
 12. (terminal C) $_c set $KEY_STANDIN off --config $CONFIG   (its page says Chat is off)
     $_c set $KEY_STANDIN read --config $CONFIG   (on again, for the restart)
     $_c list --config $CONFIG >>$EVIDENCE/chat-list.jsonl
EOF
  fi
  _units=$_su
  if [ -n "$_live" ]; then _units="$_su $_lu"; fi
  cat <<EOF
 13. OWNER ONLY: quit the console; Ctrl+C in A (exit status 0); the daemon again as in 4, then (terminal C):
     $_c list --config $CONFIG >>$EVIDENCE/chat-list.jsonl   ($KEY_STANDIN is off now)
     OWNER ONLY: Ctrl+C in A again (exit status 0); systemctl --user list-units --all -- $_units   (0 units)
 14. sh $PREP --swap-out $_vals --gate $_g --unit "\$TRIAL_UNIT"
 15. a transient unit (step 14 printed the kind): OWNER ONLY: sh $RESTART   (re-creates it as recorded)
     a unit file: OWNER ONLY: systemctl --user unmask --runtime -- "\$TRIAL_UNIT"   (when it is masked)
     a unit file: OWNER ONLY: systemctl --user start -- "\$TRIAL_UNIT"   (when trial-unit-before.txt says ActiveState=active)
     systemctl --user show -p Id -p Transient -p UnitFileState -p LoadState -p ActiveState -p InvocationID -p MainPID -p ExecStart -- "\$TRIAL_UNIT" >$EVIDENCE/trial-unit-after.txt
     journalctl --user --unit "\$TRIAL_UNIT" -I | grep 'key fingerprint' | tail -n 1   (this new invocation only)
 16. sh $PREP --status $_vals   (trial in place, the way back done); the phone shows the trial's samples
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
  for _a in 192.0.2.10:7443 10.0.0.1:1 127.0.0.1:65535 223.255.255.254:7443 240.0.0.1:7443; do
    (check_gate "$_a") 2>/dev/null || _fail "the gate address $_a is refused"
    _n=$((_n + 1))
  done
  for _a in 0.0.0.0:7443 '[::]:7443' '[2001:db8::10]:7443' '[abc]:7443' '[0:0:0:0:0:0:0:0]:7443' '[::ffff:192.0.2.10]:7443' \
    gate.example:7443 192.0.2.10:0 192.0.2.10 192.0.2.300:7443 192.0.2.10:70000 192.0.2.010:7443 192.0.2.10:07443 \
    224.0.0.1:7443 239.255.255.255:7443 255.255.255.255:7443 1.2.3:7443 1.2.3.4.5:7443 :7443; do
    if (check_gate "$_a") 2>/dev/null; then _fail "the gate address $_a is taken"; fi
    _n=$((_n + 1))
  done
  for _u in sample.service 'a b.service' -x.service 'a*.service' sample.socket 'a;b.service' ataniq-lead-scratch-x.service ''; do
    if (check_unit "$_u") 2>/dev/null; then
      [ "$_u" = sample.service ] || _fail "the unit $_u is taken"
    else
      [ "$_u" != sample.service ] || _fail "the unit $_u is refused"
    fi
    _n=$((_n + 1))
  done
  # sq: what the shell reads back is the value itself.
  for _v in 'a b' "it's" "'" "''" '$HOME' '${x}' 'a\b' '\' '`x`' '"q"' '' ' -x ' '*' '!x' '%s'; do
    eval "_r=$(sq "$_v")"
    [ "$_r" = "$_v" ] || _fail "sq does not quote $_v"
    _n=$((_n + 1))
  done
  HOME=/srv/sample/home
  set_keys 7
  set_paths /srv/sample/trial-state /srv/sample/proof
  for _m in '' sample-model; do
    _c=$(config_json "$_m")
    for _w in '"state_root": "/srv/sample/trial-state"' '"runtime_root": "/srv/sample/proof/runtime"' '"max_running": 1' \
      '"/srv/sample/trial-state/owner"' '"/srv/sample/home/.claude"' '"key": "standin-7"' '"inject": true'; do
      case $_c in *"$_w"*) ;; *) _fail "the config lacks $_w" ;; esac
      _n=$((_n + 1))
    done
    case $_c in
      *'"key": "live-7"'*) [ -n "$_m" ] || _fail 'the config has a live lead without a model' ;;
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
  _nl='
'
  # The name of the make target that builds the daemon of both tags, made
  # here so that this file never holds it: no step names it.
  _tgt=scratch-seat-
  _tgt=${_tgt}build
  for _m in '' live; do
    MODEL=sample-model
    _o=$(commands 192.0.2.10:7443 "$_m")
    case $_o in *'--swap-in '*'--swap-out '*'--status '*) ;; *) _fail 'the steps lack the swaps in order' ;; esac
    case $_o in *'--record-trial '*'systemctl --user stop'*) ;; *) _fail 'the steps do not record the trial before they stop it' ;; esac
    case $_o in *'TRIAL_UNIT'*) ;; *) _fail 'the steps do not name the unit' ;; esac
    case $_o in *'KEY'*) _fail 'the steps name a lead KEY' ;; esac
    case $_o in *'live-7'*) [ -n "$_m" ] || _fail 'the steps name a lead that is not configured' ;; esac
    case $_o in *'claude --version'*) [ -n "$_m" ] || _fail 'the steps run claude without a live lead' ;; esac
    _n=$((_n + 5))
    _rest=$_o$_nl
    while [ -n "$_rest" ]; do
      _l=${_rest%%"$_nl"*}
      _rest=${_rest#*"$_nl"}
      case $_l in
        *' rm '* | *'rm -'* | *' mv '* | *' unlink '* | *"$_tgt"*) _fail "a step deletes, moves by hand or builds: $_l" ;;
        *'<'[A-Za-z]*'>'*) _fail "a step holds a placeholder: $_l" ;;
        *'sample.service'*) _fail "a step names the unit by its value: $_l" ;;
      esac
      case $_l in
        *'systemctl --user st'* | *'systemctl --user mask'* | *'systemctl --user unmask'* | *' run --scratch '* | \
          *'ataniq-console '* | *' --version'* | *'d on '* | *'s on '* | *'netcheck.sh'* | *'trial-restart.sh'*)
          case $_l in *'OWNER ONLY'*) ;; *) _fail "a step that starts a process, changes a unit or defines a lead is not marked OWNER ONLY: $_l" ;; esac ;;
      esac
      case $_l in
        *'systemctl '*'TRIAL_UNIT'* | *'journalctl '*'TRIAL_UNIT'*)
          # shellcheck disable=SC2016  # the text of the reference itself
          case $_l in *'"$TRIAL_UNIT"'*) ;; *) _fail "the unit is not a quoted reference: $_l" ;; esac ;;
      esac
      _n=$((_n + 1))
    done
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
  MODEL=
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
      [ "$#" -eq 5 ] || [ "$#" -eq 6 ] || usage
      safe_path --state "$3"
      safe_path --root "$4"
      check_sitting "$5"
      if [ "$#" -eq 6 ]; then check_model "$6"; fi
      check_home
      set_keys "$5"
      set_paths "$3" "$4"
      config_json "${6:-}"
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
      [ "$#" -eq 6 ] || { [ "$#" -eq 7 ] && [ "$7" = live ]; } || usage
      safe_path --state "$3"
      safe_path --root "$4"
      check_gate "$5"
      check_sitting "$6"
      MODEL=sample-model
      set_keys "$6"
      set_paths "$3" "$4"
      commands "$5" "${7:-}"
      exit 0
      ;;
  esac
  usage
fi

MODE=
S=${OWNER_STATE_DIR:-}
ROOT=${ROOT:-}
GATE=${SEAT_GATE:-}
SIT=${SITTING:-}
UNIT=${TRIAL_UNIT:-}
CLAUDE_SRC=${CLAUDE_FILE:-}
MODEL=${CLAUDE_MODEL:-}
BIN=$REPO/build/canary
DAEMON=$REPO/build/scratch-seat/ataniqd
PASTA=
BWRAP=
while [ "$#" -gt 0 ]; do
  case $1 in
    --dry | --write | --status | --record-trial | --swap-in | --swap-out)
      [ -z "$MODE" ] || usage
      MODE=${1#--}
      shift
      ;;
    --state) [ "$#" -ge 2 ] || usage; S=$2; shift 2 ;;
    --root) [ "$#" -ge 2 ] || usage; ROOT=$2; shift 2 ;;
    --gate) [ "$#" -ge 2 ] || usage; GATE=$2; shift 2 ;;
    --sitting) [ "$#" -ge 2 ] || usage; SIT=$2; shift 2 ;;
    --unit) [ "$#" -ge 2 ] || usage; UNIT=$2; shift 2 ;;
    --claude) [ "$#" -ge 2 ] || usage; CLAUDE_SRC=$2; shift 2 ;;
    --model) [ "$#" -ge 2 ] || usage; MODEL=$2; shift 2 ;;
    --bin) [ "$#" -ge 2 ] || usage; BIN=$2; shift 2 ;;
    --daemon) [ "$#" -ge 2 ] || usage; DAEMON=$2; shift 2 ;;
    --pasta) [ "$#" -ge 2 ] || usage; PASTA=$2; shift 2 ;;
    --bwrap) [ "$#" -ge 2 ] || usage; BWRAP=$2; shift 2 ;;
    *) usage ;;
  esac
done
# No default action: the mode is always said.
[ -n "$MODE" ] || usage

# The values, checked as text before anything is read.
[ -n "$S" ] || die 'no state folder: give --state DIR or OWNER_STATE_DIR, the phone trial'"'"'s state folder'
[ -n "$ROOT" ] || die 'no root: give --root DIR or ROOT, a new or empty folder for the proof'
safe_path 'the state folder' "$S"
safe_path 'the root' "$ROOT"
safe_path 'the checkout' "$REPO"
case $MODE in
  dry | write | swap-in | swap-out)
    [ -n "$GATE" ] || die 'no gate address: give --gate IP:PORT or SEAT_GATE'
    check_gate "$GATE"
    check_unit "$UNIT"
    ;;
  record-trial) check_unit "$UNIT" ;;
esac
case $MODE in
  dry | write | swap-in) check_sitting "$SIT" ;;
  *) if [ -n "$SIT" ]; then check_sitting "$SIT"; fi ;;
esac
set_keys "${SIT:-0}"
for _t in /tmp /var/tmp; do
  if inside "$ROOT" "$_t"; then die "the root is in $_t, which the host clears: choose a folder in your cache folder"; fi
done
if inside "$ROOT" "$REPO" || inside "$REPO" "$ROOT"; then die "the root and the checkout lie one in the other: $ROOT"; fi
if inside "$ROOT" "$S" || inside "$S" "$ROOT"; then die "the root and the state folder lie one in the other: $ROOT"; fi
set_paths "$S" "$ROOT"
LIVE=
case $MODE in
  dry | write)
    safe_path --bin "$BIN"
    safe_path --daemon "$DAEMON"
    check_home
    if [ -n "$CLAUDE_SRC$MODEL" ]; then
      [ -n "$CLAUDE_SRC" ] || die '--model needs --claude FILE, the tested claude'
      [ -n "$MODEL" ] || die '--claude needs --model NAME: the live lead runs the model you name (the cheapest)'
      check_model "$MODEL"
      safe_path --claude "$CLAUDE_SRC"
      LIVE=live
    fi
    if [ -n "$PASTA" ]; then safe_path --pasta "$PASTA"; fi
    if [ -n "$BWRAP" ]; then safe_path 'bubblewrap' "$BWRAP"; fi
    ;;
esac
SCREENS=$REPO/internal/inject/screen/testdata/claude-$RUNTIME_VERSION

if [ "$MODE" = dry ]; then
  # From the values alone: nothing of the owner's is read here.
  PASTA_USE=${PASTA:-/usr/bin/pasta}
  BWRAP=${BWRAP:-bwrap}
  say "[dry] prepare.sh would make, and writes nothing now (the paths of pasta and bubblewrap are decided at --write):"
  say "[dry]   folder $ROOT (0700; its parent must exist) with conf/, agent-install/, bin/, upstream/, evidence/, owner-channel/ and owner-channel/drop/ (0700)"
  say "[dry]   $CONFIG (0600): the scratch config; state_root $S (the trial's state folder), runtime_root $RUNTIME, max_running 1"
  say "[dry]     lead $KEY_STANDIN: the stand-in, runtime claude $RUNTIME_VERSION, footer_mode manual, inject true, limits 4 GiB, no swap, 512 tasks"
  if [ -n "$LIVE" ]; then
    say "[dry]     lead $KEY_LIVE: live claude $RUNTIME_VERSION, argv [\"/ataniq/agent/claude\", \"--model\", \"$MODEL\"], env DISABLE_AUTOUPDATER=1 CLAUDE_CODE_NO_FLICKER=1, footer_mode manual, inject true, the same limits"
  else
    say "[dry]     no live lead: give --claude FILE and --model NAME for it"
  fi
  say "[dry]     never_mounted: $(never_mounted)"
  say "[dry]   $SETTINGS (0600): the owner channel's settings (stand-in door, proof-project, roots $OC, no sandbox template), which --swap-in puts in place"
  say "[dry]   $ROOT/conf/managed-settings.json (the marker guard-hook), $ROOT/conf/guard-hook (exits 0: the holder's marker check needs it, it guards nothing) and $ROOT/conf/replay-screens (from $SCREENS)"
  say "[dry]   $ROOT/agent-install/ataniq-canary-standin, a copy of $BIN/ataniq-canary-standin"
  if [ -n "$LIVE" ]; then say "[dry]   $ROOT/agent-install/claude (0700), a copy of $CLAUDE_SRC; it is not run"; fi
  say "[dry]   $ROOT/bin/ataniq-probe (0700), a copy of $BIN/ataniq-probe"
  if [ -n "$PASTA" ]; then
    say "[dry]   pasta: $PASTA, used as it is (it must be an executable regular file)"
  else
    say "[dry]   pasta: /usr/bin/pasta when it is an executable regular file, or $ROOT/bin/pasta, a copy of the file it leads to, when it is a link"
  fi
  if [ "$BWRAP" = bwrap ]; then say "[dry]   bubblewrap: the bwrap on PATH at --write"; else say "[dry]   bubblewrap: $BWRAP"; fi
  say "[dry]   $ROOT/netcheck.sh (0700): your network check, which prepare never runs"
  say "[dry]   $ROOT/upstream: a throwaway repository, one commit on develop, author example.invalid, made by git in a clean environment"
  say "[dry]   --write refuses, before it makes anything: a state folder that is a link or not at its physical path; an owner/scratch.json that is"
  say "[dry]   not the trial's private regular settings; an owner/scratch.json.trial (an earlier set-aside: see --status); gate material that is"
  say "[dry]   missing, empty, a link or not private; anything of $KEY_STANDIN or $KEY_LIVE in the state folder; a missing binary, daemon, pasta,"
  say "[dry]   bubblewrap or /usr/bin/git; a root whose parent is missing or a link, that is not new or empty or cannot be read, or that lies"
  say "[dry]   physically in the state folder, the checkout, /tmp, /var/tmp or \$TMPDIR; and a HOME that is not a real folder"
  say "[dry]   nothing in $S is written by --dry or --write: --swap-in and --swap-out, yours, rename within $OWNER"
  say ""
  commands "$GATE" "$LIVE"
  exit 0
fi

# ---- From here the file system is read. ----

# The state folder: a real folder at its own physical path.
check_state() {
  if [ -L "$S" ] || [ ! -d "$S" ]; then die "the state folder is not a folder (a link is refused): $S"; fi
  _p=$(phys "$S") || die "the state folder cannot be entered: $S"
  [ "$_p" = "$S" ] || die "the state folder is not at its physical path (a folder on the way is a link): give $_p"
  if [ -L "$OWNER" ] || [ ! -d "$OWNER" ]; then die "the state folder has no owner/ folder (a link is refused): $OWNER"; fi
}

# fstate PATH sets F_K to absent, link (a dangling one too), notfile,
# unreadable or file, and for a file F_ID (device:inode), F_MODE, F_UID and
# F_SHA (SHA-256).
fstate() {
  F_K='' F_ID='' F_MODE='' F_UID='' F_SHA=''
  if [ -L "$1" ]; then F_K='link'; return 0; fi
  if [ ! -e "$1" ]; then F_K='absent'; return 0; fi
  if [ ! -f "$1" ]; then F_K='notfile'; return 0; fi
  _st=$(stat -c '%d:%i %a %u' -- "$1" 2>/dev/null) || { F_K='unreadable'; return 0; }
  F_SHA=$(sha256sum -- "$1" 2>/dev/null) || { F_K='unreadable'; return 0; }
  F_SHA=${F_SHA%% *}
  F_ID=${_st%% *}
  _st=${_st#* }
  F_MODE=${_st%% *}
  F_UID=${_st#* }
  F_K='file'
}
ME=$(id -u)
# private: the file of the last fstate is the caller's, mode 0600 or narrower.
private() {
  [ "$F_K" = file ] && [ "$F_UID" = "$ME" ] || return 1
  case $F_MODE in
    [0-7]00 | 0) return 0 ;;
  esac
  return 1
}
# trial_kind PATH: a regular file that holds "trial": true.
trial_kind() { grep -Eq '"trial"[[:space:]]*:[[:space:]]*true' -- "$1" 2>/dev/null; }

# check_gate_material refuses a gate's key, certificate or pins that the
# daemon could not use.
check_gate_material() {
  for _f in gate/cert.pem gate/key.pem gate/seat-pins; do
    fstate "$OWNER/$_f"
    case $F_K in
      file) ;;
      absent) die "the state folder has no owner/$_f: run the phone trial's set-up first (docs/seat-gate.md, Phone trial)" ;;
      *) die "owner/$_f is a link, not a regular file or unreadable: $OWNER/$_f" ;;
    esac
    [ "$F_UID" = "$ME" ] || die "owner/$_f is not yours: $OWNER/$_f"
    [ -s "$OWNER/$_f" ] || die "owner/$_f is empty: $OWNER/$_f"
    case $_f in
      gate/cert.pem) ;;
      *) private || die "owner/$_f can be read by group or other: $OWNER/$_f (mode $F_MODE)" ;;
    esac
  done
  grep -Eq '^[[:space:]]*[0-9a-f]{64}[[:space:]]*$' -- "$OWNER/gate/seat-pins" ||
    die "owner/gate/seat-pins pins no seat key: pin the seat first (docs/seat-gate.md)"
}

# keys_fresh refuses when the state folder holds anything of this sitting's
# leads: their folders, records, mirrors, locks, chat, evidence or roles in
# the registry. A listing that fails is a refusal too.
keys_fresh() {
  for _k in "$KEY_STANDIN" "$KEY_LIVE"; do
    for _dir in leads scratch mirrors roles chat evidence mailbox pub; do
      if [ ! -e "$S/$_dir" ] && [ ! -L "$S/$_dir" ]; then continue; fi
      [ -d "$S/$_dir" ] && [ -r "$S/$_dir" ] && [ -x "$S/$_dir" ] || die "cannot list $S/$_dir"
      _hit=$(find "$S/$_dir" -mindepth 1 -maxdepth 1 \( -name "scratch-$_k" -o -name "scratch-$_k-*" -o -name "scratch-$_k.*" \) -print) ||
        die "cannot list $S/$_dir"
      [ -z "$_hit" ] || die "the state folder already holds the lead $_k of an earlier sitting ($_hit): give another --sitting"
    done
    _reg=$S/registry/registry.jsonl
    if [ -e "$_reg" ] || [ -L "$_reg" ]; then
      _rc=0
      grep -Fq -e "scratch-$_k/lead" -- "$_reg" || _rc=$?
      case $_rc in
        0) die "the registry of the state folder already holds the role scratch-$_k/lead: give another --sitting" ;;
        1) ;;
        *) die "cannot read $_reg" ;;
      esac
    fi
  done
}

# unit_prop UNIT PROPERTY prints one property of a user unit. It only reads.
unit_prop() {
  systemctl --user show -p "$2" --value -- "$1" || die "cannot read the user manager's state of $1"
}

# port_listens PORT succeeds when a TCP socket of this host's network listens
# on the port, at any address (/proc/net/tcp and tcp6).
port_listens() {
  [ -r /proc/net/tcp ] || die 'cannot read /proc/net/tcp to see whether the gate'"'"'s port is free'
  _hex=$(printf '%04X' "$1")
  for _f in /proc/net/tcp /proc/net/tcp6; do
    [ -e "$_f" ] || continue
    _hit=$(awk -v p=":$_hex" 'NR > 1 && $4 == "0A" && substr($2, length($2) - 4) == p { print "x"; exit }' "$_f") ||
      die "cannot read $_f"
    [ -z "$_hit" ] || return 0
  done
  return 1
}

# unit_quiet refuses a trial unit that runs and a gate port that something
# listens on.
unit_quiet() {
  _a=$(unit_prop "$UNIT" ActiveState)
  [ "$_a" = inactive ] || die "the trial's unit is $_a, not inactive: stop it first (step 1): systemctl --user stop -- \"\$TRIAL_UNIT\""
  port_free
}
port_free() {
  if port_listens "${GATE##*:}"; then die "something listens on the gate's port ${GATE##*:}: a daemon still runs (the trial's or the proof's); stop it first"; fi
}

# trial_guard is --swap-in's check that nothing can start the trial during
# the sitting, by the kind --record-trial recorded, and that the gate's port
# is free. It sets GUARD, the line --swap-in adds to the evidence.
#   A transient unit: inactive, and gone (LoadState not-found; masked is
#   taken too): only trial-restart.sh, run by hand, makes it again.
#   A unit file: inactive and masked; or, when the runtime mask did not take
#   (a unit file of higher priority shadows the runtime folder) and has been
#   taken back, loaded with no activation source: TriggeredBy, WantedBy,
#   RequiredBy, BoundBy and UpheldBy empty and UnitFileState not enabled,
#   linked or alias (a start by hand would still fail closed: the trial
#   refuses the proof's settings and the state folder's lock a second
#   daemon). Anything else is refused with its reason.
trial_guard() {
  kind_read
  case $K_ST in
    none) die "this ROOT has no record of the trial's unit: run --record-trial while the trial runs (step f, before step 1)" ;;
    bad) die "$KINDREC is a link, not a regular file, unreadable or not a record of --record-trial: look at it" ;;
  esac
  [ "$K_UNIT" = "$UNIT" ] || die "this ROOT recorded the trial's unit $K_UNIT, not $UNIT: give the unit of the record"
  _a=$(unit_prop "$UNIT" ActiveState)
  [ "$_a" = inactive ] || die "the trial's unit is $_a, not inactive: stop it first (step 1): systemctl --user stop -- \"\$TRIAL_UNIT\""
  _l=$(unit_prop "$UNIT" LoadState)
  if [ "$K_KIND" = transient ]; then
    case $_l in
      not-found | masked) GUARD="kind transient, ActiveState=$_a, LoadState=$_l: gone; only trial-restart.sh makes it again: accepted" ;;
      *) die "the trial's transient unit is $_l, not gone (not-found), so it could be started again meanwhile: a failed one stays loaded until it is reset (OWNER ONLY: systemctl --user reset-failed -- \"\$TRIAL_UNIT\"), then --swap-in again" ;;
    esac
  else
    case $_l in
      masked) GUARD="kind file, ActiveState=$_a, LoadState=masked: the runtime mask took: accepted" ;;
      loaded)
        _rt=${XDG_RUNTIME_DIR:-}
        case $_rt in
          /*) ;;
          *) die "the trial's unit is not masked and XDG_RUNTIME_DIR is not set, so the runtime mask of step 1 cannot be looked at" ;;
        esac
        if ! gone "$_rt/systemd/user/$UNIT"; then
          die "the trial's unit is not masked though its runtime mask is there: a unit file of higher priority shadows it; take the mask back (OWNER ONLY: systemctl --user unmask --runtime -- \"\$TRIAL_UNIT\"), then --swap-in again: it checks that nothing can start the unit"
        fi
        _src=''
        for _p in TriggeredBy WantedBy RequiredBy BoundBy UpheldBy; do
          _v=$(unit_prop "$UNIT" "$_p")
          [ -z "$_v" ] || _src="$_src $_p=$_v"
        done
        _ufs=$(unit_prop "$UNIT" UnitFileState)
        case $_ufs in
          enabled | enabled-runtime | linked | linked-runtime | alias) _src="$_src UnitFileState=$_ufs" ;;
        esac
        [ -z "$_src" ] || die "the trial's unit is not masked (a unit file of higher priority shadows the runtime mask) and can be started:$_src; stop the sitting here and start the trial again (step 15)"
        GUARD="kind file, ActiveState=$_a, LoadState=loaded: the runtime mask did not take (a unit file of higher priority shadows it) and was taken back; fallback: no activation source (TriggeredBy, WantedBy, RequiredBy, BoundBy and UpheldBy empty, UnitFileState=$_ufs): accepted"
        ;;
      *) die "the trial's unit is $_l, neither masked nor loaded: look at it before the sitting with systemctl --user status" ;;
    esac
  fi
  port_free
}

# The swap record in ROOT: one "key value" per line.
rec_get() { awk -v k="$1" '$1 == k { print $2; exit }' "$RECORD"; }

# kv_get FILE KEY prints the rest of the first line of FILE that starts with
# KEY and a space.
kv_get() { awk -v k="$2" '$1 == k { sub(/^[^ ]+ /, ""); print; exit }' "$1"; }

# kind_read reads ROOT/trial-unit, which --record-trial writes, and sets K_ST
# (none, rec or bad), and for a record K_KIND (transient or file), K_UNIT,
# K_INV (the invocation recorded) and K_FRAG (the unit file).
kind_read() {
  K_ST=none K_KIND='' K_UNIT='' K_INV='' K_FRAG=''
  fstate "$KINDREC"
  case $F_K in
    absent) return 0 ;;
    file) ;;
    *) K_ST=bad; return 0 ;;
  esac
  if [ "$(sed -n 1p -- "$KINDREC")" != 'trial-unit 1' ]; then K_ST=bad; return 0; fi
  K_KIND=$(kv_get "$KINDREC" kind) K_UNIT=$(kv_get "$KINDREC" unit) K_INV=$(kv_get "$KINDREC" invocation) K_FRAG=$(kv_get "$KINDREC" fragment)
  case $K_KIND in
    transient | file) K_ST=rec ;;
    *) K_ST=bad ;;
  esac
}

# kind_print prints the line of --status about the trial's unit.
kind_print() {
  case $K_ST in
    none) say "  $KINDREC: none (record the trial's unit while it runs: --record-trial, step f)" ;;
    bad) say "  $KINDREC: a link, not a regular file, unreadable or not a record of --record-trial" ;;
    *)
      if [ "$K_KIND" = transient ]; then
        say "  $KINDREC: unit $K_UNIT, transient (made by systemd-run; step 15 re-creates it with $RESTART)"
      else
        say "  $KINDREC: unit $K_UNIT, a unit file ($K_FRAG)"
      fi
      ;;
  esac
}

# classify reads the three names and the record and sets ST (trial, in,
# ambiguous), SUB, NEXT, and the facts status_print prints.
classify() {
  kind_read
  ST=ambiguous SUB='' SUBK='' NEXT=''
  HAVE_REC=
  R_STATE='' R_ID='' R_SHA='' R_PSHA='' R_ARCH=''
  fstate "$RECORD"
  case $F_K in
    file)
      HAVE_REC=1
      R_STATE=$(rec_get state) R_ID=$(rec_get trial_id) R_SHA=$(rec_get trial_sha) R_PSHA=$(rec_get proof_sha) R_ARCH=$(rec_get archive)
      ;;
    absent) ;;
    *) NEXT="ROOT/swap-record is a link, not a regular file or unreadable: look at $RECORD; nothing is moved"; return 0 ;;
  esac
  fstate "$SETTINGS"
  P_K=$F_K P_SHA=$F_SHA P_MODE=$F_MODE
  PSHA=$R_PSHA
  if [ -z "$HAVE_REC" ] && [ "$P_K" = file ]; then PSHA=$P_SHA; fi
  fstate "$CANON"
  C_K=$F_K C_ID=$F_ID C_SHA=$F_SHA C_MODE=$F_MODE
  if [ "$C_K" = file ]; then
    if private && trial_kind "$CANON"; then
      C_K=trial
    elif [ -n "$PSHA" ] && [ "$C_SHA" = "$PSHA" ]; then
      C_K=proof
    else
      C_K=other
    fi
  fi
  fstate "$ASIDE"
  T_K=$F_K T_ID=$F_ID T_SHA=$F_SHA T_MODE=$F_MODE
  if [ "$T_K" = file ] && [ -n "$HAVE_REC" ] && [ "$T_ID" = "$R_ID" ] && [ "$T_SHA" = "$R_SHA" ]; then T_K=recorded; fi
  A_K=none A_SHA='' A_ID=''
  if [ -n "$HAVE_REC" ]; then
    fstate "$OWNER/$R_ARCH"
    A_K=$F_K A_SHA=$F_SHA A_ID=$F_ID
    if [ "$A_K" = file ] && [ "$A_SHA" = "$R_PSHA" ]; then A_K=proof; fi
  fi
  if [ -n "$HAVE_REC" ] && [ "$R_STATE" != "$S" ]; then
    NEXT="this ROOT's swap record is of another state folder ($R_STATE): run --status with the root of the sitting that swapped this state folder"
    return 0
  fi
  _mask='keep the trial stopped (a unit file masked as in step 1) and both daemons stopped'
  if [ "$C_K" = trial ] && [ "$T_K" = absent ]; then
    if [ -z "$HAVE_REC" ]; then
      ST=trial SUBK=fresh SUB='not swapped'
      case $K_ST:$K_KIND in
        rec:transient) NEXT='stop the trial'"'"'s transient unit (step 1; it is not masked: stopped, it is gone), scratch-init (step 2), then --swap-in (step 3)' ;;
        rec:file) NEXT='stop the trial'"'"'s unit and mask it for this boot (step 1), scratch-init (step 2), then --swap-in (step 3)' ;;
        bad:*) NEXT="look at $KINDREC, which is not a record of --record-trial; then stop the trial (step 1), scratch-init (step 2) and --swap-in (step 3)" ;;
        *) NEXT='record the trial'"'"'s unit while it runs (--record-trial, step f), then stop it (step 1), scratch-init (step 2) and --swap-in (step 3)' ;;
      esac
      return 0
    fi
    if [ "$C_ID" = "$R_ID" ] && [ "$C_SHA" = "$R_SHA" ]; then
      case $A_K in
        proof)
          ST=trial SUBK=back SUB='the way back is done'
          case $K_ST:$K_KIND in
            rec:transient) NEXT="re-create the trial's transient unit: OWNER ONLY: sh $RESTART (step 15), then check its new invocation and fingerprint" ;;
            rec:file) NEXT='unmask the trial'"'"'s unit if it is masked and start it again (step 15), then check its new invocation and fingerprint' ;;
            *) NEXT='start the trial again as you made it (step 15)' ;;
          esac
          return 0
          ;;
        absent)
          ST=trial SUBK=recorded SUB='recorded by --swap-in, which stopped before its first rename'
          NEXT='run --swap-in again (step 3): it goes on from the record'
          return 0
          ;;
      esac
      NEXT="owner/$R_ARCH is there and is not this sitting's proof settings: $_mask, look at it; prepare moves nothing in this layout"
      return 0
    fi
    NEXT="owner/scratch.json is trial settings, but not the ones this ROOT recorded: $_mask; run --status with the root of the sitting that recorded them"
    return 0
  fi
  if [ "$C_K" = proof ] && [ "$T_K" = recorded ] && [ "$A_K" = absent ]; then
    ST=in SUB=
    NEXT='the sitting (steps 4 to 13); once the proof'"'"'s daemon has stopped, --swap-out (step 14)'
    return 0
  fi
  if [ "$C_K" = absent ] && [ "$T_K" = recorded ]; then
    SUBK=missing SUB='owner/scratch.json is missing; the recorded trial settings are set aside'
    NEXT='--swap-out (step 14): it puts the recorded trial settings back'
    return 0
  fi
  if [ "$C_K" = trial ] && [ "$T_K" = recorded ] && [ "$C_ID" = "$T_ID" ]; then
    SUBK=half-trial SUB='a rename stopped half way: the recorded trial settings have both names'
    NEXT='--swap-out (step 14): it removes the second name, owner/scratch.json.trial, of that one file'
    return 0
  fi
  if [ "$C_K" = proof ] && [ "$T_K" = recorded ] && [ "$A_K" = proof ] && [ "$C_ID" = "$A_ID" ]; then
    SUBK=half-proof SUB="a rename stopped half way: the proof's settings have both names, owner/scratch.json and owner/$R_ARCH"
    NEXT='--swap-out (step 14): it goes on from there'
    return 0
  fi
  if [ -z "$HAVE_REC" ] && [ "$T_K" != absent ]; then
    NEXT="owner/scratch.json.trial was set aside by another sitting (this ROOT has no swap record): $_mask; run --status with that sitting's root"
    return 0
  fi
  case $C_K in
    link | notfile | unreadable)
      NEXT="owner/scratch.json is a link, not a regular file or unreadable: $_mask; look at it (ls -l $OWNER); prepare moves nothing in this layout"
      return 0
      ;;
    other)
      NEXT="owner/scratch.json is neither private trial settings nor this proof's: $_mask; compare its digest above; prepare moves nothing in this layout"
      return 0
      ;;
  esac
  if [ "$T_K" != recorded ]; then
    NEXT="the trial's settings are not where this ROOT's record says (owner/scratch.json.trial): $_mask; do not start the trial; find them (the other names in owner/ are listed above)"
    return 0
  fi
  NEXT="$_mask; look at the names above; prepare moves nothing in this layout"
}

# fact NAME KIND ID SHA MODE prints one line of the status.
fact() {
  case $2 in
    absent) say "  $1: absent" ;;
    link) say "  $1: a link (a dangling one counts as taken)" ;;
    notfile) say "  $1: not a regular file" ;;
    unreadable) say "  $1: unreadable" ;;
    *) say "  $1: $2, mode $5, id $3, sha256 $4" ;;
  esac
}

status_print() {
  case $ST in
    trial) _w='trial in place' ;;
    in) _w='swapped in' ;;
    *) _w='interrupted or ambiguous' ;;
  esac
  if [ -n "$SUB" ]; then say "status: $_w ($SUB)"; else say "status: $_w"; fi
  _ck=$C_K
  [ "$_ck" != trial ] || _ck='trial settings'
  [ "$_ck" != proof ] || _ck="this proof's settings"
  [ "$_ck" != other ] || _ck='a file of other settings'
  [ "$_ck" != file ] || _ck='a file'
  fact "$CANON" "$_ck" "$C_ID" "$C_SHA" "$C_MODE"
  _tk=$T_K
  [ "$_tk" != recorded ] || _tk='the recorded trial settings'
  [ "$_tk" != file ] || _tk='a file this ROOT did not record'
  fact "$ASIDE" "$_tk" "$T_ID" "$T_SHA" "$T_MODE"
  _pk=$P_K
  [ "$_pk" != file ] || _pk="the proof's settings"
  fact "$SETTINGS" "$_pk" '-' "$P_SHA" "$P_MODE"
  if [ -n "$HAVE_REC" ]; then
    say "  $RECORD: trial id $R_ID, trial sha256 $R_SHA, proof sha256 $R_PSHA, archive owner/$R_ARCH ($A_K)"
  else
    say "  $RECORD: none"
  fi
  kind_print
  _others=$(find "$OWNER" -mindepth 1 -maxdepth 1 \( -name 'scratch.json*' -o -name '.scratch.json*' \) -print 2>/dev/null | LC_ALL=C sort) || _others=
  for _o in $_others; do
    case $_o in
      "$CANON" | "$ASIDE") ;;
      *) if [ -n "$HAVE_REC" ] && [ "$_o" = "$OWNER/$R_ARCH" ]; then :; else say "  other name in owner/: $_o"; fi ;;
    esac
  done
  say "next: $NEXT"
}

# no_replace FROM TO renames FROM to TO within one folder, refusing a TO
# that is there (a dangling link and a folder included): the new name is made
# first, by link(2), which never replaces a name, and checked to be the same
# file; then the old name is removed by unlink(2). The file itself is never
# removed. Stopped between the two, the file has both names, which --status
# names and --swap-out resolves.
no_replace() {
  ln -P -T -- "$1" "$2" || die "the new name $2 was refused (it is taken?); nothing else was moved: run --status"
  drop_name "$1" "$2"
}
# drop_name OLD KEPT removes the name OLD of a file that KEPT names too,
# after checking that the two are one file.
drop_name() {
  _i1=$(stat -c '%d:%i' -- "$1") || die "cannot read $1"
  _i2=$(stat -c '%d:%i' -- "$2") || die "cannot read $2"
  [ "$_i1" = "$_i2" ] || die "$1 and $2 are not one file: nothing was removed: run --status"
  [ ! -L "$1" ] && [ -f "$1" ] || die "$1 is not a regular file: nothing was removed"
  unlink -- "$1" || die "the old name $1 could not be removed: run --status"
  gone "$1" || die "$1 is still there after the unlink"
}
# gone PATH succeeds when no name is there, not even a dangling link.
gone() { [ ! -e "$1" ] && [ ! -L "$1" ]; }

# check_settings_file refuses ROOT/owner-scratch.json when it is not the
# proof's private, regular, non-trial settings.
check_proof_settings() {
  fstate "$SETTINGS"
  case $F_K in
    file) ;;
    absent) die "$SETTINGS is missing: run --write first" ;;
    *) die "$SETTINGS is a link, not a regular file or unreadable" ;;
  esac
  private || die "$SETTINGS is not private (mode $F_MODE)"
  if trial_kind "$SETTINGS"; then die "$SETTINGS holds trial settings"; fi
  grep -Fq -e '"door": "stand-in"' -- "$SETTINGS" || die "$SETTINGS is not the stand-in door's settings prepare writes"
}

if [ "$MODE" = status ]; then
  check_state
  [ -d "$ROOT" ] && [ ! -L "$ROOT" ] || die "the root is not a folder: $ROOT"
  classify
  status_print
  exit 0
fi

# cmdline_read FILE reads a command line as /proc/PID/cmdline holds it (each
# argument ends with a NUL) and sets ARGV0, the first argument, ARGC, and
# QARGV, every argument single-quoted, separated by spaces. It fails, with
# CL_WHY, for a file it cannot read, an empty one, one whose last byte is not
# a NUL (a process that changed its command line) and one with a newline in
# an argument, which the restart script would not keep exactly.
cmdline_read() {
  CL_WHY=''
  _oct=$(od -An -v -to1 -- "$1" 2>/dev/null) || { CL_WHY='cannot be read'; return 1; }
  ARGV0='' QARGV='' ARGC=0 _esc='' _b=''
  for _b in $_oct; do
    case $_b in
      000)
        # shellcheck disable=SC2059  # the format holds only octal escapes, one per byte
        _a=$(printf "$_esc")
        if [ "$ARGC" -eq 0 ]; then
          ARGV0=$_a QARGV=$(sq "$_a")
        else
          QARGV="$QARGV $(sq "$_a")"
        fi
        ARGC=$((ARGC + 1)) _esc=''
        ;;
      012) CL_WHY='holds a newline in an argument, which a restart script would not keep exactly'; return 1 ;;
      [0-3][0-7][0-7]) _esc="$_esc\\$_b" ;;
      *) CL_WHY='cannot be read as bytes'; return 1 ;;
    esac
  done
  [ -n "$_b" ] || { CL_WHY='is empty'; return 1; }
  [ "$_b" = 000 ] || { CL_WHY='does not end with a NUL (the process changed its command line)'; return 1; }
}

# --record-trial: the trial's unit as it runs, before step 1. It reads the
# user manager (show only) and the main process's command line, checks all
# of it, and only then writes, in ROOT, the evidence, the restart script of a
# transient unit and, last, the kind record the later modes read. It never
# runs the script it writes.
if [ "$MODE" = record-trial ]; then
  PROC=${PREPARE_PROC:-/proc}
  safe_path PREPARE_PROC "$PROC"
  [ -d "$ROOT" ] && [ ! -L "$ROOT" ] || die "the root is not a folder made by --write: $ROOT"
  [ -d "$EVIDENCE" ] && [ ! -L "$EVIDENCE" ] || die "the root has no evidence/ folder: run --write first: $EVIDENCE"
  gone "$RECORD" || die "this ROOT has swapped already ($RECORD): the trial's unit is recorded before step 1, while the trial runs; nothing was written"
  for _f in "$KINDREC" "$BEFORE" "$RESTART" "$KINDREC.new" "$BEFORE.new" "$RESTART.new"; do
    gone "$_f" || die "$_f is there already: the trial's unit is recorded once for a root, and the record is kept; nothing was written"
  done
  _nl='
'
  _txt=''
  for _p in Id Description Transient UnitFileState FragmentPath CollectMode LoadState ActiveState SubState \
    TriggeredBy WantedBy RequiredBy BoundBy UpheldBy InvocationID MainPID ExecStart Restart Environment WorkingDirectory; do
    _v=$(unit_prop "$UNIT" "$_p")
    case $_v in *"$_nl"*) die "the trial's unit's $_p has more than one line; nothing was written" ;; esac
    eval "U_$_p=\$_v"
    _txt="$_txt$_p=$_v$_nl"
  done
  [ "$U_Id" = "$UNIT" ] || die "the user manager names the unit $U_Id, not $UNIT (an alias?): give its own name; nothing was written"
  [ "$U_LoadState" = loaded ] || die "the trial's unit is $U_LoadState, not loaded: record it while the trial runs, before step 1; nothing was written"
  [ "$U_ActiveState" = active ] || die "the trial's unit is $U_ActiveState, not active: record it while the trial runs, before step 1; nothing was written"
  case $U_InvocationID in
    '' | *[!0-9a-f]*) die "the trial's unit has no invocation id ($U_InvocationID); nothing was written" ;;
  esac
  case $U_Transient in
    yes)
      KIND=transient
      [ "$U_UnitFileState" = transient ] || die "the trial's unit is transient but its UnitFileState is $U_UnitFileState; nothing was written"
      case $U_FragmentPath in
        /*/systemd/transient/*) ;;
        *) die "the trial's transient unit is not in a transient folder of the user manager ($U_FragmentPath); nothing was written" ;;
      esac
      ;;
    no)
      KIND=file
      case $U_FragmentPath in
        /*) ;;
        *) die "the trial's unit is not transient and has no unit file (FragmentPath is empty); nothing was written" ;;
      esac
      [ "$U_UnitFileState" != transient ] || die "the trial's unit says Transient=no and UnitFileState=transient; nothing was written"
      ;;
    *) die "the trial's unit says Transient=$U_Transient, neither yes nor no; nothing was written" ;;
  esac
  if [ "$KIND" = transient ]; then
    case $U_MainPID in
      '' | *[!0-9]* | 0*) die "the trial's unit has no main process (MainPID=$U_MainPID), so its command line cannot be read for the restart script; nothing was written" ;;
    esac
    case $U_CollectMode in
      inactive-or-failed) _collect=' --collect' ;;
      inactive) _collect='' ;;
      *) die "the trial's transient unit has CollectMode=$U_CollectMode, which the restart script cannot give; nothing was written" ;;
    esac
    case $U_ExecStart in
      '{ path='*' ; argv[]='*) ;;
      *) die "the trial's ExecStart is not one command with a path; nothing was written" ;;
    esac
    _es=${U_ExecStart#'{ path='}
    case $_es in
      *' ; argv[]='*' ; argv[]='*) die "the trial's ExecStart holds more than one command; nothing was written" ;;
    esac
    _prog=${_es%%' ; argv[]='*}
    _cl=$PROC/$U_MainPID/cmdline
    cmdline_read "$_cl" || die "the command line of the trial's main process ($_cl) $CL_WHY: no restart script can be written; nothing was written"
    [ "$ARGV0" = "$_prog" ] || die "the trial's main process ($U_MainPID) runs $ARGV0, not ExecStart's program $_prog: the process is not the unit's command; nothing was written"
    _desc=''
    [ -z "$U_Description" ] || _desc=" --description $(sq "$U_Description")"
  fi

  half_rec() {
    _rc=$?
    if [ "$_rc" -ne 0 ]; then printf '%s\n' "$NAME: --record-trial stopped part way: look at $ROOT (it keeps what was written)" >&2; fi
    exit "$_rc"
  }
  trap half_rec EXIT
  printf '%s' "$_txt" >"$BEFORE.new"
  no_replace "$BEFORE.new" "$BEFORE"
  _krec="trial-unit 1${_nl}unit $UNIT${_nl}kind $KIND${_nl}invocation $U_InvocationID${_nl}"
  if [ "$KIND" = transient ]; then
    printf '%s\n' '#!/bin/sh' \
      '# Written by prepare.sh --record-trial from the phone trial'"'"'s transient unit as' \
      '# it ran: its name, description and collect mode, as the user manager shows them, and the' \
      "# command line of its main process ($_cl), every argument single-quoted." \
      '# prepare never runs it. Run it by hand at step 15, after --swap-out' \
      '# (docs/scratch-seat-proof.md). It re-creates the unit with those and' \
      '# nothing else of it.' \
      'set -eu' \
      "name=$(sq "$UNIT")" \
      "exec systemd-run --user --unit \"\$name\"$_desc$_collect -- $QARGV" >"$RESTART.new"
    chmod 0700 -- "$RESTART.new"
    sh -n "$RESTART.new" || die "the restart script written does not parse: $RESTART.new"
    no_replace "$RESTART.new" "$RESTART"
    _krec="${_krec}restart $RESTART${_nl}"
  else
    _krec="${_krec}fragment $U_FragmentPath${_nl}"
  fi
  printf '%s' "$_krec" >"$KINDREC.new"
  no_replace "$KINDREC.new" "$KINDREC"
  trap - EXIT
  if [ "$KIND" = transient ]; then
    say "recorded the trial's unit: transient (made by systemd-run), its program $ARGV0 and $ARGC arguments in all"
    say "  $BEFORE: the unit as it runs"
    say "  $RESTART (0700): re-creates it at step 15, after --swap-out; prepare never runs it"
    say "  $KINDREC: its kind, for --swap-in, --status and step 15"
    say "next: step 1 stops it (a transient unit is not masked: stopped, it is gone)"
  else
    say "recorded the trial's unit: a unit file, $U_FragmentPath"
    say "  $BEFORE: the unit as it runs"
    say "  $KINDREC: its kind, for --swap-in, --status and step 15"
    say "next: step 1 stops it and masks it for this boot (--runtime)"
  fi
  exit 0
fi

if [ "$MODE" = swap-in ] || [ "$MODE" = swap-out ]; then
  check_state
  [ -d "$ROOT" ] && [ ! -L "$ROOT" ] || die "the root is not a folder made by --write: $ROOT"
  half() {
    _rc=$?
    if [ "$_rc" -ne 0 ]; then printf '%s\n' "$NAME: --$MODE stopped; nothing more was moved: run sh $PREP --status --state $S --root $ROOT" >&2; fi
    exit "$_rc"
  }
  trap half EXIT
fi

if [ "$MODE" = swap-in ]; then
  check_proof_settings
  grep -Fq -e "\"key\": \"$KEY_STANDIN\"" -- "$CONFIG" 2>/dev/null || die "$CONFIG has no lead $KEY_STANDIN: give the --sitting of --write"
  classify
  case $ST:$SUBK in
    trial:fresh | trial:recorded) ;;
    *)
      status_print >&2
      die 'the layout is not the trial'"'"'s alone, not swapped: nothing was moved'
      ;;
  esac
  if [ -z "$HAVE_REC" ]; then
    gone "$OWNER/$ARCHIVE_NAME" || die "owner/$ARCHIVE_NAME is taken, the name --swap-out uses: give another --sitting"
  elif [ "$R_ARCH" != "$ARCHIVE_NAME" ]; then
    die "this ROOT's record is of another sitting (archive owner/$R_ARCH): give its --sitting"
  fi
  trial_guard
  keys_fresh
  for _k in "$KEY_STANDIN" "$KEY_LIVE"; do
    _l=$(unit_prop "ataniq-lead-scratch-$_k.service" LoadState)
    [ "$_l" = not-found ] || die "the user manager knows the unit ataniq-lead-scratch-$_k.service ($_l): another scratch root uses the key $_k; give another --sitting"
  done
  # What the guard found, kept as evidence (a fallback of a unit file
  # included); one line for each --swap-in that gets this far.
  GUARDF=$EVIDENCE/trial-unit-guard.txt
  if [ -L "$GUARDF" ] || { [ -e "$GUARDF" ] && [ ! -f "$GUARDF" ]; }; then die "$GUARDF is a link or not a regular file: nothing was moved"; fi
  printf '%s\n' "swap-in, sitting $SITTING, unit $UNIT: $GUARD" >>"$GUARDF"
  if [ -z "$HAVE_REC" ]; then
    _new=$RECORD.new
    gone "$_new" || die "$_new is there: look at it; nothing was moved"
    printf '%s\n' 'swap-record 1' "state $S" "sitting $SITTING" "trial_id $C_ID" "trial_sha $C_SHA" "proof_sha $P_SHA" \
      "archive $ARCHIVE_NAME" >"$_new"
    no_replace "$_new" "$RECORD"
    R_ID=$C_ID R_SHA=$C_SHA R_PSHA=$P_SHA
  fi
  [ "$P_SHA" = "$R_PSHA" ] || die "$SETTINGS changed since the record was made: nothing was moved"
  # 1. The trial's settings aside, within owner/.
  no_replace "$CANON" "$ASIDE"
  fstate "$ASIDE"
  [ "$F_K" = file ] && [ "$F_ID" = "$R_ID" ] && [ "$F_SHA" = "$R_SHA" ] || die "owner/scratch.json.trial is not the recorded trial settings after the rename"
  # 2. A copy of the proof's settings, written under a dot name in owner/,
  # checked, then renamed to owner/scratch.json.
  _stg=$(mktemp -- "$OWNER/.scratch.json.proof.XXXXXX") || die "cannot make a file in $OWNER"
  cat -- "$SETTINGS" >"$_stg"
  chmod 0600 -- "$_stg"
  fstate "$_stg"
  [ "$F_K" = file ] && [ "$F_SHA" = "$R_PSHA" ] || die "the copy $_stg is not the proof's settings"
  no_replace "$_stg" "$CANON"
  fstate "$CANON"
  [ "$F_K" = file ] && private && [ "$F_SHA" = "$R_PSHA" ] || die "owner/scratch.json is not the proof's settings after the rename"
  trap - EXIT
  say "swapped in: owner/scratch.json is this proof's settings, and the trial's are owner/scratch.json.trial, as recorded in $RECORD"
  classify
  status_print
  [ "$ST" = in ] || die 'the layout after the swap is not swapped in: run --status'
  exit 0
fi

if [ "$MODE" = swap-out ]; then
  classify
  [ -n "$HAVE_REC" ] || { status_print >&2; die "this ROOT has no swap record: run --swap-out with the root of the sitting that swapped in"; }
  if [ "$ST:$SUBK" = trial:back ]; then
    trap - EXIT
    say "nothing to do: the trial's settings are back"
    status_print
    exit 0
  fi
  case $ST:$SUBK in
    in: | ambiguous:missing | ambiguous:half-trial | ambiguous:half-proof) unit_quiet ;;
    *)
      status_print >&2
      die 'the layout is not one --swap-out puts back: nothing was moved'
      ;;
  esac
  case $ST:$SUBK in
    in:) no_replace "$CANON" "$OWNER/$R_ARCH" ;;
    ambiguous:half-proof) drop_name "$CANON" "$OWNER/$R_ARCH" ;;
  esac
  if [ "$SUBK" = half-trial ]; then
    drop_name "$ASIDE" "$CANON"
  else
    gone "$CANON" || die "owner/scratch.json is still there"
    if [ "$ST" = in ] || [ "$SUBK" = half-proof ]; then
      fstate "$OWNER/$R_ARCH"
      [ "$F_K" = file ] && [ "$F_SHA" = "$R_PSHA" ] || die "owner/$R_ARCH is not the proof's settings after the rename"
    fi
    no_replace "$ASIDE" "$CANON"
  fi
  fstate "$CANON"
  [ "$F_K" = file ] && [ "$F_ID" = "$R_ID" ] && [ "$F_SHA" = "$R_SHA" ] || die "owner/scratch.json is not the recorded trial settings after the rename"
  trap - EXIT
  say "swapped out: owner/scratch.json is the recorded trial settings again (same file, same digest)"
  classify
  status_print
  exit 0
fi

# ---- --write. Everything is checked before ROOT is made. ----
check_state
fstate "$CANON"
case $F_K in
  file) ;;
  absent) die "$CANON is missing: the trial's settings may be set aside: run --status with the root of the sitting that swapped" ;;
  *) die "$CANON is a link, not a regular file or unreadable: run --status" ;;
esac
private || die "$CANON is not private (mode $F_MODE)"
trial_kind "$CANON" || die "$CANON is not the trial's settings (no \"trial\": true): run --status"
gone "$ASIDE" || die "$ASIDE is there already: the trial's settings may be set aside from an earlier proof: run --status with that sitting's root"
check_gate_material
keys_fresh

_hp=$(phys "$HOME") || die "HOME is not a folder, so ~/.ssh, ~/.claude and ~/.codex cannot be listed as never mounted: $HOME"
[ "$_hp" = "$HOME" ] || die "HOME is not at its physical path (a folder on the way is a link), so the never-mounted paths would miss: $HOME is $_hp"

# The tools.
_missing=
for _b in $BINARIES; do
  if [ ! -f "$BIN/$_b" ] || [ -L "$BIN/$_b" ] || [ ! -x "$BIN/$_b" ]; then _missing="$_missing $_b"; fi
done
[ -z "$_missing" ] || die "not built in $BIN:$_missing (docs/scratch-seat-proof.md, Before the sitting, says how to build them)"
if [ ! -f "$DAEMON" ] || [ ! -x "$DAEMON" ]; then
  die "no daemon of both tags at $DAEMON (docs/scratch-seat-proof.md, Before the sitting, says how to build it)"
fi
if [ -n "$LIVE" ]; then
  if [ -L "$CLAUDE_SRC" ]; then die "--claude is a link (the version manager's shim or a link to a version): give the file itself: $CLAUDE_SRC"; fi
  if [ ! -f "$CLAUDE_SRC" ] || [ ! -x "$CLAUDE_SRC" ]; then die "--claude is not an executable regular file: $CLAUDE_SRC"; fi
fi
PASTA_COPY=
if [ -n "$PASTA" ]; then
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
if [ -z "$BWRAP" ]; then
  BWRAP=$(command -v bwrap 2>/dev/null) || die 'no bwrap on PATH; give --bwrap FILE'
  safe_path 'bubblewrap' "$BWRAP"
fi
if [ ! -f "$BWRAP" ] || [ ! -x "$BWRAP" ]; then die "bubblewrap is not an executable regular file: $BWRAP"; fi
[ -f /usr/bin/git ] && [ -x /usr/bin/git ] || die 'no /usr/bin/git, which makes the throwaway repository'
[ -f "$_d/netcheck.sh.in" ] || die "the netcheck template is missing: $_d/netcheck.sh.in"
for _x in json txt; do
  for _s in idle-before-paste collapsed-paste-footer-c; do
    [ -f "$SCREENS/$_s.$_x" ] || die "the recorded screens are missing in the checkout: $SCREENS"
  done
done

# The root: its parent exists and is at its physical path; the root is new,
# or an empty folder that can be read; physically it lies outside the state
# folder, the checkout, /tmp, /var/tmp and $TMPDIR, and none of them in it.
_parent=${ROOT%/*}
[ -n "$_parent" ] || _parent=/
if [ -L "$ROOT" ]; then die "the root is a link: $ROOT"; fi
_pp=$(phys "$_parent") || die "the root's parent folder does not exist: $_parent (prepare makes only the root itself)"
[ "$_pp" = "$_parent" ] || die "the root's parent is not at its physical path (a folder on the way is a link): give ${_pp%/}/${ROOT##*/}"
for _t in "$S" "$REPO" /tmp /var/tmp ${TMPDIR:+"$TMPDIR"}; do
  _tp=$(phys "$_t") || continue
  if inside "$ROOT" "$_tp" || inside "$_tp" "$ROOT"; then die "the root lies physically in or around $_t ($_tp): choose another folder"; fi
done
if [ -e "$ROOT" ]; then
  [ -d "$ROOT" ] || die "the root is not a folder: $ROOT"
  [ -r "$ROOT" ] && [ -x "$ROOT" ] || die "the root cannot be read: $ROOT"
  _list=$(find "$ROOT" -mindepth 1 -maxdepth 1 -print) || die "the root cannot be listed: $ROOT"
  [ -z "$_list" ] || die "the root is not empty: $ROOT (prepare writes a new set-up; choose another folder)"
fi

# --write. From here a failure leaves a half-made root, which is never
# deleted: the last line says so.
half_made() {
  _rc=$?
  if [ "$_rc" -ne 0 ] && [ -n "${ROOT_MADE:-}" ]; then
    printf '%s\n' "$NAME: failed; $ROOT is half made: look at it, then prepare again with another --root" >&2
  fi
  exit "$_rc"
}
if [ ! -d "$ROOT" ]; then mkdir -m 0700 -- "$ROOT"; fi
chmod 0700 -- "$ROOT"
ROOT_MADE=1
trap half_made EXIT
say "making $ROOT"
mkdir -m 0700 -- "$ROOT/conf" "$ROOT/agent-install" "$ROOT/bin" "$ROOT/upstream" "$EVIDENCE" "$OC" "$OC/drop"

# The managed settings name the guard hook, bound at /ataniq/qargi/guard-hook,
# and hold the marker text guard-hook, as the canary run kit's do. The hook
# exits 0: it guards nothing; the holder refuses a claude start without the
# marker, which is all it is for.
printf '%s\n' '{"permissions":{"defaultMode":"default"},"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/ataniq/qargi/guard-hook"}]}]}}' >"$ROOT/conf/managed-settings.json"
printf '%s\n' '#!/bin/sh' 'exit 0' >"$ROOT/conf/guard-hook"
chmod 0600 -- "$ROOT/conf/managed-settings.json"
chmod 0700 -- "$ROOT/conf/guard-hook"
mkdir -m 0700 -- "$ROOT/conf/replay-screens"
for _x in json txt; do
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

# The throwaway repository both leads' mirrors are made from. Every git call
# has the same clean environment: no variable of the caller's (GIT_DIR,
# GIT_INDEX_FILE, GIT_CONFIG_PARAMETERS ...), a fixed PATH and executable,
# HOME in ROOT, no system or global config, no hooks, no signing, a fixed
# author.
proof_git() {
  /usr/bin/env -i PATH=/usr/bin:/bin HOME="$ROOT" LC_ALL=C GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
    GIT_TERMINAL_PROMPT=0 GIT_AUTHOR_NAME='Sample Author' GIT_AUTHOR_EMAIL=author@example.invalid \
    GIT_COMMITTER_NAME='Sample Author' GIT_COMMITTER_EMAIL=author@example.invalid \
    /usr/bin/git -c core.hooksPath=/dev/null -c commit.gpgSign=false -c tag.gpgSign=false -c core.fsmonitor=false \
    -c user.name='Sample Author' -c user.email=author@example.invalid "$@"
}
proof_git init -q --template= "$ROOT/upstream"
proof_git -C "$ROOT/upstream" symbolic-ref HEAD refs/heads/develop
printf '%s\n' 'A throwaway repository for the attended proof of real replies.' >"$ROOT/upstream/README.txt"
proof_git -C "$ROOT/upstream" add README.txt
proof_git -C "$ROOT/upstream" commit -q -m 'Initial throwaway commit'

# netcheck.sh: the owner's hand-run check of the lead's network. Prepare
# writes it and never runs it. Its first lines carry the real values; the body
# is the template netcheck.sh.in beside this file.
NETCHECK=$ROOT/netcheck.sh
{
  printf '%s\n' '#!/bin/sh' '# Written by prepare.sh --write. Run it by hand, with the trial running, before the sitting (docs/scratch-seat-proof.md).' \
    "SELF='$NETCHECK'" "PASTA='$PASTA_USE'" "BWRAP='$BWRAP'" "GATE_HOST='${GATE%:*}'" "GATE_PORT='${GATE##*:}'"
  cat -- "$_d/netcheck.sh.in"
} >"$NETCHECK"
chmod 0700 -- "$NETCHECK"
sh -n "$NETCHECK" || die "the netcheck script written does not parse: $NETCHECK"

config_json "$MODEL" >"$CONFIG"
chmod 0600 -- "$CONFIG"
settings_json >"$SETTINGS"
chmod 0600 -- "$SETTINGS"

say "wrote $CONFIG and $SETTINGS; nothing was started, and nothing in $S was changed"
say ""
commands "$GATE" "$LIVE"
