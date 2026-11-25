#!/bin/sh
# Prepare the owner-attended proof of real lead replies: the scratch config,
# the owner channel's settings and the files they name, in a folder of the
# proof's own, for the daemon that serves the scratch leads and the seat gate
# on the phone trial's state folder. Read docs/scratch-seat-proof.md first: it
# has every step, in order, and says which steps are the owner's alone.
#
#   prepare.sh --dry      VALUES [--claude FILE --model NAME] [--bin DIR] [--daemon FILE]
#                                [--pasta FILE] [--bwrap FILE]
#   prepare.sh --write    the same
#   prepare.sh --status   --state DIR --root DIR --gate IP:PORT [--daemon FILE] [--compare]
#   prepare.sh --self-test [gate IP:PORT | config STATE ROOT SITTING [MODEL]
#                          | settings ROOT | commands STATE ROOT IP:PORT SITTING [live]]
#
# VALUES are --state DIR --root DIR --gate IP:PORT --sitting N. A value is
# taken from its flag, else from the environment: OWNER_STATE_DIR (--state:
# the phone trial's state folder, which becomes the scratch state root, so
# that the gate's key, its seat pins and the paired phones' keys stay the
# trial's), ROOT (--root: a new or empty folder for the proof's own files,
# whose parent exists), SEAT_GATE (--gate: the gate's IPv4 address and port),
# SITTING (--sitting: the sitting's number, 1 to 999, which makes the leads'
# keys standin-N and live-N fresh for this sitting), CLAUDE_FILE and
# CLAUDE_MODEL (--claude, --model: the tested claude, one regular file, and
# the model; without them the config has the stand-in lead only). No value is
# written into the repository and none has a default that names a place of
# the owner's.
#
# The proof's daemon reads the owner channel's settings from the folder
# ROOT/owner-settings (ataniqd run --owner-settings), never from
# owner/scratch.json of the trial's state folder: nothing of the trial is
# renamed, moved, masked or swapped. The trial is stopped and started again by
# the owner's own commands, and the state folder's lock keeps the two daemons
# apart.
#
# --dry prints what --write would make and the owner's steps from the values
# alone: it reads no folder of the owner's and writes nothing, and what
# depends on the file system is printed as a condition. --write checks the
# state folder (a real folder at its physical path, with an owner/ folder, and
# nothing of this sitting's leads), the tools and ROOT (its parent exists; it
# is new or empty; physically outside the state folder, the checkout, /tmp,
# /var/tmp and $TMPDIR), then makes ROOT (mode 0700, only that folder) and
# writes in it only: the scratch config scratch.json (0600), the owner
# channel's settings owner-settings/scratch.json (the folder 0700, the file
# 0600), the managed settings and the guard hook (a no-op the holder's marker
# check needs; it guards nothing), the replay screens of the checkout, copies
# of the stand-in, the probe, pasta when it is a link and the claude file, a
# throwaway source repository (git, one commit, in a clean environment),
# netcheck.sh (the owner's network check, never run by prepare) and the
# folders owner-channel/, its drop/ and evidence/. What only the daemon can
# judge (the settings folder against the trial's folder, the leads' reach and
# the gate's identity, the gate's key and pins, the root marker, the socket
# paths) is left to the daemon's own check, which --status runs. --write never
# starts a unit, a daemon, a sandbox or an agent, never runs the claude it
# copies, never answers a prompt, never deletes anything and never writes in
# the trial's state folder.
#
# --status is the owner's: it runs the daemon's check, "ataniqd run --scratch
# ROOT/scratch.json --owner-channel --seat-gate IP:PORT --owner-settings
# ROOT/owner-settings --check", which reads the trial's state folder and binds,
# locks, makes and starts nothing, and passes its lines and its exit status
# through. With --compare (step 13) it then compares the trial's files with
# the record of step g, which must be there: ROOT/evidence/trial-before.sha256
# must list each of the six files once and nothing else before sha256sum -c
# counts as confirming them, and the trial's gate key, certificate, pins and
# settings must be the same bytes. The key list and its head are judged by the
# daemon, not here: prepare passes the prefix the owner recorded at step g
# (ROOT/evidence/keys-before.prefix, the output of the daemon's --check
# --keys-prefix: the list's length and SHA-256) to "ataniqd run --scratch
# ROOT/scratch.json --owner-channel --check --keys-since PREFIX" and prints
# what it says. The daemon checks the list and its head with the journal's own
# reader and names each record appended since step g; counter records alone (a
# phone login) pass, and anything else, damage included, is CHANGED. A record
# of step g that is incomplete or missing is CHANGED too. It writes nothing.
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
# The trial's files whose digests the sitting compares, relative to owner/.
TRIAL_FILES='gate/key.pem gate/cert.pem gate/seat-pins keys.log keys.head scratch.json'

say() { printf '%s\n' "$*"; }

die() {
  printf '%s: %s\n' "$NAME" "$*" >&2
  exit 1
}

usage() {
  printf '%s\n' 'usage: prepare.sh --dry|--write --state DIR --root DIR --gate IP:PORT --sitting N' \
    '                  [--claude FILE --model NAME] [--bin DIR] [--daemon FILE] [--pasta FILE] [--bwrap FILE]' \
    '       prepare.sh --status --state DIR --root DIR --gate IP:PORT [--daemon FILE] [--compare]' \
    '       --dry          print what --write makes and your steps, from the values alone; read and write nothing' \
    '       --write        check, make ROOT and write the proof'"'"'s files in it (nothing else)' \
    '       --status       run the daemon'"'"'s check (yours; it writes nothing)' \
    '       --compare      with --status: also compare the trial'"'"'s files with the record of step g, and have the daemon judge the key list' \
    '       --state DIR    the phone trial'"'"'s state folder (or OWNER_STATE_DIR)' \
    '       --root DIR     a new or empty folder for the proof (or ROOT); its parent exists; outside /tmp and the checkout' \
    '       --gate IP:PORT the seat gate'"'"'s IPv4 address and port (or SEAT_GATE)' \
    '       --sitting N    the sitting'"'"'s number, 1 to 999 (or SITTING): the leads are standin-N and live-N' \
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

# phys DIR prints the physical path of an existing folder, or fails.
phys() { (cd -P -- "$1" 2>/dev/null && pwd -P); }

# The values every computation uses. set_paths STATE ROOT sets the names of
# the proof's files; set_keys SITTING the leads' keys.
set_paths() {
  S=$1
  ROOT=$2
  CONFIG=$ROOT/scratch.json
  SETTINGS_DIR=$ROOT/owner-settings
  SETTINGS=$SETTINGS_DIR/scratch.json
  RUNTIME=$ROOT/runtime
  OC=$ROOT/owner-channel
  EVIDENCE=$ROOT/evidence
  BEFORE=$EVIDENCE/trial-before.sha256
  # The prefix of the trial's key list at step g, its length and SHA-256, as
  # the daemon's --check --keys-prefix prints it.
  KEYS_BEFORE=$EVIDENCE/keys-before.prefix
  OWNER=$S/owner
}

set_keys() {
  SITTING=$1
  KEY_STANDIN=standin-$1
  KEY_LIVE=live-$1
}

# never_mounted prints the paths no bind source of a lead may touch: the
# checkout, the owner's own folders of credentials, the owner folder of the
# state root, which holds the gate's key and the phones' keys, and the folder
# of the owner channel's settings. check_home has refused a HOME that cannot
# give them.
never_mounted() {
  printf '%s' "\"$REPO\", \"$HOME/.ssh\", \"$HOME/.claude\", \"$HOME/.codex\", \"$S/owner\", \"$SETTINGS_DIR\""
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
# OWNER ONLY. No printed command deletes anything or moves a file. The trial's
# unit is named "$TRIAL_UNIT", a reference the owner's shell fills in, never by
# its value; no printed line holds a placeholder in angle brackets.
commands() {
  _g=$1
  _live=${2:-}
  _c="$DAEMON chat"
  _vals="--state $S --root $ROOT --gate $_g"
  _lu=ataniq-lead-scratch-$KEY_LIVE.service
  _su=ataniq-lead-scratch-$KEY_STANDIN.service
  _run="$DAEMON run --scratch $CONFIG --owner-channel --seat-gate $_g --owner-settings $SETTINGS_DIR"
  cat <<EOF
Your steps, in order (docs/scratch-seat-proof.md says what each proves and what to do when one fails).
Three terminals in the checkout, with TRIAL_UNIT set: A for the daemon, B for the console, C for the desk.
Nothing of the trial is renamed, moved or masked: the proof's daemon reads its settings from $SETTINGS_DIR.
Before the sitting, with the trial running (terminal C):
EOF
  if [ -n "$_live" ]; then
    cat <<EOF
  c. OWNER ONLY (runs the claude copy, in a new home in the root): H=\$(mktemp -d $ROOT/version-home.XXXXXX) && env -i HOME="\$H" $ROOT/agent-install/claude --version
     must name $RUNTIME_VERSION.
EOF
  fi
  cat <<EOF
  d. Check that your firewall admits TCP to $_g from the authority host only.
  e. OWNER ONLY (starts pasta, bubblewrap, curl and a throwaway user unit): { sh $ROOT/netcheck.sh; echo "netcheck: exit status \$?"; } 2>&1 | tee -a $EVIDENCE/netcheck.txt
     It must say connected from this host and refused or filtered from the lead's network, and end "netcheck: exit status 0".
  f. $BIN/ataniq-canary scratch-init --config $CONFIG   (the root marker; it writes nothing in owner/)
  g. sh $PREP --status $_vals   (the daemon's check: it must end "check: no refusal")
     $DAEMON seat-gate pins --state-dir $S
     journalctl --user --unit "\$TRIAL_UNIT" -I | grep 'key fingerprint' | tail -n 1
     (cd $OWNER && sha256sum -- $TRIAL_FILES) >$BEFORE
     OWNER ONLY (the daemon's read-only check of the key list and its head; it writes only this file): $DAEMON run --scratch $CONFIG --owner-channel --check --keys-prefix >$KEYS_BEFORE
Switch from the trial to the proof:
  1. OWNER ONLY: systemctl --user stop -- "\$TRIAL_UNIT"
     then: systemctl --user show -p ActiveState -- "\$TRIAL_UNIT"   (inactive; nothing is masked)
  2. OWNER ONLY (terminal A): sh -c 'trap : INT; "\$@" 2>&1; echo "ataniqd: exit status \$?"' sh $_run | tee -ai $EVIDENCE/daemon.log
  3. OWNER ONLY (terminal B): $BIN/ataniq-console --daemon $RUNTIME   then L
     On the phone and in a desktop browser: the seat's web app, its Leads page.
The stand-in lead:
  4. OWNER ONLY (the daemon runs git to define it): d on $KEY_STANDIN
     then (terminal C): $_c set $KEY_STANDIN read --config $CONFIG
  5. OWNER ONLY: s on $KEY_STANDIN; m: $TOKEN
     submitted: the fence put it in; no paste.
     NOT delivered (nothing was written): w, Enter, look at the empty input line, paste that line once, Enter once; Ctrl+\\ then q.
     Any other answer, or one you are not sure of: no paste; write it in $EVIDENCE/notes.txt; stop and reconcile as the guide says.
  6. Phone and desktop: Leads, daemon:$KEY_STANDIN and its reply; then x and y on $KEY_STANDIN
EOF
  if [ -n "$_live" ]; then
    cat <<EOF
The live lead (only once the drop rule is in place):
  7. The drop rule (a separate change, SB-1) is in place: you install it as root at the start of sitting 1, and its own check passes.
     OWNER ONLY (starts pasta, bubblewrap, curl and a throwaway user unit): { sh $ROOT/netcheck.sh; echo "netcheck: exit status \$?"; } 2>&1 | tee -a $EVIDENCE/netcheck.txt
     Without the rule: no live lead; go on with step 11.
  8. Note the login rows of the provider's account page.
     OWNER ONLY (the daemon runs git to define it): d on $KEY_LIVE
     then (terminal C): $_c set $KEY_LIVE read --config $CONFIG
  9. OWNER ONLY: s on $KEY_LIVE; then (terminal C): systemctl --user status -- $_lu
     must show /ataniq/agent/claude --model $MODEL. Note the time.
     w, Enter, a fresh login in the pane; Ctrl+\\ then q.
     m with the message of the guide. submitted: w, Enter, approve only the write of the one reply into its outbox; Ctrl+\\ then q.
     NOT delivered (nothing was written): w, Enter, paste the line once, Enter once, stay for the approval of that one write; Ctrl+\\ then q.
     Any other answer: no paste; write it in $EVIDENCE/notes.txt; stop and reconcile.
 10. Phone and desktop: daemon:$KEY_LIVE and its reply; at most 30 minutes or 10 turns, by your clock; then x and y on $KEY_LIVE;
     revoke the login made in 9 and confirm it is gone (or write cannot-identify and what you saw in $EVIDENCE/notes.txt)
Off, restart, stop and back to the trial:
 11. (terminal C) $_c set $KEY_LIVE off --config $CONFIG   (its page says Chat is off; $KEY_STANDIN stays on)
     $_c list --config $CONFIG >>$EVIDENCE/chat-list.jsonl
EOF
  else
    cat <<EOF
Off, restart, stop and back to the trial:
 11. (terminal C) $_c set $KEY_STANDIN off --config $CONFIG   (its page says Chat is off)
     $_c set $KEY_STANDIN read --config $CONFIG   (on again, for the restart)
     $_c list --config $CONFIG >>$EVIDENCE/chat-list.jsonl
EOF
  fi
  _units=$_su
  if [ -n "$_live" ]; then _units="$_su $_lu"; fi
  cat <<EOF
 12. OWNER ONLY: quit the console; Ctrl+C in A (exit status 0); the daemon again as in 2, then (terminal C):
     $_c list --config $CONFIG >>$EVIDENCE/chat-list.jsonl   ($KEY_STANDIN is off now)
     OWNER ONLY: Ctrl+C in A again (exit status 0); systemctl --user list-units --all -- $_units   (0 units)
 13. sh $PREP --status $_vals --compare   (the daemon's check; the trial's files against the record of g, which lists all six; the daemon's check of the key list since g)
 14. OWNER ONLY: start the trial again with your own command (systemctl --user start -- "\$TRIAL_UNIT" for a unit file, or your systemd-run)
     journalctl --user --unit "\$TRIAL_UNIT" -I | grep 'key fingerprint' | tail -n 1   (this new invocation only; the fingerprint of g)
     The phone shows the trial's samples again.
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
  for _a in 192.0.2.10:7443 198.51.100.1:1 127.0.0.1:65535 223.255.255.254:7443 240.0.0.1:7443; do
    (check_gate "$_a") 2>/dev/null || _fail "the gate address $_a is refused"
    _n=$((_n + 1))
  done
  for _a in 0.0.0.0:7443 '[::]:7443' '[2001:db8::10]:7443' '[abc]:7443' '[0:0:0:0:0:0:0:0]:7443' '[::ffff:192.0.2.10]:7443' \
    gate.example:7443 192.0.2.10:0 192.0.2.10 192.0.2.300:7443 192.0.2.10:70000 192.0.2.010:7443 192.0.2.10:07443 \
    224.0.0.1:7443 239.255.255.255:7443 255.255.255.255:7443 1.2.3:7443 1.2.3.4.5:7443 :7443; do
    if (check_gate "$_a") 2>/dev/null; then _fail "the gate address $_a is taken"; fi
    _n=$((_n + 1))
  done
  HOME=/srv/sample/home
  set_keys 7
  set_paths /srv/sample/trial-state /srv/sample/proof
  for _m in '' sample-model; do
    _c=$(config_json "$_m")
    for _w in '"state_root": "/srv/sample/trial-state"' '"runtime_root": "/srv/sample/proof/runtime"' '"max_running": 1' \
      '"/srv/sample/trial-state/owner"' '"/srv/sample/home/.claude"' '"/srv/sample/proof/owner-settings"' '"key": "standin-7"' '"inject": true'; do
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
    case $_o in *'--status '*'systemctl --user stop'*' --owner-settings /srv/sample/proof/owner-settings'*'--status '*' --compare'*'systemctl --user start'*) ;;
      *) _fail 'the steps do not check, stop the trial, run the daemon on the settings folder, compare and start the trial, in order' ;;
    esac
    case $_o in *'scratch-init '*'--status '*) ;; *) _fail 'the steps do not make the root marker before the daemon'"'"'s check' ;; esac
    case $_o in *'sha256sum -- '*' --check --keys-prefix >/srv/sample/proof/evidence/keys-before.prefix'*'systemctl --user stop'*'--compare'*'key list since g'*'systemctl --user start'*) ;;
      *) _fail 'the steps do not record the key list'"'"'s prefix at g, before the trial stops, and have the daemon judge it at 13' ;;
    esac
    case $_o in *'cp -- '*'keys.log'* | *'sha256sum -c'*) _fail 'a step copies the key list or confirms the record by hand' ;; esac
    case $_o in *'TRIAL_UNIT'*) ;; *) _fail 'the steps do not name the unit' ;; esac
    case $_o in *'KEY'*) _fail 'the steps name a lead KEY' ;; esac
    case $_o in *'swap'* | *'mask --'* | *'unmask'* | *'record-trial'* | *'trial-restart'*) _fail 'the steps still swap, mask or record the trial' ;; esac
    case $_o in *'live-7'*) [ -n "$_m" ] || _fail 'the steps name a lead that is not configured' ;; esac
    case $_o in *'claude --version'*) [ -n "$_m" ] || _fail 'the steps run claude without a live lead' ;; esac
    case $_o in *'drop rule'*'d on live-7'*) ;; *) [ -z "$_m" ] || _fail 'the live lead does not wait for the drop rule' ;; esac
    _n=$((_n + 10))
    _rest=$_o$_nl
    while [ -n "$_rest" ]; do
      _l=${_rest%%"$_nl"*}
      _rest=${_rest#*"$_nl"}
      case $_l in
        *' rm '* | *'rm -'* | *' mv '* | *' unlink '* | *"$_tgt"*) _fail "a step deletes, moves by hand or builds: $_l" ;;
        *'<'[A-Za-z]*'>'*) _fail "a step holds a placeholder: $_l" ;;
      esac
      case $_l in
        *'systemctl --user st'* | *' run --scratch '* | *'ataniq-console '* | *' --version'* | *'d on '* | *'s on '* | *'netcheck.sh'* | \
          *'start the trial again'*)
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
COMPARE=
CLAUDE_SRC=${CLAUDE_FILE:-}
MODEL=${CLAUDE_MODEL:-}
BIN=$REPO/build/canary
DAEMON=$REPO/build/scratch-seat/ataniqd
PASTA=
BWRAP=
while [ "$#" -gt 0 ]; do
  case $1 in
    --dry | --write | --status)
      [ -z "$MODE" ] || usage
      MODE=${1#--}
      shift
      ;;
    --state) [ "$#" -ge 2 ] || usage; S=$2; shift 2 ;;
    --root) [ "$#" -ge 2 ] || usage; ROOT=$2; shift 2 ;;
    --gate) [ "$#" -ge 2 ] || usage; GATE=$2; shift 2 ;;
    --sitting) [ "$#" -ge 2 ] || usage; SIT=$2; shift 2 ;;
    --claude) [ "$#" -ge 2 ] || usage; CLAUDE_SRC=$2; shift 2 ;;
    --model) [ "$#" -ge 2 ] || usage; MODEL=$2; shift 2 ;;
    --bin) [ "$#" -ge 2 ] || usage; BIN=$2; shift 2 ;;
    --daemon) [ "$#" -ge 2 ] || usage; DAEMON=$2; shift 2 ;;
    --pasta) [ "$#" -ge 2 ] || usage; PASTA=$2; shift 2 ;;
    --bwrap) [ "$#" -ge 2 ] || usage; BWRAP=$2; shift 2 ;;
    --compare) COMPARE=1; shift ;;
    *) usage ;;
  esac
done
# No default action: the mode is always said.
[ -n "$MODE" ] || usage
# --compare belongs to --status.
if [ -n "$COMPARE" ] && [ "$MODE" != status ]; then usage; fi

# The values, checked as text before anything is read.
[ -n "$S" ] || die 'no state folder: give --state DIR or OWNER_STATE_DIR, the phone trial'"'"'s state folder'
[ -n "$ROOT" ] || die 'no root: give --root DIR or ROOT, a new or empty folder for the proof'
safe_path 'the state folder' "$S"
safe_path 'the root' "$ROOT"
safe_path 'the checkout' "$REPO"
safe_path --daemon "$DAEMON"
[ -n "$GATE" ] || die 'no gate address: give --gate IP:PORT or SEAT_GATE'
check_gate "$GATE"
case $MODE in
  dry | write) check_sitting "$SIT" ;;
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
  say "[dry]   folder $ROOT (0700; its parent must exist) with conf/, agent-install/, bin/, upstream/, evidence/, owner-settings/, owner-channel/ and owner-channel/drop/ (0700)"
  say "[dry]   $CONFIG (0600): the scratch config; state_root $S (the trial's state folder), runtime_root $RUNTIME, max_running 1"
  say "[dry]     lead $KEY_STANDIN: the stand-in, runtime claude $RUNTIME_VERSION, footer_mode manual, inject true, limits 4 GiB, no swap, 512 tasks"
  if [ -n "$LIVE" ]; then
    say "[dry]     lead $KEY_LIVE: live claude $RUNTIME_VERSION, argv [\"/ataniq/agent/claude\", \"--model\", \"$MODEL\"], env DISABLE_AUTOUPDATER=1 CLAUDE_CODE_NO_FLICKER=1, footer_mode manual, inject true, the same limits"
  else
    say "[dry]     no live lead: give --claude FILE and --model NAME for it"
  fi
  say "[dry]     never_mounted: $(never_mounted)"
  say "[dry]   $SETTINGS (0600, in a folder of 0700): the owner channel's settings (stand-in door, proof-project, roots $OC, no sandbox template),"
  say "[dry]     which the daemon reads with --owner-settings $SETTINGS_DIR; owner/scratch.json of the state folder is never read, moved or changed"
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
  say "[dry]   --write refuses, before it makes anything: a state folder that is a link, not at its physical path or without an owner/ folder;"
  say "[dry]   anything of $KEY_STANDIN or $KEY_LIVE in the state folder; a missing binary, daemon, pasta, bubblewrap or /usr/bin/git; a root whose"
  say "[dry]   parent is missing or a link, that is not new or empty or cannot be read, or that lies physically in the state folder, the"
  say "[dry]   checkout, /tmp, /var/tmp or \$TMPDIR; and a HOME that is not a real folder. The daemon's check (--status) judges the rest."
  say "[dry]   nothing in $S is written by --dry, --write or --status"
  say ""
  commands "$GATE" "$LIVE"
  exit 0
fi

# ---- From here the file system is read. ----

# The state folder: a real folder at its own physical path, with owner/.
check_state() {
  if [ -L "$S" ] || [ ! -d "$S" ]; then die "the state folder is not a folder (a link is refused): $S"; fi
  _p=$(phys "$S") || die "the state folder cannot be entered: $S"
  [ "$_p" = "$S" ] || die "the state folder is not at its physical path (a folder on the way is a link): give $_p"
  if [ -L "$OWNER" ] || [ ! -d "$OWNER" ]; then die "the state folder has no owner/ folder (a link is refused): $OWNER"; fi
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

# status_failed sets RC to 1 unless the daemon's check already failed.
status_failed() { [ "$RC" -ne 0 ] || RC=1; }

# record_complete prints nothing and succeeds when the record of step g
# (BEFORE) lists each of the six files once, as sha256sum writes it (64
# lower-case hexadecimal digits, two spaces, the name), and nothing else;
# else it prints what is wrong and fails. Without it, sha256sum -c would
# confirm only the files a partial record names.
record_complete() {
  _lines=$(wc -l <"$BEFORE") || { echo "it cannot be read"; return 1; }
  _lines=$((_lines + 0))
  _miss=
  for _f in $TRIAL_FILES; do
    _c=$(LC_ALL=C grep -c -x -e "[0-9a-f]\{64\}  $_f" -- "$BEFORE") || :
    [ "${_c:-0}" -eq 1 ] || _miss="$_miss $_f"
  done
  if [ -n "$_miss" ]; then
    echo "it does not list each of the six files once (missing or repeated:$_miss)"
    return 1
  fi
  if [ "$_lines" -ne 6 ] || [ "$(tail -c 1 -- "$BEFORE" | wc -l)" -ne 1 ]; then
    echo "it holds $_lines line(s), not exactly the six of the files"
    return 1
  fi
  return 0
}

# compare_record is --status --compare: the trial's files against the record
# of step g, which must be there and complete, and the key list judged by the
# daemon against the prefix of step g.
compare_record() {
  if [ -L "$BEFORE" ] || [ ! -f "$BEFORE" ]; then
    say "status: CHANGED: there is no record $BEFORE of step g, so nothing of the trial is confirmed"
    status_failed
    return 0
  fi
  if ! _why=$(record_complete); then
    say "status: CHANGED: the record $BEFORE is incomplete: $_why; it confirms nothing"
    status_failed
    return 0
  fi
  _out=$(cd -- "$OWNER" && sha256sum -c -- "$BEFORE" 2>&1) || :
  printf '%s\n' "$_out" | sed 's/^/status:   /'
  _others=ok
  for _f in gate/key.pem gate/cert.pem gate/seat-pins scratch.json; do
    printf '%s\n' "$_out" | grep -qxF -e "$_f: OK" || _others=changed
  done
  if [ "$_others" != ok ]; then
    say "status: CHANGED: a file of the trial other than the key list is not the bytes recorded in $BEFORE (the lines above name it)"
    status_failed
  fi
  # The key list and its head: the daemon's own check against the prefix of
  # step g. A prefix that is missing or not of the daemon's form is CHANGED.
  _prefix=
  if [ -f "$KEYS_BEFORE" ] && [ ! -L "$KEYS_BEFORE" ] && [ "$(wc -l <"$KEYS_BEFORE")" -eq 1 ]; then
    _prefix=$(cat -- "$KEYS_BEFORE")
  fi
  case $_prefix in
    '' | 0* | *[!0-9a-f:]* | *:*:* | :* | *:) _prefix= ;;
    *:????????????????????????????????????????????????????????????????) ;;
    *) _prefix= ;;
  esac
  case ${_prefix%%:*} in '' | *[!0-9]*) _prefix= ;; esac
  if [ -z "$_prefix" ]; then
    say "status: CHANGED: there is no prefix of the key list of step g in $KEYS_BEFORE (one line, LEN:SHA256, as --keys-prefix prints it), so the key list is not confirmed"
    status_failed
    return 0
  fi
  say "status: the daemon's check of the key list since step g: $DAEMON run --scratch $CONFIG --owner-channel --check --keys-since $_prefix"
  _krc=0
  "$DAEMON" run --scratch "$CONFIG" --owner-channel --check --keys-since "$_prefix" || _krc=$?
  if [ "$_krc" -ne 0 ]; then
    say "status: CHANGED: the daemon's check of the key list ended with exit status $_krc (the lines above say why)"
    status_failed
    return 0
  fi
  if [ "$_others" = ok ]; then
    say "status: the trial's gate key, certificate, pins and settings are the same bytes as recorded in $BEFORE, and the daemon's check of the key list since step g holds"
  fi
}

# --status: the daemon's own check on the files --write made, then, with
# --compare, the trial's files against the record of step g. It writes
# nothing.
if [ "$MODE" = status ]; then
  check_state
  [ -f "$CONFIG" ] && [ ! -L "$CONFIG" ] || die "no scratch config $CONFIG: run --write first"
  if [ ! -f "$DAEMON" ] || [ ! -x "$DAEMON" ]; then
    die "no daemon of both tags at $DAEMON (docs/scratch-seat-proof.md, Before the sitting, says how to build it)"
  fi
  say "status: the daemon's check: $DAEMON run --scratch $CONFIG --owner-channel --seat-gate $GATE --owner-settings $SETTINGS_DIR --check"
  RC=0
  "$DAEMON" run --scratch "$CONFIG" --owner-channel --seat-gate "$GATE" --owner-settings "$SETTINGS_DIR" --check || RC=$?
  if [ "$RC" -eq 0 ]; then
    say "status: the daemon's check found no refusal"
  else
    say "status: the daemon's check ended with exit status $RC: the daemon would not start; the lines above say why"
  fi
  if [ -n "$COMPARE" ]; then
    compare_record
  elif [ -L "$BEFORE" ] || [ ! -f "$BEFORE" ]; then
    say "status: no $BEFORE yet: record the trial's digests and the key list's prefix at step g, before the trial is stopped"
  else
    say "status: the record of step g is there; compare the trial's files with it at step 13 (--status --compare)"
  fi
  exit "$RC"
fi

# ---- --write. Everything is checked before ROOT is made. ----
check_state
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
mkdir -m 0700 -- "$ROOT/conf" "$ROOT/agent-install" "$ROOT/bin" "$ROOT/upstream" "$EVIDENCE" "$SETTINGS_DIR" "$OC" "$OC/drop"

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
  printf '%s\n' '#!/bin/sh' '# Written by prepare.sh --write. Run it by hand while a daemon serves the gate: the trial at step e, the proof'"'"'s daemon at step 7 (docs/scratch-seat-proof.md).' \
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
