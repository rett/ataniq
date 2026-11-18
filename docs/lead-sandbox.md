# Lead sandbox proposal

This is a **proposal that awaits qualification and the owner's decision on the
profile (design question Q1)**. Nothing here is qualified. Nothing starts a
sandbox in ordinary use or in the tests, and no command uses it yet: a holder
that would launch and supervise a lead in it, and a daemon that would start that
holder, are other work. The scratch-only holder of
[the lead holder guide](lead-holder.md) and the daemon skeleton of
the lead daemon guide launch no sandbox. Only the opt-in
qualification harness can launch the generated plan, on a host a coordinator
chose. This guide says what the code generates and what it checks. It does not
say that the sandbox is qualified, safe or supported, and no part of it should
be read that way.

The proposal follows the lead-hosting design (sections 3 and 7.3), revision 4:
rule B1 for every owner-side access to a path a sandbox can write, the lead's
four parent folders, assertion 11 and the walk before every start. With it goes
the owner's decision R17: nested sandboxes are allowed for now (see
[Nested sandboxes](#nested-sandboxes-owner-decision-r17)). The code differs from
the design's text in one place, the source of the three `.git` binds, and is
stricter there (see [the `.git` rows](#the-clone-rules-and-rule-g1)). The code
is in `internal/leadsandbox` (the packages
`profile`, `sandboxnet`, `safefile`, `mountpoints`, `plan` and `assert`), in
`cmd/ataniq-probe` and in `qualification/leadsandbox`. Each package's `doc.go`
is the detailed statement of what it does and does not do; this guide gathers
them. Where this guide and a `doc.go` differ, the code and its `doc.go` are
right.

## What it is and what is not built

A managed lead is an agent session (claude or codex) that Ataniq would run for
one project in its own clone, as one of the leads of the
[architecture brief](architecture.md). The proposal is a profile for that
session: what the lead sees (the file-system view), what it can reach (the
network), who it is (identity and environment), what is checked before the agent
starts, and the launch command that makes all of that so. The profile is Go data
and checks; the launch plan is a list of arguments. Nothing in the packages runs
a program, and the checks that touch files (`mountpoints`, `safefile`) walk by
handle without following links, from the state root the caller owns or, for a
source outside it, from the root of the file system.

Built, each unqualified:

- `profile`: the runtimes, the fixed inside paths, the host layout of one lead,
  the bind table, the environment allowlist, the never-mounted check and the
  profile digest. It does no I/O.
- `sandboxnet`: the arguments of pasta and the derivation of the sandbox's
  `resolv.conf`. It reads only through a small file-system interface that tests replace.
- `plan`: the whole launch command, built from the profile, with the walk of
  every bind source Ataniq supplies, and `Verify`, which parses a plan back and
  compares it with the inputs. It starts nothing.
- `mountpoints` and `safefile`: the host-side checks and preparation of the
  folders a lead can write, and the checks of the bind sources, by handle and
  without following symbolic links (rule B1); `mountpoints.SourceWalks` and
  `PlanWalks` give the walks of the bind sources as data.
- `assert`: the eleven start-up assertions, the spec and the report line, and
  the checks the probe and the holder make.
- `ataniq-probe`: the static helper that runs inside the sandbox and, in its
  pass-through mode, on the host between pasta and bubblewrap, where it walks to
  each bind source Ataniq supplies by handle and opens the report channel and
  the seccomp file.
- The opt-in harness `qualify` and its guide,
  [the lead sandbox qualification harness](qualification-lead-sandbox.md).

Not built:

- A holder for a sandboxed lead: the process that opens the report channel,
  writes the spec, runs the checks in order, starts the plan under a unit and
  stops it by the stop rule. `assert` has the stop rule's constants, and the
  holder would implement it. The scratch-only holder of
  [the lead holder guide](lead-holder.md) uses none of these packages: its
  agent is an ordinary process of the user, in no sandbox.
- A daemon that supervises holders, and the role lock and lingering checks
  behind assertion 9 (the package has the interfaces only, and nothing
  implements them). The daemon skeleton of the lead daemon guide starts no
  holder.
- Injection in use: putting text into a running lead's terminal. A provisional
  library exists (see [the lead injection guide](lead-injection.md)), and no
  holder calls it.
- Any use of the exchange mirror and the intake by which a lead's work reaches
  the owner's upstream branches. The plan binds the mirror folder read-only
  and does not make it. `ataniq-intake` (see the lead intake guide) makes a
  mirror under the state root it is given, at the same relative path,
  `mirrors/<project>.git`, and is run by hand. It shares no code with these
  packages, and the commands it runs come from a sandbox template of its own,
  not from this profile.
- Anything that uses the four parent folders (requests, mailbox, mail and the
  coordinators' published folders). They are bound folders only: nothing reads
  the request folder, and nothing writes the mailbox or publishes a
  coordinator's folders.
- The qualification record store. `assert` checks a record through an interface;
  no store exists.
- A generated or reviewed seccomp filter (see
  [The seccomp hook](#the-seccomp-hook)).

## The launch plan

`plan.Build` returns one command line. Its shape, in order, is:

```text
PASTA ARGS... -- PROBE_HOST pass-fds [--read FD=PATH]... [--write FD=PATH]... [--walk-dir FD ROOT REL]... [--walk-file FD ROOT REL]... -- BWRAP ARGS... -- /ataniq/probe --spec /ataniq/spec/spec.json --report-fd FD -- AGENT ARGV...
```

- pasta gives the sandbox a network namespace (see
  [Network and DNS](#network-and-dns)).
- The pass-through step, the probe binary in its `pass-fds` mode, runs on the
  host between pasta and bubblewrap. It exists because pasta closes every
  descriptor it inherits before it starts its command, so a descriptor the
  launcher opened would never reach bubblewrap. The step opens each file it is
  told to open (`--read`, `--write`), walks to each bind source Ataniq supplies
  (`--walk-dir`, `--walk-file`: see below), places every one at exactly the
  descriptor number given, and executes the rest of the line. The files it
  opens for reading or writing are the report channel (always) and the seccomp
  file (when one is named); the walks are one per bind row Ataniq supplies. The
  launcher opens nothing.
- bubblewrap builds the sandbox from the arguments below.
- The probe inside runs the start-up checks and then executes the agent.

The golden file `internal/leadsandbox/plan/testdata/claude-root-plain.golden`
holds this plan for one fixed set of inputs (a claude lead, identity root, every
optional host row present). Its command, with invented paths, is:

```text
/usr/bin/pasta -f -q --config-net -t none -u none -T none -U none --no-map-gw -- \
  /opt/ataniq/bin/probe pass-fds \
    --write 3=/opt/ataniq/run/arc-lead/report.fifo \
    --walk-file 10 /opt/ataniq/resolv/arc-lead resolv.conf \
    --walk-file 11 /opt/ataniq/bin probe \
    --walk-dir 12 /opt/ataniq/spec arc-lead \
    --walk-dir 13 /opt/agent/claude 2.1.0 \
    --walk-file 14 /opt/ataniq/managed/arc-lead settings.json \
    --walk-dir 15 /state leads/arc-lead/home \
    --walk-dir 16 /state leads/arc-lead/config/claude \
    --walk-dir 17 /opt/ataniq/skills arc-lead \
    --walk-file 18 /opt/ataniq/instructions/arc-lead CLAUDE.md \
    --walk-dir 19 /opt/ataniq/git arc-lead \
    --walk-dir 20 /state leads/arc-lead/tree \
    --walk-file 21 /state leads/arc-lead/gitmeta/config \
    --walk-dir 22 /state leads/arc-lead/gitmeta/hooks \
    --walk-dir 23 /state leads/arc-lead/gitmeta/info \
    --walk-dir 24 /state mirrors/demo.git \
    --walk-dir 25 /opt/ataniq qargi \
    --walk-dir 26 /state leads/arc-lead/qargi \
    --walk-dir 27 /state leads/arc-lead/requests \
    --walk-dir 28 /state mailbox/arc-lead \
    --walk-dir 29 /state leads/arc-lead/mail \
    --walk-dir 30 /state pub/demo \
    --walk-dir 31 /opt/ataniq/assignment demo \
    --walk-dir 32 /opt/ataniq/projection demo \
  -- \
  /usr/bin/bwrap \
    --die-with-parent \
    --unshare-all \
    --share-net \
    --unshare-user \
    --uid 0 \
    --gid 0 \
    --cap-drop ALL \
    --clearenv \
    --setenv CLAUDE_CONFIG_DIR /home/lead/.claude \
    --setenv COLORTERM truecolor \
    --setenv GIT_CEILING_DIRECTORIES /work \
    --setenv GIT_CONFIG_GLOBAL /ataniq/git/config \
    --setenv GOCACHE /home/lead/.cache/go-build \
    --setenv GOMODCACHE /home/lead/go/pkg/mod \
    --setenv HOME /home/lead \
    --setenv LANG C.UTF-8 \
    --setenv LOGNAME root \
    --setenv PATH /ataniq/qargi/bin:/ataniq/agent/bin:/usr/local/bin:/usr/bin:/bin:/home/lead/.local/bin \
    --setenv QARGI_LEAD arc-lead \
    --setenv SHELL /bin/bash \
    --setenv TERM xterm-256color \
    --setenv TMPDIR /tmp \
    --setenv TZ UTC \
    --setenv USER root \
    --setenv XDG_CACHE_HOME /home/lead/.cache \
    --setenv XDG_CONFIG_HOME /home/lead/.config \
    --setenv XDG_DATA_HOME /home/lead/.local/share \
    --setenv XDG_STATE_HOME /home/lead/.local/state \
    --tmpfs / \
    --ro-bind /usr /usr \
    --symlink usr/bin /bin \
    --symlink usr/lib /lib \
    --symlink usr/lib64 /lib64 \
    --symlink usr/sbin /sbin \
    --ro-bind /etc/ssl /etc/ssl \
    --ro-bind /etc/ca-certificates /etc/ca-certificates \
    --ro-bind /etc/hosts /etc/hosts \
    --ro-bind /etc/nsswitch.conf /etc/nsswitch.conf \
    --ro-bind /etc/passwd /etc/passwd \
    --ro-bind /etc/group /etc/group \
    --ro-bind /etc/localtime /etc/localtime \
    --ro-bind-fd 10 /etc/resolv.conf \
    --dev /dev \
    --proc /proc \
    --tmpfs /tmp \
    --ro-bind-fd 11 /ataniq/probe \
    --ro-bind-fd 12 /ataniq/spec \
    --ro-bind-fd 13 /ataniq/agent \
    --ro-bind-fd 14 /etc/claude-code/managed-settings.json \
    --bind-fd 15 /home/lead \
    --bind-fd 16 /home/lead/.claude \
    --ro-bind-fd 17 /home/lead/.claude/skills \
    --ro-bind-fd 18 /home/lead/.claude/CLAUDE.md \
    --ro-bind-fd 19 /ataniq/git \
    --bind-fd 20 /work/demo \
    --ro-bind-fd 21 /work/demo/.git/config \
    --ro-bind-fd 22 /work/demo/.git/hooks \
    --ro-bind-fd 23 /work/demo/.git/info \
    --ro-bind-fd 24 /ataniq/mirror/demo.git \
    --ro-bind-fd 25 /ataniq/qargi \
    --bind-fd 26 /home/lead/.qargi \
    --bind-fd 27 /ataniq/requests \
    --ro-bind-fd 28 /ataniq/mailbox \
    --bind-fd 29 /ataniq/mail \
    --ro-bind-fd 30 /coord \
    --ro-bind-fd 31 /ataniq/assignment \
    --ro-bind-fd 32 /ataniq/projection \
    --chdir /work/demo \
    --remount-ro / \
  -- \
  /ataniq/probe --spec /ataniq/spec/spec.json --report-fd 3 -- /ataniq/agent/bin/claude --model example
```

The bubblewrap arguments come in a fixed order: the flag set, one `--setenv` per
allowlist entry in name order, the optional `--seccomp FD`, the rows of the bind
table in table order, `--chdir`, and `--remount-ro /` last. An optional host row
is emitted, as `--ro-bind` or `--symlink`, only when the caller says the host has
it; otherwise it is left out and a note in the plan says so. `--ro-bind-try` is
never used, so the plan says exactly what is mounted.

**How a bind source reaches bubblewrap.** A bind row whose source Ataniq
supplies (every bind row but the host's own) is emitted as `--ro-bind-fd FD
DEST` or `--bind-fd FD DEST`, never as `--ro-bind` or `--bind` with a path, and
the pass-through step gets the matching walk: `--walk-dir FD ROOT REL` for a
source that is a folder, `--walk-file FD ROOT REL` for a regular file. In the
excerpt, `--walk-dir 20 /state leads/arc-lead/tree` and `--bind-fd 20
/work/demo` are the two halves of one row. The step opens ROOT without following
a symbolic link, walks REL beneath it, checks the type on the descriptor it got
and places that descriptor at FD; bubblewrap is given the number, not a path
(see "Checks before every start", below). For a source below the state root,
ROOT is the state root and REL the whole path below it; for any other source,
ROOT is the folder that holds the source and REL its name. `/etc/resolv.conf`,
the file the holder derived, is Ataniq's and is bound this way too.

The rows of the host (`/usr` and the `/etc` files other than `resolv.conf`) stay
bound by path, from the real path the caller resolved (in the golden files, a
host without links, so each is its own path). The reason: no sandbox writes
them, and their names may be links of the host (`/etc/localtime` often is),
which a walk that follows no link would refuse; for them bubblewrap does the
lookup by path and follows a link. Whether bubblewrap mounts the object behind a
descriptor, and closes the descriptor before the command inside starts, is not
shown by anything here. It is a qualification item, and every plan says so in a
note [qualify].

Every plan also carries notes (the golden files list them) that state the
choices below and what the plan does not show.

**Namespaces.** `--unshare-all` asks bubblewrap to unshare every namespace type
it can (as bubblewrap documents it: user, IPC, PID, network, UTS and cgroup), and
`--unshare-user` is named as well. `--share-net` then keeps the network namespace
that pasta made. `--die-with-parent` ends the sandbox when its parent ends.

**Why `--share-net` appears.** It looks like the opposite of isolation. It is
there because bubblewrap runs inside the network namespace that pasta already
made: `--unshare-all` would make a second, empty one and the sandbox would have
no network. `--share-net` shares pasta's namespace, not the host's network. The
plan carries this as a note.

**Identity.** The default mode passes `--uid 0 --gid 0` with `--unshare-user`:
inside, the lead is UID 0 and GID 0, mapped to the owner's own ids outside. The
sandbox is not given the owner's ids as such. `USER` and `LOGNAME` are `root`.
Tools that run as UID 0 are untested (an open point below). The fallback is the
owner mode: `--uid` and `--gid` are the owner's own ids, which the inputs give
(non-zero), and `USER` and `LOGNAME` are the owner's login name. Two golden
files use that mode: `claude-owner-seccomp.golden` and
`codex-owner-optional-absent.golden`.

**Capability drop.** `--cap-drop ALL`. What the lead can still do inside its user
namespace is a question for qualification, not something this guide states.

**Cleared environment.** `--clearenv`, then one `--setenv` per entry of the
allowlist (see [The environment allowlist](#the-environment-allowlist)).

**No `--new-session`.** The plan never passes it. The probe gives the agent its
session and controlling terminal (see [The probe](#the-probe)); the launcher sets
no controlling terminal. `--new-session` is in the list of options the plan
refuses to emit (`profile.ForbiddenFlags`), together with the two options that
disable user namespaces.

**What `Build` checks.** It refuses, with the reason, inputs the profile refuses;
relative executable or agent paths; a descriptor number outside 3 to 1023 or used
twice; a report path that is not directly inside the folder the holder names as
its own, that lies in a folder the lead can write or in a never-mounted path, or
that is, lies below or lies above a bind source or one of the state root's
folders `leads`, `mirrors`, `mailbox`, `pub`, `coords` and `coord-login`; host
sources that are missing for a row of the host it
emits (`/usr`, the `/etc` files: the plan binds the real path the holder
resolved, see [the clone rules](#the-clone-rules-and-rule-g1)) or given for
another row; source walks that do not match the bind rows Ataniq supplies (see
below); more descriptors than the pass-through step places; a working folder
outside the tree and the home; an operand of bubblewrap that starts with `--`;
and a command line above a fixed cap. It then runs `Verify` on its own result.
`Verify` is written apart from the code that builds the arguments: it parses
the bubblewrap arguments back and compares each part with the inputs. A plan
that verifies says the text is the one the inputs describe. It does not say
what bubblewrap, pasta or the kernel will do with it.

**The source walks and the descriptors.** There is one walk per bind row Ataniq
supplies and none for anything else, so the walks match the table row for row:
23 rows in the table today, and one more for each extra tree placeholder. The
caller gives them as data (`plan.Options.SourceWalks`, from
`mountpoints.PlanWalks`), because the plan reads no file system. `Build` refuses
a row without a walk, a walk for a row of the host or for no row, two walks for
one row, a walk that does not lead to the row's source or names the other type
(folder or regular file), a folder or a path that is not clean, and a folder in
or below one the lead writes. The descriptor numbers are the plan's own, not the
caller's: from 10 up, in the order of the table, passing over the seccomp
descriptor and the report descriptor of the options, so they depend on the
inputs and the options alone and never collide with those two. They start at 10
and not at 3 to stay clear of the low numbers a caller picks for those two, and
of 3 and 4, which the probe refuses as a target when its Go runtime holds them
(see [The probe](#the-probe)). The step places at most 128 descriptors in all
(the walks, the seccomp file and the report channel together). The cap is hard:
the probe refuses more, and the plan refuses to emit one. The number has a
reason: a plan needs one per supplied row (23 today), one per extra placeholder
(the profile allows 64), the seccomp file and the report channel, which is 89 at
most, and 128 leaves room for rows the table gains. `Plan.Walks` lists the walks
with their numbers and `Mount.FD` says which number a row is bound from.

`Verify` reads `--bind-fd` and `--ro-bind-fd` back and pairs each with the walk
of the pass-through step that places its descriptor, which it reads back from
the step on its own. The path that walk leads to is the mount's source for every
check a source gets, so a step that walks somewhere else is caught as a mount of
another source. It reports a bind row Ataniq supplies that is bound by path; a
descriptor that two mounts or two walks use, a mount without a walk, a walk
without a mount, a row of the host bound from a descriptor, and a bind
descriptor that is the seccomp or the report descriptor; and a walk, in the step
or in `Plan.Walks`, whose descriptor, folder, path or type is not the one the
inputs and the options give.

## Nested sandboxes (owner decision R17)

The owner has decided that nested sandboxes are allowed for now. So the profile
passes neither `--disable-userns` nor `--assert-userns-disabled`, and none of
the eleven start-up assertions is about them. The reason is that members, Codex
included, start their own sandboxes inside the lead's, which they cannot do
where user namespaces are disabled.

The cost, stated plainly: user namespaces stay available inside the lead's
sandbox. Code in the sandbox can create them, and through them reach kernel code
that is reachable only that way. That is a larger kernel surface than with user
namespaces disabled. Qualification must cover it (case S8, rewritten for R17 in
[the harness guide](qualification-lead-sandbox.md#the-r17-note-for-s8)): it
records that a nested user namespace and a nested sandbox start, and what they can
reach. The outside broker remains the target: this is a decision for now, not a
change of what the design aims at.

### The seccomp hook

A reviewed BPF file can be named in the profile inputs, with its SHA-256. The plan
then has the pass-through step open that file read-only at a descriptor number the
caller gives, and passes `--seccomp FD` to bubblewrap. That is all the hook does:
the package neither reads the file, nor compares the digest with it, nor generates
a filter. None is shipped and none has been reviewed. Without one, the plan says
so in a note ("no seccomp filter: none has been reviewed"), and assertion 5 then
accepts only the sysctl `dev.tty.legacy_tiocsti` reading 0 (see
[The start-up assertions](#the-start-up-assertions)). The digest of the named file
is part of the profile digest.

## The file-system view

The root is a tmpfs, and what the lead sees is the rows of the bind table
(`profile.Inputs.Binds`), in this order. "Access" is what the plan asks of
bubblewrap. The last row, `--remount-ro /`, makes the root read-only; the other
mounts keep their own access, so the read-write rows and `/tmp` stay writable.

| Inside path | Source on the host | Access |
| --- | --- | --- |
| `/` | a new tmpfs | read-only after the last row |
| `/usr` | the host's `/usr` | read-only |
| `/bin`, `/lib` | symbolic links to `usr/bin` and `usr/lib` | link |
| `/lib64`, `/sbin` | symbolic links to `usr/lib64` and `usr/sbin`, only if the host has such a path | link, optional |
| `/etc/ssl`, `/etc/ca-certificates`, `/etc/nsswitch.conf`, `/etc/localtime` | the host's own, only if present | read-only, optional |
| `/etc/hosts`, `/etc/passwd`, `/etc/group` | the host's own | read-only |
| `/etc/resolv.conf` | the file the holder derived (see [Network and DNS](#network-and-dns)) | read-only |
| `/dev`, `/proc` | bubblewrap's own `--dev` and `--proc` | as bubblewrap makes them |
| `/tmp` | a new tmpfs | read-write |
| `/ataniq/probe` | the static probe binary | read-only |
| `/ataniq/spec` | the folder that holds the probe's spec | read-only |
| `/ataniq/agent` | the agent's install folder for the pinned version | read-only |
| the managed-settings path (see below) | the managed-settings file Ataniq wrote | read-only |
| `/home/lead` | `<state>/leads/<role>/home` | read-write |
| `/home/lead/.claude` or `/home/lead/.codex` | `<state>/leads/<role>/config/<runtime>` | read-write |
| the `skills` folder in that config folder | the lead skills folder Ataniq owns | read-only |
| the instruction file in that config folder (`CLAUDE.md` or `AGENTS.md`) | the instruction file Ataniq owns | read-only |
| `/ataniq/git` | the folder Ataniq owns that holds the git identity file, `config` | read-only |
| `/work/<project>` | `<state>/leads/<role>/tree`, the lead's own clone | read-write |
| `/work/<project>/.git/config`, `.git/hooks`, `.git/info` | Ataniq's copy, `<state>/leads/<role>/gitmeta` | read-only |
| extra placeholders in the tree, if the inputs name any | a host path Ataniq owns | read-only |
| `/ataniq/mirror/<project>.git` | `<state>/mirrors/<project>.git` | read-only |
| `/ataniq/qargi` | the Qargi install | read-only |
| the Qargi records, at the inside path the inputs give | `<state>/leads/<role>/qargi`, the lead's working Qargi records | read-write |
| `/ataniq/requests` | `<state>/leads/<role>/requests` | read-write |
| `/ataniq/mailbox` | `<state>/mailbox/<role>`, the lead's mailbox, which is Ataniq's | read-only |
| `/ataniq/mail` | `<state>/leads/<role>/mail`, with one subfolder per feature | read-write |
| `/coord` | `<state>/pub/<project>`, what the project's coordinators publish | read-only |
| `/ataniq/assignment`, `/ataniq/projection` | folders written outside the sandbox | read-only |

The managed-settings path is fixed for claude, `/etc/claude-code/managed-settings.json`.
The design names no path for codex's managed configuration, so for codex the
caller gives the path (it must lie under `/etc`), and the golden file uses an
invented one. The per-runtime paths and names in the profile (the config folder
and its variable, the instruction file, the managed-settings path) are existing
configuration that is to move into provider packs; no new runtime-specific code
was added for revision 4.

**The host layout of one lead.** Under the state root (`profile.Layout`):

```text
<state>/leads/<role>/holder            the holder's own state, never bound
<state>/leads/<role>/home              read-write
<state>/leads/<role>/config/<runtime>  read-write
<state>/leads/<role>/tree              read-write
<state>/leads/<role>/gitmeta           Ataniq's copy of the tree's git files
<state>/leads/<role>/qargi             read-write, the Qargi records
<state>/leads/<role>/requests          read-write
<state>/leads/<role>/mail              read-write
<state>/mailbox/<role>                 read-only
<state>/pub/<project>                  read-only
<state>/mirrors/<project>.git          read-only
```

`<state>/leads/<role>` itself is Ataniq's and is never bound. The folders are
the layout's: a caller cannot name another folder for any of them (the git
metadata folder is an input, and it must be the layout's).
The coordinators' trees and homes (`<state>/coords`) and their shared logins
(`<state>/coord-login`) lie under the same state root and are never bound into a
lead's sandbox.

**The Qargi records.** Every lead has the row of its working Qargi records,
read-write, from `<state>/leads/<role>/qargi` (`profile.Layout.QargiRecords`).
The inside path is required and the inputs give it
(`profile.Inputs.QargiRecordsInside`), because Qargi's layout names it and the
profile knows no default; in the golden files it is `/home/lead/.qargi`.

**The four parent folders.** `/ataniq/requests`, `/ataniq/mailbox`,
`/ataniq/mail` and `/coord` are in every profile and in its digest, for both
runtimes, whether or not the lead has coordinators: a bind cannot be added to a
running sandbox, so the four are fixed at the lead's start. Their sources are
the layout's. The published folder holds only `<feature>/qargi`,
`<feature>/out` and `<feature>/archive` of the project's coordinators. In this
code the four are only bound folders: the profile says where they lie and how
they are bound and nothing about what is in them, and nothing in Ataniq reads
the request folder (a condition of the owner).

**The git identity is bound as a folder.** `GIT_CONFIG_GLOBAL` is
`/ataniq/git/config`. The row that brings the file in binds the Ataniq-owned
folder that holds it (`profile.Inputs.GitIdentityDir`), read-only at
`/ataniq/git`; the file has the fixed name `config` in it. A bind of a single
file is a bind of the file that was there when the sandbox started: when the
holder replaces the file by writing a new one and renaming it into place, the
sandbox keeps the old inode. A bind of the folder shows the new file. The whole
folder is readable inside, so it holds the identity file alone. The
managed-settings file and the instruction file stay binds of a single file, so
one that Ataniq replaces while the lead runs is seen at the lead's next start,
not before.

**What is never mounted.** The inputs list host paths that no bind source may
equal, lie below or lie above: the owner's checkout, the real provider folders,
`.ssh`, and whatever else the caller names (at least one is required). The profile
adds the other runtime's config folder. The host's `/home`, the real
provider folders and the owner's checkout are not in the table at all: the lead's
home is its own folder under the state root. The check is on the text of the
paths, so a link could hide a folder: `mountpoints.ResolveSources` runs the same
checks again on the real paths, and refuses a source Ataniq supplies that has a
link on it (it must be given as its real path).

**Other state under the state root.** The profile knows the state root, so it
refuses, whatever the caller lists as never-mounted, a bind source that lies in
or above the lead's own holder folder, another lead's state, another role's
mailbox, another project's published folder or mirror, or any coordinator's
tree, home or login (`<state>/coords`, `<state>/coord-login`). The lead's own
mailbox and the project's own published folder are each the source of exactly
one row: a source may be that folder only as that row, and no source may lie
inside it. These rules too are on the text of the paths
(`profile.StateFolders.Reason`), and `ResolveSources` runs them again on the
real paths.

**The per-lead home.** `/home/lead` is `<state>/leads/<role>/home`, the same
inside path for every lead and both runtimes, because providers key their state by
path. Each lead has its own.

**`TMPDIR`.** `/tmp` is a tmpfs of its own, new at each start, and `TMPDIR` is
`/tmp`. Nothing is shared with the host's `/tmp`.

**The per-runtime config folder.** A lead's epoch mounts only its own runtime's
config folder, `/home/lead/.claude` or `/home/lead/.codex`, with the variable
(`CLAUDE_CONFIG_DIR` or `CODEX_HOME`) that names it. The other runtime's folder is
a never-mounted path, and assertion 6 looks for it inside: it is ok only when it
is absent, or an empty folder that is not a mount point.

**The fixed inside paths.** The same for every epoch and both runtimes:
`/home/lead`, `/work/<project>`, `/ataniq/probe`, `/ataniq/spec` (with
`spec.json`), `/ataniq/agent`, `/ataniq/qargi`, `/ataniq/mirror/<project>.git`,
`/ataniq/assignment`, `/ataniq/projection`, `/ataniq/requests`,
`/ataniq/mailbox`, `/ataniq/mail`, `/coord`, `/ataniq/git` (with `config`),
`/tmp` and `/etc/resolv.conf`. The inside path of the Qargi records is the
inputs'. The working folder is the tree by default; it may be the home or a
folder below either.

## The clone rules and rule G1

**The tree is the lead's own clone.** It is a full clone that Ataniq made for the
lead, bound whole and read-write. `mountpoints.CheckOwnClone` refuses a worktree,
a submodule, a folder below another repository, a tree whose `.git` names
alternates, and a tree, home, config folder, records folder, requests folder or
mail folder (the six folders the lead writes) that is not a 0700 folder of the
caller's user.

**Rule G1: no bind source in a folder a sandbox can write.** A bind source is
resolved on the host, outside every sandbox. For a row of the host, bubblewrap
does the lookup by path and follows a symbolic link; for a row Ataniq supplies,
the pass-through step's walk is the lookup, it follows none, and bubblewrap gets
the descriptor. A source that lies in a folder the lead can write is a name the
lead chose: a lookup that followed a link there would bring a folder of the
owner into the sandbox at the next start, and the rule does not rest on the walk
refusing one. So no row of the bind table has a source in or below a host
folder the lead can write
(the home, the config folder, the tree, the records folder, the requests folder
and the mail folder: the sources of the read-write rows). The profile
refuses a table that breaks the rule, `plan.Build` checks it again, and
`plan.Verify` reports a mount that does. The rule holds across leads too, as long
as every lead on the host uses the same state root (nothing here checks that; it
is the holder's obligation, listed in `internal/leadsandbox/mountpoints/doc.go`):
every folder a lead writes lies in that lead's own state folder, so the rules
about another lead's state keep the read-only sources of one lead out of the
reach of every other. That is why the records, requests and mail folders are
always the layout's (`<state>/leads/<role>/qargi`, `requests` and `mail`), with
no input that could name another host path, and why the git metadata folder
must be `<state>/leads/<role>/gitmeta` (`profile.Layout.GitMeta`).

**The `.git` rows.** The tree's `.git/config`, `.git/hooks` and `.git/info` are
read-only inside, and are bound from an Ataniq-owned copy, not from the tree.
The copy is `<state>/leads/<role>/gitmeta`, outside the home, the config folder
and the tree, and `mountpoints.Prepare` fills it once, before the lead's first
run: a copy of the clone's `.git/config`, an empty `hooks` folder (a managed
lead's tree has no hooks) and a copy of the regular files of `.git/info`. It
builds the folder in a temporary sibling in the lead's state folder
(`gitmeta.tmp`), syncs every file and folder, and moves it into place with
`renameat2` and `RENAME_NOREPLACE`, so the place never holds a folder that is only
partly written, and nothing is replaced. A `Prepare` that fails or is interrupted
before the move leaves the sibling and no folder in the place: `Check` then
refuses the start, and the next `Prepare` refuses the leftover with a reason that
names it, until it is removed by hand.

This is a stated difference from the design's text. Revision 4 has the clone's
`.git/config`, `.git/hooks` and `.git/info` bound from "the same paths, walked
under B1". The code keeps Ataniq's own copy, `<state>/leads/<role>/gitmeta`, as
the source of the three binds. That is stricter: no bind source ever lies inside
a folder the lead writes, so no source has to be walked through one. The
destinations in the tree (`.git` itself and the three paths) are still walked
before every start, as the design asks (see below).

`Prepare` refuses a clone `.git/config` that has an `include` or `includeIf`
section (any case; a bounded scan of the section headers, and no other key is
interpreted), because git would read the file it names as configuration. For the
lead this means that the copies are read-only binds of files it cannot write, so
it cannot change the files that are bound, and that a change to the clone's git
configuration is made by Ataniq, outside, in the copy. It does not mean that git
inside reads only that configuration: a git in the sandbox can still be pointed
at configuration the lead writes (through its environment, or through a `.git` it
rebuilds in the tree), which is inside the lead's own authority. The copy is
never read as configuration by a tool outside the sandbox.

**Mount points inside folders the lead writes.** The targets of the `.git` rows,
the place of the runtime's config folder in the home, the skills folder and the
instruction file of the config folder, the place of the Qargi records when it
lies in the home, and any extra placeholder lie
inside folders the lead writes. bubblewrap would make a missing
one itself, in a folder where a planted link could redirect it. So Ataniq makes
them before the lead's first run (`mountpoints.Prepare`, which refuses unless the
caller states that no lead has run there yet, and never replaces what exists), and
lists the tree placeholders (the ones outside `.git`) in the git exclude file of
the copy, as one anchored line each. A placeholder whose path holds a character
git treats as a pattern is refused.

**Handles, not names (rule B1).** Rule B1 of the design (revision 4, section
3.6): host-side code never binds or reads a path any of whose components a
sandbox can write. It opens the nearest folder no sandbox can write by name,
walks every further component from that handle with `openat2` (`RESOLVE_BENEATH`
and `RESOLVE_NO_SYMLINKS`), refuses a symbolic link and an unexpected type, and
does so at the moment of use, every time: an earlier check is never reused.
Where bubblewrap can take it, the source is passed as the handle that walk
returned: the pass-through step makes the walk of every bind source Ataniq
supplies, and bubblewrap is given its descriptor (see "How a bind source reaches
bubblewrap", above). In `mountpoints` the folder opened by name is the state
root, which the caller owns. `Check`, `Prepare` and `CheckOwnClone` open every
folder below it that they work in once per call, from the state root's handle
and never by its name (`safefile.Root.SubRoot`: one lookup without links, the
type decided on the descriptor the lookup returned), and every later step is
beneath that descriptor. So no folder is looked at and then opened again by its
name: the folder that was checked is the folder that is used, whatever a lead
does to the name in between. Nothing found by one call is kept for the next.

**Checks before every start.** Three, in this order, right before the launch,
after the previous unit is gone and its control group is empty (the sandbox's
own process tree does not show that no lead process remains, because nested
sandboxes are allowed):

- `mountpoints.Check` is the start walk. It covers every bind target that lies
  in a folder the lead writes: the place of the runtime's config folder in the
  home (`.claude` or `.codex`), the places of the skills and of the instruction
  file in the config folder, the tree's `.git` itself and its `.git/config`,
  `.git/hooks` and `.git/info`, the tree placeholders, and the place of the
  records when it lies in the home. Each is looked up beneath the handle of the
  folder it lies in, in one lookup that follows no link in any component, and
  its type is decided on the descriptor the lookup returned. A symbolic link (at
  the path or before it, dangling or not) or a wrong type refuses the start with
  a problem that names the path. The design asks that each target be absent or
  of the expected type; the code is stricter and wants each mount point present,
  as `Prepare` made it. The tree's `.git` is no mount point and may be absent;
  its three mount points are then the ones reported as missing. `Check` also
  refuses a copy (the git metadata folder) that is incomplete, has other modes
  than `Prepare` gave it or has no exclude line for a placeholder.
- `ResolveSources` resolves each bind source to its real path and runs the
  never-mounted checks and the rules about other state again on the real paths.
  It compares names and opens nothing.
- `CheckBindSources` walks to the path bound for every row, by handle, without
  following a link in any component. A source Ataniq supplies below the state
  root is opened beneath the state root's handle, in one lookup that crosses no
  mount point (a mount point below the state root is refused); the state root's
  own path is first looked up from the root of the file system without following
  a link, and the folder found must be the folder opened by name. Any other
  source is looked up from the root of the file system in one `openat2` that
  follows no link (mount points are crossed there: the folder that holds a
  source may be a mount). For a row Ataniq supplies it then checks, on the
  descriptor it opened, that the source is a folder or a regular file, as the
  row binds. The type of a row of the host is not checked, and no content is
  read. A problem names the row by its inside path. This is the pre-check of
  the bind sources: the walk that decides is the pass-through step's (below).

`mountpoints.SourceWalks` names the walk of each row Ataniq supplies as data:
the folder to open by name, the path to walk beneath it and the type the source
must have (the state root and the whole path below it for a source below the
state root, the source's own parent folder and its name for any other). It
reads nothing. `ResolveSources` puts the same walk on each row it returns, and
`CheckBindSources` takes the place and the type of a source from it, refusing a
row Ataniq supplies that has none. `PlanWalks` gives the same walks in the type
the plan takes (`plan.Options.SourceWalks`), and the pass-through step makes
them.

**The walk that decides.** `CheckBindSources` stays as the pre-check. It runs
first, closes its descriptors when it returns and passes none on. The step walks
to every bind source Ataniq supplies once more, by handle, right before it
starts bubblewrap, and that walk decides: the descriptor it got is the one
bubblewrap is told to bind the row from, so what the name holds afterwards
changes nothing for the object that is bound (if bubblewrap mounts the object
behind the descriptor: an open point). A source that changed since the pre-check
is refused by the walk, or bound as the object the walk found. A source the walk
refuses (a link, a missing entry, another type, a mount point below its root, no
`openat2`) ends the step with exit 96, before bubblewrap is started and before
the report channel is opened for writing. The step is stricter than the
pre-check in one place: beneath the folder its walk starts from it crosses no
mount point, for any row, where `CheckBindSources` crosses mount points for a
source outside the state root.

**What is still opened or resolved by name, and why.** The state root: it is the
folder the walks start from, the nearest folder no sandbox can write. Only its
last component is protected by the open; `CheckBindSources` looks for a link
above it too, and `Check`, `Prepare` and `CheckOwnClone` leave that to the
caller. The root of the file system, `/`, for the lookups of `CheckBindSources`
that start there. The folder each walk of the pass-through step starts from
(ROOT): the step opens it by name, from the root of the file system, in one
lookup that follows no link on any component (`safefile.OpenRootNoLinks`; mount
points above it are crossed). That refuses a link on the way to ROOT and
protects nothing else above it, so ROOT and everything above it must be
Ataniq's: the plan and the holder choose it, the state root for a source below
it and otherwise the folder that holds the source. And every bind source, the
never-mounted paths and the state folders in `ResolveSources`, which resolves
names (`filepath.EvalSymlinks`) and opens nothing. That resolution is kept for
two things: the real path of a row of the host (`/usr`, `/etc/localtime` and the
like may be links of the host, which no lead writes), which is the path the plan
binds; and the comparison of real paths under the never-mounted rules and the
rules about other state. The path of a source Ataniq supplies has no component
inside a folder a sandbox can write (rule G1), so resolving its name reads no
link a lead could plant. No folder below the state root is opened by its name.

**The limit.** What the step's descriptor gives is that bubblewrap is handed the
object the walk found. Whether bubblewrap mounts the object behind the
descriptor, and closes the descriptor before the command inside starts, is not
shown by anything here (an open point). The rows of the host are still bound by
path: bubblewrap resolves each by name, from a real path that no lead writes.
And the walk of the step, like every check here, says what was there at that
moment, which is why every source and everything above it must be Ataniq's.

**The path that is bound is the path that is checked.** For a row bound by path,
bubblewrap binds the path the plan names and follows a link in it, so a source
that is checked and a different one that is bound is a check that did not
happen. For a row bound from a descriptor there is no second lookup by name: the
path the plan names for the row is the path the step walks to. For every row:

- a source Ataniq supplies (every row but the host's own) must be given as its
  real path. `ResolveSources` refuses a source whose real path differs from its
  lexical one, a link anywhere on it, naming the row and the real path. The
  plan's walk for the row leads to that source (`Build` refuses a walk that does
  not, and `Verify` takes the source of such a mount from the walk it reads back
  out of the step), the step walks to it as it is, with no link allowed on it,
  and checks its type, and `CheckBindSources` is the check before that walk, on
  the same path;
- a row of the host (`/usr`, `/etc/hosts`, `/etc/localtime` and the like), whose
  own name may be a link of the host that no lead writes, is bound from the real
  path that `ResolveSources` reported. The holder gives it to the plan
  (`plan.Options.HostSources`, which `plan.Build` requires for every host row it
  emits and `plan.Verify` checks), and `CheckBindSources` looks that real path
  up from the root of the file system.

**What the read-only binds do not protect against.** They are defence in depth.
A lead can rename `.git` (renaming the parent of a mount point is allowed) and
build another, and neither `Check` nor `CheckOwnClone` can tell a rebuilt `.git`
from the original. The primary rule is that no owner-side tool uses a lead's tree
(the lead-hosting design, section 3.3); nothing here enforces it. The checks are
made at one moment: a lead that runs between a check and the start can change what
it checked. The exclude check is textual, and a `.gitignore` of the tree can make
git show a placeholder as untracked, which is cosmetic. The records, requests
and mail folders lie in the lead's state folder, so the lookup from the state
root covers their paths; the holder makes them with mode 0700, and
`CheckOwnClone` checks their mode and owner with the tree, the home and the
config folder. The mailbox and the published folder, which are bound read-only,
are sources like any other: their paths and their type (a folder) are looked
at, and nothing here looks at their mode, owner or content.

## The environment allowlist

The environment inside is set with `--clearenv` and one `--setenv` per entry.
Names, with their values where the profile fixes them:

| Name | Value |
| --- | --- |
| `HOME` | `/home/lead` |
| `TMPDIR` | `/tmp` |
| `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME` | `/home/lead/.config`, `/home/lead/.local/share`, `/home/lead/.local/state`, `/home/lead/.cache` |
| `CLAUDE_CONFIG_DIR` or `CODEX_HOME` | the runtime's config folder inside (only the epoch's own) |
| `GIT_CEILING_DIRECTORIES` | `/work` |
| `GIT_CONFIG_GLOBAL` | `/ataniq/git/config` |
| `GOCACHE`, `GOMODCACHE` | `/home/lead/.cache/go-build`, `/home/lead/go/pkg/mod` |
| `PATH` | the inputs' value, else `/ataniq/qargi/bin:/ataniq/agent/bin:/usr/local/bin:/usr/bin:/bin:/home/lead/.local/bin` |
| `SHELL` | the inputs' value, else `/bin/bash` |
| `USER`, `LOGNAME` | `root` in the default mode, the owner's login name in the owner mode |
| `QARGI_LEAD` | the lead identity the inputs give, else the role |
| `TERM` | the host terminal's value (required) |
| `COLORTERM`, `LANG`, `TZ` | the host's values, set only when the inputs give them |

A profile may add names (`ExtraEnv`). **`PWD`** is not in the allowlist and a
profile that lists it is refused: bubblewrap keeps and sets `PWD` under
`--clearenv`, to the working folder, so the environment the agent sees is the
allowlist plus `PWD`. `plan.InsideEnvironment` gives that list, and assertion 2
compares the real environment with it, names and values.

**Refused names.** A name a profile adds is refused when it is not a plain
identifier of at most 64 bytes, when it is `SSH_AUTH_SOCK`,
`DBUS_SESSION_BUS_ADDRESS`, `XDG_RUNTIME_DIR`, `ANTHROPIC_API_KEY` or
`OPENAI_API_KEY`, when it ends `_API_KEY`, `_TOKEN`, `_SECRET`, `_PASSWORD` or
`_KEY`, when it starts `LD_`, `GIT_CONFIG_` (other than `GIT_CONFIG_GLOBAL`) or
`AWS_`, or when the profile defines it already. Names are compared in upper case.

**The list is a starting point for the owner.** It does not name every credential
or loader override, and the allowlist is what was chosen before any tool ran in
the sandbox. Case S5, which runs the bootstrap, the doctor, a test target and the
flow verbs inside, records the variables each tool needs and settles the list.

## Network and DNS

**pasta.** `sandboxnet.PastaArgs` returns `-f -q --config-net -t none -u none -T
none -U none --no-map-gw --`. That gives a private network namespace with
outbound traffic through NAT, no port forwarding from the host into the sandbox
(`-t none`, `-u none`), no forwarding from the sandbox to the host's loopback
(`-T none`, `-U none`), and no mapping of the gateway address to the host
(`--no-map-gw`).

The stated limits:

- Outbound traffic is not narrowed. Nothing restricts which destinations the
  sandbox can reach.
- NAT reaches the host's own non-loopback addresses, so a host service bound to
  all interfaces is reachable, and so is the LAN. Cases S2 and S14 record what is
  reachable; the network policy is an open owner decision.

**The `resolv.conf`.** The holder writes the file the sandbox gets, from
`sandboxnet.Derive`:

- When the host's `/etc/resolv.conf` is the systemd-resolved stub (its real path
  is the stub file, or its only server is `127.0.0.53`), the file is derived from
  systemd-resolved's upstream file, `/run/systemd/resolve/resolv.conf`. Otherwise
  it is derived from the host file.
- Loopback servers are dropped (`127.0.0.0/8`, `::1` and IPv4-mapped loopback),
  and so are scoped, unspecified and other unusable addresses, and at most three
  servers are kept.
- When no usable server is left, the start is refused with the reason, because
  the sandbox has no route to a host loopback service and would have DNS that
  cannot work. So is a stub with no upstream file.
- Input is bounded before it is kept: at most 64 KiB and 512 lines, no NUL byte,
  a bounded search list and option list. What is dropped is counted in notes.

Two limits of the derivation are stated and not detected:

- The host resolver's policy is bypassed. Deriving from the upstream list and
  dropping loopback servers skips DNS over TLS, split DNS (per-link routing
  domains such as a VPN) and filtering. Names may go to the listed servers in
  clear, and names meant for an internal server may go to a public one.
- A nameserver that is one of the host's own addresses (a resolver on the LAN
  address, say) is accepted. Inside the namespace pasta configures, that address
  belongs to the namespace, so the queries go nowhere. The package does not know
  the host's addresses, so it cannot tell. Assertion 4, which resolves a provider
  host name from inside, is the check that would meet it.

## The probe

`ataniq-probe` is a static program, built without cgo, with two modes. It exits
96, 97 or 98 and otherwise replaces itself with the command it was given (or,
when `setsid` is refused, runs it as a child and exits with its status, or 128
plus the signal number; see below).

**Pass-through mode** (`pass-fds [--read FD=PATH]... [--write FD=PATH]...
[--walk-dir FD ROOT REL]... [--walk-file FD ROOT REL]... -- COMMAND ARGV...`)
runs on the host between pasta and bubblewrap. FD is 3 to 1023, each at most
once whatever the option, and at most 128 descriptors are given in all, counting
every option: the cap is hard (see "The source walks and the descriptors" for
the number). A `--read` file is opened read-only and must be a regular file; a
`--write` file is opened write-only, appending, never created and never
truncated, and must be a regular file or a FIFO. Links are not followed at the
last component. A FIFO that no process has open for reading is refused at once,
so the holder opens its reading end first.

A walk is a bind source of the plan, reached by handle: `--walk-dir` for a
source that must be a folder, `--walk-file` for a regular file. ROOT is the
folder the walk starts from and REL the path of the source beneath it. For each
walk the step opens ROOT in one lookup that follows no symbolic link in any
component (mount points above ROOT are crossed), opens REL beneath that handle
in one `openat2` lookup that follows no link on any component, the last
included, and crosses no mount point, with `O_PATH` (so a FIFO does not block it
and a device's own open does not run), reads the type from the descriptor that
lookup returned and refuses anything but a folder or a regular file as the
option says. No content is read. It keeps a descriptor of its own on that
object. The walks are made first, before any `--read` or `--write` file is
opened, so a refused source ends the step before the report channel has been
opened. There is no fallback to a path-based open: where `openat2` is not
available the walk is refused.

Each descriptor, of a file or of a walk, is placed at exactly its number, every
other descriptor above 2 is made close-on-exec, and the command is executed.
Whether the command, bubblewrap, then binds the object behind a descriptor and
closes it before it starts what runs inside is not something the probe answers:
a qualification item. The checks on a `--read` or `--write` path are not atomic
with the open, so every such path must lie in a folder only the holder can
write. That rule is about those two options only: a walk has no such gap below
ROOT, because its lookup and its check are on one descriptor. Above ROOT a walk
refuses a link and nothing else, so ROOT and the folders above it must be ones
no sandbox can write; the plan and the holder choose ROOT. A number that is one
of the Go runtime's own descriptors is refused. The probe is built with the runtime's
cgroup-aware `GOMAXPROCS` turned off (`containermaxprocs=0`), so that the
runtime does not keep the cgroup's `cpu.max` open at the lowest free number when
the probe runs in a cgroup that has the cpu controller; the only descriptors the
runtime holds are the poller's. A `GODEBUG` setting in the probe's environment
overrides this, so the probe must not be started with `containermaxprocs=1`.

**Inside mode** (`--spec PATH --report-fd N -- AGENT ARGV...`) runs inside the
sandbox as the program the plan starts last. In order, it marks the report
descriptor close-on-exec; checks that the agent is an executable regular file;
reads the spec (a regular file of at most 256 KiB, decoded strictly); runs
assertions 1 to 8 and 11 against the real system; writes the report; judges it;
gives the agent a session; and executes the agent.

**The report channel contract.** The probe writes exactly one line, the report, to
the report descriptor, on success and on failure, and no other byte, then or
later. The holder takes one accepted line as the signal that the probe has
reported. It does not wait for end-of-file, because bubblewrap's own processes may
keep a copy of the descriptor open, so no end-of-file may come while the sandbox
lives. A second line or any further byte after the line is a failure of the start.
A line seen anywhere else, the terminal in particular, is display only: the lead
writes to the same terminal after the exec and can print the same prefix, and the
report's digest is readable by the lead, so neither authenticates anything.

**Exit codes.** 96: the pass-through mode refused a bind source in its walk;
standard error holds one line that names the option, the descriptor, the reason,
ROOT and REL, nothing was executed, and bubblewrap was not started. 97: the
inside checks did not pass; the agent is not started, and standard error lists
the problems (display only). 98: a usage error, or a failure to do what the
command is for (in the pass-through mode, any error but a refused walk). A
refusal before the report (a usage error, a report descriptor that is not open,
an agent that cannot be executed, a spec that is refused) writes nothing to the
channel, and the holder sees the exit status alone. A file the kernel does not
accept as a program fails after a good report with the same 98, which a holder
cannot tell from an agent that started and exited 98. How pasta passes on the
status of the pass-through step, 96 included, is not shown by anything here (an
open point).

**Session and controlling terminal.** When descriptor 0 is a terminal, the probe
gives the agent a new session with that terminal as its controlling terminal
(`setsid`, then `TIOCSCTTY`; variant B of the spike). If `setsid` is refused
because the probe leads a process group, it runs the agent as a child in a new
session, forwards `SIGTERM`, `SIGINT`, `SIGHUP` and `SIGQUIT`, waits, and exits
with the child's status. When descriptor 0 is not a terminal, it says so on
standard error and skips the step. It never issues `TIOCSTI`. A failure to get the
terminal when there is one is exit 98.

## The start-up assertions

The eleven assertions of the design (revision 4, section 3.8) are the table in
`assert`. The design lists the eleventh between 7 and 8; the table keeps its
number, and it comes last. The probe makes the inside half of each that has one
(1 to 8 and 11), once each, before it executes the agent. The holder would make
the outside half of 1, 3, 5 and 7, then 9 and 10
(`assert.RunOutside`; only the qualification harness calls it, and no holder
does yet), after the probe has reported and before it accepts the start, add
them to the probe's results and judge the whole. The report has no field that
says the sandbox as a whole passed or is supported.

| No. | What is checked | Where |
| --- | --- | --- |
| 1 | The attach and control sockets and the holder's state are not visible inside. | inside and outside |
| 2 | The environment inside equals the profile's allowlist (plus `PWD`). | inside |
| 3 | One host loopback port and one host abstract socket are unreachable. | inside and outside |
| 4 | A provider host name resolves. | inside |
| 5 | `dev.tty.legacy_tiocsti` is 0, or the seccomp filter is loaded. | inside and outside |
| 6 | The owner's real provider folders, `~/.ssh`, the owner's checkout and the other runtime's config folder do not exist inside. | inside |
| 7 | Before the sandbox starts, with the previous instance proven stopped, the holder's walk of 3.6 found no symlink or wrong type at any bind destination inside a lead-writable folder. Inside, the tree is the lead's own clone, and its `.git/config`, `.git/hooks` and `.git/info` are not writable. | inside and outside |
| 8 | The managed-settings file is present and read-only; for claude it contains the guard hook. | inside |
| 9 | The role lock is held, and lingering is on. | outside |
| 10 | A current qualification record exists for this runtime, version and profile digest. | outside |
| 11 | The four parent folders of 3.3 are present; `/ataniq/mailbox` and `/coord` are not writable inside, and no coordinator's tree or home is visible. | inside |

What the checks do and do not show, from the package:

- **Assertion 1** looks in the mount table for the holder's and owner's paths
  (by text) and compares the device and inode numbers the holder recorded, for
  those paths and their parents, with those of every mount point of the table and
  of the lead's home, config folder and tree. With no identity listed it is
  not-checked, never ok. Whether device numbers match between the holder's reading
  and the probe's, a btrfs subvolume layout in particular, is a qualification item.
  The list holds those paths and their parents, not their descendants: a bind of
  a folder below a listed path (say below the owner's `.ssh`) from a file system
  not mounted at `/` (a separate `/home`, a btrfs subvolume) has a mount root the
  text check cannot relate, and its identity is not listed, so neither comparison
  finds it. That is a limit of the assertion and a qualification item.
- **Assertion 3** is made on both sides. The probe dials the canary's addresses
  and classifies the failure. The canary is a loopback TCP listener (a second on
  `::1` when the host has IPv6 loopback) and an abstract socket, which the holder
  owns and which count the connections they accept. After the probe's report the
  holder settles the canary (one marker connection to each listener, so that
  everything queued before it is counted) and reads the count. That count is the
  evidence independent of the probe's own judgement, and a timeout is weaker
  evidence than a refusal and rests on it.
- **Assertion 5** accepts the sysctl reading 0 and nothing else when the profile
  names no seccomp filter, because a filter that something else loaded is not
  evidence that `TIOCSTI` is blocked. When a filter is named, the probe requires
  that one is loaded; it does not verify from inside which one.
- **Assertion 7** is checked on both sides. The outside half is the start walk
  (`mountpoints.Check`). `assert` does not make the walk: the holder makes it
  and gives what it found as a fact (`assert.StartWalk`), which
  `assert.CheckStartWalk` turns into the result. It is ok only when the walk
  was made to its end, the previous instance is proven stopped (its unit gone
  and its control group empty, or the lead has no previous instance) and
  nothing was found; failed, with the first path, when the walk found a
  problem; and not-checked for a walk that was not made or was made while a
  process of the lead may remain. The package takes the holder's word for
  both: it cannot see whether the walk was made or the instance stopped. The
  inside half shows that each of the three paths is on a read-only mount, that
  no read-write mount lies at or below it, and that a write is refused. The probe
  checks that `.git` is a folder; that the tree is the lead's own clone is the
  check of `mountpoints`. Where the kernel gives no mount ID (before Linux 5.8)
  the inside result is not-checked.
- **Assertion 8** shows that the managed-settings file is a regular file on a
  read-only mount and holds the marker text. It does not parse the file: a file
  that holds the text where it does not make the hook active passes.
- **Assertion 10** is looked up through an interface, `QualificationRecords`.
  A record counts when it is for this runtime, this version and this profile
  digest, has a recording time and a harness version, is not dated after now and
  has not expired. A missing or stale record fails the assertion, blocks the
  start and is meant to show in the console as unsupported
  (`assert.ConsoleUnsupported`; no console view uses it yet). No store is
  built, so the harness records the result as not-checked.
- **Assertion 11** shows that each of the four parent folders
  (`/ataniq/requests`, `/ataniq/mailbox`, `/ataniq/mail` and `/coord`) is a
  folder and a mount of its own; that `/ataniq/mailbox` and `/coord` are
  read-only in the way of assertion 7; and that `/ataniq/requests` and
  `/ataniq/mail` are on a mount that is not read-only and are writable by
  `access(W_OK)`. It says nothing about what is in them. That no coordinator's
  tree or home is visible is looked for in the ways of assertions 1 and 6, with
  the same limits, through two keys of the spec. `coordinator_paths` lists the
  two roots of the layout, `<state>/coords` and `<state>/coord-login`: they
  must not exist inside, and no mount may be related to them by the text of
  its root, source or mount point. `coordinator_identities` lists device and
  inode pairs, at most 1024: no mount point (the first 2048), none of the four
  folders and no entry of `/coord` may have one of them, and with none listed
  the assertion is not-checked, never ok. The probe also lists `/coord` (at
  most 4096 entries; with more the assertion is not-checked) and fails an entry
  that holds `tree` or `home`. The limit: the identities are a snapshot, taken
  when the spec is written, of the roots, their ancestors and the folders of
  the coordinators and shared logins the holder listed then. A coordinator
  made later is not in the list, and its tree or home, bound from a file system
  that is not mounted at `/`, is found by neither comparison. Because the
  comparison by text includes mount points, a state root that lies below a
  path that is a mount point inside (`/tmp`, say) fails the assertion, by the
  same rule assertion 1 has for the holder's paths.

**What `not-checked` means.** A check that cannot be made (a file that cannot be
read, a mount table that does not parse, a dial that failed for a reason that does
not show the address unreachable) is reported as not-checked, with the reason. It
is not ok. `Failed` treats it as a failure, with one exception: the spec may list
assertion 4 as optional, and only its own result is then excused. No other
assertion may be listed, a failed result is a failure whatever the spec says, and
a missing result is a failure. The text of a detail may quote what a lead or the
host controls, so it is escaped and cut to a bounded length. The checks run once,
before the agent starts; they say nothing about what the agent does after.

## Reading lead-writable folders without following links

`safefile` is the owner-side way to read or prepare a folder a lead can write. The
caller opens a folder handle once, with `OpenRoot` on a path it owns (only the
last component is checked) or with `OpenRootNoLinks` on a path that is expected
to be a real path (see below), and every access goes through `openat2` with
`RESOLVE_BENEATH`, `RESOLVE_NO_SYMLINKS`, `RESOLVE_NO_MAGICLINKS` and
`RESOLVE_NO_XDEV`, relative to that handle. It offers
reads (a regular file up to a cap, a folder listing up to a count, `Lstat`) and
the writes the mount-point work needs (`MkdirAll`, `WriteFileExclusive` which only
creates, and `AppendFile` for an existing regular file), `Sync` (fsync of a file
or folder) and `RenameNoReplace` (one name to another in the handle's folder,
which never replaces), and no replacing rename, remove or truncate.

Three methods open an entry beneath the handle and keep its descriptor, each in
one lookup with the type checked on the descriptor it got. `SubRoot` opens a
folder as a new handle, for a caller that looks at a folder and then works in
it: every later call is beneath the folder that was looked up, whatever happens
to its name afterwards. `OpenDirHandle` and `OpenFileHandle` open a folder or a
regular file with `O_PATH` and return an entry handle, for a caller that hands
the entry on (a bind source given to a child process): what it checked is the
descriptor it passes. None of the three crosses a mount point, and an entry
handle on a file refuses neither a size nor a hard-linked file, as it reads
nothing. The descriptor is close-on-exec, so no child inherits it by itself.
`Handle.Dup` gives the caller a descriptor of its own on the same object,
close-on-exec too, which it places at the number a child process is to inherit;
the number `Handle.Fd` returns stays the handle's and ends with its `Close`, so a
caller that hands the object on takes the duplicate and closes the handle.

`OpenRootNoLinks` is for a folder that a bind source is walked from, which is
not the caller's own choice of path. It looks the whole path up from the root of
the file system in one `openat2` that follows no symbolic link on any component,
and the handle is the descriptor that lookup returned. Mount points above the
folder are crossed, because the folder may be on any mount; below the handle the
rules are those of any other `Root`. The pass-through step opens the ROOT of
each walk this way.

**What it guarantees.** A symbolic link is refused, even when it points back
inside the folder or nowhere; a link in a parent component is refused by every
method. A path that is absolute, has `..` or is not clean is refused before any
system call. A file is checked on its open descriptor: a FIFO, device, socket or
folder is refused, and the open cannot block on a FIFO or a leased file. A file
over the cap is refused, not truncated, even if it grew while it was read. A
mount point below the handle is refused. A regular file with more than one hard
link is reported, and `RefuseHardLinks` makes a read refuse it; an append always
refuses it. Every refusal names its reason and matches one of the package's
errors. When `openat2` does not work (an old kernel, a seccomp filter that denies
it), `OpenRoot` and `OpenRootNoLinks` fail with `ErrUnsupported`; there is no
fallback to a path-based open.

**What the caller must still do.** For `OpenRoot`, pass a path whose parents it
owns: only the last component is checked. `OpenRootNoLinks` refuses a link on
every component and adds nothing else: whoever can write a folder above can
still rename what the path names, so the parents must be the caller's there too.
Keep the owner's state folders on a file system mounted `nodev`, because a
device node is opened before `fstat` can refuse it. Check again, right before
use, a path it will later use by name (a bind): the end check of `MkdirAll`
holds only at that moment, and folders made after a move stay where the moved
folder went. Expect that a raw error number (`EACCES`, `ENXIO`, `EPERM`,
`EAGAIN` for a leased file) can come instead of a package error from the open of
a device or a leased file. The package does not protect against changes made by
someone with owner rights to the folder behind the handle.

## The profile digest

`profile.Digest` is the SHA-256 of a canonical, versioned encoding. The profile
version is 4: the layout of revision 4 of the design, in which every table has
the rows of the four parent folders and of the Qargi records, and the git
identity is bound as its folder. The digest covers the
profile version, the runtime and its pinned version, the identity mode; the
bubblewrap flag set; the bind table (every row's kind, inside path, source role,
link target and flags, with the project replaced by a token and a host path by its
role); every environment name and the value of every variable whose value the
profile defines; the SHA-256 of the seccomp file (or "none") and of the
managed-settings file, as the caller gives them; and the pasta arguments.

It does not cover host paths (the state root, every bind source, the paths of the
seccomp and managed-settings files), the role and the project, the owner's ids and
login name, the values the host terminal or the lead gives (`TERM`, `COLORTERM`,
`LANG`, `TZ`, `QARGI_LEAD`, and `USER` and `LOGNAME` in the owner mode), or the
content of the folders and files bound from the host except the two whose
SHA-256 the caller gives (the lead skills, the instruction file, the agent and
Qargi installs, the probe, the spec, the git identity file, the derived
`resolv.conf` and the records). The package does not compare the two given
digests with the files. It does not cover what the host really is, or what the
sandbox then allows: the pasta arguments are covered, not what the network reaches.

So a digest names a profile, not a host. It is the key a qualification record is
kept under, and the record says what was checked.

## Open points

For the owner (O) and for qualification (Q). Each is stated in the package named.

1. **The profile itself (O).** This whole proposal awaits the owner's decision on
   the profile (Q1).
2. **Nested sandboxes and the kernel surface (O, Q).** R17 leaves user namespaces
   available inside, a larger kernel surface than with them disabled. Case S8
   measures it.
3. **Tools as UID 0 (O, Q).** The default identity is UID 0 inside; tools that
   run as UID 0 are untested. The owner mode is the fallback.
4. **The network policy (O, Q).** Outbound traffic is not narrowed, and the host's
   non-loopback addresses and the LAN are reachable (cases S2, S14).
5. **The DNS policy bypass (O).** The derived `resolv.conf` skips the host
   resolver's policy (DNS over TLS, split DNS, filtering), and a nameserver on one
   of the host's own addresses is not detected.
6. **The environment allowlist and the refusal list (O, Q).** Both are a starting
   point for the owner to review and extend; case S5 settles which variables the
   tools need. The layout of the agent and Qargi install folders, which `PATH`
   assumes, is settled by qualification.
7. **The codex managed-configuration path (Q).** The design names none for codex,
   so the caller gives it, and whether codex reads a file there, and what the
   marker text of assertion 8 shows for it, are open.
8. **Git configuration writes (O, Q).** A managed lead's tree has no hooks and its
   `.git/config` is Ataniq's copy. Whether Qargi's flow verbs need to write git
   configuration (branch tracking) is settled by case S5.
9. **Descriptors through bubblewrap (Q).** Whether the report descriptor reaches
   the probe inside, and which other processes hold a copy of it (bubblewrap's,
   the pass-through step's).
10. **Device numbers across namespaces (Q).** Assertion 1 compares device and
    inode numbers read outside and inside. Whether they match on a given host, a
    btrfs subvolume layout in particular, is not shown by anything here.
11. **The seccomp filter (O, Q).** None is shipped or reviewed. Whether one is
    wanted, and what it holds, is open (case S6); without one, assertion 5 rests
    on the sysctl.
12. **Policy that outranks the managed-settings file (Q).** Case S18 looks for a
    policy source, server-delivered for example, that outranks the file.
13. **The lead's `.git` (O).** A lead can rename `.git` and build another; the
    read-only binds are defence in depth, and the primary rule (no owner-side tool
    uses a lead's tree) is not enforced here. Case S16 and the intake cover it;
    the lead intake guide says what the built intake does with
    a lead's tree.
14. **Checks at one moment (Q).** The mount-point and source checks are made right
    before the start; nothing here closes the gap between a check and the launch
    except an empty control group, which the holder has to prove. For a bind
    source Ataniq supplies the gap is narrowed to the pass-through step's walk,
    which is made right before bubblewrap starts and whose descriptor bubblewrap
    is given (if it mounts that object: item 19). The rows of the host, bound by
    path, and the start walk of the mount points inside the lead's folders, which
    the step does not repeat, are not covered by it.
15. **The agent's exit 98 (Q).** A holder cannot tell an agent that exited 98 from
    a probe that failed to execute it after a good report.
16. **Owner-side state (O).** The owner's state folders should lie on a file
    system mounted `nodev`, and the holder makes the records, requests and mail
    folders (in the lead's state folder) with mode 0700; `CheckOwnClone` checks
    their mode and owner. Nothing looks at the mode, owner or content of the
    mailbox and the published folder.
17. **One state root for every lead (Q).** Rule G1 across leads rests on every
    lead on the host using the same state root, and on every source and
    everything above it being Ataniq's. No code checks either: the rules about
    another lead's state know only the state root of their own inputs, and the
    other checks cover links and the lexical and real folder rules. The holder
    has to guarantee both.
18. **The four parent folders (O, Q).** They are only bound folders: nothing
    reads the request folder, writes the mailbox or publishes a coordinator's
    folders. Assertion 11 compares the coordinators' identities as they were
    when the spec was written, so a coordinator made later is not in the list.
19. **Bind descriptors through bubblewrap (Q).** Whether bubblewrap mounts the
    object behind the descriptor that `--bind-fd` and `--ro-bind-fd` are given,
    the folder or file the step's walk found, and closes the descriptor before
    the command inside starts. Nothing here shows either; every plan says so in a
    note.
20. **`--ro-bind-fd` and an `O_PATH` descriptor (Q).** The step keeps an `O_PATH`
    descriptor on each source it walks to. Whether `--ro-bind-fd` takes an
    `O_PATH` descriptor of a regular file is not shown by anything here.
21. **The status of the step through pasta (Q).** A source the step's walk
    refuses is exit 96 of the step, before bubblewrap starts. How pasta passes on
    the exit status of its command is not known here; the harness expects a first
    process that exited before a report, and that has not been observed.
22. **The minimum bubblewrap release (Q).** The harness ends a run at the step
    `tool-versions` when bubblewrap is older than 0.10.0. That release is an
    assumption, the first one taken to have `--bind-fd` and `--ro-bind-fd`, to be
    confirmed at qualification. A bubblewrap without the options is expected to
    refuse the plan's arguments as unknown and to start nothing, which has not
    been observed; the plan has no form that binds a source Ataniq supplies by
    path.

## Qualification

The harness `qualify` launches the generated plan once, for real, on a host a
coordinator chose, and records facts: a result is `ok`, `failed` or `not-checked`,
and no field says that the profile passed. It is opt-in: `make test` does not
reach it, and building it with `make qualification-leadsandbox-build` runs
nothing. bubblewrap and pasta are qualification subjects, not developer tools. The
automated and attended cases (S1 to S20 of the design, section 7.3, and case
S21, which exercises the walk before every start: the harness plants a symbolic
link and an entry of the wrong type at the bind targets in scratch folders,
checks that every start is refused with the path, puts everything back and
launches nothing, and has run only in tests, with temporary folders), the
configuration and the R17 note for S8 are in
[the qualification harness guide](qualification-lead-sandbox.md). Until a
coordinator has run them and the owner has decided on the profile, everything in
this guide is a proposal.

Copyright (c) 2026 Everett C. Haimes III. Licensed under the MIT License;
see [LICENSE](../LICENSE).
