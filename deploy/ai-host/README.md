# AP14 level 1 on an AI host

AP14 level 1 takes passwordless and cached sudo away from your everyday user
on an AI (development) host, so that an agent session running as that user
cannot become root by itself: sudo asks for the user's own password at every
use. The changes are yours, made by hand from the steps below: the edits of
sudo rules with `visudo -f`, a small sudoers drop-in that turns off cached
credentials, and the root-owned standing-grants register `/etc/ataniq/grants`
(DL-1).

`ap14-level1.sh` is the read-only check of the result. It changes nothing.
It asks sudo itself what the user may do (`sudo -n -l -U NAME`), judges only
what sudo prints, and checks the register by simple file rules. It prints
plain steps for you, never an edit command.

You run it in host sitting A, as root, on each AI host. It takes about 10 to
15 minutes per host, edits included. Agents never run `--check`; they run
only `--self-test`.

## Before you start

- **Know the everyday user's password.** Afterwards sudo asks for it every
  time.
- **Keep a root shell open** for the whole sitting, in a second terminal, so
  you can undo an edit by hand.
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
  program of a folder the user may write stands in for one of its tools.
- The user's name is an argument (`--user NAME`), never written into the
  script. It has no dot: the drop-in of step 4 is named after the user, and
  sudo skips a file of `/etc/sudoers.d` whose name holds one.

## Steps

### 1. Self-test (optional)

Do, as the everyday user, in the checkout:
`sh deploy/ai-host/ap14-level1.sh --self-test`

You see a `self-test: ok CASE` line for each case, then `self-test: ok, N
checks; it read only the fixtures, in place, never ran sudo, and wrote
nothing`.

It proves: the report of each case in `self-test/` is exactly its
`expected.txt`, every finding and every stop included. A case is the saved
text of a `sudo -l -U` output (`sudo-l.txt`, with its error output
`sudo-l.err` and exit status `sudo-l.rc` when they matter) and a register
listing (`register.txt`). The self-test runs unprivileged and refuses root;
it reads the fixtures in place (a fixture file that is a link is refused),
never runs sudo and writes nothing.

If it does not work: a `FAIL` line names the case and shows what it expected
and what it got.

### 2. Check

Do, as root: `/bin/sh /root/ap14-new/ap14-level1.sh --check --user NAME`

You see, in this order:

- `== sudo -n -l -U NAME (exit N)`: each line sudo printed, after `  | `
  (its error output after `  ! `), then the judgement: `finding:`, `stop:`,
  `check:`, `note:` and `question:` lines;
- `== register /etc/ataniq/grants`: the listing, one line per path (`KIND
  UID MODE PATH`, KIND `d` folder, `f` regular file, `l` link, `o` anything
  else), then its judgement;
- `== by hand, for the owner`: one step in plain words for each kind of
  finding;
- `== summary`: `findings: N; stops: N` and the `result:` line, `clean`,
  `findings remain` or `STOPPED`.

It runs `sudo -n -l -U NAME` once, in a clean environment (`env -i`, the
system `PATH`, `LC_ALL=C`, no input), and reads with `stat` the folders `/`,
`/etc`, `/etc/ataniq`, `/etc/ataniq/grants` and each entry of the register.
It reads no sudoers file itself and writes nothing. As another user than
root, sudo and the register may be closed to it: that is a stop, not a clean
result.

The findings, and the step that fixes each:

| Finding | What sudo printed, or what the register holds | Step |
|---|---|---|
| `nopasswd` | a command with the `NOPASSWD:` tag | 3 |
| `otherpw` | `targetpw`, `rootpw` or `runaspw`, in Matching Defaults or a command- or runas-specific default: sudo asks for another account's password, not the user's | 3 |
| `noauth` | `!authenticate`: sudo asks no password at all | 3 |
| `exempt` | `exempt_group=...`: its members are never asked; sudo's listing does not say whether the user is one | 3 |
| `timeout` | a `timestamp_timeout` other than 0 (negative included), global or for a command or runas user: a cached ticket lets commands run unprompted after one password | 3 |
| `dropin` | no `timestamp_timeout=0` under Matching Defaults: sudo's built-in default keeps a ticket for some minutes | 4 |
| `register-missing` | `/etc/ataniq` or `/etc/ataniq/grants` is missing | 5 |
| `register-folder` | `/`, `/etc`, `/etc/ataniq` or `/etc/ataniq/grants` is a link or not a folder, not root's, or writable by group or others | 5 |
| `register-entry` | an entry is not a regular file, not root's, or writable by group or others | 5 |

A **stop** means that the check could not see everything: a run with a stop
is never clean (exit 3), whatever else it says, and no `check:` line is
printed for sudo's part. Fix what the stop says and run it again. It stops on:

- `sudo -n -l -U NAME failed (exit N)`: no such user, not run as root, or
  sudo is not installed (exit 127). A user with no grant is the one
  non-zero exit accepted (sudo versions differ), and only when sudo printed
  that one sentence and nothing on its error output;
- `sudo printed on its error output: ...`: for example a host name it
  cannot resolve, or a syntax error it recovered from, after which sudo
  judges a policy that is not the one you wrote;
- `a line of sudo -l this check does not know: ...`: any form besides the
  three sections of `sudo -l` (Matching Defaults, Runas and Command-specific
  defaults, the commands the user may run) and the sentence of a user with
  no grant; a heading for another user is such a line;
- `lists neither a grant nor that NAME is not allowed to run sudo`, `has a
  grant heading with no command under it`, `says both that NAME may run
  commands and that NAME is not allowed to run sudo`;
- `a timestamp_timeout this check does not know` (for example
  `!timestamp_timeout`): write `timestamp_timeout=0` instead; `a
  command-specific default this check does not know`;
- `no sudo grant: make sure root can log in another way`, with a
  `question:` for you (see below);
- `the register cannot be read`, `a register entry's name holds a character
  this check cannot print` (remove that entry by hand; `ls -b` shows its
  name), `a register listing line this check cannot judge`, `the register
  listing is empty`, `the register listing does not reach
  /etc/ataniq/grants`.

**When the user has no sudo grant.** sudo then says `User NAME is not
allowed to run sudo on HOST.`: no sudo route to root at all, which level 1
accepts, provided root can still log in another way (a root password at the
console, another administrator account). The script cannot know that, so it
stops and asks. When there is such a way and you have written it down in
your notes of the sitting, run the check again with `--other-root-login`:
the case becomes a `note:` and the result can be clean. When there is none,
give the user a grant that asks for the password first (step 3).

Every word of each default is judged, without its quotes, so a
quoted value with blanks (`exempt_group="wheel admins"`, or
`Defaults>root exempt_group="a b"`) is a finding like an unquoted one. A word
of another setting's value that reads as an option (for example `targetpw`
inside an `env_keep` list) is then a finding too: check it, and leave it if
it is only a value.

What the check trusts: sudo's own listing, which resolves host lists,
aliases, groups, includes and other sudoers sources (LDAP, sssd) for this
user on this host. What that listing does not show: which file holds a rule;
the order in which sudo applies Defaults (so every non-zero
`timestamp_timeout` is a finding, even beside a 0); and whether the user is
a member of an `exempt_group`.

If it does not work: a `refused:` line says why (bad arguments); nothing was
read.

### 3. Make the edits by hand

For each finding of sudo, as root:

- Find the rule or setting in `/etc/sudoers` or a file it includes (lines
  `@include`, `@includedir`, `#include`, `#includedir`; usually the files of
  `/etc/sudoers.d`), and edit that file with `visudo -f FILE`.
- **`NOPASSWD`:** remove the `NOPASSWD:` tag (or change it to `PASSWD:`),
  and keep one password-asked grant for you. Change only the tag, in place:
  never delete a rule because of this check, since a rule may name other
  users or groups, hosts, runas users or exclusions, and deleting it can
  remove your only grant or widen another's.
- **`targetpw`, `rootpw`, `runaspw`, `!authenticate`, `exempt_group`, a
  non-zero `timestamp_timeout`:** remove the setting, or for `exempt_group`
  make sure the user is not a member of that group.
- **No grant that asks for the password,** when you want one: add a rule for
  the user in a file of your own, with `visudo -f`, for example
  `NAME ALL=(ALL:ALL) ALL`.

After each edit, `visudo -c` must say `parsed OK` for every file. Then run
the check again.

### 4. Write the drop-in by hand

As root, create the drop-in with visudo (it checks the file before it saves
it):

```sh
visudo -f /etc/sudoers.d/zz-ap14-level1-NAME
```

Its whole content is one line, with the user's name:

```
Defaults:NAME timestamp_timeout=0
```

Then check the whole configuration with `visudo -c` (every file `parsed
OK`) and the file with `ls -l /etc/sudoers.d/zz-ap14-level1-NAME`: it must
be `-r--r-----`, owner `root`, group `root`. If the mode is another (visudo
gives a new file the mode root's umask allows), set it with
`chmod 0440 /etc/sudoers.d/zz-ap14-level1-NAME`: the file is new, and only
root has written it.

The name holds no dot and no `~`, both of which make sudo skip a file of
`/etc/sudoers.d`, and `zz-` sorts it after the other files there.
`/etc/sudoers` must include that folder (`@includedir /etc/sudoers.d` or
`#includedir /etc/sudoers.d`). Step 6 proves that sudo reads the drop-in:
`timestamp_timeout=0` then appears under Matching Defaults.

### 5. Create the register by hand

The register holds your verbatim words, one regular file per standing grant.
Agents can read it but not write it; it grants nothing by itself. As root,
when `--check` says it is missing:

```sh
mkdir -m 0755 /etc/ataniq
mkdir -m 0755 /etc/ataniq/grants
```

(`mkdir` fails when a folder is already there: then leave it, and let the
check judge it.)

Each standing grant is a fresh file of root's, mode 0644: write your words
into a new file in root's own folder, check them, then install a copy under
a temporary name beside the grant's name and rename it into place:

```sh
install -m 0644 -o root -g root /root/GRANT.txt /etc/ataniq/grants/.GRANT.new
mv /etc/ataniq/grants/.GRANT.new /etc/ataniq/grants/GRANT
```

**Repairing an entry or a folder.** Never change the owner or the mode of an
entry the check finds not root's or writable by others: another process may
still hold it open for writing, and would keep that. Read the entry and
confirm every word is yours; then write that text fresh, as above, and
rename the new file over the old name, which retires the old file. Remove an
entry that is not yours or not a regular file. A folder that is not root's
or is writable by group or others is replaced the same way: make a new
folder as root beside it, write into it fresh copies of only the entries you
confirm, move the old folder into root's own folder, and rename the new one
into its place.

### 6. Check again, and test by hand

Run the check of step 2 again. You see `result: clean: sudo itself shows no
way for NAME to skip the password, and the register is root's`, and exit
status 0.

Then, in a separate terminal, in a new login as the everyday user (not a
session that was already open), one line:

```sh
sudo -k; sudo -n true
```

You see `sudo: a password is required`. It types no password, so there is
nothing to capture. If you then run `sudo -k; sudo true`, the prompt names
whose password sudo asks for: `[sudo] password for NAME:`. Press Ctrl-C
there; you need not type it.

Its limit: the test shows only that sudo asks for a password before it runs
`true` as root, and the prompt only whose password that one command asks for.
Under `targetpw`, `rootpw` or `runaspw` it is another account's, and a
command- or runas-specific default can differ for other commands: `--check`
reports those (`otherpw`), and the test does not replace it.

## Other routes to root (by hand)

The script checks sudo and the register only. These other routes from the
user to root are yours to read and remedy in the same sitting, as root:

| Route | What to read | Remedy |
|---|---|---|
| groups | `id NAME`: docker, lxd, incus, incus-admin, libvirt, disk, kmem, shadow, input, or the group with gid 0 | `gpasswd -d NAME GROUP`, effective at the next login |
| polkit | the rules in `/etc/polkit-1/rules.d` and `/usr/share/polkit-1/rules.d`: a rule that returns `polkit.Result.YES` for the user, one of its groups or anyone | remove it, or put a rule first that returns `AUTH_ADMIN` |
| doas | `/etc/doas.conf`: a `permit` rule with `nopass` or `persist` for the user or its groups | remove `nopass` and `persist` |
| setuid | setuid or setgid files no package owns (`find / -xdev -perm /6000 -type f`, then the package manager's owner query) | remove it, or clear the bits, unless you put it there |
| writable | anything root runs or reads as configuration that the user may write: `/etc/sudoers*`, systemd unit folders, cron, polkit rules, PAM, the loader configuration, doas, profile scripts, `/root`, the folders of root's `PATH`, and every folder above them; access control lists on them | make it root's and not writable by group or others; remove the list entry |

## Undoing it

- The drop-in: as root, `rm /etc/sudoers.d/zz-ap14-level1-NAME`, then
  `visudo -c`.
- The edits of step 3: put back the tag or setting with `visudo -f`, from
  your notes of the sitting.
- The register: remove only what you created, an entry you wrote, then the
  folders when they are empty.

## Exit status

`0` clean: no finding and no stop. `1` findings remain, for you to fix by
hand. `2` refused before anything was read: bad arguments, root for
`--self-test`, or an unusable `--fixtures` or `--register-tree` folder. `3`
stopped: the check could not see everything (a failed or unknown sudo
output, an unreadable register, an unanswered question), so it is not clean.

## What it does not cover

- **Your password typed in the everyday account.** An agent session runs as
  the same user as your terminals, so it can change that user's shell start
  files and capture the password the next time you type `sudo` there. Level 1
  removes the free routes; it does not separate you from the agents. Type the
  password only in a session agents cannot reach.
- **Shell start files and `PATH` folders the user can write.** Those of the
  user's own account (`~/.profile`, `~/.bashrc`, `~/.local/bin` and the like)
  remain writable by every agent session, which can put a program in front of
  `sudo` or of what you run after it. This is a known limit of level 1; only a
  separate account, AP14 level 2, closes it.
- What `sudo -l` does not print (see step 2), and the routes of "Other
  routes to root", which the script does not read.

## For agents

Agents run this script only with `--self-test` (optionally
`--self-test --fixtures DIR --user NAME [--other-root-login]`, the report of
one case DIR, or `--self-test --register-tree DIR`, the register listing of
a tree DIR that stands for `/`), never with `--check`, which runs sudo as
root. They also run the Go tests of `./tests/`, which run `--self-test` on
the cases beside the script, on cases and trees they write in temporary
folders, and on a temporary copy of the script, and run `--check` only where
it is refused on its arguments. The targeted check is
`sh scripts/go-tool.sh test ./tests/ -run AiHost` after `make doctor`.
