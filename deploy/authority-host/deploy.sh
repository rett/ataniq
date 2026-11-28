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
#   --self-test          compute and print every command, and the ssh options,
#                        without running ah-release, ssh-add, rsync or ssh,
#                        and without AUTHORITY_HOST_SSH, a known-hosts file or
#                        an agent. It starts nothing and contacts nothing.
#
# The connection reads no ssh configuration of this host (-F /dev/null), so
# everything it needs comes from the owner's environment, read at run time and
# never from the repository:
#   AUTHORITY_HOST_SSH          USER@HOST of the authority host (a host name
#                               or an address; an ssh alias does not work)
#   AUTHORITY_HOST_KNOWN_HOSTS  the absolute path of a known-hosts file the
#                               owner keeps, holding the authority host's key
#   AUTHORITY_HOST_PORT         the ssh port, when it is not 22
# The host is never written to a file and never printed: the output says `the
# authority host`. Without the first two the script refuses (except for
# --self-test).
#
# The key is the agent's, never a key file. Every ssh, and the ssh of every
# rsync, runs with these options (SSH_OPTS below), then
# -o UserKnownHostsFile=FILE and, with a port, -o Port=N:
#   -F /dev/null                        no ssh configuration file at all: no
#                                       IdentityFile, IdentityAgent, ProxyJump,
#                                       Match or alias of the user's or the
#                                       system's applies
#   -o BatchMode=yes                    never ask for a password or passphrase
#   -o IdentityFile=none                no default key file (~/.ssh/id_*)
#   -o IdentitiesOnly=no                offer the agent's keys
#   -o IdentityAgent=SSH_AUTH_SOCK      only the agent of SSH_AUTH_SOCK, the
#                                       one checked below
#   -o PreferredAuthentications=publickey
#   -o PasswordAuthentication=no        no password
#   -o KbdInteractiveAuthentication=no  no keyboard-interactive login
#   -o GSSAPIAuthentication=no          no Kerberos
#   -o HostbasedAuthentication=no       no host key of this host
#   -o PKCS11Provider=none              no token library
#   -o ProxyCommand=none                a direct connection
#   -o ProxyJump=none
#   -o ControlMaster=no                 no shared connection: every use of
#   -o ControlPath=none                 the key is a new login, and so a new
#                                       confirmation on the laptop
#   -o ForwardAgent=no                  the agent goes no further than here
#   -o StrictHostKeyChecking=yes        only the host key of the known-hosts
#   -o GlobalKnownHostsFile=/dev/null   file the owner names
# so the only key it can log in with is one the agent of SSH_AUTH_SOCK holds:
# the owner's, forwarded from the laptop for this session and loaded there
# with `ssh-add -c` (a confirmation for each use) or held on a FIDO2 key (a
# touch for each use), and only to the host the owner's known-hosts file
# names. Except for --self-test the script refuses, before it runs anything
# else, when SSH_AUTH_SOCK is unset or empty, when the agent cannot be
# reached, or when it holds no key (`ssh-add -l`). It cannot tell a confirmed
# key from one that is not: --dry says what to look for; see DEPLOY.md, "The
# deploy key".
#
# POSIX sh only. Tools: ssh-add, rsync and ssh (not in --self-test), grep,
# sed, cd, pwd. OpenSSH 8.9 or newer (see DEPLOY.md).
#
# Exit status: 0 done; 1 a step failed (nothing later was run); 2 refused
# before anything was run (bad arguments, environment, agent, release or
# confirm; `ssh-add -l`, which only asks the agent for its list, is the one
# command such a refusal may follow).

set -eu
LC_ALL=C
export LC_ALL

DP_NAME=deploy.sh
# The ssh options of every connection (see the header); the known-hosts file
# and the port are added below. Each is one word without a glob character or a
# blank, so the unquoted expansion below splits it into exactly these words.
SSH_BASE='-F /dev/null -o BatchMode=yes -o IdentityFile=none -o IdentitiesOnly=no -o IdentityAgent=SSH_AUTH_SOCK -o PreferredAuthentications=publickey -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o GSSAPIAuthentication=no -o HostbasedAuthentication=no -o PKCS11Provider=none -o ProxyCommand=none -o ProxyJump=none -o ControlMaster=no -o ControlPath=none -o ForwardAgent=no -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null'
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

# valid_host TEXT: USER@HOST, a host name or an address; no option, no
# space, no quote.
valid_host() {
  case $1 in
    '' | *'
'*) return 1 ;;
  esac
  printf '%s\n' "$1" | grep -Eq '^[A-Za-z0-9_][A-Za-z0-9._-]*@[A-Za-z0-9][A-Za-z0-9.-]*$'
}

# valid_file TEXT: an absolute path of safe characters (one word for ssh),
# with no empty, . or .. part.
valid_file() {
  case $1 in
    '' | *'
'*) return 1 ;;
  esac
  printf '%s\n' "$1" | grep -Eq '^/[A-Za-z0-9._/-]*[A-Za-z0-9_-]$' || return 1
  case $1 in
    *//* | */./* | */../*) return 1 ;;
  esac
  return 0
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
valid_host "$HOST" || die "AUTHORITY_HOST_SSH must be USER@HOST (letters, digits, . _ -): the connection reads no ssh configuration, so an alias does not work"

# --- the known-hosts file and the port ------------------------------------------
if [ "$MODE" = self-test ]; then
  KNOWN_HOSTS=${AUTHORITY_HOST_KNOWN_HOSTS-/KNOWN-HOSTS-FILE}
else
  [ -n "${AUTHORITY_HOST_KNOWN_HOSTS-}" ] ||
    die "AUTHORITY_HOST_KNOWN_HOSTS is not set: set it to the absolute path of a known-hosts file you keep with the authority host's key (see DEPLOY.md, \"The deploy key\")"
  KNOWN_HOSTS=$AUTHORITY_HOST_KNOWN_HOSTS
fi
valid_file "$KNOWN_HOSTS" || die "AUTHORITY_HOST_KNOWN_HOSTS must be an absolute path of letters, digits, . _ / -"
if [ "$MODE" != self-test ]; then
  [ -f "$KNOWN_HOSTS" ] && [ ! -L "$KNOWN_HOSTS" ] || die "AUTHORITY_HOST_KNOWN_HOSTS must name a regular file (not a link): $KNOWN_HOSTS"
fi
SSH_OPTS="$SSH_BASE -o UserKnownHostsFile=$KNOWN_HOSTS"
if [ -n "${AUTHORITY_HOST_PORT-}" ]; then
  printf '%s\n' "$AUTHORITY_HOST_PORT" | grep -Eq '^[1-9][0-9]{0,4}$' && [ "$AUTHORITY_HOST_PORT" -le 65535 ] ||
    die "AUTHORITY_HOST_PORT must be a port number (1 to 65535)"
  SSH_OPTS="$SSH_OPTS -o Port=$AUTHORITY_HOST_PORT"
fi

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

# --- the agent (not in --self-test) -------------------------------------------
# The last refusal, so that a bad argument is refused before anything runs.
# `ssh-add -l` only asks the agent for its list of keys: it uses no key, so it
# asks for no confirmation. Its exit status is 0 for a list, 1 for an agent
# with no key, 2 for an agent it cannot reach.
if [ "$MODE" != self-test ]; then
  [ -n "${SSH_AUTH_SOCK-}" ] ||
    die "SSH_AUTH_SOCK is not set: the deploy uses the agent's key only, never a key file; forward the laptop's agent for this session (see DEPLOY.md, \"The deploy key\")"
  command -v ssh-add >/dev/null 2>&1 || die "ssh-add is not in PATH: it is needed to ask the agent for its keys"
  AGENT_RC=0
  ssh-add -l >/dev/null 2>&1 || AGENT_RC=$?
  case $AGENT_RC in
    0) ;;
    1) die "the agent of SSH_AUTH_SOCK holds no key: load the deploy key on the laptop with ssh-add -c (see DEPLOY.md, \"The deploy key\")" ;;
    *) die "cannot reach the agent of SSH_AUTH_SOCK (ssh-add -l exit $AGENT_RC): forward the laptop's agent for this session (see DEPLOY.md, \"The deploy key\")" ;;
  esac
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
RSYNC_SHOW="rsync -a ${RSYNC_NO}--delete --itemize-changes -e 'ssh $SSH_OPTS' -- $(q "$RELEASE_DIR/") <the authority host>:$(q "$RSYNC_DEST")"
SSH_SHOW="ssh $SSH_OPTS -- <the authority host> $(q "$REMOTE_CMD")"

say "ssh options: $SSH_OPTS"
if [ "$MODE" = self-test ]; then
  say "agent: SSH_AUTH_SOCK must be set and its agent hold a key (ssh-add -l); not checked by --self-test"
else
  say "agent: SSH_AUTH_SOCK is set and its agent holds a key"
fi
run_check
say "step 2: $RSYNC_SHOW"
case $MODE in
  self-test)
    say "step 3: $SSH_SHOW"
    say "self-test: computed the commands above; ah-release, ssh-add, rsync and ssh were not run"
    exit 0
    ;;
  dry)
    rsync -a -n --delete --itemize-changes -e "ssh $SSH_OPTS" -- "$RELEASE_DIR/" "$HOST:$RSYNC_DEST" ||
      fail "rsync -n failed (is $BASE there on the host, and owned by the deploying user? was the confirmation on the laptop refused?); nothing was copied"
    say "step 3, only with --go --confirm $ID: $SSH_SHOW"
    say "deploy: dry run, nothing was copied and nothing was switched"
    say "reminder: this dry run used the deploy key once (the ssh of rsync -n), and that use must have popped a confirmation on the laptop (a touch, with a FIDO2 key). If none popped, the key is not loaded with ssh-add -c: stop here, do not run --go, and load it again as DEPLOY.md says (\"The deploy key\"). --go uses it twice: the copy and the switch."
    ;;
  go)
    rsync -a --delete --itemize-changes -e "ssh $SSH_OPTS" -- "$RELEASE_DIR/" "$HOST:$RSYNC_DEST" ||
      fail "rsync failed; the switch was not run"
    say "step 3: $SSH_SHOW"
    # shellcheck disable=SC2086  # SSH_OPTS splits into its words, see above
    ssh $SSH_OPTS -- "$HOST" "$REMOTE_CMD" ||
      fail "the switch on the authority host failed or could not run; see DEPLOY.md, \"When a step fails\""
    say "deploy: release $ID switched; verify with ah-switch status on the host (see DEPLOY.md)"
    ;;
esac
