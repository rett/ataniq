# AP14 level 1 on an AI host

`ap14-level1.sh` helps you take passwordless and cached sudo away from your
everyday user on an AI (development) host, so that an agent session running
as that user cannot become root by itself: afterwards `sudo -n true` fails
for that user with "a password is required". It audits every route from that
user to root it knows and reports each one with a remedy. It writes only two
kinds of new file: a sudoers drop-in that turns off cached credentials, and
the root-owned standing-grants register, `/etc/ataniq/grants` (DL-1). Every
edit of an existing sudoers file is yours, by hand, with `visudo`, from the
exact steps the audit prints.

You run it in host sitting A, as root, on each AI host, after reading its
dry run. It takes about 10 to 15 minutes per host. Agents never run it except
with `--self-test`.

## What it changes, and what it never changes

It **never moves, edits or removes an existing file**. `--apply` writes only
new files, and only those the dry run listed:

1. **No cached credentials.** A new file
   `/etc/sudoers.d/zz-ap14-level1-NAME` (mode 0440, owner root), named after
   the user so that a second account never replaces the first's, with one
   setting, `Defaults:NAME timestamp_timeout=0`, so sudo asks for the
   password every time. It writes it under a temporary name in the same folder
   (a name with a dot, which sudo skips), checks that it holds the whole text,
   sets the owner and mode, checks it alone with `visudo -cf`, and publishes
   it with `ln`, which never replaces a file. If a file is already at that
   name it must be exactly this one (the same bytes, owner root, mode 0440):
   then nothing changes; anything else at that name is a stop, and the script
   leaves it alone.
2. **The register.** It creates `/etc/ataniq` and `/etc/ataniq/grants`
   (mode 0755, owner root) and their README (mode 0644) when they are not
   there yet, the same way. Your verbatim words go there; agents can read it
   but not write it. It grants nothing until you write an entry.

Then it checks the whole configuration with `visudo -c`. The whole run holds
one lock (`/run/ap14-level1.lock`), so a second `--apply` refuses while the
first runs, and it reads everything again under the lock before it writes.

If any step fails (a write on a full disk, a `visudo` check, an interrupt) it
removes the files this run created, newest first, and nothing else. A removal
that fails is reported as `NOT undone: ... remove it by hand (rm -f '...')`
and the rest goes on. A failed write never leaves half a file under a name
sudo reads.

**Passwordless rules are yours to edit.** The audit finds every `NOPASSWD`
rule, every `Defaults !authenticate` and every `exempt_group` that applies
to the user or one of its groups, in `/etc/sudoers` and in every file it
includes (`#include`, `@include`, `#includedir`, `@includedir`, user and
runas aliases, `%group`, `%#gid`, `#uid`, netgroups). For each it prints the
exact steps, `by hand N:`, to make with `visudo -f 'FILE'`. `--apply` refuses
while any of them is still there.

It **never** changes a group, a polkit rule, doas, a setuid file, a
permission or an access control list. Those are reported only (see
[Findings](#findings-and-their-remedies)).

## Before you start

- **Know the everyday user's password.** After the change sudo asks for it
  every time. When the user has a sudo rule and no usable password
  (`passwd -S` says `L`, `LK` or `NP`), or `passwd -S` does not say, the dry
  run lists a stop.
- **Keep a root shell open** for the whole sitting, in a second terminal, so
  you can undo anything by hand.
- **Install the script as root's own regular file.** Your checkout is
  writable by the everyday user, so an agent session could change the script
  before you run it, and a link in the checkout could later point anywhere.
  Never run it from the checkout as root, and never copy the checkout's tree.
  As root, make a fresh folder only root can use (`mkdir` fails when it is
  already there: then choose another name), and install the one file:

  ```sh
  mkdir -m 0700 /root/ap14-new
  install -m 0500 /PATH/TO/CHECKOUT/deploy/ai-host/ap14-level1.sh /root/ap14-new/ap14-level1.sh
  sha256sum /root/ap14-new/ap14-level1.sh
  ```

  `install` writes a new regular file with the script's bytes, whatever the
  checkout's path is. Compare the hash with the one you took of the reviewed
  commit on a machine agents do not use (for example
  `git show COMMIT:deploy/ai-host/ap14-level1.sh | sha256sum` on the laptop),
  typed by hand: they must be equal. Then read
  `/root/ap14-new/ap14-level1.sh`, and run only that copy.
- **Run it with `/bin/sh`.** The script sets `PATH` to the system folders
  (`/usr/sbin:/usr/bin:/sbin:/bin`) before its first external command, so no
  program of a folder the user may write stands in for one of its tools. It
  keeps the `PATH` it was started with only to report root's `PATH`.
- The user's name is an argument (`--user NAME`). It is never written into
  the script; on the host it appears only in the drop-in's name and text.
  The name has no dot (sudo skips a drop-in whose name holds one).

## Steps

### 1. Self-test (optional)

Do, as the everyday user, in the checkout:
`sh deploy/ai-host/ap14-level1.sh --self-test`

You see: `self-test: ok, 13 checks; it read only the fixtures, wrote only
temporary copies of them, and ran nothing from them`.

It proves: the audit reads the fixture configurations in `self-test/` as
expected, and `--apply` against temporary copies of them writes exactly the
new files, refuses the sole, shared and mixed grants and a name collision,
undoes a failure at every one of its steps, and recovers on a rerun when an
undo failed too. It runs unprivileged and refuses root; it reads only the
fixtures (a link that leads out of them, or an include with a `..` part, is
refused), never runs anything from them, and writes only below `TMPDIR`.

If it does not work: a `FAIL` line names the case and shows the difference.

### 2. Dry run

Do, as root: `/bin/sh /root/ap14-new/ap14-level1.sh --dry --user NAME`

You see, in this order:

- `== user`: the user, its uid and groups;
- `== sudo`: the files sudo reads, then every rule (`rule: FILE:LINE:`): the
  users it names, whether it applies to the user (`yes`, `no`, or `maybe`
  for a netgroup or non-Unix group), and whether it asks for a password, then
  what `sudo -l -U NAME` adds;
- `== by hand`: for each rule that gives the user sudo without a password,
  `by hand N:` with `why:` and the `do:` steps for `visudo -f 'FILE'`;
- `== plan`: `change N:` for each new file `--apply` will write, `no
  change:` for what is already there, and `stop:` with a `do:` line for what
  you must fix first;
- `== routes to root`: each `finding:` with its `remedy:`, plus `note:` lines;
- `== summary`: `changes: N; by hand: N; stops: N; findings: N`.

It reads `/etc/sudoers` and what it includes, `getent`, `id`, `passwd -S`,
the polkit rules, `doas.conf`, `/proc/self/mounts`, and runs `find` (setuid
files; files root runs that the user may write), `getfacl`, `pacman -Qo` (or
`dpkg -S`, `rpm -qf`) and `sudo -n -l -U NAME`. The setuid search can take a
few minutes. It changes nothing.

A dry run is never clean when it could not see everything. It lists a
**stop** for: an include it cannot read (missing, unreadable, naming `%h`,
with a `..` part, or nested more than 16 deep); a failed `sudo -l -U NAME`;
a passwordless command `sudo -l` lists that the files do not explain; a
password status `passwd -S` does not give; a register entry that is a link,
not a regular file, not root's or writable by another account; a file at
the drop-in's name that is not exactly the script's; a file name with a
newline or a tab where it must read every name; and a passwordless rule
that, once you delete it, would leave no rule that gives the user sudo with
a password (`sudo would refuse NAME, not ask for a password`: first add a
rule such as `NAME ALL=(ALL:ALL) ALL`, or change `NOPASSWD:` to `PASSWD:` in
place, or make sure root can log in another way).

It proves: what `--apply` would write, the edits that are yours, and every
route the script knows.

If it does not work: a `refused:` line says why (no such user, bad
arguments) and nothing was read.

### 3. Make the edits by hand, and handle the stops

For each `by hand N:`, as root, open the file with `visudo -f 'FILE'` and
make the edit on the line it names: delete each `NOPASSWD:` tag (or change it
to `PASSWD:`), or delete the whole rule; for `!authenticate` or
`exempt_group`, delete that setting. When the rule names other users or
groups the audit says so: the edit changes their sudo too. Each `stop:` has
its `do:` line. Then run the dry run again, until it lists no edit and no
stop.

### 4. Apply

Do, as root:
`/bin/sh /root/ap14-new/ap14-level1.sh --apply --user NAME --confirm NAME`

`--confirm` must repeat the user's name exactly; otherwise nothing is read
and nothing changes.

You see: the audit again (read afresh, under the lock), then `== apply` with
each new file (`visudo -cf (the new file alone): ok`, `wrote FILE (mode ...,
owner root)`), `visudo -c (the whole configuration): ok`, and then the checks
of step 5.

It proves: sudo now asks the user for a password every time, and no rule
lets it skip the password, while the user keeps its sudo rule.

If it does not work: a `refused:` line when the audit still lists an edit or
a stop, or another `--apply` holds the lock: nothing was written. A failed
step prints `a step failed: removing the files this run created`, then an
`undone:` or `NOT undone:` line for each.

### 5. Verify

`--apply` ends with these checks; run them again any time, as root:
`/bin/sh /root/ap14-new/ap14-level1.sh --verify --user NAME`

You see `== checks of the result` and:

- `check: sudo -l -U NAME lists a grant that asks for the password: ...`:
  the user's intended grant is still there, and nothing without a password;
- `check: sudo -l -U NAME: the last timestamp_timeout is 0`;
- `check: sudo -n true run as NAME fails: a password is required`.

A `finding:` instead tells apart that `sudo -n true` succeeds (passwordless
sudo remains), that sudo refuses the user outright (not allowed: a password
would not help, the user is locked out of sudo), and that the probe could
not run.

Then, by hand, in a new terminal, as the everyday user: `sudo -k; sudo -n
true`. You see `sudo: a password is required`. Then `sudo true` asks for the
password.

## Findings and their remedies

| Finding | What it means | Remedy (you do it) |
|---|---|---|
| `sudo-timeout` | a `timestamp_timeout` read after the drop-in, or set for a command or runas user (`Defaults!CMND`, `Defaults>USER`) anywhere, which may override it | remove it or set it to 0 with `visudo -f 'FILE'` |
| `sudo-l` | `sudo -l -U NAME` shows a cached-credential time other than 0 | as above |
| `sudo-n` | after `--apply`: `sudo -n true` as the user succeeds, sudo refuses the user outright, or the probe could not run | read `sudo -l -U NAME` and the audit |
| `verify` | after `--apply`: `sudo -l -U NAME` failed, lists no grant that asks for the password, lists a command without one, or no `timestamp_timeout=0` | read `sudo -l -U NAME` and the audit |
| `group` | the user is in docker, lxd, incus, incus-admin, libvirt, disk, kmem, shadow, input, or the group with gid 0 | `gpasswd -d NAME GROUP`, effective at the next login |
| `polkit` | a rule in `/etc/polkit-1/rules.d` or `/usr/share/polkit-1/rules.d` returns `polkit.Result.YES` for the user, its groups, anyone, or users the script cannot tell | remove it, or put a rule first that returns `AUTH_ADMIN` |
| `doas` | a `permit` rule with `nopass` or `persist` for the user or its groups | remove `nopass` and `persist` |
| `setuid` | a setuid or setgid root file no package owns | remove it or `chmod u-s,g-s` it, unless you put it there |
| `writable` | the user may write something root runs or reads as configuration: `/etc/sudoers*`, systemd unit folders, cron, polkit rules, PAM, the loader configuration, doas, profile scripts, `/root`, the folders of root's `PATH`, or any folder above them | `chown root:root` and `chmod go-w` |
| `acl` | an access control list entry lets the user or its group write one of those | `setfacl -x ENTRY FILE` |
| `root-path` | root's `PATH` holds an empty or relative entry | remove it |

The remedies quote every path. The script never acts on these. The findings
are your list for the sitting.

## Rollback

- A failed step removes the files that run created by itself, and names any
  it could not remove.
- By hand, from a root shell, remove only the files the run created, as its
  output named them (`wrote ...`, `made ...`):
  - the drop-in: `rm /etc/sudoers.d/zz-ap14-level1-NAME && visudo -c`;
  - the register, only if that run created it and you have written no entry
    yet: `rm /etc/ataniq/grants/README`, then `rmdir /etc/ataniq/grants` and,
    if the run made it, `rmdir /etc/ataniq`.
- Nothing else was changed, so nothing else is undone. A temporary file a
  run left (`.ap14-level1.tmp.*`, which sudo skips) is named by the next dry
  run; remove it by hand.

## Exit status

`0` clean: for `--dry`, nothing to write, no edit by hand, no stop and no
finding; for `--apply`, written (or nothing to write) and the checks pass;
for `--verify`, every check passes. `1` something remains: for `--dry`,
something to write, an edit, a stop or a finding; for `--apply`, a failed
step whose files were removed, or a failed check; for `--verify`, a failed
check. `2` refused before anything was written: bad arguments, not root (for
`--apply` and `--verify`), root (for `--self-test`), no such user, the lock
is held, or an edit or a stop in the audit.

## What it does not cover

- **Your password typed in the everyday account.** An agent session runs as
  the same user as your terminals, so it can change that user's shell start
  files and capture the password the next time you type `sudo` there. Level 1
  removes the free routes; it does not separate you from the agents. Type the
  password only in a session agents cannot reach (AP14 level 2, a separate
  user, closes this).
- Polkit `.pkla` files and the defaults in `/usr/share/polkit-1/actions`, files
  in setuid snapshots and container layers, nested btrfs subvolumes that are
  not mounted, and access control lists on the folders above the scanned ones.
- The polkit reading is a careful pattern match, not a JavaScript parser:
  anything it cannot tell is reported as `for users the script cannot tell`.
- The sudoers reading does not evaluate host lists or command aliases: a rule
  is read as applying on every host, and a fallback grant counts only when it
  names `ALL` itself.

## For agents

Agents run this script only with `--self-test` (optionally
`--self-test --fixtures DIR --user NAME`), never with `--dry`, `--apply` or
`--verify`, and through the Go tests of `./tests/`, which run `--self-test`
on fixtures and temporary copies and run `--apply` and `--verify` only where
they are refused on their arguments, never as root. The targeted check is
`sh scripts/go-tool.sh test ./tests/ -run AiHost` after `make doctor`.
