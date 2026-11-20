#!/bin/sh
# Prepare the set-up of pass A of the canary run: the roots, the throwaway
# mirror, the config, the sandbox inputs, the replay screens, the managed
# settings and the stand-in copy, and the profile digest. Read README.md
# beside this file first.
#
#   canary-prepare.sh [--root DIR] [--pass A] [--bin DIR] [--pasta PATH]
#                     [--bwrap PATH] [--build] [--template-review PATH]
#                     [--unit-report PATH] [--plan-summary PATH] [--dry]
#
# It never answers a prompt and starts no unit, no daemon, no sandbox and no
# agent: it makes folders and files, runs git on a throwaway repository,
# `ataniq-intake init` (which makes the mirror) and, to read the pin,
# `ataniq-canary profile-digest` and `check-inputs`. With --build it runs
# `make canary-build` first; without it the binaries must be built. It
# prints no secret. With --dry it prints what it would create and writes
# nothing: no command is run.
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
    '       --root DIR    the folder to make (default: ataniq-canary-a in your cache folder);' \
    '                     it must be new or empty, outside /tmp and outside the checkout' \
    '       --pass A      the pass (only A is prepared: pass B has a live config of its own)' \
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

[ "$PASS" = A ] || cc_die "only pass A is prepared; pass B uses a live agent and a config of its own (README.md, The config)"

if [ -z "$ROOT" ]; then
  _cache=${XDG_CACHE_HOME:-}
  if [ -z "$_cache" ]; then
    case ${HOME:-} in
      /*) _cache=$HOME/.cache ;;
      *) cc_die 'no --root, and neither XDG_CACHE_HOME nor an absolute HOME names the cache folder' ;;
    esac
  fi
  ROOT=$_cache/ataniq-canary-a
fi
cc_check_root "$ROOT"
case $REPO in
  *[!A-Za-z0-9._/+-]*) cc_die 'the path of the checkout has a character outside A-Z a-z 0-9 . _ / + -' ;;
esac

# A new root, or an empty folder that already exists.
if [ -d "$ROOT" ]; then
  if [ -n "$(find "$ROOT" -mindepth 1 -maxdepth 1 2>/dev/null)" ]; then
    cc_die "the root is not empty: $ROOT (prepare makes a new set-up; canary-reset.sh puts a failed one back)"
  fi
fi

# --bin is checked now unless --build will make it.
case $BIN in /*) ;; *) cc_die "--bin is not an absolute path: $BIN" ;; esac
if [ "$BUILD" -eq 0 ]; then cc_check_bin "$BIN"; fi

pasta_plan /usr/bin/pasta

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
  cc_say "[dry]   $CONFIG (0600): pass A, stand-in agent, runtime claude 2.1.325, footer_mode manual, inject true, limits 4 GiB, no swap, 512 tasks, 1 GiB floor, 50 percent"
  cc_say "[dry]   $INPUTS (0600), provider_host api.anthropic.com"
  cc_say "[dry]   $ROOT/conf/replay-screens (idle-before-paste as 1, collapsed-paste-footer-a as 2, report.txt)"
  cc_say "[dry]   $ROOT/conf/managed-settings.json and a no-op $ROOT/conf/guard-hook"
  cc_say "[dry]   $ROOT/agent-install/ataniq-canary-standin, a copy of the built stand-in"
  if [ "$PASTA_COPY" -eq 1 ]; then
    cc_say "[dry]   $PASTA_USE, a copy of $PASTA_SRC (the subject is then a copy of passt, not pasta itself)"
  else
    cc_say "[dry]   pasta: $PASTA_USE (used as it is)"
  fi
  cc_say "[dry]   run: ataniq-canary profile-digest, write it into holder.profile_digest; run: ataniq-canary check-inputs"
  cc_say "[dry]   $ROOT/mirror-qual/config.json for qualify-mirror, with the acknowledgement left false for you"
  cc_say "[dry]   $ROOT/answers.hint (0600): the paths the run suggests at its path prompts: mirror-report=$ROOT/mirror-qual/report.json, and the unit report, plan summary and template review when given"
  cc_say "[dry]   $ROOT/CHECKLIST.txt (0600): one page for you, from checklist.txt.in, with these paths filled in"
  cc_say "[dry]   $ROOT/$CC_MARKER, the mark that canary-reset.sh looks for"
  cc_say "[dry]   print the commands you type next"
  exit 0
fi

if [ "$BUILD" -eq 1 ]; then
  cc_say "building: make canary-build"
  make -C "$REPO" canary-build || cc_die 'make canary-build failed'
  cc_check_bin "$BIN"
fi

# the parents get 0700 from the umask
# shellcheck disable=SC2174
if [ ! -d "$ROOT" ]; then mkdir -p -m 0700 -- "$ROOT"; fi
chmod 0700 -- "$ROOT"
cc_say "making $ROOT"
mkdir -m 0700 -- "$ROOT/evidence" "$ROOT/conf" "$ROOT/agent-install" "$ROOT/bin"

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
  cp -- "$SCREENS/collapsed-paste-footer-a.$_x" "$ROOT/conf/replay-screens/2.$_x"
done
printf '%s\n' 'state=idle:app=claude-code' >"$ROOT/conf/replay-screens/report.txt"
chmod 0600 -- "$ROOT/conf/replay-screens/"*

printf '%s\n' '{"permissions":{"defaultMode":"default"}}' >"$ROOT/conf/managed-settings.json"
printf '%s\n' '#!/bin/sh' 'exit 0' >"$ROOT/conf/guard-hook"
chmod 0600 -- "$ROOT/conf/managed-settings.json"
chmod 0700 -- "$ROOT/conf/guard-hook"

cp -- "$BIN/ataniq-canary-standin" "$ROOT/agent-install/ataniq-canary-standin"
chmod 0700 -- "$ROOT/agent-install/ataniq-canary-standin"

if [ "$PASTA_COPY" -eq 1 ]; then
  cp -- "$PASTA_SRC" "$PASTA_USE"
  chmod 0700 -- "$PASTA_USE"
  cc_say "pasta: /usr/bin/pasta is a link, which the harness refuses; $PASTA_USE is a copy of the file it leads to ($PASTA_SRC), so the subject is a copy of passt, not pasta itself"
fi

SETTINGS_SHA=$(sha256sum <"$ROOT/conf/managed-settings.json" | awk '{print $1}')
ZERO=0000000000000000000000000000000000000000000000000000000000000000

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
    "kind": "stand-in",
    "runtime": "claude",
    "runtime_version": "2.1.325",
    "footer_mode": "manual",
    "argv": ["/ataniq/agent/ataniq-canary-standin"],
    "env": []
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
  "runtime": "claude", "runtime_version": "2.1.325",
  "executables": {"pasta": "$PASTA_USE", "bwrap": "$BWRAP", "probe": "$BIN/ataniq-probe"},
  "sources": {
    "agent_install": "$ROOT/agent-install",
    "managed_settings": "$LEAD/managed-settings.json",
    "managed_settings_sha256": "$SETTINGS_SHA",
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
  "terminal": {"term": "xterm-256color"},
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

# The owner's page: the template of the kit with the real paths. A value goes
# into the sed expression with \, & and | escaped; the paths of a root hold no
# such character, and --bin is escaped here.
sed_value() { printf '%s' "$1" | sed 's/[\\&|]/\\&/g'; }
CHECKLIST=$ROOT/CHECKLIST.txt
[ -f "$_d/checklist.txt.in" ] || cc_die "the checklist template is missing: $_d/checklist.txt.in"
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
  "$_d/checklist.txt.in" >"$CHECKLIST"
chmod 0600 -- "$CHECKLIST"
if grep -q '@[A-Z_]*@' "$CHECKLIST"; then cc_die "the checklist template holds a placeholder that was not filled: $CHECKLIST"; fi

# The mark of this prepare, last: a prepare that failed is not reset.
printf '%s\nproject %s\n' "$CC_MARKER_LINE" "$CC_PROJECT" >"$ROOT/$CC_MARKER"
chmod 0600 -- "$ROOT/$CC_MARKER"

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
