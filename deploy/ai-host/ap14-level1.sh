#!/bin/sh
# ap14-level1.sh: AP14 level 1 on an AI (development) host. The everyday user
# loses passwordless and cached sudo, so `sudo -n true` fails for it, and every
# other route from that user to root that the script can find is reported.
# It also creates the root-owned standing-grants register (DL-1).
#
#   ap14-level1.sh --dry --user NAME
#   ap14-level1.sh --apply --user NAME --confirm NAME
#   ap14-level1.sh --self-test [--fixtures DIR --user NAME]
#
#   --dry        run as root. Reads the sudo, polkit and doas configuration,
#                the user's groups, the setuid files and the folders root runs
#                things from, then prints every change --apply would make, in
#                order, and every route to root it finds. Changes nothing.
#   --apply      run as root, with --confirm repeating the --user name. Reads
#                everything again, prints the same plan and makes only those
#                changes, in the same order:
#                  1. write the sudoers drop-in /etc/sudoers.d/zz-ap14-level1,
#                     `Defaults:NAME timestamp_timeout=0` (no cached
#                     credentials), checked with visudo before and after;
#                  2. move aside, into a root-only backup folder, each sudoers
#                     file in /etc/sudoers.d whose one rule gives NAME sudo
#                     without a password, checking with visudo after each;
#                  3. create the standing-grants register /etc/ataniq/grants.
#                A failed visudo check undoes every sudoers change of the run.
#                Then it checks the result (`sudo -l -U NAME`, and `sudo -n
#                true` run as NAME) and reports again.
#   --self-test  reads nothing on the host and runs nothing: it runs the same
#                scan over the fixture folders in self-test/ beside the script
#                and compares the reports with the expected ones. With
#                --fixtures DIR it prints the report for the fake root DIR
#                instead (the tests use it).
#
# The script never changes a group, a polkit rule, doas, a setuid file or a
# permission: it reports them, each with a one-line remedy for the owner. It
# rewrites no sudoers file: a passwordless rule that does not sit alone in a
# file of /etc/sudoers.d is listed with its file and line, and --apply then
# stops before changing anything.
#
# The user's name is an argument only; it is never written into this script.
#
# POSIX sh. Tools: awk, sed, grep, sort, cut, head, ls, find, id, getent,
# mktemp, date, chmod, chown, mv, rm, mkdir, cmp and diff (--self-test);
# visudo and sudo for the sudo steps; runuser (or su) for the `sudo -n` check;
# passwd -S; pacman, dpkg or rpm when present; getfacl when present. Before
# the root check it runs no program but `id -u`.
#
# Exit status: 0 clean (nothing to change, no finding); 1 findings remain (for
# --dry: something to change or a finding; for --apply: a finding after the
# changes, or a failed check whose changes were undone); 2 refused before
# anything changed (bad arguments, not root, or a stop in the plan).

# shellcheck disable=SC2016  # the awk programs are single-quoted on purpose
set -eu
LC_ALL=C
export LC_ALL
umask 077

ME=ap14-level1.sh
TAB=$(printf '\t')

SUDOERS=/etc/sudoers
SUDOERS_D=/etc/sudoers.d
DROPIN_NAME=zz-ap14-level1
DROPIN=$SUDOERS_D/$DROPIN_NAME
BACKUP_BASE=/etc/ap14-level1
REGISTER_PARENT=/etc/ataniq
REGISTER=$REGISTER_PARENT/grants
POLKIT_ETC=/etc/polkit-1/rules.d
POLKIT_USR=/usr/share/polkit-1/rules.d
DOAS_FILES="/etc/doas.conf /usr/local/etc/doas.conf"
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
    "       $ME --self-test [--fixtures DIR --user NAME]" >&2
  exit 2
}

# valid_name TEXT: a login name: lowercase letter or _, then lowercase letters,
# digits, _ . or -, at most 32 characters.
valid_name() {
  case $1 in
    [a-z_]*) ;;
    *) return 1 ;;
  esac
  case $1 in
    *[!a-z0-9_.-]*) return 1 ;;
  esac
  [ ${#1} -le 32 ]
}

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
    --dry | --apply | --self-test)
      [ -z "$MODE" ] || [ "$MODE" = "${1#--}" ] || refuse "give one of --dry, --apply and --self-test"
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
if [ "$MODE" != self-test ] || [ -n "$FIX_ARG" ]; then
  [ "$HAVE_USER" = 1 ] || refuse "--user NAME is required: the everyday user's login name"
  valid_name "$NAME" || refuse "--user must be a login name (lowercase letters, digits, _ . -)"
  [ "$NAME" != root ] || refuse "--user must be the everyday user, not root"
fi
if [ "$MODE" = apply ]; then
  [ "$HAVE_CONFIRM" = 1 ] || refuse "--apply needs --confirm NAME, repeating the --user name"
  [ "$CONFIRM" = "$NAME" ] || refuse "--confirm must repeat the --user name exactly"
fi
if [ "$MODE" != self-test ]; then
  [ "$(id -u)" = 0 ] || refuse "--$MODE runs as root (log in as root, or sudo -i, then run it)"
fi

# FIX is the fake root of a self-test; empty on the host. hp PATH prints where
# PATH is read from.
FIX=
hp() { printf '%s%s' "$FIX" "$1"; }

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
#   NOPASS FILE LINE CERTAIN AS TEXT   a rule with NOPASSWD for the user
#   NOAUTH FILE LINE CERTAIN - TEXT    Defaults !authenticate for the user
#   LATE FILE LINE VALUE TEXT          a non-zero timestamp_timeout after the
#                                      place of the drop-in
#   WARN FILE LINE TEXT                something to check by hand
#   PLACE 0|1                          whether the drop-in would be read
#   LINES FILE COUNT VIA               content lines of each file read
# CERTAIN is yes, or maybe (a netgroup or non-Unix group may hold the user).
# AS is root when the rule may run commands as root, else other.
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
# nopass RHS: "" when no command of the rule runs without a password, else
# root or other.
function nopass(s,   t, root, any, ng, grp, gi, g, m, parts, pi, piece, state, hit) {
  t = s
  root = 0
  any = 0
  while (match(t, /\([^)]*\)/)) {
    any = 1
    if (asroot(substr(t, RSTART + 1, RLENGTH - 2))) root = 1
    t = substr(t, RSTART + RLENGTH)
  }
  if (!any) root = 1
  gsub(/\([^)]*\)/, " ", s)
  gsub(/NOPASSWD[ ]*:/, " @@N@@ ", s)
  gsub(/PASSWD[ ]*:/, " @@P@@ ", s)
  gsub(/(NOEXEC|EXEC|NOSETENV|SETENV|NOLOG_INPUT|LOG_INPUT|NOLOG_OUTPUT|LOG_OUTPUT|NOMAIL|MAIL|NOFOLLOW|FOLLOW|NOINTERCEPT|INTERCEPT)[ ]*:/, " ", s)
  ng = split(s, grp, ":")
  hit = 0
  state = 0
  for (gi = 1; gi <= ng; gi++) {
    g = grp[gi]
    if (gi > 1 && g ~ /^ *[^ =,]+( *, *[^ =,]+)* *=/) { state = 0; g = substr(g, index(g, "=") + 1) }
    m = split(g, parts, ",")
    for (pi = 1; pi <= m; pi++) {
      piece = parts[pi]
      while (match(piece, /@@[NP]@@/)) {
        state = (substr(piece, RSTART + 2, 1) == "N")
        piece = substr(piece, 1, RSTART - 1) " " substr(piece, RSTART + RLENGTH)
      }
      if (state && trim(piece) != "") hit = 1
    }
  }
  if (!hit) return ""
  return root ? "root" : "other"
}
function defaults(f, ln, t,   rest, c, sp, bind, set, app, m, S, j, s, v) {
  rest = substr(t, 9)
  c = substr(rest, 1, 1)
  app = 1
  if (c == ":" || c == "@" || c == ">" || c == "!") {
    sp = index(rest, " ")
    if (sp == 0) return
    bind = substr(rest, 2, sp - 2)
    set = substr(rest, sp + 1)
    if (c == ":") { app = ulist(bind, 0); if (app == 0) return }
  } else set = rest
  m = split(set, S, ",")
  for (j = 1; j <= m; j++) {
    s = trim(S[j])
    if (s == "!authenticate") print "NOAUTH\t" f "\t" ln "\t" (app == 2 ? "maybe" : "yes") "\t-\t" t
    else if (s ~ /^timestamp_timeout[ ]*=/) {
      v = s
      sub(/^timestamp_timeout[ ]*=[ ]*/, "", v)
      gsub(/"/, "", v)
      if (after && (v !~ /^[0-9.]+$/ || v + 0 != 0)) print "LATE\t" f "\t" ln "\t" v "\t" t
    }
  }
}
function userspec(f, ln, t,   e, lhs, rhs, W, nl, r, np) {
  e = index(t, "=")
  if (e == 0) return
  lhs = substr(t, 1, e - 1)
  rhs = substr(t, e + 1)
  gsub(/ *, */, ",", lhs)
  nl = split(trim(lhs), W, " ")
  if (nl < 2) return
  r = ulist(W[1], 0)
  if (r == 0) return
  np = nopass(rhs)
  if (np == "") return
  print "NOPASS\t" f "\t" ln "\t" (r == 2 ? "maybe" : "yes") "\t" np "\t" t
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
# each command listed with NOPASSWD, NOAUTH for !authenticate among the
# defaults, and TIMEOUT VALUE for the last timestamp_timeout among them.
SUDOL_AWK='
function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
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
sec == "cmd" && /NOPASSWD[ ]*:/ { print "NOPASS\t" trim($0); next }
sec == "cmd" && /^[ \t]*Options:.*!authenticate/ { print "NOAUTH\t" trim($0); next }
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
    lu_pw=$(cat "$FIX/etc/passwd")
    lu_gr=$(cat "$FIX/etc/group")
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

# sudoers_expand FILE DEPTH VIA prints the record stream of FILE and of what it
# includes, in the order sudo reads them: B FILE VIA when a file starts, L FILE
# LINE TEXT, I or D FILE LINE PATH, W FILE LINE TEXT, and P where the drop-in
# would sit in /etc/sudoers.d. A subshell keeps each level's variables.
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
    printf 'W\t%s\t0\tnot a readable file; not checked\n' "$se_f"
    exit 0
  fi
  awk "$LINES_AWK" "$se_m" | while IFS="$TAB" read -r se_k se_ln se_rest; do
    case $se_k in
      L) printf 'L\t%s\t%s\t%s\n' "$se_f" "$se_ln" "$se_rest" ;;
      I | D)
        se_p=$se_rest
        case $se_p in
          *%h*)
            printf 'W\t%s\t%s\t%s\n' "$se_f" "$se_ln" "an include names %h (the host name); check it by hand"
            continue
            ;;
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
        if [ ! -d "$se_md" ]; then
          printf 'W\t%s\t%s\t%s\n' "$se_f" "$se_ln" "includedir names a missing folder"
          continue
        fi
        # shellcheck disable=SC2012  # sudo reads plain names; a name with a newline is not one
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

# field N TEXT: the Nth tab-separated field of a record.
field() { printf '%s\n' "$2" | cut -f "$1"; }

# dir_safe PATH: PATH is a folder (not a link) owned by root that no one else
# may write. file_safe PATH: the same for a regular file.
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

# --- the sudo part: what is read, and the plan --------------------------------------

DROPIN_TEXT=
dropin_text() {
  DROPIN_TEXT="# AP14 level 1, written by $ME: sudo asks for the password every
# time; no cached credentials for this user.
Defaults:$NAME timestamp_timeout=0"
}

# Filled by plan_sudo and plan_register:
#   PLAN     display lines: change N: ... / no change: ... / stop: ... / do: ...
#   ACTIONS  dropin new|replace / move FILE / register
#   SFIND    sudo findings (finding: and remedy: lines)
#   N_CHANGE N_STOP
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

plan_sudo() {
  say "== sudo"
  if [ ! -f "$(hp "$SUDOERS")" ]; then
    if [ -n "$FIX" ] || ! command -v sudo >/dev/null 2>&1; then
      say "sudo is not installed (no $SUDOERS): nothing to change for sudo"
      return 0
    fi
    plan_add_stop "$SUDOERS is not a readable file" "check the sudo installation by hand, then run --dry again"
    return 0
  fi
  if [ -z "$FIX" ] && ! command -v visudo >/dev/null 2>&1; then
    plan_add_stop "visudo is not installed: no change to sudoers can be checked" "install sudo's visudo, then run --dry again"
  fi
  ps_stream=$(sudoers_expand "$SUDOERS" 0 top)
  SA=$(printf '%s\n' "$ps_stream" | awk -v u="$NAME" -v uid="$U_UID" -v groups="$U_GROUPS" -v gids="$U_GIDS" "$SUDO_AWK")
  say "read: $(printf '%s\n' "$SA" | awk -F "$TAB" '$1 == "LINES" { printf "%s%s", (n++ ? ", " : ""), $2 } END { if (!n) printf "nothing" }')"
  printf '%s\n' "$SA" | while IFS="$TAB" read -r w_k w_f w_ln w_t; do
    [ "$w_k" = WARN ] || continue
    say "check by hand: $w_f:$w_ln: $w_t"
  done

  # The drop-in.
  dropin_text
  if printf '%s\n' "$SA" | grep -q "^PLACE${TAB}1\$"; then
    ps_d=$(hp "$DROPIN")
    if [ -L "$ps_d" ] || { [ -e "$ps_d" ] && [ ! -f "$ps_d" ]; }; then
      plan_add_stop "$DROPIN exists and is not a regular file" "look at it and remove it by hand, then run --dry again"
    elif [ -f "$ps_d" ] && [ "$(cat "$ps_d")" = "$DROPIN_TEXT" ]; then
      plan_add_same "$DROPIN already says: Defaults:$NAME timestamp_timeout=0"
    elif [ -f "$ps_d" ]; then
      plan_add_change "replace $DROPIN (the old copy goes to the backup folder), mode 0440, owner root, with:
$(printf '%s\n' "$DROPIN_TEXT" | sed 's/^/    | /')" "dropin${TAB}replace"
    else
      plan_add_change "write $DROPIN, mode 0440, owner root, with:
$(printf '%s\n' "$DROPIN_TEXT" | sed 's/^/    | /')" "dropin${TAB}new"
    fi
  else
    plan_add_stop "$SUDOERS does not read $SUDOERS_D (no includedir for it): the drop-in would have no effect" "add the line @includedir $SUDOERS_D at the end of $SUDOERS with visudo, then run --dry again"
  fi

  # Passwordless rules.
  ps_moved=
  ps_rules=$(printf '%s\n' "$SA" | grep -E "^(NOPASS|NOAUTH)$TAB" || true)
  if [ -z "$ps_rules" ]; then
    say "no rule gives $NAME sudo without a password"
  fi
  while IFS="$TAB" read -r r_k r_f r_ln r_c r_as r_t; do
    [ -n "$r_k" ] || continue
    if [ "$r_k" = NOAUTH ]; then r_what="Defaults !authenticate"; else r_what="NOPASSWD, as $r_as"; fi
    say "rule: $r_f:$r_ln: $r_what: $r_t"
    if [ "$r_c" = maybe ]; then
      sfind "sudo-nopasswd: $r_f:$r_ln: may apply to $NAME (a netgroup or non-Unix group): $r_t" "check whether $NAME is in it; if so, remove the rule with visudo -f $r_f"
      continue
    fi
    r_info=$(printf '%s\n' "$SA" | awk -F "$TAB" -v f="$r_f" '$1 == "LINES" && $2 == f { print $3 "\t" $4 }')
    r_count=$(field 1 "$r_info")
    r_via=$(field 2 "$r_info")
    if [ "${r_f%/*}" = "$SUDOERS_D" ] && [ "$r_via" = "dir:$SUDOERS_D" ] && [ "$r_count" = 1 ] &&
      [ "$r_f" != "$DROPIN" ] && [ ! -L "$(hp "$r_f")" ] && [ -f "$(hp "$r_f")" ]; then
      case "
$ps_moved
" in
        *"
$r_f
"*) ;;
        *)
          ps_moved="$ps_moved
$r_f"
          plan_add_change "move $r_f aside into $BACKUP_BASE/backup-<UTC time of the run>/ (its one rule, line $r_ln: $r_t)" "move${TAB}$r_f"
          ;;
      esac
    else
      plan_add_stop "$r_f:$r_ln: $r_t" "edit it with visudo -f $r_f: drop NOPASSWD (or !authenticate) for $NAME and its groups, or remove the line; then run --dry again"
      sfind "sudo-nopasswd: $r_f:$r_ln: $r_t" "remove NOPASSWD (or !authenticate) for $NAME with visudo -f $r_f"
    fi
  done <<EOF
$ps_rules
EOF

  # Later settings of the cache time.
  while IFS="$TAB" read -r l_k l_f l_ln l_v l_t; do
    [ "$l_k" = LATE ] || continue
    sfind "sudo-timeout: $l_f:$l_ln: sets timestamp_timeout=$l_v after the drop-in, which it may override: $l_t" "remove it or make it timestamp_timeout=0, with visudo -f $l_f"
  done <<EOF
$SA
EOF

  # The backup folder, when something moves.
  if printf '%s\n' "$ACTIONS" | grep -Eq "^(move|dropin${TAB}replace)"; then
    if [ -e "$(hp "$BACKUP_BASE")" ] || [ -L "$(hp "$BACKUP_BASE")" ]; then
      dir_safe "$BACKUP_BASE" ||
        plan_add_stop "$BACKUP_BASE exists but is not a folder owned by root that only root may write" "look at it and remove it by hand, then run --dry again"
    fi
  fi
}

plan_register() {
  pr_new=
  for pr_p in "$REGISTER_PARENT" "$REGISTER"; do
    if [ -e "$(hp "$pr_p")" ] || [ -L "$(hp "$pr_p")" ]; then
      if ! dir_safe "$pr_p"; then
        plan_add_stop "$pr_p exists but is not a folder owned by root that only root may write" "look at who made it and what it holds, move it away by hand, then run --dry again"
        return 0
      fi
    else
      pr_new="$pr_new $pr_p"
    fi
  done
  pr_readme=$REGISTER/README
  if [ -z "$pr_new" ]; then
    if [ -e "$(hp "$pr_readme")" ] || [ -L "$(hp "$pr_readme")" ]; then
      if file_safe "$pr_readme"; then
        plan_add_same "the standing-grants register $REGISTER exists, owned by root"
      else
        plan_add_stop "$pr_readme is not a file owned by root that only root may write" "look at it and remove it by hand, then run --dry again"
      fi
      return 0
    fi
  fi
  plan_add_change "create the standing-grants register $REGISTER (folders mode 0755, owner root), holding only a README (mode 0644): the owner's verbatim words go there; agents may read it, never write it" "register"
}

plan_password() {
  if [ -n "$FIX" ]; then
    [ -f "$FIX/passwd-S.txt" ] || return 0
    pp_st=$(awk '{ print $2; exit }' "$FIX/passwd-S.txt")
  elif command -v passwd >/dev/null 2>&1; then
    pp_st=$(passwd -S "$NAME" 2>/dev/null | awk '{ print $2; exit }') || pp_st=
  else
    pp_st=
  fi
  case $pp_st in
    P | PS) say "$NAME has a usable password: sudo can still ask for it" ;;
    L | LK | NP)
      if [ "$N_CHANGE" -gt 0 ]; then
        plan_add_stop "$NAME has no usable password (passwd -S says $pp_st): after the change sudo would ask for a password $NAME does not have" "set one with passwd $NAME (as root), or make sure root can log in another way, then run --dry again"
      fi
      ;;
    *) say "note: passwd -S did not say whether $NAME has a usable password; make sure it has one before --apply" ;;
  esac
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
    # shellcheck disable=SC2012  # polkit reads plain names; a name with a newline is not one
    ls -A "$rt_md" | sort | while IFS= read -r rt_e; do
      case $rt_e in *.rules) ;; *) continue ;; esac
      if [ "$rt_d" = "$POLKIT_USR" ] && [ -e "$(hp "$POLKIT_ETC")/$rt_e" ]; then continue; fi
      [ -f "$rt_md/$rt_e" ] || continue
      awk -v F="$rt_d/$rt_e" -v u="$NAME" -v groups="$U_GROUPS" -v sq="'" "$POLKIT_AWK" "$rt_md/$rt_e"
    done
  done | while IFS="$TAB" read -r pk_k pk_kind pk_f pk_ln pk_a; do
    [ "$pk_k" = POLKIT ] || continue
    case $pk_kind in
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
      finding "setuid: $rt_s is setuid or setgid root; its package is unknown" "check where it comes from; chmod u-s,g-s $rt_s unless it is meant to be there"
    else
      finding "setuid: $rt_s is setuid or setgid root and no package owns it" "remove it, or chmod u-s,g-s $rt_s, unless you put it there on purpose"
    fi
  done <<EOF
$rt_suid
EOF

  # Root's PATH.
  if [ -n "$FIX" ]; then
    rt_path=$(if [ -f "$FIX/root-path.txt" ]; then cat "$FIX/root-path.txt"; else say "$STD_PATH"; fi)
  else
    rt_path=$PATH
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
    finding "writable: $NAME may write $rt_e, which root runs or reads as configuration" "chown root:root $rt_e and chmod go-w $rt_e (or remove $NAME's access)"
  done
  printf '%s\n' "$rt_acl" | awk -v u="$NAME" -v uid="$U_UID" -v groups="$U_GROUPS" -v gids="$U_GIDS" "$ACL_AWK" |
    while IFS="$TAB" read -r ac_k ac_p ac_e; do
      [ "$ac_k" = ACL ] || continue
      finding "acl: $ac_p grants $ac_e: $NAME may write it" "setfacl -x ${ac_e%:*} $ac_p"
    done

  # What sudo itself says.
  if [ -n "$FIX" ]; then
    rt_sl=$(if [ -f "$FIX/sudo-l.txt" ]; then cat "$FIX/sudo-l.txt"; fi)
  elif command -v sudo >/dev/null 2>&1; then
    rt_sl=$(sudo -l -U "$NAME" 2>&1) || true
  else
    rt_sl=
  fi
  if [ -n "$rt_sl" ]; then
    printf '%s\n' "$rt_sl" | awk "$SUDOL_AWK" | while IFS="$TAB" read -r sl_k sl_t; do
      case $sl_k in
        NOPASS) finding "sudo-l: sudo -l -U $NAME lists a command without a password: $sl_t" "find the rule (see the sudo part above) and remove NOPASSWD" ;;
        NOAUTH) finding "sudo-l: sudo -l -U $NAME lists !authenticate: $sl_t" "find the Defaults line and remove !authenticate" ;;
        TIMEOUT)
          case $sl_t in
            0 | 0.0) say "check: sudo -l -U $NAME: the last timestamp_timeout is 0" ;;
            *) finding "sudo-l: sudo -l -U $NAME shows timestamp_timeout=$sl_t last: credentials are cached" "remove the later timestamp_timeout setting (see the sudo part above)" ;;
          esac
          ;;
      esac
    done
  fi
}

# --- the report ---------------------------------------------------------------------

# report prints what the run reads, the plan and the routes, and returns 0
# when there is nothing to change and nothing found, else 1.
report() {
  PLAN=
  ACTIONS=
  SFIND=
  N_CHANGE=0
  N_STOP=0
  load_user
  say "== user"
  say "user: $NAME, uid $U_UID; groups: ${U_GROUPS:-none}"
  plan_sudo
  plan_password
  plan_register
  say "== plan: what --apply changes, in this order"
  if [ -n "$PLAN" ]; then printf '%s\n' "$PLAN" | sed '1{/^$/d;}'; fi
  [ "$N_CHANGE" -gt 0 ] || [ "$N_STOP" -gt 0 ] || say "nothing to change"
  if [ "$N_STOP" -gt 0 ]; then
    say "--apply refuses while a stop is listed: it changes nothing until each is handled"
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
  say "changes: $N_CHANGE; stops: $N_STOP; findings: $N_FIND"
  [ "$N_CHANGE" -eq 0 ] && [ "$N_STOP" -eq 0 ] && [ "$N_FIND" -eq 0 ]
}

# --- --apply ------------------------------------------------------------------------

UNDO=
undo_push() { UNDO="$UNDO
$1"; }
rollback() {
  say "a check failed: undoing every sudoers change of this run"
  printf '%s\n' "$UNDO" | awk 'NF { a[++n] = $0 } END { for (i = n; i > 0; i--) print a[i] }' |
    while IFS="$TAB" read -r rb_k rb_a rb_b; do
      case $rb_k in
        rm)
          rm -f "$rb_a"
          say "undone: removed $rb_a"
          ;;
        mv)
          mv -f "$rb_a" "$rb_b"
          say "undone: moved $rb_a back to $rb_b"
          ;;
      esac
    done
  if visudo -c >/dev/null 2>&1; then
    say "visudo -c passes again"
  else
    say "visudo -c still fails: fix sudoers by hand from this root shell (visudo -c names the file)"
  fi
  exit 1
}
check_sudoers() {
  if ! cs_out=$(visudo -c 2>&1); then
    printf '%s\n' "$cs_out" | sed 's/^/    | /'
    rollback
  fi
  say "visudo -c: ok"
}

apply_changes() {
  ac_stamp=$(date -u +%Y%m%dT%H%M%SZ)
  ac_bk=$BACKUP_BASE/backup-$ac_stamp
  ac_n=0
  if printf '%s\n' "$ACTIONS" | grep -Eq "^(move|dropin${TAB}replace)"; then
    if [ ! -d "$BACKUP_BASE" ]; then
      { mkdir -m 0700 "$BACKUP_BASE" && chown 0:0 "$BACKUP_BASE"; } || rollback
    fi
    { mkdir -m 0700 "$ac_bk" && chown 0:0 "$ac_bk"; } || rollback
    say "backup folder: $ac_bk (mode 0700, owner root)"
  fi
  # A here-document keeps the loop in this shell, so UNDO and rollback work.
  while IFS="$TAB" read -r ac_k ac_a; do
    [ -n "$ac_k" ] || continue
    ac_n=$((ac_n + 1))
    case $ac_k in
      dropin)
        say "change $ac_n: the drop-in $DROPIN"
        ac_tmp=$(mktemp "$SUDOERS_D/.ap14-level1.XXXXXX") || rollback
        if ! { printf '%s\n' "$DROPIN_TEXT" >"$ac_tmp" && chown 0:0 "$ac_tmp" && chmod 0440 "$ac_tmp"; }; then
          rm -f "$ac_tmp"
          rollback
        fi
        if ! ac_out=$(visudo -cf "$ac_tmp" 2>&1); then
          printf '%s\n' "$ac_out" | sed 's/^/    | /'
          rm -f "$ac_tmp"
          rollback
        fi
        say "visudo -cf (the new file alone): ok"
        if [ "$ac_a" = replace ]; then
          mv -f "$DROPIN" "$ac_bk/$DROPIN_NAME" || {
            rm -f "$ac_tmp"
            rollback
          }
          undo_push "mv${TAB}$ac_bk/$DROPIN_NAME${TAB}$DROPIN"
          printf '%s -> %s\n' "$DROPIN" "$ac_bk/$DROPIN_NAME" >>"$ac_bk/MOVED" || true
        fi
        mv -f "$ac_tmp" "$DROPIN" || {
          rm -f "$ac_tmp"
          rollback
        }
        undo_push "rm${TAB}$DROPIN"
        check_sudoers
        say "wrote $DROPIN (mode 0440, owner root):"
        sed 's/^/    | /' "$DROPIN"
        ;;
      move)
        say "change $ac_n: move $ac_a aside"
        ac_to=$ac_bk/${ac_a##*/}
        mv "$ac_a" "$ac_to" || rollback
        undo_push "mv${TAB}$ac_to${TAB}$ac_a"
        printf '%s -> %s\n' "$ac_a" "$ac_to" >>"$ac_bk/MOVED" || true
        check_sudoers
        say "moved $ac_a to $ac_to"
        ;;
      register)
        say "change $ac_n: the standing-grants register"
        for ac_p in "$REGISTER_PARENT" "$REGISTER"; do
          if [ ! -d "$ac_p" ]; then
            mkdir -m 0755 "$ac_p"
            chown 0:0 "$ac_p"
            chmod 0755 "$ac_p"
          fi
          dir_safe "$ac_p" || {
            say "$ac_p is not a folder owned by root that only root may write; the register is not created"
            exit 1
          }
        done
        ac_tmp=$(mktemp "$REGISTER/.README.XXXXXX")
        register_readme >"$ac_tmp"
        chown 0:0 "$ac_tmp"
        chmod 0644 "$ac_tmp"
        mv -f "$ac_tmp" "$REGISTER/README"
        say "register: $REGISTER (folder 0755, owner root; README 0644)"
        ;;
    esac
  done <<EOF
$ACTIONS
EOF
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

# verify runs the checks of the result: `sudo -n true` as the user must fail.
# Returns 1 when it succeeds.
verify() {
  say "== checks of the result"
  if ! command -v sudo >/dev/null 2>&1; then
    say "check: sudo is not installed"
    return 0
  fi
  vf_env="env -i PATH=$STD_PATH sudo -n true"
  if command -v runuser >/dev/null 2>&1; then
    # shellcheck disable=SC2086  # vf_env is a fixed command line of plain words
    if runuser -u "$NAME" -- $vf_env </dev/null >/dev/null 2>&1; then vf_ok=0; else vf_ok=1; fi
  elif command -v su >/dev/null 2>&1; then
    if su -s /bin/sh -c "$vf_env" "$NAME" </dev/null >/dev/null 2>&1; then vf_ok=0; else vf_ok=1; fi
  else
    say "finding: sudo-n: neither runuser nor su is here: sudo -n true was not tried as $NAME"
    say "  remedy: as $NAME, in a new terminal, run sudo -k; sudo -n true: it must fail"
    return 1
  fi
  if [ "$vf_ok" = 1 ]; then
    say "check: sudo -n true run as $NAME fails: a password is required"
    return 0
  fi
  say "finding: sudo-n: sudo -n true run as $NAME succeeds: $NAME still has sudo without a password"
  say "  remedy: read sudo -l -U $NAME and the sudo part of this report, and remove the rule"
  return 1
}

# --- self-test ----------------------------------------------------------------------

self_test() {
  if [ -n "$FIX_ARG" ]; then
    [ -d "$FIX_ARG" ] || refuse "--fixtures must name a folder"
    FIX=$FIX_ARG
    if report; then exit 0; else exit 1; fi
  fi
  case $0 in
    */*) st_dir=${0%/*} ;;
    *) st_dir=. ;;
  esac
  st_base=$st_dir/self-test
  [ -d "$st_base" ] || {
    say "self-test: no folder $st_base: keep self-test/ beside the script"
    exit 1
  }
  st_fail=0
  st_n=0
  for st_case in "$st_base"/*/; do
    st_case=${st_case%/}
    [ -f "$st_case/expected.txt" ] || continue
    st_n=$((st_n + 1))
    st_user=$(cat "$st_case/user.txt")
    if st_out=$(FIX=$st_case NAME=$st_user; report 2>&1); then st_code=0; else st_code=$?; fi
    st_out="$st_out
exit: $st_code"
    if printf '%s\n' "$st_out" | cmp -s - "$st_case/expected.txt"; then
      say "self-test: ok ${st_case##*/}"
    else
      say "self-test: FAIL ${st_case##*/}: the report differs from expected.txt:"
      printf '%s\n' "$st_out" | diff -u "$st_case/expected.txt" - || true
      st_fail=1
    fi
  done
  [ "$st_n" -gt 0 ] || {
    say "self-test: no case in $st_base"
    exit 1
  }
  if [ "$st_fail" = 0 ]; then
    say "self-test: ok, $st_n cases; nothing on this host was read or run"
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
  apply)
    say "$ME --apply: the plan, read again now"
    report || true
    if [ "$N_STOP" -gt 0 ]; then
      refuse "the plan lists a stop: nothing was changed"
    fi
    if [ "$N_CHANGE" -eq 0 ]; then
      say "== apply: nothing to change"
    else
      say "== apply"
      apply_changes
    fi
    if verify; then vf=0; else vf=1; fi
    say "== the report after the changes"
    if report; then af=0; else af=1; fi
    say "register: $REGISTER"
    if [ "$vf" = 0 ] && [ "$af" = 0 ]; then exit 0; fi
    exit 1
    ;;
esac
