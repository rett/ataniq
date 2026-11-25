#!/bin/sh
# ap14-level1.sh: the read-only check of AP14 level 1 on an AI (development)
# host. It asks sudo itself what the everyday user may do and judges only
# what sudo prints, and it checks the root-owned standing-grants register
# (DL-1). It changes nothing: every change is the owner's, by hand, from the
# steps of README.md.
#
#   ap14-level1.sh --check --user NAME [--other-root-login]
#   ap14-level1.sh --self-test [--fixtures DIR --user NAME [--other-root-login]]
#   ap14-level1.sh --self-test --register-tree DIR
#
#   --check      read-only, best run as root (as another user, sudo -l -U and
#                the register may be closed to it: that is a stop). It runs
#                `sudo -n -l -U NAME` once and judges what sudo prints: a
#                NOPASSWD command; a targetpw, rootpw, runaspw, !authenticate
#                or exempt_group, global or command-specific; a non-zero
#                timestamp_timeout, global or command-specific; and no
#                timestamp_timeout=0 at all, are findings. It checks
#                /etc/ataniq/grants, every folder above it and every entry in
#                it by simple stat rules. It prints plain steps for the owner,
#                never an edit command.
#   --other-root-login
#                the owner's answer, with --check, when NAME has no sudo grant
#                at all: root can log in on this host another way, written
#                down by the owner. Without it that case is a stop that asks.
#   --self-test  unprivileged only (it refuses root). Judges the saved sudo -l
#                outputs and register listings of the cases in self-test/
#                beside the script, read in place, and compares each report
#                with the case's expected.txt. It never runs sudo, writes
#                nothing and runs nothing from the fixtures. With --fixtures
#                DIR --user NAME it prints the report of the one case DIR;
#                with --register-tree DIR it prints the register listing
#                --check would make if DIR were / (the tests use both).
#
# The user's name is an argument only; it is never written into this script.
#
# PATH is set to the system folders before the first external command. POSIX
# sh. Tools: awk, cat, env, id, stat, tr, and sudo for --check. Until the
# arguments are checked it runs no program; then only `id -u`.
#
# Exit status: 0 clean (no finding, no stop); 1 findings remain, for the
# owner to fix by hand; 2 refused before anything was read (bad arguments,
# root for --self-test, an unusable --fixtures folder); 3 stopped: the check
# could not see everything (a failed or unknown sudo output, an unreadable
# register, an unanswered question), so it is not clean.

# The first commands: from here on every program comes from the system folders.
# shellcheck disable=SC2016  # the awk programs are single-quoted on purpose
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
set -eu
LC_ALL=C
export LC_ALL
umask 077

ME=ap14-level1.sh
SYS_PATH=$PATH
REGISTER_PARENT=/etc/ataniq
REGISTER=$REGISTER_PARENT/grants

say() { printf '%s\n' "$*"; }
refuse() {
  printf '%s: refused: %s\n' "$ME" "$*" >&2
  exit 2
}
usage() {
  printf '%s\n' "usage: $ME --check --user NAME [--other-root-login]" \
    "       $ME --self-test [--fixtures DIR --user NAME [--other-root-login]]" \
    "       $ME --self-test --register-tree DIR" >&2
  exit 2
}

# valid_name TEXT: a login name: lowercase letter or _, then lowercase letters,
# digits, _ or -, at most 32 characters. No dot: the drop-in the owner writes
# is named after the user, and sudo skips a file of /etc/sudoers.d whose name
# holds one.
valid_name() {
  case $1 in
    [a-z_]*) ;;
    *) return 1 ;;
  esac
  case $1 in
    *[!a-z0-9_-]*) return 1 ;;
  esac
  [ ${#1} -le 32 ]
}

# --- arguments -----------------------------------------------------------------
# Every refusal here comes before anything is read or run on the host.
MODE=
NAME=
HAVE_USER=0
FIX_ARG=
HAVE_FIX=0
TREE_ARG=
HAVE_TREE=0
ROUTE=0
RT=
while [ $# -gt 0 ]; do
  case $1 in
    --check | --self-test)
      [ -z "$MODE" ] || [ "$MODE" = "${1#--}" ] || refuse "give one of --check and --self-test"
      MODE=${1#--}
      ;;
    --dry | --apply | --verify | --confirm)
      refuse "$1 is gone: the script only checks (--check); the owner writes the drop-in and the register by hand, as README.md says"
      ;;
    --user | --fixtures | --register-tree)
      [ $# -ge 2 ] || refuse "$1 needs a value"
      case $1 in
        --user)
          NAME=$2
          HAVE_USER=1
          ;;
        --fixtures)
          FIX_ARG=$2
          HAVE_FIX=1
          ;;
        *)
          TREE_ARG=$2
          HAVE_TREE=1
          ;;
      esac
      shift
      ;;
    --other-root-login) ROUTE=1 ;;
    -h | --help) usage ;;
    *) refuse "unknown argument (see --help)" ;;
  esac
  shift
done
[ -n "$MODE" ] || usage
if [ "$MODE" = check ] && { [ "$HAVE_FIX" = 1 ] || [ "$HAVE_TREE" = 1 ]; }; then
  refuse "--fixtures and --register-tree go with --self-test only"
fi
if [ "$HAVE_TREE" = 1 ]; then
  [ "$HAVE_FIX" = 0 ] && [ "$HAVE_USER" = 0 ] && [ "$ROUTE" = 0 ] || refuse "--register-tree goes alone with --self-test"
fi
if [ "$MODE" = self-test ] && [ "$HAVE_FIX" = 0 ]; then
  [ "$HAVE_USER" = 0 ] || refuse "--user goes with --fixtures in a self-test"
  [ "$ROUTE" = 0 ] || refuse "--other-root-login goes with --check, or with --fixtures in a self-test"
fi
if [ "$MODE" = check ] || [ "$HAVE_FIX" = 1 ]; then
  [ "$HAVE_USER" = 1 ] || refuse "--user NAME is required: the everyday user's login name"
  [ "$NAME" != root ] || refuse "--user must be the everyday user, not root"
  valid_name "$NAME" || refuse "--user must be a login name (lowercase letters, digits, _ -; no dot, which sudo skips in a drop-in's name)"
fi
if [ "$MODE" = self-test ]; then
  [ "$(id -u)" != 0 ] || refuse "--self-test runs unprivileged: run it as an ordinary user, never as root"
fi

# --- judging what sudo printed ---------------------------------------------------
#
# judge_sudo OUT ERR RC prints the judgement of `sudo -n -l -U NAME` from its
# standard output OUT, its error output ERR and its exit status RC: lines
# that start with check:, finding: KIND:, stop:, note: or question:, then a
# record "#kind KIND" for each kind of finding and "#tally FINDINGS STOPS".
#
# What sudo prints (the short form of sudo -l, as root, with LC_ALL=C); sudo
# may wrap a long line, continuing a Defaults line indented by 4 spaces and a
# command by 8:
#
#   Matching Defaults entries for NAME on HOST:
#       OPTION, OPTION, ...
#
#   Runas and Command-specific defaults for NAME:
#       Defaults!COMMAND OPTION, OPTION
#       Defaults>RUNAS OPTION
#
#   User NAME may run the following commands on HOST:
#       (RUNAS) [TAG: ...] COMMAND, COMMAND
#
# or, for a user with no grant, "User NAME is not allowed to run sudo on
# HOST." (older versions: "User NAME may not run sudo on HOST."). Any other
# line is a stop: this check judges only the forms it knows.
judge_sudo() {
  printf '%s\n' "$1" | J_ERR=$2 J_RC=$3 J_NAME=$NAME J_ROUTE=$ROUTE awk '
    function clean(s) { gsub(/[[:cntrl:]]/, "?", s); return s }
    function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
    function stop(msg) { print "stop: " clean(msg); nstop++ }
    function finding(kind, msg) { print "finding: " kind ": " clean(msg); nfind++; kinds[kind] = 1 }
    function starts(s, p) { return substr(s, 1, length(p)) == p }
    function lastword(s,   n, w) { n = split(s, w, /[ \t]+/); return n ? w[n] : "" }
    function numeric(v) { return v ~ /^-?[0-9]*\.?[0-9]*$/ && v ~ /[0-9]/ }
    function zero(v) { return v ~ /^0+\.?0*$/ || v ~ /^0*\.0+$/ }
    # option TOKEN WHERE GLOBAL judges one Defaults option.
    function option(t, where, global,   v) {
      if (t == "targetpw") { finding("otherpw", "sudo asks for the target user\047s password (usually root\047s), not " name "\047s: " where " has targetpw"); pw++ }
      else if (t == "rootpw") { finding("otherpw", "sudo asks for root\047s password, not " name "\047s: " where " has rootpw"); pw++ }
      else if (t == "runaspw") { finding("otherpw", "sudo asks for the password of the runas_default user, not " name "\047s: " where " has runaspw"); pw++ }
      else if (t == "!authenticate") { finding("noauth", "sudo asks no password at all: " where " has !authenticate"); pw++ }
      else if (t ~ /^exempt_group[+-]?=/) { finding("exempt", "members of that group are never asked for a password, and sudo -l does not say whether " name " is one: " where " has " t); pw++ }
      else if (t ~ /^timestamp_timeout[+-]?=/) {
        v = t
        sub(/^[^=]*=/, "", v)
        gsub(/"/, "", v)
        if (!numeric(v)) { stop("a timestamp_timeout this check does not know: " where " has " t); odd++ }
        else if (!zero(v)) { finding("timeout", "a cached ticket lets commands run unprompted after one password: " where " has " t); nonzero++ }
        else if (global) zeroes++
      }
      else if (t ~ /timestamp_timeout/) { stop("a timestamp_timeout this check does not know: " where " has " t); odd++ }
    }
    BEGIN {
      name = ENVIRON["J_NAME"]; err = ENVIRON["J_ERR"]; rc = ENVIRON["J_RC"]; route = ENVIRON["J_ROUTE"]
      md_head = "Matching Defaults entries for " name " on "
      cs_head = "Runas and Command-specific defaults for " name ":"
      pr_head = "User " name " may run the following commands on "
      no_head1 = "User " name " is not allowed to run sudo on "
      no_head2 = "User " name " may not run sudo on "
      sec = ""; ne = 0; md = ""; cs = ""
    }
    /^[ \t]*$/ { sec = ""; next }
    starts($0, md_head) && /:$/ { sec = "md"; next }
    $0 == cs_head { sec = "cs"; next }
    starts($0, pr_head) && /:$/ { sec = "pr"; prheads++; next }
    (starts($0, no_head1) || starts($0, no_head2)) && /\.$/ { nogrant++; sec = ""; next }
    sec == "md" && /^    [^ ]/ { s = trim($0); sub(/,$/, "", s); md = md "," s; next }
    sec == "cs" && /^    [^ ]/ { s = trim($0); sub(/,$/, "", s); cs = cs "," s; next }
    sec == "pr" && /^    \(/ { entry[++ne] = trim($0); next }
    sec == "pr" && /^        [^ ]/ && ne { entry[ne] = entry[ne] " " trim($0); next }
    { stop("a line of sudo -l this check does not know: " $0) }
    END {
      if (rc !~ /^[0-9]+$/ || (rc != 0 && !(nogrant && !prheads && err == "")))
        stop("sudo -n -l -U " name " failed (exit " rc ")")
      n = split(err, el, "\n")
      for (i = 1; i <= n; i++) if (el[i] != "") stop("sudo printed on its error output: " el[i])
      if (nogrant && prheads) stop("sudo -l -U " name " says both that " name " may run commands and that " name " is not allowed to run sudo")
      else if (rc == 0 && !nogrant && !prheads) stop("sudo -l -U " name " lists neither a grant nor that " name " is not allowed to run sudo")
      else if (prheads && ne == 0) stop("sudo -l -U " name " has a grant heading with no command under it")
      if (nogrant && !prheads) {
        if (route == 1) {
          print "note: no sudo grant: make sure root can log in another way"
          print "note: you answered that it can (--other-root-login), so " name " has no sudo route to root"
        } else {
          stop("no sudo grant: make sure root can log in another way")
          print "question: " name " has no sudo grant. How does root log in on this host without it (a root password at the console, another administrator account)? If there is such a way and you have written it down, run --check again with --other-root-login. If there is none, give " name " a grant that asks for the password (README, step 3) before you go on."
        }
      }
      if (prheads && ne) {
        np = 0
        for (i = 1; i <= ne; i++) if (entry[i] ~ /[ ,]NOPASSWD:/) { finding("nopasswd", "sudo runs this for " name " without a password: " entry[i]); np++ }
        if (!np) checks = checks "check: no command sudo lists for " name " skips the password\n"
        n = split(md, op, ",")
        for (i = 1; i <= n; i++) { t = lastword(trim(op[i])); if (t != "") option(t, "Matching Defaults", 1) }
        n = split(cs, op, ",")
        bind = ""
        for (i = 1; i <= n; i++) {
          s = trim(op[i])
          if (s == "") continue
          if (starts(s, "Defaults")) { bind = s; sub(/[ \t]+[^ \t]+$/, "", bind) }
          else if (bind == "") { stop("a command-specific default this check does not know: " s); continue }
          option(lastword(s), bind, 0)
        }
        if (!zeroes) finding("dropin", "sudo -l lists no timestamp_timeout=0 for " name ": sudo\047s built-in default keeps a ticket for some minutes, so commands run unprompted after one password")
        else if (!nonzero && !odd) checks = checks "check: timestamp_timeout=0 is listed for " name ", and no other value\n"
        if (!pw) checks = checks "check: no default makes sudo ask for another account\047s password, or for none (targetpw, rootpw, runaspw, !authenticate, exempt_group)\n"
      }
      # A check holds only for an output the check read whole.
      if (!nstop) printf "%s", checks
      for (k in kinds) print "#kind " k
      print "#tally " nfind + 0 " " nstop + 0
    }
  '
}

# --- the register -----------------------------------------------------------------
#
# A register listing has one line per path: KIND UID MODE PATH, where KIND is
# d (folder), f (regular file), l (link) or o (anything else), UID the
# owner's number and MODE the octal mode; or "missing - - PATH",
# "unreadable - - PATH", or "badname - - PATH" (an entry whose name has a
# character the check cannot print, shown as ?). It lists /, /etc,
# /etc/ataniq and /etc/ataniq/grants, ending at the first that is not a
# folder, then every entry of the register.

# reg_one PATH prints the listing line of PATH (below RT, the tree that
# stands for /: empty on the host), from one stat of it; a link is not
# followed.
reg_one() {
  if ro_s=$(stat -c '%u %a %F' -- "$RT$1" 2>/dev/null); then
    ro_u=${ro_s%% *}
    ro_s=${ro_s#* }
    ro_m=${ro_s%% *}
    case ${ro_s#* } in
      directory) ro_k=d ;;
      'regular file' | 'regular empty file') ro_k=f ;;
      'symbolic link') ro_k=l ;;
      *) ro_k=o ;;
    esac
    printf '%s %s %s %s\n' "$ro_k" "$ro_u" "$ro_m" "$1"
  elif [ -e "$RT$1" ] || [ -L "$RT$1" ]; then
    printf 'unreadable - - %s\n' "$1"
  else
    printf 'missing - - %s\n' "$1"
  fi
}

# reg_list prints the listing of the register of the tree RT (this host's
# when RT is empty).
reg_list() {
  rl_prev=
  for rl_p in / /etc "$REGISTER_PARENT" "$REGISTER"; do
    if [ -n "$rl_prev" ] && ! [ -x "$RT$rl_prev" ]; then
      printf 'unreadable - - %s\n' "$rl_prev"
      return 0
    fi
    rl_l=$(reg_one "$rl_p")
    printf '%s\n' "$rl_l"
    case $rl_l in
      d\ *) ;;
      *) return 0 ;;
    esac
    rl_prev=$rl_p
  done
  if ! [ -r "$RT$REGISTER" ] || ! [ -x "$RT$REGISTER" ]; then
    printf 'unreadable - - %s\n' "$REGISTER"
    return 0
  fi
  for rl_e in "$RT$REGISTER"/* "$RT$REGISTER"/.[!.]* "$RT$REGISTER"/..?*; do
    [ -e "$rl_e" ] || [ -L "$rl_e" ] || continue
    rl_n=${rl_e##*/}
    case $rl_n in
      *[![:print:]]*)
        printf 'badname - - %s/%s\n' "$REGISTER" "$(printf '%s' "$rl_n" | tr -c '[:print:]' '?')"
        continue
        ;;
    esac
    reg_one "$REGISTER/$rl_n"
  done
}

# judge_register LISTING prints the judgement of a register listing, in the
# form of judge_sudo.
judge_register() {
  printf '%s\n' "$1" | J_REG=$REGISTER awk '
    function clean(s) { gsub(/[[:cntrl:]]/, "?", s); return s }
    function stop(msg) { print "stop: " clean(msg); nstop++ }
    function finding(kind, msg) { print "finding: " kind ": " clean(msg); nfind++; kinds[kind] = 1 }
    function writable(m) { m = "00" m; return substr(m, length(m) - 1, 1) ~ /[2367]/ || substr(m, length(m), 1) ~ /[2367]/ }
    BEGIN {
      reg = ENVIRON["J_REG"]; parent = reg; sub(/\/[^\/]*$/, "", parent)
      folder["/"] = 1; folder["/etc"] = 1; folder[parent] = 1; folder[reg] = 1
    }
    /^$/ { next }
    {
      lines++
      kind = $1; uid = $2; mode = $3
      p = $0
      sub(/^[^ ]+ [^ ]+ [^ ]+ /, "", p)
      rest = substr(p, length(reg) + 2)
      entry = substr(p, 1, length(reg) + 1) == reg "/" && rest != "" && index(rest, "/") == 0
      if (kind == "missing" && (p == parent || p == reg)) { finding("register-missing", "there is no register: " p " is missing"); ended = 1; next }
      if (kind == "unreadable") { stop("the register cannot be read: " p " (run --check as root)"); next }
      if (kind == "badname" && entry) { stop("a register entry\047s name holds a character this check cannot print: " p); next }
      if (!(kind ~ /^[dflo]$/ && uid ~ /^[0-9]+$/ && mode ~ /^[0-7]+$/ && (p in folder || entry))) {
        stop("a register listing line this check cannot judge: " $0)
        next
      }
      if (p in folder) {
        if (kind != "d") { finding("register-folder", p " is not a folder of its own (" (kind == "l" ? "a link" : "a file or another kind") ")"); ended = 1 }
        if (uid != 0) finding("register-folder", p " is owned by uid " uid ", not root")
        if (kind != "l" && writable(mode)) finding("register-folder", p " is writable by its group or by others (mode " mode ")")
        if (p == reg && kind == "d") regdir = 1
      } else {
        entries++
        if (kind != "f") finding("register-entry", p " is not a regular file (" (kind == "l" ? "a link" : (kind == "d" ? "a folder" : "another kind")) ")")
        if (uid != 0) finding("register-entry", p " is owned by uid " uid ", not root")
        if (kind != "l" && writable(mode)) finding("register-entry", p " is writable by its group or by others (mode " mode ")")
      }
    }
    END {
      if (!lines) stop("the register listing is empty")
      else if (!regdir && !ended) stop("the register listing does not reach " reg)
      else if (!nfind && !nstop) print "check: " reg " and every folder above it are root\047s and not writable by group or others; its " entries + 0 " entries are regular files of root\047s, not writable by group or others"
      for (k in kinds) print "#kind " k
      print "#tally " nfind + 0 " " nstop + 0
    }
  '
}

# --- the report ---------------------------------------------------------------------

# quoted TEXT PREFIX prints each line of TEXT after PREFIX, a control
# character shown as ?.
quoted() {
  [ -z "$1" ] || printf '%s\n' "$1" | Q_P=$2 awk '{ gsub(/[[:cntrl:]]/, "?"); print ENVIRON["Q_P"] $0 }'
}

# steps KIND prints the owner's steps for a kind of finding, in prose.
steps() {
  case $1 in
    nopasswd) say "- NOPASSWD: as root, find the rule in /etc/sudoers or a file it includes (usually in /etc/sudoers.d), and edit that file with visudo -f; remove the NOPASSWD: tag; keep one password-asked grant for you. Change only the tag: do not delete the rule, which may hold other users, hosts or exclusions (README, step 3)." ;;
    otherpw) say "- targetpw, rootpw or runaspw: sudo asks for another account's password, so whoever knows that one runs commands as root, and your test of step 6 shows only that a password is asked, not whose. As root, edit the file that sets it with visudo -f and remove the setting (README, step 3)." ;;
    noauth) say "- !authenticate: sudo asks no password at all. As root, edit the file that sets it with visudo -f and remove the setting (README, step 3)." ;;
    exempt) say "- exempt_group: its members are never asked for a password. As root, edit the file that sets it with visudo -f and remove the setting, or make sure $NAME is not a member of that group (README, step 3)." ;;
    timeout) say "- a non-zero timestamp_timeout: after one password a cached ticket lets any process of $NAME run sudo unprompted, an agent session included. As root, edit the file that sets it with visudo -f and remove the setting (README, step 3); the drop-in of step 4 sets 0." ;;
    dropin) say "- no timestamp_timeout=0: write the drop-in /etc/sudoers.d/zz-ap14-level1-$NAME by hand and check the whole configuration with visudo -c (README, step 4)." ;;
    register-missing) say "- no register: create $REGISTER by hand, root's folders of mode 0755 (README, step 5)." ;;
    register-folder) say "- a register folder: it and every folder above it must be folders of root's, not links, and not writable by group or others. If one is not, make a new folder as root, write into it fresh copies of only the entries you confirm, and put it in place of the old one (README, step 5)." ;;
    register-entry) say "- a register entry: read it and confirm every word is yours. Then, as root, write that text into a new file of root's (mode 0644) beside it and rename the new file over the old name, which retires the old file: another process may still hold it open for writing. Never change the owner or the mode of the old file in place. Remove an entry that is not yours or not a regular file (README, step 5)." ;;
  esac
}

# report OUT ERR RC LISTING prints the whole report and returns 0 (clean), 1
# (findings) or 3 (stopped).
report() {
  rp_f=0
  rp_s=0
  rp_n=0
  rp_kinds=
  say "== sudo -n -l -U $NAME (exit $3)"
  quoted "$1" "  | "
  quoted "$2" "  ! "
  rp_j=$(judge_sudo "$1" "$2" "$3")
  printf '%s\n' "$rp_j" | awk '!/^#/'
  rp_all=$rp_j
  say "== register $REGISTER"
  quoted "$4" "  | "
  rp_j=$(judge_register "$4")
  printf '%s\n' "$rp_j" | awk '!/^#/'
  rp_all="$rp_all
$rp_j"
  for rp_t in $(printf '%s\n' "$rp_all" | awk '/^#tally [0-9]+ [0-9]+$/ { print $2 ":" $3 }'); do
    rp_n=$((rp_n + 1))
    rp_f=$((rp_f + ${rp_t%:*}))
    rp_s=$((rp_s + ${rp_t#*:}))
  done
  rp_kinds=$(printf '%s\n' "$rp_all" | awk '/^#kind / { print $2 }')
  if [ "$rp_f" -gt 0 ]; then
    say "== by hand, for the owner"
    for rp_k in nopasswd otherpw noauth exempt timeout dropin register-missing register-folder register-entry; do
      case "
$rp_kinds
" in
        *"
$rp_k
"*) steps "$rp_k" ;;
      esac
    done
  fi
  say "== summary"
  say "findings: $rp_f; stops: $rp_s"
  if [ "$rp_n" != 2 ] || [ "$rp_s" -gt 0 ]; then
    say "result: STOPPED: the check could not see everything, so it is not clean; fix what each stop says and run --check again"
    return 3
  elif [ "$rp_f" -gt 0 ]; then
    say "result: findings remain: fix them by hand, then run --check again"
    return 1
  fi
  say "result: clean: sudo itself shows no way for $NAME to skip the password, and the register is root's"
  return 0
}

# --- --check ------------------------------------------------------------------------

# sudo_query sets SQ_OUT, SQ_ERR and SQ_RC from one run of `sudo -n -l -U
# NAME` in a clean environment. Its error output arrives before the line
# X:RC and its standard output after it, each line marked O:; another line
# there, or not exactly one X: line, makes the judgement stop.
sudo_query() {
  if ! command -v sudo >/dev/null 2>&1; then
    SQ_OUT=
    SQ_ERR="sudo is not installed here (command -v sudo found nothing)"
    SQ_RC=127
    return 0
  fi
  sq_all=$(
    {
      if sq_o=$(env -i PATH="$SYS_PATH" LC_ALL=C sudo -n -l -U "$NAME" </dev/null 2>&3); then sq_r=0; else sq_r=$?; fi
      printf '\nX:%s\n' "$sq_r"
      [ -z "$sq_o" ] || printf '%s\n' "$sq_o" | awk '{ print "O:" $0 }'
    } 3>&1
  )
  sq_split "$sq_all"
}

# sq_split TEXT sets SQ_RC, SQ_ERR and SQ_OUT from the text sudo_query
# captured: RC is ? unless there is exactly one X: line, and a line after it
# without O: is kept, marked ?, so that the judgement stops on it.
sq_split() {
  SQ_RC=$(printf '%s\n' "$1" | awk '/^X:[0-9]+$/ { n++; v = substr($0, 3) } END { print (n == 1 ? v : "?") }')
  SQ_ERR=$(printf '%s\n' "$1" | awk '/^X:[0-9]+$/ { exit } NF { print }')
  SQ_OUT=$(printf '%s\n' "$1" | awk 'x && /^O:/ { print substr($0, 3); next } /^X:[0-9]+$/ { x = 1; next } x { print "?" $0 }')
}

check_main() {
  say "$ME --check --user $NAME: reads only; changes nothing"
  if [ "$(id -u)" != 0 ]; then
    say "note: not run as root: sudo -l -U and the register may be closed to this user, which is then a stop"
  fi
  sudo_query
  if report "$SQ_OUT" "$SQ_ERR" "$SQ_RC" "$(reg_list)"; then exit 0; else exit $?; fi
}

# --- self-test ----------------------------------------------------------------------

# fx_file DIR NAME [optional] prints the fixture file DIR/NAME, which must be
# a regular file, not a link; an optional one may be absent (then it prints
# nothing). Otherwise it returns 1 with the reason on standard error.
fx_file() {
  if [ -L "$1/$2" ]; then
    printf '%s\n' "$2 is a link; a fixture file must be a regular file of its case" >&2
    return 1
  fi
  if ! [ -e "$1/$2" ] && [ "${3-}" = optional ]; then return 0; fi
  if ! [ -f "$1/$2" ] || ! [ -r "$1/$2" ]; then
    printf '%s\n' "$2 is missing or not a readable regular file" >&2
    return 1
  fi
  cat -- "$1/$2"
}

# fx_report DIR prints the report of the case DIR, read in place, with its
# exit status as the last line. It returns 1, with the reason on standard
# error, when the case is not usable.
fx_report() {
  fr_out=$(fx_file "$1" sudo-l.txt) || return 1
  fr_err=$(fx_file "$1" sudo-l.err optional) || return 1
  fr_rc=$(fx_file "$1" sudo-l.rc optional) || return 1
  fr_reg=$(fx_file "$1" register.txt) || return 1
  case ${fr_rc:-0} in
    *[!0-9]*)
      printf '%s\n' "sudo-l.rc is not a number" >&2
      return 1
      ;;
  esac
  if fr_r=$(report "$fr_out" "$fr_err" "${fr_rc:-0}" "$fr_reg"); then fr_c=0; else fr_c=$?; fi
  printf '%s\nexit: %s\n' "$fr_r" "$fr_c"
}

# fx_root OPTION DIR refuses a folder of --fixtures or --register-tree that
# is the filesystem root, by its device and inode, whatever its spelling (//,
# /., a link to /).
fx_root() {
  fr_id=$(stat -L -c '%d:%i' -- "$2" 2>/dev/null) && [ -d "$2" ] || refuse "$1 must name a folder"
  fr_root=$(stat -L -c '%d:%i' -- /) || refuse "cannot stat /"
  [ "$fr_id" != "$fr_root" ] || refuse "$1 must not be the filesystem root (by any spelling or link): that is this host's own configuration"
}

# st_split NAME TEXT RC ERR OUT checks that sq_split reads TEXT as the exit
# status RC, the error output ERR and the standard output OUT.
st_split() {
  sq_split "$2"
  if [ "$SQ_RC" = "$3" ] && [ "$SQ_ERR" = "$4" ] && [ "$SQ_OUT" = "$5" ]; then return 0; fi
  say "self-test: FAIL capture $1: read exit $SQ_RC, error output [$SQ_ERR], output [$SQ_OUT]"
  return 1
}

self_test() {
  if [ "$HAVE_TREE" = 1 ]; then
    fx_root --register-tree "$TREE_ARG"
    RT=$TREE_ARG
    reg_list
    exit 0
  fi
  if [ "$HAVE_FIX" = 1 ]; then
    fx_root --fixtures "$FIX_ARG"
    st_o=$(fx_report "$FIX_ARG" 2>&1) || refuse "--fixtures: $st_o"
    printf '%s\n' "$st_o"
    case $st_o in
      *"
exit: 0") exit 0 ;;
      *"
exit: 1") exit 1 ;;
    esac
    exit 3
  fi
  case $0 in
    */*) st_dir=${0%/*} ;;
    *) st_dir=. ;;
  esac
  st_base=$st_dir/self-test
  [ -d "$st_base/clean" ] || {
    say "self-test: no case $st_base/clean: run the self-test from the checkout, with self-test/ beside the script"
    exit 1
  }
  st_fail=0
  # The capture of --check: error output, then X:RC, then O: lines.
  st_n=1
  st_ok=0
  st_split plain "sudo: a warning

X:1
O:User agent is not allowed to run sudo on aihost." 1 "sudo: a warning" "User agent is not allowed to run sudo on aihost." || st_ok=1
  st_split two-exits "X:0
X:1
O:a" "?" "" "a" || st_ok=1
  st_split stray "
X:0
O:a
b" 0 "" "a
?b" || st_ok=1
  st_split no-exit "O:a" "?" "O:a" "" || st_ok=1
  if [ "$st_ok" = 0 ]; then say "self-test: ok capture"; else st_fail=1; fi
  for st_case in "$st_base"/*/; do
    st_case=${st_case%/}
    st_c=${st_case##*/}
    st_n=$((st_n + 1))
    if ! st_user=$(fx_file "$st_case" user.txt 2>&1) || ! valid_name "$st_user" || [ "$st_user" = root ]; then
      say "self-test: FAIL $st_c: user.txt is not an everyday user's login name: $st_user"
      st_fail=1
      continue
    fi
    if ! st_args=$(fx_file "$st_case" args.txt optional 2>&1); then
      say "self-test: FAIL $st_c: $st_args"
      st_fail=1
      continue
    fi
    case $st_args in
      '' | --other-root-login) ;;
      *)
        say "self-test: FAIL $st_c: args.txt may hold only --other-root-login"
        st_fail=1
        continue
        ;;
    esac
    if ! st_want=$(fx_file "$st_case" expected.txt 2>&1); then
      say "self-test: FAIL $st_c: $st_want"
      st_fail=1
      continue
    fi
    if ! st_got=$(
      NAME=$st_user
      ROUTE=0
      [ -z "$st_args" ] || ROUTE=1
      fx_report "$st_case" 2>&1
    ); then
      say "self-test: FAIL $st_c: $st_got"
      st_fail=1
      continue
    fi
    if [ "$st_got" = "$st_want" ]; then
      say "self-test: ok $st_c"
    else
      say "self-test: FAIL $st_c: the report differs from expected.txt"
      say "--- expected.txt"
      printf '%s\n' "$st_want"
      say "--- the report"
      printf '%s\n' "$st_got"
      st_fail=1
    fi
  done
  if [ "$st_fail" = 0 ]; then
    say "self-test: ok, $st_n checks; it read only the fixtures, in place, never ran sudo, and wrote nothing"
    exit 0
  fi
  exit 1
}

# --- main ---------------------------------------------------------------------------

case $MODE in
  self-test) self_test ;;
  check) check_main ;;
esac
