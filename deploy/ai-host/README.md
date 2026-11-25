# AP14 level 1 on an AI host

`ap14-level1.sh` takes passwordless and cached sudo away from your everyday
user on an AI (development) host, so that an agent session running as that
user cannot become root by itself: afterwards `sudo -n true` fails for that
user. It also looks for every other route from that user to root it knows,
and reports each one with a one-line remedy. Last, it creates the root-owned
standing-grants register, `/etc/ataniq/grants` (DL-1).

You run it in host sitting A, as root, on each AI host, after reading its
dry run. It takes about 10 to 15 minutes per host. Agents never run it except
with `--self-test`.

## What it changes, and what it never changes

`--apply` makes only these changes, in this order, and only those the dry run
listed:

1. **No cached credentials.** It writes `/etc/sudoers.d/zz-ap14-level1`
   (mode 0440, owner root) with one setting,
   `Defaults:NAME timestamp_timeout=0`, so sudo asks for the password every
   time. It writes the file under a temporary name first, checks it with
   `visudo -cf`, moves it into place and checks the whole configuration with
   `visudo -c`. It prints exactly what it wrote. If the file is already there
   with that text, nothing changes.
2. **No passwordless sudo.** It finds every `NOPASSWD` rule and every
   `Defaults !authenticate` that applies to the user or to one of its groups,
   in `/etc/sudoers` and in every file it includes (`#include`, `@include`,
   `#includedir`, `@includedir`, user aliases, `%group`, `%#gid`, `#uid`).
   When such a rule is the only line of a file in `/etc/sudoers.d`, it moves
   that file aside into `/etc/ap14-level1/backup-TIME/` (mode 0700, owner
   root) and checks with `visudo -c` again. It never rewrites a sudoers file:
   a rule anywhere else is listed as a **stop** with its file and line, and
   `--apply` then changes nothing at all until you have edited it yourself.
3. **The register.** It creates `/etc/ataniq/grants` (folders mode 0755, owner
   root) holding only a README. Your verbatim words go there; agents can read
   it but not write it. It grants nothing until you write an entry.

If any `visudo` check fails, it undoes every sudoers change of the run, in
reverse order, and exits with status 1.

It **never** changes a group, a polkit rule, doas, a setuid file, a
permission or an access control list. Those are reported only (see
[Findings](#findings-and-their-remedies)).

## Before you start

- **Know the everyday user's password.** After the change sudo asks for it
  every time. When sudo would change and the user has no usable password
  (`passwd -S` says `L`, `LK` or `NP`), the dry run lists a stop.
- **Keep a root shell open** for the whole sitting, in a second terminal, so
  you can undo anything by hand.
- **Run a copy root owns.** Your checkout is writable by the everyday user, so
  an agent session could change the script before you run it. As root:

  ```sh
  install -d -m 0700 /root/ap14
  cp -R /PATH/TO/CHECKOUT/deploy/ai-host/. /root/ap14/
  ```

  Then read `/root/ap14/ap14-level1.sh`, or compare its `sha256sum` with the
  one of the reviewed commit taken on a machine agents do not use.
- The user's name is an argument (`--user NAME`). It is never written into
  the script; on the host it appears only in the drop-in.

## Steps

### 1. Self-test (optional)

Do: `sh /root/ap14/ap14-level1.sh --self-test`

You see: `self-test: ok, 3 cases; nothing on this host was read or run`.

It proves: the parsers read the fixture configurations in
`self-test/` as expected. It reads nothing on the host and runs nothing.

If it does not work: the copy is incomplete (`self-test/` must sit beside
the script) or changed. Copy it again.

### 2. Dry run

Do, as root: `sh /root/ap14/ap14-level1.sh --dry --user NAME`

You see, in this order:

- `== user`: the user, its uid and groups;
- `== sudo`: the files sudo reads, each passwordless rule (`rule: FILE:LINE:`),
  and what has to be checked by hand;
- `== plan`: `change N:` for each change `--apply` will make, in order,
  `no change:` for what is already done, and `stop:` with a `do:` line for
  what you must fix first;
- `== routes to root`: each `finding:` with its `remedy:`, plus `note:` lines;
- `== summary`: `changes: N; stops: N; findings: N`.

It reads `/etc/sudoers` and what it includes, `getent`, `id`, `passwd -S`,
the polkit rules, `doas.conf`, `/proc/self/mounts`, and runs `find` (setuid
files; files root runs that the user may write), `getfacl`, `pacman -Qo` (or
`dpkg -S`, `rpm -qf`) and `sudo -l -U NAME`. The setuid search can take a few
minutes. It changes nothing.

It proves: what `--apply` would do, and every route the script knows.

If it does not work: a `refused:` line says why (not root, no such user, bad
arguments) and nothing was read.

### 3. Handle the stops

Each `stop:` line names a file and line, and its `do:` line says what to do.
Typically: edit the file with `visudo -f FILE` and drop `NOPASSWD` for the
user and its groups. Then run the dry run again until it lists no stop.

### 4. Apply

Do, as root: `sh /root/ap14/ap14-level1.sh --apply --user NAME --confirm NAME`

`--confirm` must repeat the user's name exactly; otherwise nothing is read
and nothing changes.

You see: the plan again (read afresh), then `== apply` with each change,
`visudo -cf ...: ok`, `visudo -c: ok`, the text it wrote and the files it
moved, then `== checks of the result`:
`check: sudo -n true run as NAME fails: a password is required`, then the
whole report again, and last `register: /etc/ataniq/grants`.

It proves: sudo now asks the user for a password every time, and no rule
lets it skip the password. The repeated report shows `nothing to change`.

If it does not work: a failed `visudo` check prints the error and
`undoing every sudoers change of this run`; the configuration is as before.
If `sudo -n true` still succeeds, the report names what `sudo -l -U NAME`
still lists.

### 5. Check by hand

In a new terminal, as the everyday user: `sudo -k; sudo -n true`. You see
`sudo: a password is required`. Then `sudo true` asks for the password.

## Findings and their remedies

| Finding | What it means | Remedy (you do it) |
|---|---|---|
| `sudo-nopasswd` | a passwordless rule left (also listed as a stop), or one that may apply through a netgroup or non-Unix group | remove `NOPASSWD` with `visudo -f FILE` |
| `sudo-timeout` | a `timestamp_timeout` read after the drop-in, which may override it | remove it or set it to 0 |
| `sudo-l` | what `sudo -l -U NAME` itself still shows | as above |
| `sudo-n` | `sudo -n true` as the user still succeeds (after `--apply`) | read `sudo -l -U NAME` |
| `group` | the user is in docker, lxd, incus, incus-admin, libvirt, disk, kmem, shadow, input, or the group with gid 0 | `gpasswd -d NAME GROUP`, effective at the next login |
| `polkit` | a rule in `/etc/polkit-1/rules.d` or `/usr/share/polkit-1/rules.d` returns `polkit.Result.YES` for the user, its groups, anyone, or users the script cannot tell | remove it, or put a rule first that returns `AUTH_ADMIN` |
| `doas` | a `permit` rule with `nopass` or `persist` for the user or its groups | remove `nopass` and `persist` |
| `setuid` | a setuid or setgid root file no package owns | remove it or `chmod u-s,g-s` it, unless you put it there |
| `writable` | the user may write something root runs or reads as configuration: `/etc/sudoers*`, systemd unit folders, cron, polkit rules, PAM, the loader configuration, doas, profile scripts, `/root`, the folders of root's `PATH`, or any folder above them | `chown root:root` and `chmod go-w` |
| `acl` | an access control list entry lets the user or its group write one of those | `setfacl -x ENTRY FILE` |
| `root-path` | root's `PATH` holds an empty or relative entry | remove it |

The script never acts on these. The findings are your list for the sitting.

## Rollback

- A failed `visudo` check undoes the run's sudoers changes by itself.
- By hand, from a root shell:
  - the drop-in: `rm /etc/sudoers.d/zz-ap14-level1 && visudo -c`;
  - a moved file: `/etc/ap14-level1/backup-TIME/MOVED` lists each move as
    `FROM -> TO`; put it back with `mv TO FROM && visudo -c`. A replaced
    drop-in's old copy is there too.
- The register holds nothing but your entries and its README; leave it.

## Exit status

`0` clean: nothing to change and no finding. `1` findings remain: for
`--dry`, something to change, a stop or a finding; for `--apply`, a finding
after the changes, a failed `sudo -n` check, or a failed check whose changes
were undone. `2` refused before anything changed: bad arguments, not root, no
such user, or a stop in the plan.

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

## For agents

Agents run this script only with `--self-test` (optionally
`--self-test --fixtures DIR --user NAME`), never with `--dry` or `--apply`.
The targeted check is
`sh scripts/go-tool.sh test ./tests/ -run AiHost` after `make doctor`.
