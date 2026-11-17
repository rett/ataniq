#!/bin/sh
# Fold the changelog fragments in changelog.d into the [Unreleased] section of
# CHANGELOG.md, or check that none are left.
#
#   sh scripts/changelog-fold.sh [--dry-run] [--root DIR]
#   sh scripts/changelog-fold.sh --check [--root DIR]
#
# A fragment is changelog.d/<section>-<slug>.md, where <section> is one of
# added, changed, deprecated, removed, fixed, security and <slug> is lowercase
# letters, digits and hyphens. Its content is the entry text, written as in
# CHANGELOG.md: a list item starting with "- " and wrapped continuation lines,
# with no blank line and no heading. changelog.d/README.md is not a fragment.
#
# Folding appends each fragment to the end of its section in [Unreleased]
# (the heading is created in the Keep a Changelog order when the section is
# missing), fragments in a section sorted by file name, and removes the folded
# fragments. A fragment whose whole text (every line) is already an entry in
# [Unreleased] is not added again but is removed, so a run that stopped between
# writing CHANGELOG.md and removing the fragments can be repeated. A fragment
# that only starts like an existing entry is folded. Nothing is changed unless
# every fragment is valid. CHANGELOG.md is replaced by one rename of a
# temporary file made beside it (created exclusively with noclobber, so an
# existing file or symbolic link of that name is never written through; the
# rename is atomic and the file mode is kept), so an interrupted run leaves it
# intact. --dry-run only reports what would happen: it writes no file at all.
#
# --check changes nothing and fails while any fragment is left, which the
# release flow runs after the fold.
#
# Exit status: 0 success; 1 --check found fragments, or a file could not be
# written (CHANGELOG.md is then unchanged); 2 refused (invalid fragment,
# missing CHANGELOG.md or [Unreleased]) or usage error; 129, 130 or 143 when
# interrupted by HUP, INT or TERM, with CHANGELOG.md unchanged.
# Uses only POSIX shell and utilities that are always present with it.
#
# Test hook: when CHANGELOG_FOLD_TEST_HOLD names an existing file, a fold
# waits while that file exists, before it renders, so a test can interrupt it.

set -eu
LC_ALL=C
export LC_ALL

SECTIONS="Added Changed Deprecated Removed Fixed Security"

usage() {
	cat <<'EOF'
usage: changelog-fold.sh [--dry-run | --check] [--root DIR]

Fold changelog.d/<section>-<slug>.md into [Unreleased] in CHANGELOG.md and
remove them. --dry-run reports without changing anything. --check fails when
any fragment is left. --root DIR selects the repository root (default: the
parent of this script's directory).
EOF
}

mode='fold'
root=''
while [ $# -gt 0 ]; do
	case $1 in
	--dry-run) mode=dry ;;
	--check) mode=check ;;
	--root)
		[ $# -ge 2 ] || { echo "error: --root needs a directory" >&2; exit 2; }
		root=$2
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "error: unknown argument: $1" >&2
		usage >&2
		exit 2
		;;
	esac
	shift
done

if [ -z "$root" ]; then
	root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
fi
changelog=$root/CHANGELOG.md
fragdir=$root/changelog.d

failed=0
err() {
	printf 'error: %s\n' "$*" >&2
	failed=1
}

rank() {
	case $1 in
	Added) echo 1 ;;
	Changed) echo 2 ;;
	Deprecated) echo 3 ;;
	Removed) echo 4 ;;
	Fixed) echo 5 ;;
	Security) echo 6 ;;
	*) echo 99 ;;
	esac
}

lowercase() {
	case $1 in
	Added) echo added ;;
	Changed) echo changed ;;
	Deprecated) echo deprecated ;;
	Removed) echo removed ;;
	Fixed) echo fixed ;;
	Security) echo security ;;
	esac
}

# Every file in the fragment directory except the README, one per line, by
# name. A name that is not a valid fragment is still listed so --check and the
# fold both see it.
list_entries() {
	[ -d "$fragdir" ] || return 0
	for f in "$fragdir"/* "$fragdir"/.[!.]* "$fragdir"/..?*; do
		if [ -e "$f" ] || [ -L "$f" ]; then
			[ "${f##*/}" = README.md ] || printf '%s\n' "${f##*/}"
		fi
	done
}

entries=$(list_entries)
set -f

if [ "$mode" = check ]; then
	if [ -n "$entries" ]; then
		echo "error: unfolded changelog fragments (run scripts/changelog-fold.sh):" >&2
		printf '%s\n' "$entries" | while IFS= read -r leftover; do
			printf '  changelog.d/%s\n' "$leftover" >&2
		done
		exit 1
	fi
	echo "changelog fragments: none unfolded"
	exit 0
fi

if [ -z "$entries" ]; then
	echo "changelog fragments: nothing to fold"
	exit 0
fi

# Validate every entry before anything is read for folding or changed.
valid=
nl='
'
cr=$(printf '\r')
oldifs=$IFS
IFS=$nl
# The list is split at newlines only, once; the body runs with the usual IFS.
for name in $entries; do
	IFS=$oldifs
	path=$fragdir/$name
	case $name in
	added-*.md | changed-*.md | deprecated-*.md | removed-*.md | fixed-*.md | security-*.md) ;;
	*)
		err "changelog.d/$name: the name must start with a section (added, changed, deprecated, removed, fixed, security) and a hyphen, and end in .md"
		continue
		;;
	esac
	slug=${name#*-}
	slug=${slug%.md}
	case $slug in
	'' | -* | *[!a-z0-9-]*)
		err "changelog.d/$name: the slug must be lowercase letters, digits and hyphens, not starting with a hyphen"
		continue
		;;
	esac
	if [ ! -f "$path" ] || [ -L "$path" ]; then
		err "changelog.d/$name: not a regular file"
		continue
	fi
	first=
	bad=
	n=0
	while IFS= read -r line || [ -n "$line" ]; do
		n=$((n + 1))
		[ "$n" -ne 1 ] || first=$line
		case $line in
		"")
			bad="line $n is empty"
			break
			;;
		"#"*)
			bad="line $n starts with #, headings are added by the fold"
			break
			;;
		*"$cr"*)
			bad="line $n has a carriage return, use Unix line ends"
			break
			;;
		esac
	done <"$path"
	if [ -n "$bad" ]; then
		err "changelog.d/$name: $bad"
		continue
	fi
	if [ "$n" -eq 0 ]; then
		err "changelog.d/$name: empty"
		continue
	fi
	case $first in
	"- "?*) ;;
	*)
		err "changelog.d/$name: the first line must start with \"- \" and have text"
		continue
		;;
	esac
	valid="$valid $name"
done
if [ "$failed" -ne 0 ]; then
	echo "refused: nothing was changed" >&2
	exit 2
fi

[ -f "$changelog" ] || { echo "error: $changelog not found" >&2; exit 2; }

# What [Unreleased] already holds, to recognise a fragment that is folded.
unrel=
state=before
while IFS= read -r line || [ -n "$line" ]; do
	case $state in
	before) [ "$line" != "## [Unreleased]" ] || state=in ;;
	in)
		case $line in
		"## ["*) break ;;
		esac
		unrel="$unrel
$line"
		;;
	esac
done <"$changelog"
if [ "$state" = before ]; then
	echo "error: CHANGELOG.md has no \"## [Unreleased]\" section" >&2
	exit 2
fi
unrel="$unrel
"

b_Added=''
b_Changed=''
b_Deprecated=''
b_Removed=''
b_Fixed=''
b_Security=''
block_of() {
	case $1 in
	Added) printf '%s' "$b_Added" ;;
	Changed) printf '%s' "$b_Changed" ;;
	Deprecated) printf '%s' "$b_Deprecated" ;;
	Removed) printf '%s' "$b_Removed" ;;
	Fixed) printf '%s' "$b_Fixed" ;;
	Security) printf '%s' "$b_Security" ;;
	esac
}
add_block() {
	case $1 in
	Added) b_Added=$b_Added$2 ;;
	Changed) b_Changed=$b_Changed$2 ;;
	Deprecated) b_Deprecated=$b_Deprecated$2 ;;
	Removed) b_Removed=$b_Removed$2 ;;
	Fixed) b_Fixed=$b_Fixed$2 ;;
	Security) b_Security=$b_Security$2 ;;
	esac
}

# Sections in Keep a Changelog order, then fragments by name within each.
actions=
for sec in $SECTIONS; do
	lower=$(lowercase "$sec")
	for name in $valid; do
		case $name in
		"$lower"-*) ;;
		*) continue ;;
		esac
		path=$fragdir/$name
		text=
		while IFS= read -r line || [ -n "$line" ]; do
			text="$text$line$nl"
		done <"$path"
		# Already folded: every line is there as consecutive lines, and the
		# entry there does not go on with a continuation line.
		case $unrel in
		*"$nl$text" | *"$nl$text"[!\ ]*)
			actions="$actions${actions:+$nl}skip changelog.d/$name: already in [Unreleased]"
			continue
			;;
		esac
		add_block "$sec" "$text"
		actions="$actions${actions:+$nl}fold changelog.d/$name into $sec"
	done
done

if [ "$mode" = dry ]; then
	printf '%s\n' "$actions" | while IFS= read -r line; do
		printf 'would %s\n' "$line"
	done
	exit 0
fi

# A signal ends the script (the handler must exit, or the run would go on after
# the temporary file is removed); the EXIT trap removes the temporary file.
tmp=
trap '[ -z "$tmp" ] || rm -f "$tmp"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
new=$changelog.fold.$$
# Noclobber makes the redirection an exclusive create: it fails when the name
# exists, even as a dangling symbolic link.
( set -C; : >"$new" ) 2>/dev/null || {
	echo "error: cannot create a temporary file beside CHANGELOG.md" >&2
	exit 1
}
tmp=$new
# The new file starts from CHANGELOG.md's mode, which the rename keeps.
cp -p "$changelog" "$tmp"

emitted=" "
pend=0
lastins=0
cur=
content=0

flush_pend() {
	while [ "$pend" -gt 0 ]; do
		printf '\n'
		pend=$((pend - 1))
	done
}
out() {
	flush_pend
	printf '%s\n' "$1"
	lastins=0
}
# End of the current section: its block goes after its last entry.
close_cur() {
	[ -n "$cur" ] || return 0
	blk=$(block_of "$cur"; echo x)
	blk=${blk%x}
	[ -n "$blk" ] || return 0
	case $emitted in *" $cur "*) return 0 ;; esac
	if [ "$content" -eq 0 ]; then pend=1; else pend=0; fi
	flush_pend
	printf '%s' "$blk"
	pend=1
	lastins=1
	emitted="$emitted$cur "
}
# Sections that have entries but no heading yet, ranked before $1.
flush_before() {
	for s in $SECTIONS; do
		[ "$(rank "$s")" -lt "$1" ] || continue
		case $emitted in *" $s "*) continue ;; esac
		blk=$(block_of "$s"; echo x)
		blk=${blk%x}
		[ -n "$blk" ] || continue
		[ "$pend" -ge 1 ] || pend=1
		flush_pend
		printf '### %s\n\n%s' "$s" "$blk"
		pend=1
		lastins=1
		emitted="$emitted$s "
	done
}

{
	while [ -n "${CHANGELOG_FOLD_TEST_HOLD:-}" ] && [ -e "$CHANGELOG_FOLD_TEST_HOLD" ]; do
		sleep 1
	done
	state=before
	while IFS= read -r line || [ -n "$line" ]; do
		case $state in
		before)
			printf '%s\n' "$line"
			[ "$line" != "## [Unreleased]" ] || state=in
			;;
		in)
			case $line in
			"## ["*)
				close_cur
				flush_before 100
				state=after
				out "$line"
				;;
			"### "*)
				name=${line#"### "}
				close_cur
				flush_before "$(rank "$name")"
				out "$line"
				cur=$name
				content=0
				;;
			"") pend=$((pend + 1)) ;;
			*)
				out "$line"
				content=1
				;;
			esac
			;;
		after) printf '%s\n' "$line" ;;
		esac
	done <"$changelog"
	if [ "$state" = in ]; then
		close_cur
		flush_before 100
		[ "$lastins" -eq 1 ] || flush_pend
	fi
} >"$tmp"

mv -f "$tmp" "$changelog"
for name in $valid; do
	rm -f -- "$fragdir/$name"
done
printf '%s\n' "$actions"
