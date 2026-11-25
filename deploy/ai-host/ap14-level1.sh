#!/bin/sh
# ap14-level1.sh: AP14 level 1 on an AI (development) host. The everyday user
# loses cached sudo credentials, and every passwordless route from that user
# to root that the script can find is reported, with the exact edit for the
# owner to make by hand. It also creates the root-owned standing-grants
# register (DL-1).
#
#   ap14-level1.sh --dry --user NAME
#   ap14-level1.sh --apply --user NAME --confirm NAME
#   ap14-level1.sh --verify --user NAME
#   ap14-level1.sh --self-test [--fixtures DIR --user NAME]
#
#   --dry        a read-only audit, as root or not (unreadable parts are stops).
#                It reads the sudo, polkit and doas configuration, the user's
#                groups, the setuid files and the folders root runs things
#                from, and prints: every sudo rule, who it gives sudo and
#                whether it asks for a password; for each rule that gives NAME
#                sudo without a password, the edit for the owner to make by
#                hand with visudo; what --apply would write; every stop; every
#                other route to root. Changes nothing.
#   --apply      run as root, with --confirm repeating the --user name. Under a
#                lock it reads everything again and refuses while the audit
#                lists a stop or a by-hand edit. Otherwise it writes only NEW
#                files, never moving, editing or removing an existing one:
#                  1. the drop-in /etc/sudoers.d/zz-ap14-level1-NAME,
#                     `Defaults:NAME timestamp_timeout=0` (no cached
#                     credentials): a temporary file, mode 0440, owner root,
#                     checked alone with `visudo -cf`, then published under
#                     its name with ln (which never replaces a file);
#                  2. the standing-grants register /etc/ataniq/grants and its
#                     README, when they are not there yet;
#                then checks the whole configuration with `visudo -c`. A
#                failed step removes the files this run created, newest
#                first, and says which it could not remove. Then --verify.
#   --verify     read-only, as root: `sudo -l -U NAME` must still list a grant
#                that asks for the password, nothing without one, and
#                timestamp_timeout=0; `sudo -n true` run as NAME must say that
#                a password is required (not that NAME is not allowed, and
#                not a probe that could not run).
#   --self-test  unprivileged only (it refuses root). Reads only the fixture
#                folders in self-test/ beside the script, never following a
#                link out of them, and runs nothing from them: it compares the
#                audit of each with its expected.txt, and runs --apply against
#                a temporary copy of the transaction cases, with a simulated
#                visudo and a failure at every step in turn. With --fixtures
#                DIR it prints the audit of the fake root DIR (the tests use
#                it).
#
# The script never changes a group, a polkit rule, doas, a setuid file, a
# permission or an existing file: it reports them, each with a remedy.
#
# The user's name is an argument only; it is never written into this script.
#
# PATH is set to the system folders before the first external command; the
# caller's PATH is kept only to report root's PATH. POSIX sh. Tools: awk, sed,
# grep, sort, cut, head, ls, find, id, getent, mktemp, chmod, chown, ln, rm,
# mkdir, rmdir, cmp, cksum, cp and realpath (--self-test); visudo and sudo for
# the sudo steps; runuser (or su) for the `sudo -n` check; passwd -S; pacman,
# dpkg or rpm when present; getfacl when present. Before the root check it
# runs no program but `id -u`.
#
# Exit status: 0 clean (--dry: nothing to write, no by-hand edit, no stop, no
# finding; --apply: written or nothing to write, and the checks pass;
# --verify: every check passes); 1 something remains (--dry: something to
# write, an edit, a stop or a finding; --apply: a failed step whose files were
# removed, or a failed check; --verify: a failed check); 2 refused before
# anything was written (bad arguments, not root, the lock is held, or a stop
# or a by-hand edit in the audit).

# The first commands: from here on every program comes from the system folders.
# shellcheck disable=SC2016  # the awk programs are single-quoted on purpose
CALLER_PATH=${PATH-}
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
set -eu
LC_ALL=C
export LC_ALL
umask 077

ME=ap14-level1.sh
TAB=$(printf '\t')
NL='
'

SUDOERS=/etc/sudoers
SUDOERS_D=/etc/sudoers.d
DROPIN_PREFIX=zz-ap14-level1-
TMP_PREFIX=.ap14-level1.tmp.
REGISTER_PARENT=/etc/ataniq
REGISTER=$REGISTER_PARENT/grants
LOCK=/run/ap14-level1.lock
POLKIT_ETC=/etc/polkit-1/rules.d
POLKIT_USR=/usr/share/polkit-1/rules.d
DOAS_FILES="/etc/doas.conf /usr/local/etc/doas.conf"
SYS_PATH=$PATH
STD_PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
# Root runs what these hold (searched through, links followed).
WRITE_TREES="/etc/sudoers /etc/sudoers.d /etc/systemd/system /usr/lib/systemd/system
/etc/crontab /etc/anacrontab /etc/cron.d /etc/cron.hourly /etc/cron.daily
/etc/cron.weekly /etc/cron.monthly /etc/polkit-1/rules.d /etc/pam.d
/etc/ld.so.preload /etc/ld.so.conf /etc/ld.so.conf.d /etc/doas.conf
/etc/profile /etc/profile.d"
# Only the folder itself and what is directly in it.
WRITE_FLAT=/root
# Only the path itself (a user's own crontab sits beside root's).
WRITE_EXACT="/var/spool/cron /var/spool/cron/crontabs /var/spool/cron/root
/var/spool/cron/crontabs/root"

say() { printf '%s\n' "$*"; }
refuse() {
  printf '%s: refused: %s\n' "$ME" "$*" >&2
  exit 2
}
# Up to the root check the script runs no program but `id -u`: usage and
# valid_name use only the shell.
usage() {
  printf '%s\n' "usage: $ME --dry --user NAME" \
    "       $ME --apply --user NAME --confirm NAME" \
    "       $ME --verify --user NAME" \
    "       $ME --self-test [--fixtures DIR --user NAME]" >&2
  exit 2
}

# valid_name TEXT: a login name: lowercase letter or _, then lowercase letters,
# digits, _ or -, at most 32 characters. No dot: sudo skips a file of
# /etc/sudoers.d whose name holds one, and the drop-in is named after the user.
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

# sq TEXT prints TEXT as one single-quoted shell word.
sq() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }

# --- arguments -----------------------------------------------------------------
# Every refusal here comes before anything is read or run on the host.
MODE=
NAME=
HAVE_USER=0
CONFIRM=
HAVE_CONFIRM=0
FIX_ARG=
while [ $# -gt 0 ]; do
  case $1 in
    --dry | --apply | --verify | --self-test)
      [ -z "$MODE" ] || [ "$MODE" = "${1#--}" ] || refuse "give one of --dry, --apply, --verify and --self-test"
      MODE=${1#--}
      ;;
    --user | --confirm | --fixtures)
      [ $# -ge 2 ] || refuse "$1 needs a value"
      case $1 in
        --user)
          NAME=$2
          HAVE_USER=1
          ;;
        --confirm)
          CONFIRM=$2
          HAVE_CONFIRM=1
          ;;
        --fixtures) FIX_ARG=$2 ;;
      esac
      shift
      ;;
    -h | --help) usage ;;
    *) refuse "unknown argument (see --help)" ;;
  esac
  shift
done
[ -n "$MODE" ] || usage
if [ "$MODE" != self-test ] && [ -n "$FIX_ARG" ]; then
  refuse "--fixtures goes with --self-test only"
fi
if [ "$MODE" != apply ] && [ "$HAVE_CONFIRM" = 1 ]; then
  refuse "--confirm goes with --apply only"
fi
if [ "$MODE" = self-test ] && [ -z "$FIX_ARG" ] && [ "$HAVE_USER" = 1 ]; then
  refuse "--user goes with --fixtures in a self-test"
fi
if [ "$MODE" != self-test ] || [ -n "$FIX_ARG" ]; then
  [ "$HAVE_USER" = 1 ] || refuse "--user NAME is required: the everyday user's login name"
  [ "$NAME" != root ] || refuse "--user must be the everyday user, not root"
  valid_name "$NAME" || refuse "--user must be a login name (lowercase letters, digits, _ -; no dot, which sudo skips in a drop-in's name)"
fi
if [ "$MODE" = apply ]; then
  [ "$HAVE_CONFIRM" = 1 ] || refuse "--apply needs --confirm NAME, repeating the --user name"
  [ "$CONFIRM" = "$NAME" ] || refuse "--confirm must repeat the --user name exactly"
fi
case $MODE in
  apply | verify)
    [ "$(id -u)" = 0 ] || refuse "--$MODE runs as root (log in as root, or sudo -i, then run it)"
    ;;
  self-test)
    [ "$(id -u)" != 0 ] || refuse "--self-test runs unprivileged: run it as an ordinary user, never as root"
    ;;
esac

# FIX is the fake root of a self-test; empty on the host. hp PATH prints where
# PATH is read from (and, in a self-test transaction, written to).
FIX=
hp() { printf '%s%s' "$FIX" "$1"; }
# TX_TEST is 1 while a self-test runs --apply against a temporary copy: visudo
# is simulated, chown is skipped, and step TX_FAIL_AT fails (TX_UNDO_FAIL: the
# undo fails too).
TX_TEST=0
TX_FAIL_AT=0
TX_UNDO_FAIL=0
TX_N=0

# set_user NAME: the drop-in of that user.
set_user() {
  NAME=$1
  DROPIN_NAME=$DROPIN_PREFIX$NAME
  DROPIN=$SUDOERS_D/$DROPIN_NAME
}
if [ -n "$NAME" ]; then set_user "$NAME"; fi

# --- awk programs ----------------------------------------------------------------

# USER_AWK reads P:passwd and G:group lines and prints the user's uid, gids and
# group names (the primary group, every group listing the user, and the names
# in the variable extra, from id -Gn).
USER_AWK='
function add(n, g) {
  if (n != "" && n !~ /^[A-Za-z0-9_][A-Za-z0-9_.-]*[$]?$/) return
  if (n != "" && (n in hasn)) return
  if (n == "" && (g in hasg)) return
  if (n != "") { hasn[n] = 1; names = names " " n }
  if (g != "" && !(g in hasg)) { hasg[g] = 1; gids = gids " " g }
}
BEGIN { FS = ":" }
/^P:/ { sub(/^P:/, ""); if ($1 == u && !found) { uid = $3; pg = $4; found = 1 }; next }
/^G:/ { sub(/^G:/, ""); n++; gn[n] = $1; gg[n] = $3; gm[n] = "," $4 ","; if (!($1 in byname)) byname[$1] = $3; next }
END {
  if (!found) exit 0
  for (i = 1; i <= n; i++) if (gg[i] == pg || index(gm[i], "," u ",")) add(gn[i], gg[i])
  m = split(extra, x, " ")
  for (i = 1; i <= m; i++) add(x[i], (x[i] in byname) ? byname[x[i]] : "")
  if (!(pg in hasg)) add("", pg)
  print "uid " uid
  print "gids" gids
  print "groups" names
}'

# LINES_AWK turns one sudoers file into records: L LINE TEXT for a logical
# line (continuations joined, comments and blank lines dropped, blanks
# squeezed), I LINE PATH for an include, D LINE PATH for an includedir.
LINES_AWK='
function emit(t, ln,   s, out, i, nx, k, p) {
  gsub(/\t/, " ", t)
  s = t
  sub(/^[ ]+/, "", s)
  if (s ~ /^[@#]include(dir)?[ ]/) {
    k = (s ~ /^[@#]includedir/) ? "D" : "I"
    p = s
    sub(/^[@#]include(dir)?[ ]+/, "", p)
    sub(/[ ]+$/, "", p)
    if (p ~ /^".*"$/) p = substr(p, 2, length(p) - 2)
    print k "\t" ln "\t" p
    return
  }
  out = ""
  while ((i = index(s, "#")) > 0) {
    nx = substr(s, i + 1, 1)
    if (nx ~ /[0-9]/) { out = out substr(s, 1, i); s = substr(s, i + 1); continue }
    s = substr(s, 1, i - 1)
    break
  }
  s = out s
  gsub(/[ ]+/, " ", s)
  sub(/^ /, "", s)
  sub(/ $/, "", s)
  if (s != "") print "L\t" ln "\t" s
}
{
  sub(/\r$/, "")
  if (cont != "") line = cont " " $0
  else { line = $0; start = NR }
  if (line ~ /\\$/) { cont = substr(line, 1, length(line) - 1); next }
  cont = ""
  emit(line, start)
}
END { if (cont != "") emit(cont, start) }'

# SUDO_AWK reads the expanded record stream of /etc/sudoers (see
# sudoers_expand) and prints, for the user (u, uid, groups, gids):
#   RULE FILE LINE WHO APPLIES PW AS ALLPW TEXT  every user rule: WHO its
#        user list; APPLIES yes, maybe (a netgroup or non-Unix group may hold
#        the user) or no; PW asked, none (no command asks for a password) or
#        mixed; AS root or other, where the passwordless commands run (- when
#        there are none); ALLPW 1 when it lets the user run ALL as root with
#        the password
#   NOAUTH FILE LINE APPLIES WHO TEXT    Defaults !authenticate
#   EXEMPT FILE LINE GROUP APPLIES TEXT  Defaults exempt_group (its members
#                                        never type a password)
#   LATE FILE LINE VALUE TEXT    a non-zero timestamp_timeout after the place
#                                of the drop-in
#   CMDTO FILE LINE VALUE TEXT   a non-zero timestamp_timeout of a command or
#                                runas Defaults (Defaults!CMND, Defaults>USER),
#                                which sudo applies last, wherever it sits
#   WARN FILE LINE TEXT          a part sudo reads that the script could not
#   PLACE 0|1                    whether the drop-in would be read
#   LINES FILE COUNT VIA         content lines of each file read
SUDO_AWK='
function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
function defalias(A, s,   n, p, i, cur, e) {
  n = split(s, p, ":")
  cur = ""
  for (i = 1; i <= n; i++) {
    if (p[i] ~ /^ *[A-Z][A-Z0-9_]* *=/) {
      e = index(p[i], "=")
      cur = trim(substr(p[i], 1, e - 1))
      A[cur] = trim(substr(p[i], e + 1))
    } else if (cur != "") A[cur] = A[cur] ":" p[i]
  }
}
function uitem(it, d) {
  gsub(/"/, "", it)
  if (it == "ALL" || it == u || it == "#" uid) return 1
  if (substr(it, 1, 2) == "%:") return 2
  if (substr(it, 1, 2) == "%#") return (substr(it, 3) in GID) ? 1 : 0
  if (substr(it, 1, 1) == "%") return (substr(it, 2) in G) ? 1 : 0
  if (substr(it, 1, 1) == "+") return 2
  if ((it in UA) && d < 16) return ulist(UA[it], d + 1)
  return 0
}
function ulist(l, d,   n, a, i, it, neg, r, res) {
  n = split(l, a, ",")
  res = 0
  for (i = 1; i <= n; i++) {
    it = trim(a[i])
    neg = 0
    while (substr(it, 1, 1) == "!") { neg = !neg; it = trim(substr(it, 2)) }
    if (it == "") continue
    r = uitem(it, d)
    if (r == 1) res = neg ? 0 : 1
    else if (r == 2 && !neg && res != 1) res = 2
  }
  return res
}
function rlist(l, d,   n, a, i, it) {
  n = split(l, a, ",")
  for (i = 1; i <= n; i++) {
    it = trim(a[i])
    while (substr(it, 1, 1) == "!") it = trim(substr(it, 2))
    if (it == "ALL" || it == "root" || it == "#0") return 1
    if ((it in RA) && d < 16 && rlist(RA[it], d + 1)) return 1
  }
  return 0
}
function asroot(r,   c, ru) {
  c = index(r, ":")
  ru = trim((c > 0) ? substr(r, 1, c - 1) : r)
  if (ru == "") return (c > 0) ? 0 : 1
  return rlist(ru, 0)
}
# rhs RHS sets RN (a command runs without a password), RP (one asks for it),
# RNROOT (a passwordless one runs as root) and RALL (ALL as root, asking).
# A runas spec applies to the commands after it, a tag until the opposite
# tag; both start again at each host part (": host ="). A command after "!"
# is an exclusion, which grants nothing.
function rhs(s,   t, ng, grp, gi, g, m, parts, pi, piece, state, rt, tok) {
  t = ""
  while (match(s, /\([^)]*\)/)) {
    t = t substr(s, 1, RSTART - 1) (asroot(substr(s, RSTART + 1, RLENGTH - 2)) ? " @@R1@@ " : " @@R0@@ ")
    s = substr(s, RSTART + RLENGTH)
  }
  s = t s
  gsub(/NOPASSWD[ ]*:/, " @@N@@ ", s)
  gsub(/PASSWD[ ]*:/, " @@P@@ ", s)
  gsub(/(NOEXEC|EXEC|NOSETENV|SETENV|NOLOG_INPUT|LOG_INPUT|NOLOG_OUTPUT|LOG_OUTPUT|NOMAIL|MAIL|NOFOLLOW|FOLLOW|NOINTERCEPT|INTERCEPT)[ ]*:/, " ", s)
  RN = 0; RP = 0; RNROOT = 0; RALL = 0
  ng = split(s, grp, ":")
  state = 0
  rt = 1
  for (gi = 1; gi <= ng; gi++) {
    g = grp[gi]
    if (gi > 1 && g ~ /^ *[^ =,]+( *, *[^ =,]+)* *=/) { state = 0; rt = 1; g = substr(g, index(g, "=") + 1) }
    m = split(g, parts, ",")
    for (pi = 1; pi <= m; pi++) {
      piece = parts[pi]
      while (match(piece, /@@(N|P|R0|R1)@@/)) {
        tok = substr(piece, RSTART + 2, RLENGTH - 4)
        if (tok == "N") state = 1
        else if (tok == "P") state = 0
        else rt = (tok == "R1")
        piece = substr(piece, 1, RSTART - 1) " " substr(piece, RSTART + RLENGTH)
      }
      piece = trim(piece)
      gsub(/^[A-Z_]+=[^ ]* /, "", piece)
      if (piece == "" || substr(piece, 1, 1) == "!") continue
      if (state) { RN = 1; if (rt) RNROOT = 1 }
      else { RP = 1; if (rt && piece == "ALL") RALL = 1 }
    }
  }
}
function defaults(f, ln, t,   rest, c, sp, bind, set, app, m, S, j, s, v, who, hit) {
  rest = substr(t, 9)
  c = substr(rest, 1, 1)
  app = 1
  who = "everyone"
  if (c == ":" || c == "@" || c == ">" || c == "!") {
    sp = index(rest, " ")
    if (sp == 0) return
    bind = substr(rest, 2, sp - 2)
    set = substr(rest, sp + 1)
    if (c == ":") { app = ulist(bind, 0); who = bind; if (app == 0) return }
    else if (c == "!") who = "everyone running " bind
    else if (c == ">") who = "everyone running as " bind
  } else set = rest
  m = split(set, S, ",")
  for (j = 1; j <= m; j++) {
    s = trim(S[j])
    if (s == "!authenticate") print "NOAUTH\t" f "\t" ln "\t" (app == 2 ? "maybe" : "yes") "\t" who "\t" t
    else if (s ~ /^exempt_group[ ]*[+]?=/) {
      v = s
      sub(/^exempt_group[ ]*[+]?=[ ]*/, "", v)
      gsub(/"/, "", v)
      sub(/^%/, "", v)
      hit = (v in G) || (substr(v, 1, 1) == "#" && (substr(v, 2) in GID))
      print "EXEMPT\t" f "\t" ln "\t" v "\t" (hit ? (app == 2 ? "maybe" : "yes") : "no") "\t" t
    } else if (s ~ /^timestamp_timeout[ ]*=/) {
      v = s
      sub(/^timestamp_timeout[ ]*=[ ]*/, "", v)
      gsub(/"/, "", v)
      if (v ~ /^[0-9.]+$/ && v + 0 == 0) continue
      if (c == "!" || c == ">") print "CMDTO\t" f "\t" ln "\t" v "\t" t
      else if (after) print "LATE\t" f "\t" ln "\t" v "\t" t
    }
  }
}
function userspec(f, ln, t,   e, lhs, rest, W, nl, r, pw, as) {
  e = index(t, "=")
  if (e == 0) return
  lhs = substr(t, 1, e - 1)
  rest = substr(t, e + 1)
  gsub(/ *, */, ",", lhs)
  nl = split(trim(lhs), W, " ")
  if (nl < 2) return
  r = ulist(W[1], 0)
  rhs(rest)
  pw = RN ? (RP ? "mixed" : "none") : "asked"
  as = RN ? (RNROOT ? "root" : "other") : "-"
  print "RULE\t" f "\t" ln "\t" W[1] "\t" (r == 1 ? "yes" : (r == 2 ? "maybe" : "no")) "\t" pw "\t" as "\t" RALL "\t" t
}
BEGIN {
  FS = "\t"
  n = split(groups, x, " "); for (i = 1; i <= n; i++) G[x[i]] = 1
  n = split(gids, x, " "); for (i = 1; i <= n; i++) GID[x[i]] = 1
  n = 0
}
{
  n++; K[n] = $1; F[n] = $2; LN[n] = $3; T[n] = $4
  if ($1 == "L" && $4 ~ /^User_Alias /) defalias(UA, substr($4, 12))
  if ($1 == "L" && $4 ~ /^Runas_Alias /) defalias(RA, substr($4, 13))
}
END {
  after = 0
  place = 0
  nf = 0
  for (i = 1; i <= n; i++) {
    k = K[i]
    if (k == "B") { via[F[i]] = LN[i]; if (!(F[i] in seen)) { seen[F[i]] = 1; order[++nf] = F[i] }; continue }
    if (k == "P") { after = 1; place = 1; continue }
    if (k == "W") { print "WARN\t" F[i] "\t" LN[i] "\t" T[i]; continue }
    if (k == "I" || k == "D") { lines[F[i]]++; continue }
    if (k != "L") continue
    lines[F[i]]++
    t = T[i]
    if (t ~ /^(User|Runas|Host|Cmnd|Cmd)_Alias /) continue
    if (t ~ /^Defaults([ :@>!]|$)/) { defaults(F[i], LN[i], t); continue }
    userspec(F[i], LN[i], t)
  }
  print "PLACE\t" place
  for (j = 1; j <= nf; j++) print "LINES\t" order[j] "\t" (lines[order[j]] + 0) "\t" via[order[j]]
}'

# SUDOL_AWK reads the output of `sudo -l -U NAME` and prints NOPASS LINE for
# each command listed with NOPASSWD, NOAUTH for !authenticate, GRANT LINE for
# a command line that lets the user run ALL as root with the password,
# NOTALLOWED when sudo says the user may not run sudo, and TIMEOUT VALUE for
# the last timestamp_timeout among the defaults.
SUDOL_AWK='
function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
/is not allowed to run sudo/ { print "NOTALLOWED"; sec = ""; next }
/^Matching Defaults entries for / { sec = "def"; next }
/^Runas and Command-specific defaults for / { sec = "def"; next }
/^User .* may run the following commands/ { sec = "cmd"; next }
/^[^ \t]/ { sec = ""; next }
sec == "def" {
  n = split($0, a, ",")
  for (i = 1; i <= n; i++) {
    s = trim(a[i])
    if (s ~ /(^|[ \t])!authenticate$/) print "NOAUTH\t" s
    if (s ~ /^timestamp_timeout=/) { last = substr(s, 19); gsub(/"/, "", last) }
  }
  next
}
sec == "cmd" {
  l = trim($0)
  if (l ~ /NOPASSWD[ ]*:/) print "NOPASS\t" l
  else if (l ~ /^Options:.*!authenticate/) print "NOAUTH\t" l
  else if (l ~ /^\((ALL|root)([ ]*:[^)]*)?\)[ ]*(PASSWD:[ ]*)?ALL$/) print "GRANT\t" l
  next
}
END { if (last != "") print "TIMEOUT\t" last }'

# POLKIT_AWK reads one rules file (F its path) and prints, for each
# `return polkit.Result.YES` that may apply to the user (u, groups):
#   POLKIT any|match|unsure FILE LINE ACTIONS
# any: the rule names no user and no group; match: it names the user or one
# of its groups; unsure: it tests the user in a way the script cannot read.
POLKIT_AWK='
function lits(s,   i, j, q, a, b) {
  nl = 0
  for (;;) {
    a = index(s, "\""); b = index(s, sq)
    if (a == 0 && b == 0) break
    if (a == 0 || (b > 0 && b < a)) { i = b; q = sq } else { i = a; q = "\"" }
    s = substr(s, i + 1)
    j = index(s, q)
    if (j == 0) break
    L[++nl] = substr(s, 1, j - 1)
    s = substr(s, j + 1)
  }
}
function judge(   i, ident, hit, unsure, acts, na, kind) {
  if (ny == 0) return
  lits(text)
  ident = (text ~ /subject\.user/ || text ~ /isInGroup/ || text ~ /isInNetGroup/ || text ~ /unix-(user|group):/)
  hit = 0
  for (i = 1; i <= nl; i++) if (L[i] == u || (L[i] in G) || L[i] == "unix-user:" u) hit = 1
  for (i = 1; i <= nl; i++) if (index(L[i], "unix-group:") == 1 && (substr(L[i], 12) in G)) hit = 1
  unsure = (text ~ /isInGroup[ ]*\([ ]*[^ "\047]/ || text ~ /subject\.user[ ]*!=/ || text ~ /![ ]*subject\.isInGroup/ || text ~ /isInNetGroup/ || text ~ /subject\.user[ ]*===?[ ]*[^ ="\047]/)
  if (!ident) kind = "any"
  else if (hit) kind = "match"
  else if (unsure) kind = "unsure"
  else kind = ""
  acts = ""; na = 0
  for (i = 1; i <= nl && na < 3; i++) if (L[i] ~ /^[a-z0-9-]+(\.[A-Za-z0-9_-]+)+$/) { acts = acts (na ? " " : "") L[i]; na++ }
  if (acts == "") acts = "-"
  if (kind != "") for (i = 1; i <= ny; i++) print "POLKIT\t" kind "\t" F "\t" Y[i] "\t" acts
}
BEGIN { n = split(groups, x, " "); for (i = 1; i <= n; i++) G[x[i]] = 1 }
{
  s = $0; out = ""
  while (s != "") {
    if (inc) { i = index(s, "*/"); if (i == 0) { s = ""; break }; s = substr(s, i + 2); inc = 0; continue }
    i = index(s, "/*"); j = index(s, "//")
    if (j > 0 && (i == 0 || j < i)) { out = out substr(s, 1, j - 1); break }
    if (i > 0) { out = out substr(s, 1, i - 1); s = substr(s, i + 2); inc = 1; continue }
    out = out s; s = ""
  }
  if (!inrule && out ~ /polkit\.addRule[ ]*\(/) { inrule = 1; depth = 0; started = 0; text = ""; ny = 0 }
  if (inrule) {
    text = text " " out
    if (out ~ /polkit\.Result\.YES/) Y[++ny] = FNR
    t = out
    while (match(t, /[{}]/)) {
      if (substr(t, RSTART, 1) == "{") { depth++; started = 1 } else depth--
      t = substr(t, RSTART + 1)
    }
    if (started && depth <= 0) { judge(); inrule = 0 }
  } else if (out ~ /polkit\.Result\.YES/) print "POLKIT\tunsure\t" F "\t" FNR "\t-"
}
END { if (inrule) judge() }'

# DOAS_AWK reads one doas.conf (F its path) and prints DOAS LINE OPTIONS TEXT
# for each permit rule with nopass or persist that names the user or one of
# its groups.
DOAS_AWK='
BEGIN { n = split(groups, x, " "); for (i = 1; i <= n; i++) G[x[i]] = 1; n = split(gids, x, " "); for (i = 1; i <= n; i++) GID[x[i]] = 1 }
{
  sub(/\r$/, "")
  if (cont != "") line = cont " " $0
  else { line = $0; start = FNR }
  if (line ~ /\\$/) { cont = substr(line, 1, length(line) - 1); next }
  cont = ""
  sub(/#.*/, "", line)
  nw = split(line, w, /[ \t]+/)
  k = 1
  while (k <= nw && w[k] == "") k++
  if (w[k] != "permit") next
  k++
  np = 0; ps = 0
  while (k <= nw) {
    if (w[k] == "nopass") np = 1
    else if (w[k] == "persist") ps = 1
    else if (w[k] == "nolog" || w[k] == "keepenv") { }
    else if (w[k] == "setenv") { while (k <= nw && w[k] !~ /}$/) k++ }
    else break
    k++
  }
  id = w[k]
  ok = (id == u || id == uid || (substr(id, 1, 1) == ":" && ((substr(id, 2) in G) || (substr(id, 2) in GID))))
  if (!ok || (!np && !ps)) next
  t = line
  gsub(/[ \t]+/, " ", t); sub(/^ /, "", t); sub(/ $/, "", t)
  print "DOAS\t" start "\t" (np ? "nopass" : "") (np && ps ? "," : "") (ps ? "persist" : "") "\t" t
}'

# ACL_AWK reads `getfacl -RLsp` output and prints ACL PATH ENTRY for each
# entry that lets the user (u, uid) or one of its groups (groups, gids) write.
ACL_AWK='
function flush(   i, e, a) {
  if (f != "") for (i = 1; i <= ne; i++) {
    e = E[i]
    split(e, a, ":")
    if (substr(a[3], 2, 1) != "w" || substr(mask, 2, 1) != "w") continue
    if (a[1] == "user" && (a[2] == u || a[2] == uid)) print "ACL\t" f "\t" e
    if (a[1] == "group" && ((a[2] in G) || (a[2] in GID))) print "ACL\t" f "\t" e
  }
  ne = 0; mask = "rwx"
}
BEGIN { n = split(groups, x, " "); for (i = 1; i <= n; i++) G[x[i]] = 1; n = split(gids, x, " "); for (i = 1; i <= n; i++) GID[x[i]] = 1; mask = "rwx" }
/^# file: / { flush(); f = substr($0, 9); next }
/^#/ { next }
/^[ \t]*$/ { flush(); f = ""; next }
{ e = $0; sub(/[ \t]*#.*/, "", e) }
e ~ /^mask::/ { mask = substr(e, 7); next }
e ~ /^(user|group):[^:]+:/ { E[++ne] = e }
END { flush() }'

# MOUNTS_AWK reads /proc/self/mounts and prints the mount points to search
# for setuid files: local file systems without nosuid. SKIP MOUNT for one it
# leaves out (an escaped name, or a snapshot folder).
MOUNTS_AWK='
{
  mp = $2; fs = $3; o = "," $4 ","
  if (o ~ /,nosuid,/) next
  if (fs !~ /^(ext2|ext3|ext4|xfs|btrfs|f2fs|zfs|bcachefs|jfs|reiserfs|nilfs2|tmpfs)$/) next
  if (mp ~ /\\/ || mp ~ /(^|\/)\.snapshots(\/|$)/) { print "SKIP\t" mp; next }
  if (!(mp in seen)) { seen[mp] = 1; print "MP\t" mp }
}'

# --- reading the host (or the fixtures) ---------------------------------------------

load_user() {
  if [ -n "$FIX" ]; then
    lu_pw=$(cat "$FIX/etc/passwd") || refuse "the fixtures have no etc/passwd"
    lu_gr=$(cat "$FIX/etc/group") || lu_gr=
    lu_extra=
  else
    lu_pw=$(getent passwd "$NAME") || refuse "there is no user named $NAME"
    lu_gr=$(getent group) || lu_gr=
    lu_extra=$(id -Gn "$NAME" 2>/dev/null) || lu_extra=
  fi
  lu_info=$({
    printf '%s\n' "$lu_pw" | sed 's/^/P:/'
    printf '%s\n' "$lu_gr" | sed 's/^/G:/'
  } | awk -v u="$NAME" -v extra="$lu_extra" "$USER_AWK")
  U_UID=$(printf '%s\n' "$lu_info" | sed -n 's/^uid //p')
  U_GIDS=$(printf '%s\n' "$lu_info" | sed -n 's/^gids *//p')
  U_GROUPS=$(printf '%s\n' "$lu_info" | sed -n 's/^groups *//p')
  [ -n "$U_UID" ] || refuse "there is no user named $NAME"
  case $U_UID in *[!0-9]*) refuse "the uid of $NAME is not a number" ;; esac
  [ "$U_UID" != 0 ] || refuse "$NAME has uid 0: it is root already"
}

# name_ge A B: A sorts at or after B (C collation).
name_ge() {
  [ "$(printf '%s\n%s\n' "$1" "$2" | sort | head -n 1)" = "$2" ]
}

# odd_names DIR prints something when a name directly in DIR holds a newline
# or a tab, which the line-based reading below cannot represent.
odd_names() {
  find "$1/." ! -name . -prune \( -name "*$NL*" -o -name "*$TAB*" \) -print 2>/dev/null | head -n 1
}

# sudoers_expand FILE DEPTH VIA prints the record stream of FILE and of what it
# includes, in the order sudo reads them: B FILE VIA when a file starts, L FILE
# LINE TEXT, I or D FILE LINE PATH, W FILE LINE TEXT for a part it could not
# read, and P where the drop-in would sit in /etc/sudoers.d. A subshell keeps
# each level's variables.
sudoers_expand() (
  se_f=$1
  se_depth=$2
  printf 'B\t%s\t%s\n' "$se_f" "$3"
  if [ "$se_depth" -gt 16 ]; then
    printf 'W\t%s\t0\tincludes nested more than 16 deep; not followed\n' "$se_f"
    exit 0
  fi
  se_m=$(hp "$se_f")
  if [ ! -f "$se_m" ] || [ ! -r "$se_m" ]; then
    printf 'W\t%s\t0\tnot a readable file; not read\n' "$se_f"
    exit 0
  fi
  awk "$LINES_AWK" "$se_m" | while IFS="$TAB" read -r se_k se_ln se_rest; do
    case $se_k in
      L) printf 'L\t%s\t%s\t%s\n' "$se_f" "$se_ln" "$se_rest" ;;
      I | D)
        se_p=$se_rest
        case $se_p in
          *%h*)
            printf 'W\t%s\t%s\t%s\n' "$se_f" "$se_ln" "an include names %h (the host name); not followed"
            continue
            ;;
        esac
        case /$se_p/ in
          */../*)
            printf 'W\t%s\t%s\t%s\n' "$se_f" "$se_ln" "an include path with a .. part; not followed"
            continue
            ;;
        esac
        case $se_p in
          /*) ;;
          *) se_p=${se_f%/*}/$se_p ;;
        esac
        while [ "${se_p%/}" != "$se_p" ] && [ "${se_p%/}" != "" ]; do se_p=${se_p%/}; done
        printf '%s\t%s\t%s\t%s\n' "$se_k" "$se_f" "$se_ln" "$se_p"
        if [ "$se_k" = I ]; then
          sudoers_expand "$se_p" $((se_depth + 1)) file
          continue
        fi
        se_md=$(hp "$se_p")
        if [ ! -d "$se_md" ] || [ ! -r "$se_md" ]; then
          printf 'W\t%s\t%s\t%s\n' "$se_f" "$se_ln" "includedir names a missing or unreadable folder; not read"
          continue
        fi
        if [ -n "$(odd_names "$se_md")" ]; then
          printf 'W\t%s\t%s\t%s\n' "$se_f" "$se_ln" "a file name in $se_p holds a newline or a tab; the folder was not read"
          continue
        fi
        # shellcheck disable=SC2012  # names with a newline or a tab are refused above
        ls -A "$se_md" | sort | {
          se_placed=0
          while IFS= read -r se_e; do
            case $se_e in '' | *.* | *~) continue ;; esac
            if [ "$se_p" = "$SUDOERS_D" ] && [ "$se_placed" = 0 ] && name_ge "$se_e" "$DROPIN_NAME"; then
              printf 'P\t%s\t0\n' "$se_p"
              se_placed=1
            fi
            sudoers_expand "$se_p/$se_e" $((se_depth + 1)) "dir:$se_p"
          done
          if [ "$se_p" = "$SUDOERS_D" ] && [ "$se_placed" = 0 ]; then printf 'P\t%s\t0\n' "$se_p"; fi
        }
        ;;
    esac
  done
)

# dir_safe PATH: PATH is a folder (not a link) owned by root that no one else
# may write. file_safe PATH: the same for a regular file. In a self-test the
# fixture's not-root.txt lists the paths that are not.
dir_safe() { path_safe d "$1"; }
file_safe() { path_safe f "$1"; }
path_safe() {
  ps_where=$(hp "$2")
  [ ! -L "$ps_where" ] || return 1
  case $1 in
    d) [ -d "$ps_where" ] || return 1 ;;
    *) [ -f "$ps_where" ] || return 1 ;;
  esac
  if [ -n "$FIX" ]; then
    [ ! -f "$FIX/not-root.txt" ] || ! grep -Fxq -- "$2" "$FIX/not-root.txt"
    return
  fi
  [ -n "$(find "$2" -prune -type "$1" -user 0 ! -perm -0020 ! -perm -0002 -print)" ]
}

# mode_is PATH OCTAL: PATH has exactly that mode. In a self-test the
# fixture's modes.txt (PATH OCTAL lines) says it when it lists PATH, since
# git keeps no mode but 0644 and 0755.
mode_is() {
  if [ -n "$FIX" ] && [ -f "$FIX/modes.txt" ] &&
    mi_m=$(awk -v p="$1" '$1 == p { m = $2; f = 1 } END { if (f) print m; exit !f }' "$FIX/modes.txt"); then
    [ "$mi_m" = "$2" ]
    return
  fi
  [ -n "$(find "$(hp "$1")" -prune -perm "$2" -print 2>/dev/null)" ]
}

# pkg_owned PATH: 0 when a package owns PATH, 1 when none does, 2 when this
# host has no package query the script knows.
pkg_owned() {
  if [ -n "$FIX" ]; then
    [ -f "$FIX/packaged.txt" ] || return 2
    grep -Fxq -- "$1" "$FIX/packaged.txt"
    return
  fi
  if command -v pacman >/dev/null 2>&1; then
    pacman -Qoq "$1" >/dev/null 2>&1
    return
  fi
  if command -v dpkg >/dev/null 2>&1; then
    dpkg -S "$1" >/dev/null 2>&1 && return 0
    case $1 in
      /usr/bin/* | /usr/sbin/* | /usr/lib/*) dpkg -S "${1#/usr}" >/dev/null 2>&1 && return 0 ;;
    esac
    return 1
  fi
  if command -v rpm >/dev/null 2>&1; then
    rpm -qf "$1" >/dev/null 2>&1
    return
  fi
  return 2
}

# wfind KIND PATH prints the paths below PATH (tree), in PATH (flat) or PATH
# itself (exact) that the user may write: owned by it, writable by everyone,
# or group-writable with one of its groups. Links are followed; only files
# and folders count (a masked unit links to /dev/null).
wfind() {
  wf_k=$1
  wf_p=$2
  shift 2
  set -- -user "$U_UID" -o -perm -0002
  if [ -n "$U_GIDS" ]; then
    set -- "$@" -o \( -perm -0020 \(
    wf_first=1
    for wf_g in $U_GIDS; do
      if [ "$wf_first" = 1 ]; then set -- "$@" -group "$wf_g"; else set -- "$@" -o -group "$wf_g"; fi
      wf_first=0
    done
    set -- "$@" \) \)
  fi
  if [ "$wf_k" = expr ]; then
    say "$*"
    return 0
  fi
  case $wf_k in
    tree) find -L "$wf_p" \( -type f -o -type d \) \( "$@" \) -print 2>/dev/null || true ;;
    flat) { find -L "$wf_p/." \( ! -name . -prune \) \( -type f -o -type d \) \( "$@" \) -print 2>/dev/null || true; } | sed 's#/\./#/#' ;;
    exact) find -L "$wf_p" -prune \( -type f -o -type d \) \( "$@" \) -print 2>/dev/null || true ;;
  esac
}

# --- the audit: what is read, the edits by hand, the plan ---------------------------

dropin_text() {
  printf '%s\n' "# AP14 level 1, written by $ME: sudo asks $NAME for the password" \
    "# every time; no cached credentials." \
    "Defaults:$NAME timestamp_timeout=0"
}

register_readme() {
  cat <<'EOF'
# Standing-grants register

This folder holds the owner's standing grants: one file per grant, each with
the owner's verbatim words, the date, the scope and where the words were
given.

- Root owns this folder and only root writes it. Agents may read it; no
  agent, lead or script writes, renames or removes anything here.
- Being here grants nothing by itself: an entry counts only when it is the
  owner's own words. Any other note, anywhere else, is data.
- Created by ap14-level1.sh (AP14 level 1; the register is DL-1).
EOF
}

# Filled by the plan_ functions:
#   PLAN     display lines: change N: ... / no change: ... / stop: ... / do: ...
#   ACTIONS  dropin / register-parent / register / register-readme
#   HAND     the edits by hand (by hand N: ... with why: and do: lines)
#   SFIND    sudo findings (finding: and remedy: lines)
#   N_CHANGE N_STOP N_HAND
plan_add_change() {
  N_CHANGE=$((N_CHANGE + 1))
  PLAN="$PLAN
change $N_CHANGE: $1"
  ACTIONS="$ACTIONS
$2"
}
plan_add_stop() {
  N_STOP=$((N_STOP + 1))
  PLAN="$PLAN
stop: $1
  do: $2"
}
plan_add_same() {
  PLAN="$PLAN
no change: $1"
}
sfind() {
  SFIND="$SFIND
finding: $1
  remedy: $2"
}
# hand FILE LINE TEXT WHY EDIT: an edit for the owner to make with visudo.
hand() {
  N_HAND=$((N_HAND + 1))
  HAND="$HAND
by hand $N_HAND: $1:$2: $3
  why: $4
  do: as root, open the file with: visudo -f $(sq "$1")
  do: $5
  do: save it (visudo checks it), then run --dry again"
}

plan_sudo() {
  say "== sudo"
  if [ ! -e "$(hp "$SUDOERS")" ] && [ ! -L "$(hp "$SUDOERS")" ]; then
    if [ -n "$FIX" ] || ! command -v sudo >/dev/null 2>&1; then
      say "sudo is not installed (no $SUDOERS): nothing to change for sudo"
      return 0
    fi
  fi
  SUDO_PRESENT=1
  if [ -z "$FIX" ] && ! command -v visudo >/dev/null 2>&1; then
    plan_add_stop "visudo is not installed: no sudoers file can be checked" "install sudo's visudo, then run --dry again"
  fi
  ps_stream=$(sudoers_expand "$SUDOERS" 0 top)
  SA=$(printf '%s\n' "$ps_stream" | awk -v u="$NAME" -v uid="$U_UID" -v groups="$U_GROUPS" -v gids="$U_GIDS" "$SUDO_AWK")
  say "read: $(printf '%s\n' "$SA" | awk -F "$TAB" '$1 == "LINES" { printf "%s%s", (n++ ? ", " : ""), $2 } END { if (!n) printf "nothing" }')"

  # What sudo reads that the script could not: the audit is incomplete.
  while IFS="$TAB" read -r w_k w_f w_ln w_t; do
    [ "$w_k" = WARN ] || continue
    plan_add_stop "$w_f:$w_ln: $w_t: the audit cannot tell what sudo reads there" "read it by hand and fix it with visudo (so that the script can read it), then run --dry again"
  done <<EOF
$SA
EOF

  # Every rule, who it gives sudo and whether it asks for a password.
  ps_fallback=0
  while IFS="$TAB" read -r r_k r_f r_ln r_a r_b r_c r_d r_e r_t; do
    case $r_k in
      RULE)
        # r_a who, r_b applies, r_c pw, r_d as, r_e allpw
        case $r_c in
          asked) r_pw="asked" ;;
          none) r_pw="not asked (as $r_d)" ;;
          *) r_pw="not asked for some commands (as $r_d)" ;;
        esac
        say "rule: $r_f:$r_ln: users $r_a; applies to $NAME: $r_b; password: $r_pw: $r_t"
        [ "$r_b" = yes ] && U_GRANTS=$((U_GRANTS + 1))
        if [ "$r_b" = yes ] && [ "$r_c" = asked ] && [ "$r_e" = 1 ]; then ps_fallback=1; fi
        [ "$r_b" != no ] && [ "$r_c" != asked ] || continue
        case $r_a in
          "$NAME" | "#$U_UID") r_why="it gives $NAME sudo without a password" ;;
          *) r_why="it gives $NAME sudo without a password; it also applies to others ($r_a), and the edit changes their sudo too" ;;
        esac
        [ "$r_b" = yes ] || r_why="it may give $NAME sudo without a password ($r_a is a netgroup or non-Unix group: check whether $NAME is in it); the script cannot tell, so --apply waits for the edit"
        hand "$r_f" "$r_ln" "$r_t" "$r_why" "on line $r_ln, delete each NOPASSWD: tag (or change it to PASSWD:), or delete the whole rule"
        ;;
      NOAUTH)
        # r_a applies, r_b who, r_c the text (a record of six fields)
        say "rule: $r_f:$r_ln: Defaults !authenticate for $r_b; applies to $NAME: $r_a; password: not asked: $r_c"
        hand "$r_f" "$r_ln" "$r_c" "!authenticate lets $r_b run sudo without a password" "on line $r_ln, delete !authenticate (the whole line when it is the only setting)"
        ;;
      EXEMPT)
        # r_a group, r_b applies, r_c the text (a record of six fields)
        say "rule: $r_f:$r_ln: exempt_group $r_a (its members never type a password); applies to $NAME: $r_b: $r_c"
        [ "$r_b" != no ] || continue
        hand "$r_f" "$r_ln" "$r_c" "$NAME is in $r_a, whose members never type a password for sudo" "on line $r_ln, delete the exempt_group setting (the whole line when it is the only setting)"
        ;;
    esac
  done <<EOF
$SA
EOF

  # Before the owner deletes a passwordless rule: would another rule still
  # give the user sudo with the password?
  if [ "$N_HAND" -gt 0 ] && [ "$ps_fallback" = 0 ]; then
    plan_add_stop "no other rule gives $NAME sudo with a password: once the passwordless rules are gone, sudo would refuse $NAME, not ask for a password" "first add a rule that asks for it, for example $NAME ALL=(ALL:ALL) ALL (as root: visudo -f $(sq "$SUDOERS_D/50-$NAME")); if $NAME is to have no sudo at all, make sure root can log in another way first; this stop goes when the passwordless rules do"
  fi
  if [ "$U_GRANTS" = 0 ]; then
    say "no rule gives $NAME sudo: sudo refuses $NAME (the drop-in changes nothing for it)"
  fi

  # Settings of the cache time that may override the drop-in.
  while IFS="$TAB" read -r l_k l_f l_ln l_v l_t; do
    case $l_k in
      LATE) sfind "sudo-timeout: $l_f:$l_ln: sets timestamp_timeout=$l_v after the drop-in, which it may override: $l_t" "remove it or make it timestamp_timeout=0, as root with visudo -f $(sq "$l_f")" ;;
      CMDTO) sfind "sudo-timeout: $l_f:$l_ln: sets timestamp_timeout=$l_v for a command or runas user, which sudo applies after the drop-in wherever it sits: $l_t" "remove it or make it timestamp_timeout=0, as root with visudo -f $(sq "$l_f")" ;;
    esac
  done <<EOF
$SA
EOF

  # The drop-in: a new file, or exactly the one this script writes.
  if ! printf '%s\n' "$SA" | grep -q "^PLACE${TAB}1\$"; then
    plan_add_stop "$SUDOERS does not read $SUDOERS_D (no includedir for it): the drop-in would have no effect" "add the line @includedir $SUDOERS_D at the end of $SUDOERS with visudo, then run --dry again"
    return 0
  fi
  if ! dir_safe "$SUDOERS_D"; then
    plan_add_stop "$SUDOERS_D is not a folder owned by root that only root may write" "look at it by hand; the script writes nothing there until it is"
    return 0
  fi
  ps_d=$(hp "$DROPIN")
  if [ -L "$ps_d" ] || { [ -e "$ps_d" ] && [ ! -f "$ps_d" ]; }; then
    plan_add_stop "$DROPIN exists and is not a regular file: the script never replaces or removes it" "look at it; remove it by hand if it is not wanted, then run --dry again"
  elif [ -f "$ps_d" ]; then
    if dropin_text | cmp -s - "$ps_d" && file_safe "$DROPIN" && mode_is "$DROPIN" 0440; then
      plan_add_same "$DROPIN already says: Defaults:$NAME timestamp_timeout=0 (owner root, mode 0440)"
    else
      plan_add_stop "$DROPIN exists and is not the file this script writes (its text, owner root, mode 0440): the script never replaces it" "read it; if it is not wanted, remove it by hand (rm -f $(sq "$DROPIN"), then visudo -c), then run --dry again"
    fi
  else
    plan_add_change "write $DROPIN (a new file), mode 0440, owner root, with:
$(dropin_text | sed 's/^/    | /')" "dropin"
  fi
}

# plan_sudo_l asks sudo itself for the effective policy.
plan_sudo_l() {
  [ "$SUDO_PRESENT" = 1 ] || return 0
  if [ -n "$FIX" ]; then
    if [ -f "$FIX/sudo-l.txt" ]; then
      sl_out=$(cat "$FIX/sudo-l.txt")
      sl_rc=$(if [ -f "$FIX/sudo-l.rc" ]; then cat "$FIX/sudo-l.rc"; else echo 0; fi)
    else
      sl_out="(no sudo-l.txt in the fixtures)"
      sl_rc=127
    fi
  elif command -v sudo >/dev/null 2>&1; then
    if sl_out=$(sudo -n -l -U "$NAME" 2>&1 </dev/null); then sl_rc=0; else sl_rc=$?; fi
  else
    sl_out="(sudo is not in $SYS_PATH)"
    sl_rc=127
  fi
  sl_rec=$(printf '%s\n' "$sl_out" | awk "$SUDOL_AWK")
  if [ "$sl_rc" != 0 ]; then
    if printf '%s\n' "$sl_rec" | grep -qx NOTALLOWED; then
      say "sudo -l -U $NAME: $NAME is not allowed to run sudo"
      if [ "$U_GRANTS" -gt 0 ]; then
        plan_add_stop "sudo -l -U $NAME says $NAME may not run sudo, but the files hold rules for it: the audit and sudo disagree" "read sudo -l -U $NAME as root and the rules above, then run --dry again"
      fi
      return 0
    fi
    plan_add_stop "sudo -l -U $NAME failed (exit $sl_rc): the effective policy is unknown: $(printf '%s\n' "$sl_out" | head -n 1)" "run sudo -l -U $NAME as root and read the error; fix it, then run --dry again"
    return 0
  fi
  while IFS="$TAB" read -r sl_k sl_t; do
    case $sl_k in
      NOPASS | NOAUTH)
        if [ "$N_HAND" = 0 ]; then
          plan_add_stop "sudo -l -U $NAME lists $sl_t, which needs no password, but the files read above hold no such rule for $NAME" "find the rule by hand (sudo -ll -U $NAME names its source in newer sudo); remove it with visudo, then run --dry again"
        else
          say "sudo -l -U $NAME also lists, without a password: $sl_t"
        fi
        ;;
      TIMEOUT)
        case $sl_t in
          0 | 0.0) say "sudo -l -U $NAME: the last timestamp_timeout is 0" ;;
          *) sfind "sudo-l: sudo -l -U $NAME shows timestamp_timeout=$sl_t last: credentials are cached" "remove the later timestamp_timeout setting (see the sudo part above)" ;;
        esac
        ;;
    esac
  done <<EOF
$sl_rec
EOF
}

plan_password() {
  [ "$SUDO_PRESENT" = 1 ] || return 0
  pp_rc=0
  if [ -n "$FIX" ]; then
    if [ -f "$FIX/passwd-S.txt" ]; then
      pp_st=$(awk '{ print $2; exit }' "$FIX/passwd-S.txt")
    else
      pp_st=
      pp_rc=127
    fi
  elif command -v passwd >/dev/null 2>&1; then
    if pp_out=$(passwd -S "$NAME" 2>/dev/null </dev/null); then pp_rc=0; else pp_rc=$?; fi
    pp_st=$(printf '%s\n' "$pp_out" | awk '{ print $2; exit }')
  else
    pp_st=
    pp_rc=127
  fi
  [ "$pp_rc" = 0 ] || pp_st=
  case $pp_st in
    P | PS) say "$NAME has a usable password: sudo can still ask for it" ;;
    L | LK | NP)
      if [ "$U_GRANTS" -gt 0 ]; then
        plan_add_stop "$NAME has no usable password (passwd -S says $pp_st): sudo would ask for a password $NAME does not have" "set one with passwd $NAME (as root), or make sure root can log in another way, then run --dry again"
      else
        say "$NAME has no usable password (passwd -S says $pp_st), and no sudo rule"
      fi
      ;;
    *) plan_add_stop "passwd -S did not say whether $NAME has a usable password (exit $pp_rc)" "check it by hand as root (passwd -S $NAME); the script cannot tell whether sudo could still ask $NAME for one" ;;
  esac
}

plan_register() {
  pr_new=0
  for pr_p in "$REGISTER_PARENT" "$REGISTER"; do
    if [ -e "$(hp "$pr_p")" ] || [ -L "$(hp "$pr_p")" ]; then
      if ! dir_safe "$pr_p"; then
        plan_add_stop "$pr_p exists but is not a folder owned by root that only root may write" "look at who made it and what it holds, and fix or remove it by hand, then run --dry again"
        return 0
      fi
    elif [ "$pr_p" = "$REGISTER_PARENT" ]; then
      plan_add_change "create the folder $REGISTER_PARENT (a new folder), mode 0755, owner root" "register-parent"
      pr_new=1
    else
      pr_new=1
    fi
  done
  if [ "$pr_new" = 1 ]; then
    plan_add_change "create the standing-grants register $REGISTER (a new folder), mode 0755, owner root: the owner's verbatim words go there; agents may read it, never write it" "register"
    plan_add_change "write $REGISTER/README (a new file), mode 0644, owner root" "register-readme"
    return 0
  fi
  # Every entry of an existing register: a regular file root owns and only
  # root may write.
  pr_md=$(hp "$REGISTER")
  if [ -n "$(odd_names "$pr_md")" ]; then
    plan_add_stop "a file name in $REGISTER holds a newline or a tab: the script cannot check that entry" "look at the register by hand and rename the entry, then run --dry again"
    return 0
  fi
  pr_bad=0
  # shellcheck disable=SC2012  # names with a newline or a tab are refused above
  pr_list=$(ls -A "$pr_md" | sort)
  while IFS= read -r pr_e; do
    [ -n "$pr_e" ] || continue
    pr_x=$REGISTER/$pr_e
    if [ -L "$(hp "$pr_x")" ]; then
      plan_add_stop "the register entry $pr_x is a link: anyone who may change its target changes the grant" "replace it by a regular file root owns, or remove it, by hand; then run --dry again"
      pr_bad=1
    elif [ ! -f "$(hp "$pr_x")" ]; then
      plan_add_stop "the register entry $pr_x is not a regular file" "look at it by hand; the register holds only files; then run --dry again"
      pr_bad=1
    elif ! file_safe "$pr_x"; then
      plan_add_stop "the register entry $pr_x is not owned by root or may be written by another account: that account can change the grant" "check its words with the owner, then chown root:root $(sq "$pr_x") and chmod go-w $(sq "$pr_x"); then run --dry again"
      pr_bad=1
    fi
  done <<EOF
$pr_list
EOF
  if [ -e "$pr_md/README" ] || [ -L "$pr_md/README" ]; then
    [ "$pr_bad" = 1 ] || plan_add_same "the standing-grants register $REGISTER exists, owned by root, and every entry is root's"
  else
    plan_add_change "write $REGISTER/README (a new file), mode 0644, owner root" "register-readme"
  fi
}

# plan_leftovers notes temporary files an earlier, interrupted --apply left.
plan_leftovers() {
  for pl_d in "$SUDOERS_D" "$REGISTER"; do
    [ -d "$(hp "$pl_d")" ] || continue
    # shellcheck disable=SC2012  # a temporary name has no newline
    ls -A "$(hp "$pl_d")" 2>/dev/null | while IFS= read -r pl_e; do
      case $pl_e in
        "$TMP_PREFIX"*) say "note: $pl_d/$pl_e is a temporary file of an earlier run (sudo skips it): remove it by hand: rm -f $(sq "$pl_d/$pl_e")" ;;
      esac
    done
  done
}

# --- other routes to root (reported only) -------------------------------------------

group_reason() {
  case $1 in
    docker) say "the Docker socket runs a container with the host's / mounted" ;;
    lxd | incus-admin) say "the container daemon runs a privileged container with the host's / attached" ;;
    incus) say "the incus socket; root-equivalent unless the daemon confines this group to a restricted project" ;;
    libvirt) say "the system libvirt daemon runs a guest with a host disk or folder attached" ;;
    disk) say "reads and writes the host's disks directly" ;;
    kmem) say "reads kernel memory" ;;
    shadow) say "reads the password hash of every account, root's too" ;;
    input) say "reads every keystroke, the password typed at sudo too" ;;
    *) return 1 ;;
  esac
}

routes() {
  finding() { printf 'finding: %s\n  remedy: %s\n' "$1" "$2"; }

  # Groups.
  for rt_g in $U_GROUPS; do
    if rt_why=$(group_reason "$rt_g"); then
      finding "group: $NAME is in $rt_g: $rt_why" "gpasswd -d $NAME $rt_g (it takes effect at the next login)"
    fi
    case $rt_g in
      wheel | sudo | admin) say "note: $NAME is in $rt_g: sudo and polkit admin actions ask for $NAME's password" ;;
    esac
  done
  for rt_gid in $U_GIDS; do
    [ "$rt_gid" = 0 ] && finding "group: $NAME is in the group with gid 0: it writes every file the root group may write" "remove $NAME from the group with gid 0"
  done

  # Polkit.
  for rt_d in "$POLKIT_ETC" "$POLKIT_USR"; do
    rt_md=$(hp "$rt_d")
    [ -d "$rt_md" ] || continue
    if [ -n "$(odd_names "$rt_md")" ]; then
      printf 'POLKIT\todd\t%s\t0\t-\n' "$rt_d"
      continue
    fi
    # shellcheck disable=SC2012  # names with a newline or a tab are refused above
    ls -A "$rt_md" | sort | while IFS= read -r rt_e; do
      case $rt_e in *.rules) ;; *) continue ;; esac
      if [ "$rt_d" = "$POLKIT_USR" ] && [ -e "$(hp "$POLKIT_ETC")/$rt_e" ]; then continue; fi
      [ -f "$rt_md/$rt_e" ] || continue
      awk -v F="$rt_d/$rt_e" -v u="$NAME" -v groups="$U_GROUPS" -v sq="'" "$POLKIT_AWK" "$rt_md/$rt_e"
    done
  done | while IFS="$TAB" read -r pk_k pk_kind pk_f pk_ln pk_a; do
    [ "$pk_k" = POLKIT ] || continue
    case $pk_kind in
      odd)
        finding "polkit: a file name in $pk_f holds a newline or a tab: its rules were not read" "read the folder by hand and rename the file"
        continue
        ;;
      any) pk_w="returns YES without authentication for any user" ;;
      match) pk_w="returns YES without authentication for $NAME or one of its groups" ;;
      *) pk_w="returns YES without authentication, for users the script cannot tell; read it" ;;
    esac
    finding "polkit: $pk_f:$pk_ln: $pk_w (actions: $pk_a)" "remove the rule, or put a rule before it in $POLKIT_ETC that returns polkit.Result.AUTH_ADMIN for these actions"
  done

  # doas.
  for rt_f in $DOAS_FILES; do
    [ -f "$(hp "$rt_f")" ] || continue
    awk -v u="$NAME" -v uid="$U_UID" -v groups="$U_GROUPS" -v gids="$U_GIDS" "$DOAS_AWK" "$(hp "$rt_f")" |
      while IFS="$TAB" read -r dz_k dz_ln dz_o dz_t; do
        [ "$dz_k" = DOAS ] || continue
        finding "doas: $rt_f:$dz_ln: permits $NAME with $dz_o: $dz_t" "remove $dz_o from the rule in $rt_f"
      done
  done

  # setuid and setgid root files no package owns.
  if [ -n "$FIX" ]; then
    rt_mounts=$(if [ -f "$FIX/proc/self/mounts" ]; then awk "$MOUNTS_AWK" "$FIX/proc/self/mounts"; fi)
  else
    rt_mounts=$(awk "$MOUNTS_AWK" /proc/self/mounts)
  fi
  say "note: setuid search covers:$(printf '%s\n' "$rt_mounts" | awk -F "$TAB" '$1 == "MP" { printf " %s", $2; n++ } END { if (!n) printf " nothing" }')"
  printf '%s\n' "$rt_mounts" | awk -F "$TAB" '$1 == "SKIP" { print "note: setuid search leaves out " $2 }'
  if [ -n "$FIX" ]; then
    rt_suid=$(if [ -f "$FIX/setuid.txt" ]; then sort -u "$FIX/setuid.txt"; fi)
  else
    rt_suid=$(printf '%s\n' "$rt_mounts" | awk -F "$TAB" '$1 == "MP" { print $2 }' | while IFS= read -r rt_mp; do
      printf '%s: searching %s for setuid and setgid files (this can take a few minutes)\n' "$ME" "$rt_mp" >&2
      find "$rt_mp" -xdev -type f \( -perm -4000 -user 0 -o -perm -2000 -group 0 \) -print 2>/dev/null || true
    done | sort -u)
  fi
  rt_query=1
  while IFS= read -r rt_s; do
    [ -n "$rt_s" ] || continue
    if pkg_owned "$rt_s"; then
      continue
    else
      rt_st=$?
    fi
    if [ "$rt_st" = 2 ]; then
      [ "$rt_query" = 0 ] || say "note: no package query (pacman, dpkg, rpm) here: every setuid file is listed"
      rt_query=0
      finding "setuid: $rt_s is setuid or setgid root; its package is unknown" "check where it comes from; chmod u-s,g-s $(sq "$rt_s") unless it is meant to be there"
    else
      finding "setuid: $rt_s is setuid or setgid root and no package owns it" "remove it, or chmod u-s,g-s $(sq "$rt_s"), unless you put it there on purpose"
    fi
  done <<EOF
$rt_suid
EOF

  # Root's PATH: the one the script was started with.
  if [ -n "$FIX" ]; then
    rt_path=$(if [ -f "$FIX/root-path.txt" ]; then cat "$FIX/root-path.txt"; else say "$STD_PATH"; fi)
  else
    rt_path=$CALLER_PATH
  fi
  rt_flat=$(printf '%s:%s\n' "$rt_path" "$STD_PATH" | awk -F: '{ for (i = 1; i <= NF; i++) print $i }' | while IFS= read -r rt_e; do
    case $rt_e in
      /*) [ -d "$(hp "$rt_e")" ] && say "$rt_e" ;;
      *) say "BAD:$rt_e" ;;
    esac
  done | awk '!seen[$0]++')
  printf '%s\n' "$rt_flat" | while IFS= read -r rt_e; do
    case $rt_e in
      BAD:*) finding "root-path: root's PATH holds '${rt_e#BAD:}', a relative or empty entry: root runs programs from whatever folder it stands in" "remove the entry from root's PATH (its shell start files)" ;;
    esac
  done

  # Files and folders root runs from that the user may write.
  rt_roots=$(
    for rt_e in $WRITE_TREES; do say "tree $rt_e"; done
    for rt_e in $WRITE_FLAT; do say "flat $rt_e"; done
    printf '%s\n' "$rt_flat" | while IFS= read -r rt_e; do case $rt_e in /*) say "flat $rt_e" ;; esac; done
    for rt_e in $WRITE_EXACT; do say "exact $rt_e"; done
  )
  say "note: user-writable test: find -L PATH ( $(wfind expr /) )"
  if [ -n "$FIX" ]; then
    rt_w=$(if [ -f "$FIX/writable.txt" ]; then sort -u "$FIX/writable.txt"; fi)
    rt_acl=$(if [ -f "$FIX/getfacl.txt" ]; then cat "$FIX/getfacl.txt"; fi)
  else
    rt_w=$(printf '%s\n' "$rt_roots" | while read -r rt_k rt_p; do
      [ -e "$rt_p" ] || continue
      wfind "$rt_k" "$rt_p"
      rt_a=${rt_p%/*}
      while [ -n "$rt_a" ]; do
        wfind exact "$rt_a"
        rt_a=${rt_a%/*}
      done
      wfind exact /
    done | sort -u)
    if command -v getfacl >/dev/null 2>&1; then
      rt_acl=$(printf '%s\n' "$rt_roots" | while read -r rt_k rt_p; do
        [ -e "$rt_p" ] || continue
        case $rt_k in
          tree) getfacl -RLsp "$rt_p" 2>/dev/null || true ;;
          *) getfacl -sp "$rt_p" "$rt_p"/* "$rt_p"/.[!.]* 2>/dev/null || true ;;
        esac
      done)
    else
      say "note: getfacl is not installed: access control lists were not read"
      rt_acl=
    fi
  fi
  printf '%s\n' "$rt_w" | while IFS= read -r rt_e; do
    [ -n "$rt_e" ] || continue
    finding "writable: $NAME may write $rt_e, which root runs or reads as configuration" "chown root:root $(sq "$rt_e") and chmod go-w $(sq "$rt_e") (or remove $NAME's access)"
  done
  printf '%s\n' "$rt_acl" | awk -v u="$NAME" -v uid="$U_UID" -v groups="$U_GROUPS" -v gids="$U_GIDS" "$ACL_AWK" |
    while IFS="$TAB" read -r ac_k ac_p ac_e; do
      [ "$ac_k" = ACL ] || continue
      finding "acl: $ac_p grants $ac_e: $NAME may write it" "setfacl -x $(sq "${ac_e%:*}") $(sq "$ac_p")"
    done
}

# --- the report ---------------------------------------------------------------------

# report prints what the run reads, the edits by hand, the plan and the
# routes, and returns 0 when there is nothing to write, no edit, no stop and
# no finding, else 1.
report() {
  PLAN=
  ACTIONS=
  SFIND=
  HAND=
  N_CHANGE=0
  N_STOP=0
  N_HAND=0
  U_GRANTS=0
  SUDO_PRESENT=0
  load_user
  say "== user"
  say "user: $NAME, uid $U_UID; groups: ${U_GROUPS:-none}"
  plan_sudo
  plan_sudo_l
  plan_password
  plan_register
  plan_leftovers
  say "== by hand: the edits the owner makes with visudo before --apply"
  if [ -n "$HAND" ]; then
    printf '%s\n' "$HAND" | sed '1{/^$/d;}'
  else
    say "nothing: no rule gives $NAME sudo without a password"
  fi
  say "== plan: what --apply writes (new files only; it never moves, edits or removes a file)"
  if [ -n "$PLAN" ]; then printf '%s\n' "$PLAN" | sed '1{/^$/d;}'; fi
  [ "$N_CHANGE" -gt 0 ] || [ "$N_STOP" -gt 0 ] || say "nothing to write"
  if [ "$N_STOP" -gt 0 ] || [ "$N_HAND" -gt 0 ]; then
    say "--apply refuses while a stop or a by-hand edit is listed: it writes nothing until each is handled"
  fi
  say "== routes to root (reported, never changed by this script)"
  rp_routes=$(
    if [ -n "$SFIND" ]; then printf '%s\n' "$SFIND" | sed '1{/^$/d;}'; fi
    routes
  )
  if [ -n "$rp_routes" ]; then printf '%s\n' "$rp_routes"; fi
  N_FIND=$(printf '%s\n' "$rp_routes" | grep -c '^finding:' || true)
  [ "$N_FIND" -gt 0 ] || say "no route found"
  say "== summary"
  say "changes: $N_CHANGE; by hand: $N_HAND; stops: $N_STOP; findings: $N_FIND"
  [ "$N_CHANGE" -eq 0 ] && [ "$N_HAND" -eq 0 ] && [ "$N_STOP" -eq 0 ] && [ "$N_FIND" -eq 0 ]
}

# --- --apply: new files only, one lock, one-step rollback ---------------------------

CREATED=
LOCKED=0

# tx_step WHAT: one step of the run. In a self-test, step TX_FAIL_AT fails.
tx_step() {
  TX_N=$((TX_N + 1))
  if [ "$TX_TEST" = 1 ] && [ "$TX_N" = "$TX_FAIL_AT" ]; then
    say "self-test: step $TX_N fails here: $1"
    return 1
  fi
  return 0
}
tx_created() { CREATED="$CREATED$NL$1"; }
tx_forget() { CREATED=$(printf '%s\n' "$CREATED" | grep -vxF -- "$1" || true); }
tx_own() {
  [ "$TX_TEST" = 1 ] && return 0
  chown 0:0 -- "$1"
}
tx_visudo() {
  [ "$TX_TEST" = 1 ] && return 0
  if ! tv_out=$(visudo "$@" 2>&1); then
    printf '%s\n' "$tv_out" | sed 's/^/    | /'
    return 1
  fi
}

# tx_mkdir PATH: a new folder, mode 0755, owner root.
tx_mkdir() {
  tx_step "make the folder $1" || return 1
  mkdir -m 0755 -- "$(hp "$1")" || return 1
  tx_created "d $1"
  tx_step "owner and mode of $1" || return 1
  tx_own "$(hp "$1")" || return 1
  chmod 0755 -- "$(hp "$1")" || return 1
  say "made $1 (mode 0755, owner root)"
}

# tx_publish PATH MODE TEXT CHECK: a new file at PATH with the output of the
# function TEXT, written under a temporary name in the same folder (a name
# with a dot, which sudo skips), checked (CHECK visudo: visudo -cf on it
# alone), then given its name with ln, which never replaces a file.
tx_publish() {
  tp_dir=${1%/*}
  tx_step "make a temporary file in $tp_dir" || return 1
  tp_tmp=$(mktemp "$(hp "$tp_dir")/${TMP_PREFIX}XXXXXX") || return 1
  tp_log=${tp_tmp#"$FIX"}
  tx_created "f $tp_log"
  tx_step "write $tp_log" || return 1
  "$3" >"$tp_tmp" || return 1
  if ! "$3" | cmp -s - "$tp_tmp"; then
    say "$tp_log does not hold the whole text (is the disk full?)"
    return 1
  fi
  tx_step "owner and mode of $tp_log" || return 1
  tx_own "$tp_tmp" || return 1
  chmod "$2" -- "$tp_tmp" || return 1
  if [ "$4" = visudo ]; then
    tx_step "visudo -cf $tp_log" || return 1
    tx_visudo -cf "$tp_tmp" || return 1
    say "visudo -cf (the new file alone): ok"
  fi
  tx_step "publish $1" || return 1
  if ! ln -- "$tp_tmp" "$(hp "$1")"; then
    say "$1 could not be made (did it appear meanwhile? it is never replaced)"
    return 1
  fi
  tx_created "f $1"
  tx_step "remove the temporary name $tp_log" || return 1
  rm -f -- "$tp_tmp" || return 1
  tx_forget "f $tp_log"
  say "wrote $1 (mode $2, owner root)"
}

# tx_rollback removes what this run created, newest first; a failed removal
# is reported and the rest goes on.
tx_rollback() {
  say "a step failed: removing the files this run created, newest first"
  tr_list=$(printf '%s\n' "$CREATED" | awk 'NF { a[++n] = $0 } END { for (i = n; i > 0; i--) print a[i] }')
  [ -n "$tr_list" ] || say "nothing was created"
  while read -r tr_k tr_p; do
    [ -n "$tr_k" ] || continue
    if [ "$TX_TEST" = 1 ] && [ "$TX_UNDO_FAIL" = 1 ]; then
      tr_ok=1
    elif [ "$tr_k" = d ]; then
      if rmdir -- "$(hp "$tr_p")"; then tr_ok=0; else tr_ok=1; fi
    else
      if rm -f -- "$(hp "$tr_p")"; then tr_ok=0; else tr_ok=1; fi
    fi
    if [ "$tr_ok" = 0 ]; then
      say "undone: removed $tr_p"
    elif [ "$tr_k" = d ]; then
      say "NOT undone: the folder $tr_p is still there: remove it by hand (rmdir $(sq "$tr_p"))"
    else
      say "NOT undone: $tr_p is still there: remove it by hand (rm -f $(sq "$tr_p"))"
    fi
  done <<EOF
$tr_list
EOF
  CREATED=
  if [ "$TX_TEST" = 1 ]; then
    return 0
  fi
  if visudo -c >/dev/null 2>&1; then
    say "visudo -c passes"
  else
    say "visudo -c fails: fix sudoers by hand from a root shell (visudo -c names the file)"
  fi
}

lock_take() {
  if ! mkdir -m 0700 -- "$(hp "$LOCK")" 2>/dev/null; then
    refuse "another --apply holds the lock $LOCK: wait for it; if none is running, remove it by hand (rmdir $LOCK) and run --apply again"
  fi
  LOCKED=1
  trap 'lock_drop' EXIT
  trap 'tx_abort' INT TERM HUP
}
lock_drop() {
  if [ "$LOCKED" = 1 ]; then
    rmdir -- "$(hp "$LOCK")" 2>/dev/null || true
    LOCKED=0
  fi
}
tx_abort() {
  trap - INT TERM HUP
  say "interrupted"
  tx_rollback
  exit 1
}

tx_apply() {
  while IFS= read -r ta_a; do
    case $ta_a in
      dropin)
        say "change: the drop-in $DROPIN"
        tx_publish "$DROPIN" 0440 dropin_text visudo || return 1
        ;;
      register-parent) tx_mkdir "$REGISTER_PARENT" || return 1 ;;
      register) tx_mkdir "$REGISTER" || return 1 ;;
      register-readme)
        say "change: the register's README"
        tx_publish "$REGISTER/README" 0644 register_readme none || return 1
        ;;
    esac
  done <<EOF
$ACTIONS
EOF
}

apply_main() {
  lock_take
  say "$ME --apply: the audit, read again now under the lock"
  if report; then :; fi
  if [ "$N_STOP" -gt 0 ] || [ "$N_HAND" -gt 0 ]; then
    refuse "the audit lists a stop or a by-hand edit (stops: $N_STOP; by hand: $N_HAND): nothing was written"
  fi
  say "== apply"
  [ "$N_CHANGE" -gt 0 ] || say "nothing to write"
  if ! tx_apply; then
    tx_rollback
    exit 1
  fi
  if ! { tx_step "visudo -c (the whole configuration)" && tx_visudo -c; }; then
    tx_rollback
    exit 1
  fi
  say "visudo -c (the whole configuration): ok"
  CREATED=
  if [ "$TX_TEST" = 1 ]; then
    say "self-test: the run took $TX_N steps"
    exit 0
  fi
  say "findings: $N_FIND (see the routes above; the script never changes them)"
  if verify_checks; then exit 0; fi
  exit 1
}

# --- --verify -------------------------------------------------------------------------

# verify_checks checks the result: sudo -l -U lists the grant that asks for the
# password and nothing without one, and sudo -n true run as the user says a
# password is required. Returns 1 when a check fails.
verify_checks() {
  say "== checks of the result"
  vf_bad=0
  # What sudo says the user may run.
  if [ -n "$FIX" ]; then
    vf_sl=$(if [ -f "$FIX/sudo-l.txt" ]; then cat "$FIX/sudo-l.txt"; fi)
    vf_slrc=$(if [ -f "$FIX/sudo-l.rc" ]; then cat "$FIX/sudo-l.rc"; elif [ -f "$FIX/sudo-l.txt" ]; then echo 0; else echo 127; fi)
  elif command -v sudo >/dev/null 2>&1; then
    if vf_sl=$(sudo -n -l -U "$NAME" 2>&1 </dev/null); then vf_slrc=0; else vf_slrc=$?; fi
  else
    vf_sl=
    vf_slrc=127
  fi
  vf_rec=$(printf '%s\n' "$vf_sl" | awk "$SUDOL_AWK")
  if [ "$vf_slrc" != 0 ]; then
    say "finding: verify: sudo -l -U $NAME failed (exit $vf_slrc): $(printf '%s\n' "$vf_sl" | head -n 1)"
    vf_bad=1
  else
    vf_grant=$(printf '%s\n' "$vf_rec" | awk -F "$TAB" '$1 == "GRANT" { print $2; exit }')
    if [ -n "$vf_grant" ]; then
      say "check: sudo -l -U $NAME lists a grant that asks for the password: $vf_grant"
    else
      say "finding: verify: sudo -l -U $NAME lists no grant of ALL as root that asks for the password: $NAME's intended grant is not there"
      vf_bad=1
    fi
    printf '%s\n' "$vf_rec" | awk -F "$TAB" '$1 == "NOPASS" || $1 == "NOAUTH" { print "finding: verify: sudo -l -U lists, without a password: " $2 }'
    if printf '%s\n' "$vf_rec" | grep -Eq "^(NOPASS|NOAUTH)$TAB"; then vf_bad=1; fi
    vf_to=$(printf '%s\n' "$vf_rec" | awk -F "$TAB" '$1 == "TIMEOUT" { print $2 }')
    case $vf_to in
      0 | 0.0) say "check: sudo -l -U $NAME: the last timestamp_timeout is 0" ;;
      '')
        say "finding: verify: sudo -l -U $NAME shows no timestamp_timeout: is the drop-in read?"
        vf_bad=1
        ;;
      *)
        say "finding: verify: sudo -l -U $NAME shows timestamp_timeout=$vf_to last: credentials are cached"
        vf_bad=1
        ;;
    esac
  fi
  # sudo -n true, run as the user.
  vf_env="env -i PATH=$SYS_PATH LC_ALL=C sudo -n true"
  if [ -n "$FIX" ]; then
    vf_out=$(if [ -f "$FIX/sudo-n.txt" ]; then cat "$FIX/sudo-n.txt"; else echo "(no sudo-n.txt in the fixtures)"; fi)
    vf_rc=$(if [ -f "$FIX/sudo-n.rc" ]; then cat "$FIX/sudo-n.rc"; else echo 1; fi)
  elif command -v runuser >/dev/null 2>&1; then
    # shellcheck disable=SC2086  # vf_env is a fixed command line of plain words
    if vf_out=$(runuser -u "$NAME" -- $vf_env </dev/null 2>&1); then vf_rc=0; else vf_rc=$?; fi
  elif command -v su >/dev/null 2>&1; then
    if vf_out=$(su -s /bin/sh -c "$vf_env" "$NAME" </dev/null 2>&1); then vf_rc=0; else vf_rc=$?; fi
  else
    vf_out="neither runuser nor su is here"
    vf_rc=127
  fi
  vf_line=$(printf '%s\n' "$vf_out" | head -n 1)
  if [ "$vf_rc" = 0 ]; then
    say "finding: sudo-n: sudo -n true run as $NAME succeeds: $NAME still has sudo without a password"
    vf_bad=1
  elif printf '%s\n' "$vf_out" | grep -q 'a password is required'; then
    say "check: sudo -n true run as $NAME fails: a password is required"
  elif printf '%s\n' "$vf_out" | grep -Eq 'not in the sudoers file|is not allowed to|may not run sudo'; then
    say "finding: sudo-n: sudo refuses $NAME outright (not allowed, no password asked): $vf_line"
    vf_bad=1
  else
    say "finding: sudo-n: the probe sudo -n true as $NAME could not run or said something else (exit $vf_rc): $vf_line"
    say "  remedy: as $NAME, in a new terminal, run sudo -k; sudo -n true: it must say that a password is required"
    vf_bad=1
  fi
  return "$vf_bad"
}

# --- self-test ----------------------------------------------------------------------

# fx_root DIR sets FIX to the physical path of DIR, refusing / and a fixture
# tree with an unrepresentable name or a link that leads out of it.
fx_root() {
  fr_d=$(cd -P -- "$1" 2>/dev/null && pwd -P) || refuse "--fixtures must name a folder"
  [ "$fr_d" != / ] || refuse "--fixtures must not be /: that is this host's own configuration"
  if [ -n "$(find "$fr_d" \( -name "*$NL*" -o -name "*$TAB*" \) -print | head -n 1)" ]; then
    refuse "a file name in the fixtures holds a newline or a tab"
  fi
  fr_bad=$(find "$fr_d" -type l -print | while IFS= read -r fr_l; do
    if command -v realpath >/dev/null 2>&1 && fr_t=$(realpath -- "$fr_l" 2>/dev/null); then
      case $fr_t in
        "$fr_d" | "$fr_d"/*) continue ;;
      esac
    fi
    say "${fr_l#"$fr_d"}"
  done | head -n 1)
  [ -z "$fr_bad" ] || refuse "a link in the fixtures leads out of them (or cannot be resolved): $fr_bad"
  FIX=$fr_d
}

# st_snapshot ROOT [notmp] prints a line per path below ROOT/etc: kind, mode,
# checksum and path, sorted; notmp leaves out temporary files.
st_snapshot() (
  cd -P -- "$1" || exit 1
  find etc -print | sort | while IFS= read -r ss_p; do
    case $ss_p in
      */"$TMP_PREFIX"*) [ "${2-}" != notmp ] || continue ;;
    esac
    # shellcheck disable=SC2012  # the mode string only; names are representable
    ss_m=$(ls -ld -- "$ss_p" | cut -c1-10)
    if [ -L "$ss_p" ]; then
      printf 'l %s %s\n' "$ss_m" "$ss_p"
    elif [ -d "$ss_p" ]; then
      printf 'd %s %s\n' "$ss_m" "$ss_p"
    else
      printf 'f %s %s %s\n' "$ss_m" "$(cksum <"$ss_p" | cut -d ' ' -f 1)" "$ss_p"
    fi
  done
)

# st_copy CASE DEST copies a transaction case to DEST, applies its modes.txt
# and makes DEST/run for the lock.
st_copy() {
  mkdir -- "$2" || return 1
  cp -RP -- "$1/." "$2/" || return 1
  mkdir -p -- "$2/run" || return 1
  if [ -f "$2/modes.txt" ]; then
    while read -r sc_p sc_m; do
      [ -n "$sc_p" ] || continue
      chmod "$sc_m" -- "$2$sc_p" || return 1
    done <"$2/modes.txt"
  fi
  fx_root "$2"
}

# st_apply ROOT USER FAIL UNDOFAIL runs --apply against ROOT, prints its output
# and its exit status as the last line.
st_apply() {
  # shellcheck disable=SC2030  # the run's own fake root, in its subshell
  if sa_out=$(
    FIX=$1
    set_user "$2"
    TX_TEST=1
    TX_FAIL_AT=$3
    TX_UNDO_FAIL=$4
    apply_main 2>&1
  ); then sa_rc=0; else sa_rc=$?; fi
  printf '%s\nexit: %s\n' "$sa_out" "$sa_rc"
}

# st_tx CASE USER checks the transaction of a case: refused.txt, the line the
# refusal must print, nothing written; after.txt, the snapshot after a
# successful run, every failure undone, a rerun after a failed undo recovers,
# and a second run changes nothing.
st_tx() {
  st_c=$1
  st_u=$2
  st_w=$(mktemp -d "${TMPDIR:-/tmp}/ap14-self-test.XXXXXX") || return 1
  st_ok=0
  st_run=0
  st_fresh() {
    st_run=$((st_run + 1))
    st_r=$st_w/r$st_run
    st_copy "$st_c" "$st_r" || return 1
  }
  if [ -f "$st_c/refused.txt" ]; then
    st_fresh || st_ok=1
    st_pre=$(st_snapshot "$st_r")
    st_o=$(st_apply "$st_r" "$st_u" 0 0)
    st_want=$(cat "$st_c/refused.txt")
    case $st_o in
      *"$st_want"*"exit: 2") ;;
      *)
        say "self-test: FAIL ${st_c##*/}: --apply was not refused with: $st_want"
        printf '%s\n' "$st_o" | tail -n 5
        st_ok=1
        ;;
    esac
    if [ "$(st_snapshot "$st_r")" != "$st_pre" ]; then
      say "self-test: FAIL ${st_c##*/}: the refused --apply changed the tree"
      st_ok=1
    fi
  fi
  if [ -f "$st_c/after.txt" ]; then
    st_after=$(cat "$st_c/after.txt")
    # A run without a failure, then a second one.
    st_fresh || st_ok=1
    st_pre=$(st_snapshot "$st_r")
    printf '%s\n' "$st_pre" >"$st_w/pre.txt"
    st_o=$(st_apply "$st_r" "$st_u" 0 0)
    st_steps=$(printf '%s\n' "$st_o" | sed -n 's/^self-test: the run took \([0-9]*\) steps$/\1/p')
    if [ -z "$st_steps" ] || [ "$(printf '%s\n' "$st_o" | tail -n 1)" != "exit: 0" ] || [ "$(st_snapshot "$st_r")" != "$st_after" ]; then
      say "self-test: FAIL ${st_c##*/}: --apply did not end with the tree of after.txt:"
      printf '%s\n' "$st_o" | tail -n 5
      st_snapshot "$st_r" | diff -u "$st_c/after.txt" - || true
      st_ok=1
      st_steps=0
    fi
    st_o=$(st_apply "$st_r" "$st_u" 0 0)
    case $st_o in
      *"nothing to write"*"exit: 0") ;;
      *)
        say "self-test: FAIL ${st_c##*/}: a second --apply did not say nothing to write"
        st_ok=1
        ;;
    esac
    [ "$(st_snapshot "$st_r")" = "$st_after" ] || {
      say "self-test: FAIL ${st_c##*/}: a second --apply changed the tree"
      st_ok=1
    }
    # A failure at every step: undone, so the tree is as before.
    st_k=1
    while [ "$st_k" -le "$st_steps" ]; do
      st_fresh || st_ok=1
      st_o=$(st_apply "$st_r" "$st_u" "$st_k" 0)
      if [ "$(printf '%s\n' "$st_o" | tail -n 1)" != "exit: 1" ] || [ "$(st_snapshot "$st_r")" != "$st_pre" ] || [ -d "$st_r/run/ap14-level1.lock" ]; then
        say "self-test: FAIL ${st_c##*/}: a failure at step $st_k was not undone:"
        printf '%s\n' "$st_o" | tail -n 8
        st_snapshot "$st_r" | diff -u "$st_w/pre.txt" - || true
        st_ok=1
      fi
      # The same failure with every undo failing too: reported, and a rerun
      # recovers.
      st_fresh || st_ok=1
      st_o=$(st_apply "$st_r" "$st_u" "$st_k" 1)
      if printf '%s\n' "$st_o" | grep -q '^undone: '; then
        say "self-test: FAIL ${st_c##*/}: step $st_k: an undo that failed was reported as done"
        st_ok=1
      fi
      st_o=$(st_apply "$st_r" "$st_u" 0 0)
      if [ "$(printf '%s\n' "$st_o" | tail -n 1)" != "exit: 0" ] || [ "$(st_snapshot "$st_r" notmp)" != "$st_after" ]; then
        say "self-test: FAIL ${st_c##*/}: the rerun after a failed undo at step $st_k did not recover:"
        printf '%s\n' "$st_o" | tail -n 8
        st_ok=1
      fi
      st_k=$((st_k + 1))
    done
    ST_DETAIL="transaction of steps: $st_steps; each failure undone, each rerun recovers"
  else
    ST_DETAIL="transaction: --apply refused, nothing written"
  fi
  rm -rf -- "$st_w"
  return "$st_ok"
}

self_test() {
  if [ -n "$FIX_ARG" ]; then
    fx_root "$FIX_ARG"
    if report; then st_code=0; else st_code=1; fi
    # shellcheck disable=SC2031  # fx_root set FIX in this shell
    if [ -f "$FIX/sudo-n.txt" ]; then
      if ! verify_checks; then st_code=1; fi
    fi
    exit "$st_code"
  fi
  case $0 in
    */*) st_dir=${0%/*} ;;
    *) st_dir=. ;;
  esac
  st_base=$st_dir/self-test
  [ -d "$st_base" ] || {
    say "self-test: no folder $st_base: run the self-test from the checkout, with self-test/ beside the script"
    exit 1
  }
  st_fail=0
  st_n=0
  for st_case in "$st_base"/*/; do
    st_case=${st_case%/}
    [ -f "$st_case/user.txt" ] || continue
    st_user=$(cat "$st_case/user.txt")
    valid_name "$st_user" || {
      say "self-test: FAIL ${st_case##*/}: user.txt is not a login name"
      st_fail=1
      continue
    }
    if [ -f "$st_case/expected.txt" ]; then
      st_n=$((st_n + 1))
      if st_out=$(
        fx_root "$st_case"
        set_user "$st_user"
        report 2>&1
      ); then st_code=0; else st_code=$?; fi
      st_out="$st_out
exit: $st_code"
      if printf '%s\n' "$st_out" | cmp -s - "$st_case/expected.txt"; then
        say "self-test: ok ${st_case##*/} (audit)"
      else
        say "self-test: FAIL ${st_case##*/}: the audit differs from expected.txt:"
        printf '%s\n' "$st_out" | diff -u "$st_case/expected.txt" - || true
        st_fail=1
      fi
    fi
    if [ -f "$st_case/after.txt" ] || [ -f "$st_case/refused.txt" ]; then
      st_n=$((st_n + 1))
      ST_DETAIL=
      if st_tx "$st_case" "$st_user"; then
        say "self-test: ok ${st_case##*/} ($ST_DETAIL)"
      else
        st_fail=1
      fi
    fi
  done
  [ "$st_n" -gt 0 ] || {
    say "self-test: no case in $st_base"
    exit 1
  }
  if [ "$st_fail" = 0 ]; then
    say "self-test: ok, $st_n checks; it read only the fixtures, wrote only temporary copies of them, and ran nothing from them"
    exit 0
  fi
  exit 1
}

# --- main ---------------------------------------------------------------------------

case $MODE in
  self-test) self_test ;;
  dry)
    say "$ME --dry: reads only; changes nothing"
    if report; then exit 0; else exit 1; fi
    ;;
  apply) apply_main ;;
  verify)
    say "$ME --verify: reads only; changes nothing"
    if verify_checks; then exit 0; else exit 1; fi
    ;;
esac
