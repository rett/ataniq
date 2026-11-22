#!/bin/sh
# deploy.sh: put a built authority-host release on the host and switch to it.
# Run on the development host, by the owner or with the owner's word.
#
#   deploy.sh --release DIR/ID [--incoming-base PATH] [--ah-release FILE]
#             [--skip-check]
#             [--dry | --go --confirm ID | --self-test]
#
#   --release DIR/ID     a release folder built by `ah-release build`; the
#                        folder's name is the release id (COUNTER-COMMIT, for
#                        example 0007-ab12cd3).
#   --incoming-base PATH a folder on the host that the deploying user owns,
#                        outside /opt/authority-host: absolute, or `~/NAME`
#                        for the user's home there. Default ~/incoming. It
#                        must exist; the release goes to PATH/ID/.
#   --ah-release FILE    the release tool, for `ah-release check`; default
#                        `ah-release` in PATH.
#   --skip-check         do not run `ah-release check` (prints a warning).
#                        The check is `ah-release check --no-noaicheck`: it
#                        reads the release folder back and never runs the
#                        release's no-AI check here, which would look at this
#                        development machine, not at the authority host.
#   --dry (the default)  check the release, run `rsync -n` (a listing of what
#                        would be copied, nothing copied) and print the ssh
#                        command that --go would run; run nothing else. It
#                        contacts the host, read-only (ssh and rsync -n); the
#                        host must be reachable and the incoming folder must
#                        exist. The remote rsync reads the destination; no
#                        files are copied and the switch is not run.
#   --go --confirm ID    check the release, copy it with rsync, then run the
#                        switch on the host:
#                          sudo /usr/local/sbin/ah-switch --from INCOMING/ID ID
#                        and stream its output. Refused unless ID given with
#                        --confirm is the release id. The switch itself runs
#                        the release's no-AI check, on the verified copy it
#                        imports, before anything changes; the driver never
#                        runs a file of the incoming folder.
#   --self-test          compute and print every command without running
#                        ah-release, rsync or ssh, and without AUTHORITY_HOST_SSH.
#                        It starts nothing and contacts nothing.
#
# The host is named only by the environment variable AUTHORITY_HOST_SSH
# (USER@HOST or an ssh alias), read at run time; it is never written to a file
# and never printed: the output says `the authority host`. Without the
# variable the script refuses (except for --self-test). ssh runs with
# `-o BatchMode=yes`: it never asks for a password or passphrase.
#
# POSIX sh only. Tools: rsync and ssh (not in --self-test), grep, sed, cd, pwd.
#
# Exit status: 0 done; 1 a step failed (nothing later was run); 2 refused
# before anything was run (bad arguments, environment, release or confirm).

set -eu
LC_ALL=C
export LC_ALL

DP_NAME=deploy.sh
say() { printf '%s\n' "$*"; }
die() {
  printf '%s: %s\n' "$DP_NAME" "$*" >&2
  exit 2
}
fail() {
  printf '%s: %s\n' "$DP_NAME" "$*" >&2
  exit 1
}
usage() {
  cat >&2 <<EOF
usage: $DP_NAME --release DIR/ID [--incoming-base PATH] [--ah-release FILE] [--skip-check]
                 [--dry | --go --confirm ID | --self-test]
EOF
  exit 2
}

# q ARG prints ARG as one shell word, single-quoted when it needs it.
q() {
  case $1 in
    '') printf "''" ;;
    *[!A-Za-z0-9._/:@=+,%-]*) printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
    *) printf '%s' "$1" ;;
  esac
}

# qline WORD... prints the words, each quoted, separated by spaces.
qline() {
  _sep=
  for _w in "$@"; do
    printf '%s' "$_sep"
    q "$_w"
    _sep=' '
  done
  printf '\n'
}

# valid_id TEXT: the release id of the README, COUNTER-COMMIT: a counter of at
# least four digits, a hyphen, and 7 to 40 lowercase hex digits.
valid_id() {
  case $1 in
    '' | *'
'*) return 1 ;;
  esac
  printf '%s\n' "$1" | grep -Eq '^[0-9]{4,}-[0-9a-f]{7,40}$'
}

# valid_host TEXT: USER@HOST or an alias; no option, no space, no quote.
valid_host() {
  case $1 in
    '' | *'
'*) return 1 ;;
  esac
  printf '%s\n' "$1" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._@-]*$'
}

# valid_base TEXT: an absolute path, or ~/PATH, of safe characters, with no
# empty, . or .. part, and outside /opt/authority-host (the switch refuses an
# incoming folder that overlaps releases/, but the driver never aims there).
valid_base() {
  case $1 in
    '' | *'
'*) return 1 ;;
  esac
  printf '%s\n' "$1" | grep -Eq '^(/|~/)[A-Za-z0-9._/-]*[A-Za-z0-9_-]$' || return 1
  case $1 in
    */ | *//* | */./* | */../* | */. | */..) return 1 ;;
    /opt/authority-host | /opt/authority-host/*) return 1 ;;
  esac
  return 0
}

MODE=
RELEASE=
# shellcheck disable=SC2088  # the literal ~/ is the home folder on the host, not here
BASE='~/incoming'
AHREL=
SKIP=0
CONFIRM=
HAVE_CONFIRM=0
while [ $# -gt 0 ]; do
  case $1 in
    --dry | --go | --self-test)
      [ -z "$MODE" ] || [ "$MODE" = "${1#--}" ] || usage
      MODE=${1#--}
      ;;
    --skip-check) SKIP=1 ;;
    --release | --incoming-base | --ah-release | --confirm)
      [ $# -ge 2 ] || usage
      case $1 in
        --release) RELEASE=$2 ;;
        --incoming-base) BASE=$2 ;;
        --ah-release) AHREL=$2 ;;
        --confirm)
          CONFIRM=$2
          HAVE_CONFIRM=1
          ;;
      esac
      shift
      ;;
    *) usage ;;
  esac
  shift
done
MODE=${MODE:-dry}

# --- the host -----------------------------------------------------------------
if [ "$MODE" = self-test ]; then
  HOST=${AUTHORITY_HOST_SSH-self-test@authority-host.invalid}
else
  [ -n "${AUTHORITY_HOST_SSH-}" ] ||
    die "AUTHORITY_HOST_SSH is not set: set it to USER@HOST (or an ssh alias) of the authority host"
  HOST=$AUTHORITY_HOST_SSH
fi
valid_host "$HOST" || die "AUTHORITY_HOST_SSH must be USER@HOST or an ssh alias (letters, digits, . _ @ -)"

# --- the release --------------------------------------------------------------
[ -n "$RELEASE" ] || die "--release DIR/ID is required"
while [ "${RELEASE%/}" != "$RELEASE" ]; do RELEASE=${RELEASE%/}; done
[ -n "$RELEASE" ] || die "--release must name a folder"
ID=${RELEASE##*/}
valid_id "$ID" || die "the release folder's name must be a release id (COUNTER-COMMIT, e.g. 0007-ab12cd3), not: $ID"
[ -d "$RELEASE" ] && [ ! -L "$RELEASE" ] || die "--release must name a real folder (not a link): $RELEASE"
RELEASE_DIR=$(cd -P -- "$RELEASE" && pwd -P) || die "cannot enter the release folder: $RELEASE"
[ -f "$RELEASE_DIR/MANIFEST" ] && [ ! -L "$RELEASE_DIR/MANIFEST" ] ||
  die "no MANIFEST in $RELEASE_DIR: not a built release"
grep -Eq "^id:[[:space:]]*${ID}[[:space:]]*\$" "$RELEASE_DIR/MANIFEST" ||
  die "the MANIFEST of $RELEASE_DIR does not say id: $ID"

# --- the incoming folder ------------------------------------------------------
valid_base "$BASE" || die "--incoming-base must be an absolute path or ~/NAME of safe characters, outside /opt/authority-host: $BASE"
# shellcheck disable=SC2088  # a literal ~/ prefix, as valid_base accepts it
case $BASE in
  '~/'*)
    REL=${BASE#'~/'}
    RSYNC_DEST=$REL/$ID/
    REMOTE_FROM="\"\$HOME/$REL/$ID\""
    ;;
  *)
    RSYNC_DEST=$BASE/$ID/
    REMOTE_FROM=$(q "$BASE/$ID")
    ;;
esac
# The remote shell reads this string. Every word in it is of the characters
# valid_id and valid_base allow (nothing the shell treats specially), or is
# the double-quoted "$HOME/..." that the remote shell expands to the deploying
# user's home; q quotes a word only when it has any other character.
REMOTE_CMD="sudo /usr/local/sbin/ah-switch --from $REMOTE_FROM $(q "$ID")"

# --- confirm (--go only) ------------------------------------------------------
if [ "$MODE" = go ]; then
  [ "$HAVE_CONFIRM" = 1 ] || die "--go needs --confirm $ID"
  [ "$CONFIRM" = "$ID" ] || die "--confirm must equal the release id $ID"
fi

# --- ah-release ---------------------------------------------------------------
if [ "$SKIP" = 0 ]; then
  if [ -z "$AHREL" ]; then
    if [ "$MODE" = self-test ]; then
      AHREL=ah-release
    else
      AHREL=$(command -v ah-release) ||
        die "ah-release is not in PATH: build it, or pass --ah-release FILE (or --skip-check)"
    fi
  elif [ "$MODE" != self-test ]; then
    [ -f "$AHREL" ] && [ -x "$AHREL" ] || die "--ah-release must name an executable file: $AHREL"
  fi
fi

# --- plan ---------------------------------------------------------------------
say "deploy: release $ID, mode $MODE, to the authority host"
say "release folder: $RELEASE_DIR"
say "incoming folder on the host: $BASE/$ID"

run_check() {
  if [ "$SKIP" = 1 ]; then
    printf '%s: warning: --skip-check: the release was NOT checked by ah-release check\n' "$DP_NAME" >&2
    return 0
  fi
  say "step 1: $(qline "$AHREL" check --no-noaicheck "$RELEASE_DIR")"
  if [ "$MODE" = self-test ]; then
    return 0
  fi
  "$AHREL" check --no-noaicheck "$RELEASE_DIR" || fail "ah-release check failed; nothing was copied"
}

RSYNC_NO=
[ "$MODE" = go ] || RSYNC_NO='-n '
RSYNC_SHOW="rsync -a ${RSYNC_NO}--delete --itemize-changes -e 'ssh -o BatchMode=yes' -- $(q "$RELEASE_DIR/") <the authority host>:$(q "$RSYNC_DEST")"
SSH_SHOW="ssh -o BatchMode=yes -- <the authority host> $(q "$REMOTE_CMD")"

run_check
say "step 2: $RSYNC_SHOW"
case $MODE in
  self-test)
    say "step 3: $SSH_SHOW"
    say "self-test: computed the commands above; ah-release, rsync and ssh were not run"
    exit 0
    ;;
  dry)
    rsync -a -n --delete --itemize-changes -e 'ssh -o BatchMode=yes' -- "$RELEASE_DIR/" "$HOST:$RSYNC_DEST" ||
      fail "rsync -n failed (is $BASE there on the host, and owned by the deploying user?); nothing was copied"
    say "step 3, only with --go --confirm $ID: $SSH_SHOW"
    say "deploy: dry run, nothing was copied and nothing was switched"
    ;;
  go)
    rsync -a --delete --itemize-changes -e 'ssh -o BatchMode=yes' -- "$RELEASE_DIR/" "$HOST:$RSYNC_DEST" ||
      fail "rsync failed; the switch was not run"
    say "step 3: $SSH_SHOW"
    ssh -o BatchMode=yes -- "$HOST" "$REMOTE_CMD" ||
      fail "the switch on the authority host failed or could not run; see DEPLOY.md, \"When a step fails\""
    say "deploy: release $ID switched; verify with ah-switch status on the host (see DEPLOY.md)"
    ;;
esac
