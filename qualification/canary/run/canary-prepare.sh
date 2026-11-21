#!/bin/sh
# Prepare the set-up of a pass of the canary run: the roots, the throwaway
# mirror, the config, the sandbox inputs, the replay screens, the managed
# settings and, for pass A, the stand-in copy or, for pass B, the copy of the
# tested claude, and the profile digest. Read README.md beside this file first.
#
#   canary-prepare.sh [--root DIR] [--pass A] [--bin DIR] [--pasta PATH]
#                     [--bwrap PATH] [--build] [--template-review PATH]
#                     [--unit-report PATH] [--plan-summary PATH] [--dry]
#   canary-prepare.sh --pass B --claude PATH [--model NAME] [the same]
#
# Pass B is the live agent: --claude names the tested claude executable (one
# regular file: not a link, not the version manager's shim), which is copied to
# ROOT/agent-install/claude after `env -i HOME=<a new empty folder> COPY
# --version` names the tested version the config declares (this is the one
# program prepare runs besides ataniq-intake and ataniq-canary, and it runs it
# only for pass B and only without --dry). The config is a live one (agent.kind
# live, the executable as argv[0], DISABLE_AUTOUPDATER=1 and CLAUDE_CODE_NO_FLICKER=1 in agent.env,
# footer_mode manual, inject true), and the pin is taken last, after the
# environment and everything else the digest covers is in place. It also writes
# ROOT/netcheck.sh, which the owner runs by hand: prepare never runs it.
#
# It never answers a prompt and starts no unit, no daemon, no sandbox and no
# agent: it makes folders and files, runs git on a throwaway repository,
# `ataniq-intake init` (which makes the mirror) and, to read the pin,
# `ataniq-canary profile-digest` and `check-inputs`. With --build it runs
# `make canary-build` first; without it the binaries must be built. It
# prints no secret. With --dry it prints what it would create and writes
# nothing: no command is run.
#
# The managed-settings file it writes names the guard hook, /ataniq/qargi/guard-hook
# (where the sandbox binds the no-op hook it also writes), and the inputs file
# carries sources.managed_marker "guard-hook", a text that file holds: the holder
# refuses a claude start without the marker, and its assertion 8 searches the
# file for it. The managed-settings file is written before its hash is taken,
# and the hash is the one the inputs file records.
#
# It also writes ROOT/answers.hint (mode 0600), a list of "key=path" lines:
# the paths the run script suggests at its path prompts (Enter takes one). The
# mirror report's path is the one qualify-mirror is told to write; the other
# three are written only for a path given with --unit-report, --plan-summary
# or --template-review, which must name an existing regular file (a hint that
# cannot be known is not written). A hint is a suggestion: the run still
# checks and records the file. No word the owner types is ever a hint.
#
# It writes ROOT/CHECKLIST.txt (mode 0600) too, from the template
# checklist.txt.in beside this file with the real paths filled in: one page for
# the owner, what to start and where, and what each prompt of the run expects.
set -eu
umask 077

# shellcheck disable=SC2034  # read by canary-lib.sh
CC_NAME=canary-prepare.sh
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
PASS=A
CLAUDE_SRC=
MODEL=
# The tested version of the runtime, the one the config and the inputs file
# declare; pass B holds the claude copy to it.
RUNTIME_VERSION=2.1.325
BIN=$REPO/build/canary
PASTA=
BWRAP=
BUILD=0
DRY=0
TEMPLATE_REVIEW=
UNIT_REPORT=
PLAN_SUMMARY=

# pasta_plan DEFAULT chooses the pasta file. The harness refuses a symbolic
# link, so a link is copied: PASTA_COPY is then 1, PASTA_SRC the regular file
# it leads to and PASTA_USE the copy in ROOT/bin. A regular file (--pasta, or
# DEFAULT when --pasta is not given) is used as it is.
PASTA_COPY=0
PASTA_SRC=
PASTA_USE=
pasta_plan() {
  if [ -n "$PASTA" ]; then
    case $PASTA in /*) ;; *) cc_die "--pasta is not an absolute path: $PASTA" ;; esac
    if [ -L "$PASTA" ] || [ ! -f "$PASTA" ] || [ ! -x "$PASTA" ]; then
      cc_die "--pasta is not an executable regular file (a link is refused): $PASTA"
    fi
    PASTA_USE=$PASTA
    return 0
  fi
  [ -e "$1" ] || cc_die "no $1; give --pasta PATH"
  if [ -L "$1" ]; then
    PASTA_SRC=$(readlink -f -- "$1") || cc_die "the link $1 cannot be resolved; give --pasta PATH"
    if [ ! -f "$PASTA_SRC" ] || [ ! -x "$PASTA_SRC" ]; then cc_die "$1 does not lead to an executable regular file; give --pasta PATH"; fi
    PASTA_COPY=1
    PASTA_USE=$ROOT/bin/pasta
  else
    if [ ! -f "$1" ] || [ ! -x "$1" ]; then cc_die "$1 is not an executable regular file; give --pasta PATH"; fi
    PASTA_USE=$1
  fi
}

usage() {
  printf '%s\n' 'usage: canary-prepare.sh [--root DIR] [--pass A] [--bin DIR] [--pasta PATH] [--bwrap PATH] [--build]' \
    '                         [--template-review PATH] [--unit-report PATH] [--plan-summary PATH] [--dry]' \
    '       canary-prepare.sh --pass B --claude PATH [--model NAME] [the same arguments]' \
    '       --root DIR    the folder to make (default: ataniq-canary-a, or -b for pass B, in your cache' \
    '                     folder); it must be new or empty, outside /tmp and outside the checkout' \
    '       --pass A|B    the pass: A the stand-in (default), B the live agent (needs --claude)' \
    '       --claude PATH pass B: the tested claude executable, one regular file (a link is refused);' \
    '                     it is copied to ROOT/agent-install/claude after its --version is checked' \
    '       --model NAME  pass B: add --model NAME to the agent command (default: no model flag; you pick)' \
    '       --bin DIR     the built binaries (default: build/canary of the checkout)' \
    '       --pasta PATH  a regular file to use as pasta (default: /usr/bin/pasta; a link is copied)' \
    '       --bwrap PATH  the bubblewrap executable (default: the one on PATH)' \
    '       --build       run make canary-build first' \
    '       --template-review PATH, --unit-report PATH, --plan-summary PATH' \
    '                     an existing file the run script suggests at its path prompt (a hint)' \
    '       --dry         print what would be made and write nothing' \
    '       canary-prepare.sh --self-test pasta-plan PATH' \
    '                     what the pasta choice does with PATH as the default (computes only)' >&2
  exit 2
}

# --self-test NAME VALUE...: the test entry. It computes and prints only.
#   pasta-plan PATH   what the pasta choice does with PATH as the default:
#                     "use PATH", "copy SOURCE", or a refusal
if [ "${1:-}" = --self-test ]; then
  case ${2:-} in
    pasta-plan)
      [ "$#" -eq 3 ] || usage
      pasta_plan "$3"
      if [ "$PASTA_COPY" -eq 1 ]; then cc_say "copy $PASTA_SRC"; else cc_say "use $PASTA_USE"; fi
      exit 0
      ;;
  esac
  usage
fi

while [ "$#" -gt 0 ]; do
  case $1 in
    --root) [ "$#" -ge 2 ] || usage; ROOT=$2; shift 2 ;;
    --pass) [ "$#" -ge 2 ] || usage; PASS=$2; shift 2 ;;
    --claude) [ "$#" -ge 2 ] || usage; CLAUDE_SRC=$2; shift 2 ;;
    --model) [ "$#" -ge 2 ] || usage; MODEL=$2; shift 2 ;;
    --bin) [ "$#" -ge 2 ] || usage; BIN=$2; shift 2 ;;
    --pasta) [ "$#" -ge 2 ] || usage; PASTA=$2; shift 2 ;;
    --bwrap) [ "$#" -ge 2 ] || usage; BWRAP=$2; shift 2 ;;
    --build) BUILD=1; shift ;;
    --template-review) [ "$#" -ge 2 ] || usage; TEMPLATE_REVIEW=$2; shift 2 ;;
    --unit-report) [ "$#" -ge 2 ] || usage; UNIT_REPORT=$2; shift 2 ;;
    --plan-summary) [ "$#" -ge 2 ] || usage; PLAN_SUMMARY=$2; shift 2 ;;
    --dry) DRY=1; shift ;;
    *) usage ;;
  esac
done

case $PASS in
  A | B) ;;
  *) cc_die "--pass is A or B: $PASS" ;;
esac
if [ "$PASS" = A ]; then
  [ -z "$CLAUDE_SRC$MODEL" ] || cc_die "--claude and --model are for --pass B; pass A runs the stand-in"
else
  [ -n "$CLAUDE_SRC" ] || cc_die "--pass B needs --claude PATH, the tested claude executable (README.md, The set-up of pass B)"
  if [ -n "$MODEL" ]; then
    case $MODEL in
      -* | *[!A-Za-z0-9._:+-]*) cc_die "--model is a model name of letters, digits and . _ : + - that does not start with -: $MODEL" ;;
    esac
    [ "${#MODEL}" -le 64 ] || cc_die '--model is longer than 64 characters'
  fi
fi

if [ -z "$ROOT" ]; then
  _cache=${XDG_CACHE_HOME:-}
  if [ -z "$_cache" ]; then
    case ${HOME:-} in
      /*) _cache=$HOME/.cache ;;
      *) cc_die 'no --root, and neither XDG_CACHE_HOME nor an absolute HOME names the cache folder' ;;
    esac
  fi
  if [ "$PASS" = B ]; then ROOT=$_cache/ataniq-canary-b; else ROOT=$_cache/ataniq-canary-a; fi
fi
cc_check_root "$ROOT"
case $REPO in
  *[!A-Za-z0-9._/+-]*) cc_die 'the path of the checkout has a character outside A-Z a-z 0-9 . _ / + -' ;;
esac

# A new root, or an empty folder that already exists.
if [ -d "$ROOT" ]; then
  if [ -n "$(find "$ROOT" -mindepth 1 -maxdepth 1 2>/dev/null)" ]; then
    if [ -e "$ROOT/$CC_MARKER" ]; then
      cc_die "the root is not empty: $ROOT (prepare makes a new set-up; this root carries the mark of a finished prepare, so canary-reset.sh puts it back; to remove it by hand instead, first check that this folder is the one you meant, then: chmod -R u+w $ROOT, then rm -r $ROOT)"
    fi
    cc_die "the root is not empty: $ROOT (this folder was not made by canary-prepare.sh; check it before you remove anything)"
  fi
fi

# --bin is checked now unless --build will make it.
case $BIN in /*) ;; *) cc_die "--bin is not an absolute path: $BIN" ;; esac
if [ "$BUILD" -eq 0 ]; then cc_check_bin "$BIN"; fi

pasta_plan /usr/bin/pasta

# claude_plan checks the file --claude names, with no program run: an absolute
# path to one regular executable file. A link is refused because the manager's
# `claude` is a shim or a link and the copy must be the tested file itself.
claude_plan() {
  case $CLAUDE_SRC in /*) ;; *) cc_die "--claude is not an absolute path: $CLAUDE_SRC" ;; esac
  case $CLAUDE_SRC in *[[:cntrl:]]*) cc_die '--claude has a control character' ;; esac
  if [ -L "$CLAUDE_SRC" ]; then cc_die "--claude is a link (the manager's shim or a link to a version): give the executable file itself: $CLAUDE_SRC"; fi
  if [ -d "$CLAUDE_SRC" ]; then cc_die "--claude is a folder, not the executable file: $CLAUDE_SRC"; fi
  if [ ! -f "$CLAUDE_SRC" ]; then cc_die "--claude is not a regular file: $CLAUDE_SRC"; fi
  if [ ! -x "$CLAUDE_SRC" ]; then cc_die "--claude is not executable: $CLAUDE_SRC"; fi
}
if [ "$PASS" = B ]; then claude_plan; fi

# The paths given for a hint: absolute, no control character; a file that is
# not there is no hint, and the output says so.
HINT_TEMPLATE_REVIEW=
HINT_UNIT_REPORT=
HINT_PLAN_SUMMARY=
hint_path() {
  [ -n "$2" ] || return 0
  case $2 in /*) ;; *) cc_die "$1 is not an absolute path: $2" ;; esac
  case $2 in *[[:cntrl:]]*) cc_die "$1 has a control character" ;; esac
  if [ -L "$2" ] || [ ! -f "$2" ]; then
    cc_say "no hint is written for $1: not a regular file: $2"
    return 0
  fi
  case $1 in
    --template-review) HINT_TEMPLATE_REVIEW=$2 ;;
    --unit-report) HINT_UNIT_REPORT=$2 ;;
    --plan-summary) HINT_PLAN_SUMMARY=$2 ;;
  esac
}
hint_path --template-review "$TEMPLATE_REVIEW"
hint_path --unit-report "$UNIT_REPORT"
hint_path --plan-summary "$PLAN_SUMMARY"

if [ -z "$BWRAP" ]; then
  BWRAP=$(command -v bwrap 2>/dev/null) || BWRAP=/usr/bin/bwrap
fi
case $BWRAP in /*) ;; *) cc_die "bubblewrap is not an absolute path: $BWRAP" ;; esac

case ${HOME:-} in
  /*) NEVER="\"$REPO\", \"$HOME/.ssh\", \"$HOME/.claude\", \"$HOME/.codex\"" ;;
  *) NEVER="\"$REPO\"" ;;
esac
case $NEVER in *\\*) cc_die 'a path to keep out of the sandbox has a backslash' ;; esac

CONFIG=$ROOT/config.json
INPUTS=$ROOT/conf/sandbox-inputs.json
LEAD=$ROOT/state/leads/${CC_PROJECT}-lead

if [ "$DRY" -eq 1 ]; then
  cc_say "[dry] canary-prepare.sh would make, and write nothing now:"
  if [ "$BUILD" -eq 1 ]; then cc_say "[dry]   run: make canary-build (in the checkout)"; else cc_say "[dry]   check the binaries in $BIN"; fi
  cc_say "[dry]   folder $ROOT (0700) with state/ and runtime/ (0700), evidence/, conf/, agent-install/, bin/"
  cc_say "[dry]   $ROOT/upstream: a throwaway repository, one commit on develop, author example.invalid"
  cc_say "[dry]   run: ataniq-intake init --state $ROOT/state --project $CC_PROJECT, then push develop into the mirror"
  if [ "$PASS" = B ]; then
    _argv='["/ataniq/agent/claude"]'
    if [ -n "$MODEL" ]; then _argv="[\"/ataniq/agent/claude\", \"--model\", \"$MODEL\"]"; fi
    cc_say "[dry]   $CONFIG (0600): pass B, live agent, runtime claude $RUNTIME_VERSION, argv $_argv, env [\"DISABLE_AUTOUPDATER=1\", \"CLAUDE_CODE_NO_FLICKER=1\"], footer_mode manual, inject true, limits 4 GiB, no swap, 512 tasks, 1 GiB floor, 50 percent"
    if [ -z "$MODEL" ]; then cc_say "[dry]   no --model flag in the agent command: you pick the model (the run's step 15 prints the arguments and asks you)"; fi
    cc_say "[dry]   $INPUTS (0600), provider_host api.anthropic.com, sources.managed_marker guard-hook, sources.agent_install $ROOT/agent-install, terminal xterm-256color, truecolor, C.UTF-8"
  else
    cc_say "[dry]   $CONFIG (0600): pass A, stand-in agent, runtime claude $RUNTIME_VERSION, footer_mode manual, inject true, limits 4 GiB, no swap, 512 tasks, 1 GiB floor, 50 percent"
    cc_say "[dry]   $INPUTS (0600), provider_host api.anthropic.com, sources.managed_marker guard-hook"
  fi
  cc_say "[dry]   $ROOT/conf/replay-screens (idle-before-paste as 1, collapsed-paste-footer-c as 2, report.txt)"
  cc_say "[dry]   $ROOT/conf/managed-settings.json (valid JSON, naming the PreToolUse hook /ataniq/qargi/guard-hook, the text of the marker) and a no-op $ROOT/conf/guard-hook"
  if [ "$PASS" = B ]; then
    cc_say "[dry]   run: env -i HOME=<a new empty folder> $ROOT/agent-install/claude --version, on the copy of $CLAUDE_SRC (the output must name $RUNTIME_VERSION); then $ROOT/agent-install/claude (0700) is the agent executable the sandbox binds"
  else
    cc_say "[dry]   $ROOT/agent-install/ataniq-canary-standin, a copy of the built stand-in"
  fi
  cc_say "[dry]   $ROOT/bin/ataniq-probe (0700), a copy of $BIN/ataniq-probe: the inputs file names the copy, because the probe is a bind source and the checkout is never mounted"
  if [ "$PASTA_COPY" -eq 1 ]; then
    cc_say "[dry]   $PASTA_USE, a copy of $PASTA_SRC (the subject is then a copy of passt, not pasta itself)"
  else
    cc_say "[dry]   pasta: $PASTA_USE (used as it is)"
  fi
  if [ "$PASS" = B ]; then
    cc_say "[dry]   run: ataniq-canary profile-digest, write it into holder.profile_digest (last: the environment is part of the digest); run: ataniq-canary check-inputs"
  else
    cc_say "[dry]   run: ataniq-canary profile-digest, write it into holder.profile_digest; run: ataniq-canary check-inputs"
  fi
  cc_say "[dry]   $ROOT/mirror-qual/config.json for qualify-mirror, with the acknowledgement left false for you"
  cc_say "[dry]   $ROOT/answers.hint (0600): the paths the run suggests at its path prompts: mirror-report=$ROOT/mirror-qual/report.json, and the unit report, plan summary and template review when given"
  if [ "$PASS" = B ]; then
    cc_say "[dry]   $ROOT/netcheck.sh (0700): the network and executable checks for you to run by hand (prepare and the run never run it)"
    cc_say "[dry]   $ROOT/CHECKLIST.txt (0600): one page for you, from checklist-b.txt.in, with these paths filled in"
  else
    cc_say "[dry]   $ROOT/CHECKLIST.txt (0600): one page for you, from checklist.txt.in, with these paths filled in"
  fi
  cc_say "[dry]   $ROOT/$CC_MARKER, the mark that canary-reset.sh looks for"
  cc_say "[dry]   print the commands you type next"
  exit 0
fi

if [ "$BUILD" -eq 1 ]; then
  cc_say "building: make canary-build"
  make -C "$REPO" canary-build || cc_die 'make canary-build failed'
  cc_check_bin "$BIN"
fi

# half_made_root runs when the script ends. After a failure it says, as the
# last line, how to remove the root by hand; it removes nothing itself.
half_made_root() {
  _rc=$?
  if [ "$_rc" -ne 0 ] && [ -n "${ROOT_MADE:-}" ]; then
    printf '%s\n' "$CC_NAME: failed; the half-made root $ROOT has no mark, so canary-reset.sh refuses it: look at it, then remove it by hand (chmod -R u+w $ROOT, then rm -r $ROOT), or prepare again with another --root" >&2
  fi
  exit "$_rc"
}

# the parents get 0700 from the umask
# shellcheck disable=SC2174
if [ ! -d "$ROOT" ]; then mkdir -p -m 0700 -- "$ROOT"; fi
chmod 0700 -- "$ROOT"
cc_say "making $ROOT"
# From here a failure leaves a half-made root. Prepare never deletes anything:
# the last line says how to remove it by hand. The mark that canary-reset.sh
# requires is written only by a prepare that finished.
ROOT_MADE=1
trap half_made_root EXIT
mkdir -m 0700 -- "$ROOT/evidence" "$ROOT/conf" "$ROOT/agent-install" "$ROOT/bin"

# Pass B: the tested claude, copied first so that a wrong version stops the
# prepare before anything else is made. The version is asked of the COPY, as
# step 9 of the run asks it of the file the sandbox binds: an empty
# environment (no PATH) and a home that is a new empty folder.
if [ "$PASS" = B ]; then
  CLAUDE_COPY=$ROOT/agent-install/claude
  cp -- "$CLAUDE_SRC" "$CLAUDE_COPY"
  chmod 0700 -- "$CLAUDE_COPY"
  _vh=$(mktemp -d) || cc_die 'mktemp -d failed: no home for the version check'
  _vrc=0
  _vout=$(env -i HOME="$_vh" "$CLAUDE_COPY" --version 2>&1) || _vrc=$?
  rmdir -- "$_vh" 2>/dev/null || cc_say "note: the version check left files in $_vh; look at them and remove the folder yourself"
  if [ "$_vrc" -ne 0 ] || ! printf '%s\n' "$_vout" | grep -qwF -- "$RUNTIME_VERSION"; then
    cc_say "claude --version (the copy $CLAUDE_COPY, exit status $_vrc) printed:"
    printf '%s\n' "$_vout" | head -n 20 | sed 's/^/    /'
    cc_die "the copy does not name the tested version $RUNTIME_VERSION: give the executable of that version (the config and the inputs declare it)"
  fi
  cc_say "claude copy: $CLAUDE_COPY names $RUNTIME_VERSION in its --version"
fi

# The throwaway repository: one small commit on develop.
mkdir -m 0700 -- "$ROOT/upstream"
cc_git init -q --template= "$ROOT/upstream"
cc_git -C "$ROOT/upstream" symbolic-ref HEAD refs/heads/develop
printf '%s\n' 'A throwaway repository for the canary run.' >"$ROOT/upstream/README.txt"
cc_git -C "$ROOT/upstream" add README.txt
env GIT_AUTHOR_NAME='Sample Author' GIT_AUTHOR_EMAIL=author@example.invalid \
  GIT_COMMITTER_NAME='Sample Author' GIT_COMMITTER_EMAIL=author@example.invalid \
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 \
  git -C "$ROOT/upstream" -c commit.gpgsign=false commit -q -m 'Initial throwaway commit'

# The roots and the mirror, as canary-reset.sh makes them again.
cc_make_state "$ROOT" "$BIN"

# The replay screens, as the worked example took them from the recorded set.
SCREENS=$REPO/internal/inject/screen/testdata/claude-2.1.325
mkdir -m 0700 -- "$ROOT/conf/replay-screens"
for _x in json txt; do
  [ -f "$SCREENS/idle-before-paste.$_x" ] || cc_die "the recorded screens are missing in the checkout: $SCREENS"
  cp -- "$SCREENS/idle-before-paste.$_x" "$ROOT/conf/replay-screens/1.$_x"
  cp -- "$SCREENS/collapsed-paste-footer-c.$_x" "$ROOT/conf/replay-screens/2.$_x"
done
printf '%s\n' 'state=idle:app=claude-code' >"$ROOT/conf/replay-screens/report.txt"
chmod 0600 -- "$ROOT/conf/replay-screens/"*

# The managed settings name the guard hook as the PreToolUse hook command: the
# provisioned copy of conf/guard-hook is bound at /ataniq/qargi/guard-hook
# (profile.QargiInside and the guard hook's name), and the holder's assertion 8
# searches this file for the marker text, GUARD_MARKER, which the inputs file
# carries as sources.managed_marker (the spec requires it for claude). The
# marker is a text this file holds, so the two cannot drift apart: it is the
# name of the hook inside the sandbox.
GUARD_MARKER=guard-hook
GUARD_INSIDE=/ataniq/qargi/$GUARD_MARKER
printf '%s\n' "{\"permissions\":{\"defaultMode\":\"default\"},\"hooks\":{\"PreToolUse\":[{\"hooks\":[{\"type\":\"command\",\"command\":\"$GUARD_INSIDE\"}]}]}}" >"$ROOT/conf/managed-settings.json"
grep -qF -- "$GUARD_MARKER" "$ROOT/conf/managed-settings.json" ||
  cc_die 'the managed-settings file does not hold the marker text it is written for'
printf '%s\n' '#!/bin/sh' 'exit 0' >"$ROOT/conf/guard-hook"
chmod 0600 -- "$ROOT/conf/managed-settings.json"
chmod 0700 -- "$ROOT/conf/guard-hook"

if [ "$PASS" = A ]; then
  cp -- "$BIN/ataniq-canary-standin" "$ROOT/agent-install/ataniq-canary-standin"
  chmod 0700 -- "$ROOT/agent-install/ataniq-canary-standin"
fi

# The probe is a bind source, and the checkout (where --bin lies by default) is
# the first entry of never_mounted, which the holder's validate step refuses as
# a bind source: the inputs file names this copy in the root, as it names the
# copy of pasta. Step 9 of the run compares it with the built probe.
cp -- "$BIN/ataniq-probe" "$ROOT/bin/ataniq-probe"
chmod 0700 -- "$ROOT/bin/ataniq-probe"
PROBE_USE=$ROOT/bin/ataniq-probe

if [ "$PASTA_COPY" -eq 1 ]; then
  cp -- "$PASTA_SRC" "$PASTA_USE"
  chmod 0700 -- "$PASTA_USE"
  cc_say "pasta: /usr/bin/pasta is a link, which the harness refuses; $PASTA_USE is a copy of the file it leads to ($PASTA_SRC), so the subject is a copy of passt, not pasta itself"
fi

SETTINGS_SHA=$(sha256sum <"$ROOT/conf/managed-settings.json" | awk '{print $1}')
ZERO=0000000000000000000000000000000000000000000000000000000000000000

# The agent block: the stand-in of pass A, the live claude of pass B. The
# live one has the executable itself as argv[0] (never a wrapper: step 9 runs
# that file), the model flag only when --model gave one, and
# two names in the environment: DISABLE_AUTOUPDATER=1 so that the agent does
# not draw an update notice on the screen the fence reads, and
# CLAUDE_CODE_NO_FLICKER=1, which selects the fullscreen (alternate-screen)
# renderer, the only one the screen recogniser has fixtures for and reads
# (without it claude chooses the renderer per process and may draw on the main
# screen, where every inject ends not-now). The environment is part of the
# profile digest, so the pin is taken after it is here.
if [ "$PASS" = B ]; then
  AGENT_KIND=live
  AGENT_ARGV='["/ataniq/agent/claude"]'
  if [ -n "$MODEL" ]; then AGENT_ARGV="[\"/ataniq/agent/claude\", \"--model\", \"$MODEL\"]"; fi
  AGENT_ENV='["DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_NO_FLICKER=1"]'
  TERMINAL='{"term": "xterm-256color", "colorterm": "truecolor", "lang": "C.UTF-8"}'
else
  AGENT_KIND=stand-in
  AGENT_ARGV='["/ataniq/agent/ataniq-canary-standin"]'
  AGENT_ENV='[]'
  TERMINAL='{"term": "xterm-256color"}'
fi

write_config() {
  cat >"$CONFIG" <<EOF
{
  "version": 1,
  "role": "$CC_PROJECT/lead",
  "state_root": "$ROOT/state",
  "runtime_root": "$ROOT/runtime",
  "holder": {
    "executable": "$BIN/ataniq-holder",
    "cols": 120,
    "rows": 40,
    "sandbox_inputs": "$INPUTS",
    "profile_digest": "$1"
  },
  "agent": {
    "kind": "$AGENT_KIND",
    "runtime": "claude",
    "runtime_version": "$RUNTIME_VERSION",
    "footer_mode": "manual",
    "argv": $AGENT_ARGV,
    "env": $AGENT_ENV
  },
  "inject": true,
  "limits": {"memory_max_bytes": 4294967296, "swap_max_bytes": 0, "tasks_max": 512, "memory_floor_bytes": 1073741824, "memory_share_percent": 50},
  "provision": {
    "mirror": "$ROOT/state/mirrors/$CC_PROJECT.git",
    "upstream_branch": "develop",
    "git_name": "Sample Lead",
    "git_email": "lead@example.invalid",
    "managed_settings": "$ROOT/conf/managed-settings.json",
    "guard_hook": "$ROOT/conf/guard-hook",
    "replay_screens": "$ROOT/conf/replay-screens"
  }
}
EOF
  chmod 0600 -- "$CONFIG"
}

cat >"$INPUTS" <<EOF
{
  "version": 1, "canary": true, "project": "$CC_PROJECT", "layout_role": "${CC_PROJECT}-lead",
  "state_root": "$ROOT/state", "runtime_root": "$ROOT/runtime",
  "runtime": "claude", "runtime_version": "$RUNTIME_VERSION",
  "executables": {"pasta": "$PASTA_USE", "bwrap": "$BWRAP", "probe": "$PROBE_USE"},
  "sources": {
    "agent_install": "$ROOT/agent-install",
    "managed_settings": "$LEAD/managed-settings.json",
    "managed_settings_sha256": "$SETTINGS_SHA",
    "managed_marker": "$GUARD_MARKER",
    "lead_skills": "$LEAD/skills",
    "instruction_file": "$LEAD/instructions.md",
    "git_identity_dir": "$LEAD/git-identity",
    "qargi_install": "$LEAD/qargi-install",
    "qargi_records_inside": "/ataniq/records",
    "assignment_dir": "$LEAD/assignment",
    "projection_dir": "$LEAD/projection"
  },
  "never_mounted": [$NEVER],
  "provider_host": "api.anthropic.com",
  "terminal": $TERMINAL,
  "time_limit_seconds": 60
}
EOF
chmod 0600 -- "$INPUTS"

# The pin: the config carries a placeholder until the digest is known.
write_config "$ZERO"
DIGEST=$("$BIN/ataniq-canary" profile-digest --config "$CONFIG" --inputs "$INPUTS") ||
  cc_die 'ataniq-canary profile-digest failed'
case $DIGEST in
  *[!0-9a-f]* | '') cc_die 'ataniq-canary profile-digest did not print one digest' ;;
esac
[ "${#DIGEST}" -eq 64 ] || cc_die 'ataniq-canary profile-digest did not print 64 hex digits'
write_config "$DIGEST"
"$BIN/ataniq-canary" check-inputs --config "$CONFIG" --inputs "$INPUTS" >/dev/null ||
  cc_die 'ataniq-canary check-inputs failed'
cc_say "profile digest pinned in holder.profile_digest; check-inputs: the inputs match the config"

# The config of qualify-mirror. The acknowledgement is the owner's.
mkdir -m 0700 -- "$ROOT/mirror-qual"
cat >"$ROOT/mirror-qual/config.json" <<EOF
{
  "state_root": "$ROOT/mirror-qual/state",
  "template": "$REPO/qualification/canary/templates/canary-intake.json",
  "role": "coder",
  "project": "proj",
  "i_run_this_on_purpose": false
}
EOF
chmod 0600 -- "$ROOT/mirror-qual/config.json"

# The hints for the run script's path prompts: suggested paths and nothing
# else, no word and no secret.
HINTFILE=$ROOT/answers.hint
printf 'mirror-report=%s\n' "$ROOT/mirror-qual/report.json" >"$HINTFILE"
if [ -n "$HINT_UNIT_REPORT" ]; then printf 'unit-report=%s\n' "$HINT_UNIT_REPORT" >>"$HINTFILE"; fi
if [ -n "$HINT_PLAN_SUMMARY" ]; then printf 'plan-summary=%s\n' "$HINT_PLAN_SUMMARY" >>"$HINTFILE"; fi
if [ -n "$HINT_TEMPLATE_REVIEW" ]; then printf 'template-review=%s\n' "$HINT_TEMPLATE_REVIEW" >>"$HINTFILE"; fi
chmod 0600 -- "$HINTFILE"

# netcheck.sh, pass B only: the owner's hand-run check of the network and of
# the executable in the shape of the sandbox. Prepare writes it and never runs
# it. Its first lines carry the real values, single-quoted; the body is the
# template netcheck.sh.in beside this file.
sq() { printf '%s' "$1" | sed "s/'/'\\\\''/g"; }
NETCHECK=$ROOT/netcheck.sh
if [ "$PASS" = B ]; then
  [ -f "$_d/netcheck.sh.in" ] || cc_die "the netcheck template is missing: $_d/netcheck.sh.in"
  {
    printf '%s\n' '#!/bin/sh' '# Written by canary-prepare.sh. Run it by hand, once, before pass B (README.md).' \
      "SELF='$(sq "$NETCHECK")'" "PASTA='$(sq "$PASTA_USE")'" "BWRAP='$(sq "$BWRAP")'" \
      "INSTALL='$(sq "$ROOT/agent-install")'" "VERSION='$RUNTIME_VERSION'"
    cat "$_d/netcheck.sh.in"
  } >"$NETCHECK"
  chmod 0700 -- "$NETCHECK"
  sh -n "$NETCHECK" || cc_die "the netcheck script written does not parse: $NETCHECK"
fi

# The owner's page: the template of the kit with the real paths. A value goes
# into the sed expression with \, & and | escaped; the paths of a root hold no
# such character, and --bin is escaped here.
sed_value() { printf '%s' "$1" | sed 's/[\\&|]/\\&/g'; }
CHECKLIST=$ROOT/CHECKLIST.txt
CHECKLIST_TEMPLATE=checklist.txt.in
if [ "$PASS" = B ]; then CHECKLIST_TEMPLATE=checklist-b.txt.in; fi
[ -f "$_d/$CHECKLIST_TEMPLATE" ] || cc_die "the checklist template is missing: $_d/$CHECKLIST_TEMPLATE"
if [ -n "$MODEL" ]; then
  MODELNOTE="Model: $MODEL (--model); step 15 asks you to confirm it."
else
  MODELNOTE='Model: none chosen. Prepare a NEW root with --model NAME, or add "--model", "NAME" to agent.argv in config.json by hand (not pinned) and run preflight again.'
fi
sed \
  -e "s|@ROOT@|$(sed_value "$ROOT")|g" \
  -e "s|@REPO@|$(sed_value "$REPO")|g" \
  -e "s|@BIN@|$(sed_value "$BIN")|g" \
  -e "s|@CONFIG@|$(sed_value "$CONFIG")|g" \
  -e "s|@EVIDENCE@|$(sed_value "$ROOT/evidence")|g" \
  -e "s|@HINTS@|$(sed_value "$HINTFILE")|g" \
  -e "s|@HOLDER_RUN@|$(sed_value "$ROOT/runtime/leads/${CC_PROJECT}-lead")|g" \
  -e "s|@RUN@|$(sed_value "$_d/canary-run.sh")|g" \
  -e "s|@PREFLIGHT@|$(sed_value "$_d/canary-preflight.sh")|g" \
  -e "s|@RESET@|$(sed_value "$_d/canary-reset.sh")|g" \
  -e "s|@NETCHECK@|$(sed_value "$NETCHECK")|g" \
  -e "s|@MODELNOTE@|$(sed_value "$MODELNOTE")|g" \
  -e "s|@FOOTER@|manual|g" \
  "$_d/$CHECKLIST_TEMPLATE" >"$CHECKLIST"
chmod 0600 -- "$CHECKLIST"
if grep -q '@[A-Z_]*@' "$CHECKLIST"; then cc_die "the checklist template holds a placeholder that was not filled: $CHECKLIST"; fi

# The mark of this prepare, last: a prepare that failed is not reset.
printf '%s\nproject %s\n' "$CC_MARKER_LINE" "$CC_PROJECT" >"$ROOT/$CC_MARKER"
chmod 0600 -- "$ROOT/$CC_MARKER"

if [ "$PASS" = B ]; then
  cat <<EOF

Done. Nothing was started. Your one page, with these paths filled in:
     $CHECKLIST
What you type next, in the checkout ($REPO):

1. The mirror qualification of this root, as in pass A, with the lead present:
   set "i_run_this_on_purpose" to true in $ROOT/mirror-qual/config.json
   yourself (see docs/qualification-lead-mirror.md), then:
     sh scripts/go-tool.sh build -o build/qualify-mirror ./qualification/leadmirror/cmd/qualify-mirror
     build/qualify-mirror --config $ROOT/mirror-qual/config.json --out $ROOT/mirror-qual/report.json
   Then remove $ROOT/mirror-qual/state yourself. Or give the preflight
   --mirror-report FILE: an existing report of the same template.

2. The network and the executable, by hand (it starts pasta, bubblewrap, curl
   and, under a throwaway unit, claude --version; it prints PASS or FINDING):
     sh $NETCHECK

3. The preflight, which must say READY:
     sh qualification/canary/run/canary-preflight.sh --root $ROOT

4. The attended run (read its dry run first, add --dry):
     sh qualification/canary/run/canary-run.sh --pass B --config $CONFIG --evidence $ROOT/evidence
   Every word it asks for (yes, read, logged-in, ...) is yours.

A failed or finished attempt is put back with:
     sh qualification/canary/run/canary-reset.sh --root $ROOT
EOF
  exit 0
fi

cat <<EOF

Done. Nothing was started. Your one page, with these paths filled in:
     $CHECKLIST
What you type next, in the checkout ($REPO):

1. The mirror qualification is your own step. Read docs/qualification-lead-mirror.md
   and the template note, then set "i_run_this_on_purpose" to true in
   $ROOT/mirror-qual/config.json yourself, and run it with the lead present:
     sh scripts/go-tool.sh build -o build/qualify-mirror ./qualification/leadmirror/cmd/qualify-mirror
     build/qualify-mirror --config $ROOT/mirror-qual/config.json --out $ROOT/mirror-qual/report.json
   Then remove $ROOT/mirror-qual/state yourself.

2. The attended run (read its dry run first, add --dry):
     sh qualification/canary/run/canary-run.sh --pass A --config $CONFIG --evidence $ROOT/evidence
   At its path prompts it shows "(Enter for: PATH)" from $HINTFILE; a path you
   type wins, and every word it asks for (yes, read, integrate, ...) is yours.

A failed or finished attempt is put back with:
     sh qualification/canary/run/canary-reset.sh --root $ROOT
EOF
