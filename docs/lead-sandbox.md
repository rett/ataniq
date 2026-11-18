# Lead sandbox proposal

This is a **proposal that awaits qualification and the owner's decision on the
profile (design question Q1)**. Nothing here is qualified. Nothing starts a
sandbox in ordinary use or in the tests, and no command uses it yet: a holder
that would launch and supervise a lead in it, and a daemon that would start that
holder, are other work. The scratch-only holder of
[the lead holder guide](lead-holder.md) and the daemon skeleton of
[the lead daemon guide](lead-daemon.md) launch no sandbox. Only the opt-in
qualification harness can launch the generated plan, on a host a coordinator
chose. This guide says what the code generates and what it checks. It does not
say that the sandbox is qualified, safe or supported, and no part of it should
be read that way.

The proposal comes from the lead-hosting design (sections 3 and 7.3), revision 3,
with one change by the owner (R17, nested sandboxes, see
[Nested sandboxes](#nested-sandboxes-owner-decision-r17)). Where the design says
otherwise, R17 wins. The code is in `internal/leadsandbox` (the packages
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
a program, and the checks that touch files (`mountpoints`, `safefile`) work on a
folder the caller opened.

Built, each unqualified:

- `profile`: the runtimes, the fixed inside paths, the host layout of one lead,
  the bind table, the environment allowlist, the never-mounted check and the
  profile digest. It does no I/O.
- `sandboxnet`: the arguments of pasta and the derivation of the sandbox's
  `resolv.conf`. It reads only through a small file-system interface that tests replace.
- `plan`: the whole launch command, built from the profile, and `Verify`, which
  parses a plan back and compares it with the inputs. It starts nothing.
- `mountpoints` and `safefile`: the host-side checks and preparation of the
  folders a lead can write, without following symbolic links.
- `assert`: the ten start-up assertions, the spec and the report line, and the
  checks the probe and the holder make.
- `ataniq-probe`: the static helper that runs inside the sandbox (and, in its
  pass-through mode, between pasta and bubblewrap).
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
  implements them). The daemon skeleton of
  [the lead daemon guide](lead-daemon.md) starts no holder.
- Injection in use: putting text into a running lead's terminal. A provisional
  library exists (see [the lead injection guide](lead-injection.md)), and no
  holder calls it.
- Any use of the exchange mirror and the intake by which a lead's work reaches
  the owner's upstream branches. The plan binds the mirror folder read-only
  and does not make it. `ataniq-intake` (see
  [the lead intake guide](lead-intake.md)) makes a mirror under the state root
  it is given, at the same relative path, `mirrors/<project>.git`, and is run
  by hand. It shares no code with these packages, and the commands it runs
  come from a sandbox template of its own, not from this profile.
- The qualification record store. `assert` checks a record through an interface;
  no store exists.
- A generated or reviewed seccomp filter (see
  [The seccomp hook](#the-seccomp-hook)).

## The launch plan

`plan.Build` returns one command line. Its shape, in order, is:

```text
PASTA ARGS... -- PROBE_HOST pass-fds [--read FD=PATH]... [--write FD=PATH]... -- BWRAP ARGS... -- /ataniq/probe --spec /ataniq/spec/spec.json --report-fd FD -- AGENT ARGV...
```

- pasta gives the sandbox a network namespace (see
  [Network and DNS](#network-and-dns)).
- The pass-through step, the probe binary in its `pass-fds` mode, runs on the
  host between pasta and bubblewrap. It exists because pasta closes every
  descriptor it inherits before it starts its command, so a descriptor the
  launcher opened would never reach bubblewrap. The step opens each named file
  at exactly the descriptor number given, and executes the rest of the line.
  The files are the report channel (always) and the seccomp file (when one is
  named). The launcher opens nothing.
- bubblewrap builds the sandbox from the arguments below.
- The probe inside runs the start-up checks and then executes the agent.

The golden file `internal/leadsandbox/plan/testdata/claude-root-plain.golden`
holds this plan for one fixed set of inputs (a claude lead, identity root, every
optional host row present). Its command, with invented paths, is:

```text
/usr/bin/pasta -f -q --config-net -t none -u none -T none -U none --no-map-gw -- \
  /opt/ataniq/bin/probe pass-fds --write 3=/opt/ataniq/run/arc-lead/report.fifo -- \
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
    --ro-bind /opt/ataniq/resolv/arc-lead/resolv.conf /etc/resolv.conf \
    --dev /dev \
    --proc /proc \
    --tmpfs /tmp \
    --ro-bind /opt/ataniq/bin/probe /ataniq/probe \
    --ro-bind /opt/ataniq/spec/arc-lead /ataniq/spec \
    --ro-bind /opt/agent/claude/2.1.0 /ataniq/agent \
    --ro-bind /opt/ataniq/managed/arc-lead/settings.json /etc/claude-code/managed-settings.json \
    --bind /state/leads/arc-lead/home /home/lead \
    --bind /state/leads/arc-lead/config/claude /home/lead/.claude \
    --ro-bind /opt/ataniq/skills/arc-lead /home/lead/.claude/skills \
    --ro-bind /opt/ataniq/instructions/arc-lead/CLAUDE.md /home/lead/.claude/CLAUDE.md \
    --ro-bind /opt/ataniq/git/arc-lead/config /ataniq/git/config \
    --bind /state/leads/arc-lead/tree /work/demo \
    --ro-bind /state/leads/arc-lead/gitmeta/config /work/demo/.git/config \
    --ro-bind /state/leads/arc-lead/gitmeta/hooks /work/demo/.git/hooks \
    --ro-bind /state/leads/arc-lead/gitmeta/info /work/demo/.git/info \
    --ro-bind /state/mirrors/demo.git /ataniq/mirror/demo.git \
    --ro-bind /opt/ataniq/qargi /ataniq/qargi \
    --ro-bind /opt/ataniq/assignment/demo /ataniq/assignment \
    --ro-bind /opt/ataniq/projection/demo /ataniq/projection \
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
never used, so the plan says exactly what is mounted. A row of the host is bound
from the real path the caller resolved (in the golden files, a host without
links, so each is its own path). Every plan also carries
notes (the golden files list them) that state the choices below and what the
plan does not show.

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
its own, or that lies in a folder the lead can write, in a never-mounted path or
above a bind source; host sources that are missing for a row of the host it
emits (`/usr`, the `/etc` files: the plan binds the real path the holder
resolved, see [the clone rules](#the-clone-rules-and-rule-g1)) or given for
another row; a working folder outside the tree and the home; an operand of
bubblewrap that starts with `--`; and a command line above a fixed cap. It
then runs `Verify` on its own result. `Verify` is written apart from the code
that builds the arguments: it parses the bubblewrap arguments back and compares
each part with the inputs. A plan that verifies says the text is the one the
inputs describe. It does not say what bubblewrap, pasta or the kernel will do
with it.

## Nested sandboxes (owner decision R17)

The owner has decided that nested sandboxes are allowed for now. So
`--disable-userns` is deliberately not passed, and neither is
`--assert-userns-disabled`, and the ten start-up assertions include none about
them. The reason is that members, Codex included, start their own sandboxes
inside the lead's, which they cannot do where user namespaces are disabled.

The cost, stated plainly: user namespaces stay available inside the lead's
sandbox. Code in the sandbox can create them, and through them reach kernel code
that is reachable only that way. That is a larger kernel surface than the design's
first profile exposed. Qualification must cover it (case S8, rewritten for R17 in
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
| `/ataniq/git/config` | the git identity file Ataniq owns | read-only |
| `/work/<project>` | `<state>/leads/<role>/tree`, the lead's own clone | read-write |
| `/work/<project>/.git/config`, `.git/hooks`, `.git/info` | Ataniq's copy, `<state>/leads/<role>/gitmeta` | read-only |
| extra placeholders in the tree, if the inputs name any | a host path Ataniq owns | read-only |
| `/ataniq/mirror/<project>.git` | `<state>/mirrors/<project>.git` | read-only |
| `/ataniq/qargi` | the Qargi install | read-only |
| the Qargi records, if the inputs name them | `<state>/leads/<role>/records` (the only host path the profile accepts for them), at the inside path the inputs give | read-write |
| `/ataniq/assignment`, `/ataniq/projection` | folders written outside the sandbox | read-only |

The managed-settings path is fixed for claude, `/etc/claude-code/managed-settings.json`.
The design names no path for codex's managed configuration, so for codex the
caller gives the path (it must lie under `/etc`), and the golden file uses an
invented one.

**What is never mounted.** The inputs list host paths that no bind source may
equal, lie below or lie above: the owner's checkout, the real provider folders,
`.ssh`, and whatever else the caller names (at least one is required). The profile
adds the other runtime's config folder, and refuses a source in or above another
lead's state or another project's mirror folder. The host's `/home`, the real
provider folders and the owner's checkout are not in the table at all: the lead's
home is its own folder under the state root. The check is on the text of the
paths, so a link could hide a folder: `mountpoints.ResolveSources` runs the same
checks again on the real paths, and refuses a source Ataniq supplies that has a
link on it (it must be given as its real path).

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
`/ataniq/assignment`, `/ataniq/projection`, `/ataniq/git/config`, `/tmp` and
`/etc/resolv.conf`. The working folder is the tree by default; it may be the
home or a folder below either.

## The clone rules and rule G1

**The tree is the lead's own clone.** It is a full clone that Ataniq made for the
lead, bound whole and read-write. `mountpoints.CheckOwnClone` refuses a worktree,
a submodule, a folder below another repository, a tree whose `.git` names
alternates, and a tree, home or config folder that is not a 0700 folder of the
caller's user.

**Rule G1: no bind source in a folder a sandbox can write.** bubblewrap resolves
a bind source on the host, outside every sandbox, and follows a symbolic link
there. A source that lies in a folder the lead can write could be replaced by a
link that brings a folder of the owner into the sandbox at the next start. So no
row of the bind table has a source in or below a host folder the lead can write
(the home, the config folder, the tree and the records folder). The profile
refuses a table that breaks the rule, `plan.Build` checks it again, and
`plan.Verify` reports a mount that does. The rule holds across leads too, as long
as every lead on the host uses the same state root (nothing here checks that; it
is the holder's obligation, listed in `internal/leadsandbox/mountpoints/doc.go`):
every folder a lead writes lies in that lead's own state folder, so the rules
about another lead's state keep the read-only sources of one lead out of the
reach of every other. That is why the records folder, when the inputs name one, must be
`<state>/leads/<role>/records` (`profile.Layout.Records`) and any other host path
is refused, and why the git metadata folder must be `<state>/leads/<role>/gitmeta`
(`profile.Layout.GitMeta`).

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
the skills folder and the instruction file of the config folder, the inside path
of the records folder (when the inputs name one), and any extra placeholder lie
inside folders the lead writes. bubblewrap would make a missing
one itself, in a folder where a planted link could redirect it. So Ataniq makes
them before the lead's first run (`mountpoints.Prepare`, which refuses unless the
caller states that no lead has run there yet, and never replaces what exists), and
lists the tree placeholders (the ones outside `.git`) in the git exclude file of
the copy, as one anchored line each. A placeholder whose path holds a character
git treats as a pattern is refused.

**Checks before every start.** `mountpoints.Check` finds missing or wrong mount
points and a copy that is incomplete or has no exclude line for a placeholder;
`ResolveSources` resolves each bind source to its real path and runs the
never-mounted checks again; `CheckBindSources` opens the path bound for every row
from the root of the file system with `openat2`, without following a symbolic
link on any component, and refuses a path that has one, naming the row by its
inside path. They run in that order, right before the launch, after the previous
unit is gone and its control group is empty (the sandbox's own process tree does
not show that no lead process remains, because nested sandboxes are allowed).

**The path that is bound is the path that is checked.** bubblewrap binds the path
the plan names and follows a link in it, so a source that is checked and a
different one that is bound is a check that did not happen. For every row:

- a source Ataniq supplies (every row but the host's own) must be given as its
  real path. `ResolveSources` refuses a source whose real path differs from its
  lexical one, a link anywhere on it, naming the row and the real path; the plan
  binds the source as given, and `CheckBindSources` opens that path as it is, with
  no link allowed on it;
- a row of the host (`/usr`, `/etc/hosts`, `/etc/localtime` and the like), whose
  own name may be a link of the host that no lead writes, is bound from the real
  path that `ResolveSources` reported. The holder gives it to the plan
  (`plan.Options.HostSources`, which `plan.Build` requires for every host row it
  emits and `plan.Verify` checks), and `CheckBindSources` opens that real path.

**What the read-only binds do not protect against.** They are defence in depth.
A lead can rename `.git` (renaming the parent of a mount point is allowed) and
build another, and neither `Check` nor `CheckOwnClone` can tell a rebuilt `.git`
from the original. The primary rule is that no owner-side tool uses a lead's tree
(the lead-hosting design, section 3.3); nothing here enforces it. The checks are
made at one moment: a lead that runs between a check and the start can change what
it checked. The exclude check is textual, and a `.gitignore` of the tree can make
git show a placeholder as untracked, which is cosmetic. The records folder lies
in the lead's state folder, so the lookup from the state root covers its path,
but its mode and owner are not checked by `CheckOwnClone`: the holder makes it
0700 and checks it.

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

`ataniq-probe` is a static program, built without cgo, with two modes. It exits 97
or 98 and otherwise replaces itself with the command it was given (or, when
`setsid` is refused, runs it as a child and exits with its status, or 128 plus
the signal number; see below).

**Pass-through mode** (`pass-fds [--read FD=PATH]... [--write FD=PATH]... --
COMMAND ARGV...`) runs on the host between pasta and bubblewrap. FD is 3 to 1023
and at most 8 files are given. A `--read` file is opened read-only and must be a
regular file; a `--write` file is opened write-only, appending, never created and
never truncated, and must be a regular file or a FIFO. Links are not followed at
the last component. A FIFO that no process has open for reading is refused at
once, so the holder opens its reading end first. Each file is placed at exactly
its number, every other descriptor above 2 is made close-on-exec, and the command
is executed. The checks on a path are not atomic with the open, so every path must
lie in a folder only the holder can write. A number that is one of the Go
runtime's own descriptors is refused.

**Inside mode** (`--spec PATH --report-fd N -- AGENT ARGV...`) runs inside the
sandbox as the program the plan starts last. In order, it marks the report
descriptor close-on-exec; checks that the agent is an executable regular file;
reads the spec (a regular file of at most 256 KiB, decoded strictly); runs
assertions 1 to 8 against the real system; writes the report; judges it; gives the
agent a session; and executes the agent.

**The report channel contract.** The probe writes exactly one line, the report, to
the report descriptor, on success and on failure, and no other byte, then or
later. The holder takes one accepted line as the signal that the probe has
reported. It does not wait for end-of-file, because bubblewrap's own processes may
keep a copy of the descriptor open, so no end-of-file may come while the sandbox
lives. A second line or any further byte after the line is a failure of the start.
A line seen anywhere else, the terminal in particular, is display only: the lead
writes to the same terminal after the exec and can print the same prefix, and the
report's digest is readable by the lead, so neither authenticates anything.

**Exit codes.** 97: the inside checks did not pass; the agent is not started, and
standard error lists the problems (display only). 98: a usage error, or a failure
to do what the command is for. A refusal before the report (a usage error, a
report descriptor that is not open, an agent that cannot be executed, a spec that
is refused) writes nothing to the channel, and the holder sees the exit status
alone. A file the kernel does not accept as a program fails after a good report
with the same 98, which a holder cannot tell from an agent that started and exited
98.

**Session and controlling terminal.** When descriptor 0 is a terminal, the probe
gives the agent a new session with that terminal as its controlling terminal
(`setsid`, then `TIOCSCTTY`; variant B of the spike). If `setsid` is refused
because the probe leads a process group, it runs the agent as a child in a new
session, forwards `SIGTERM`, `SIGINT`, `SIGHUP` and `SIGQUIT`, waits, and exits
with the child's status. When descriptor 0 is not a terminal, it says so on
standard error and skips the step. It never issues `TIOCSTI`. A failure to get the
terminal when there is one is exit 98.

## The start-up assertions

The ten assertions of the design (section 3.8) are the table in `assert`. The
probe makes the inside half of each that has one, once each, before it executes
the agent. The holder would make the outside half and the others
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
| 7 | The tree is the lead's own clone, and its `.git/config`, `.git/hooks` and `.git/info` are not writable inside. | inside |
| 8 | The managed-settings file is present and read-only; for claude it contains the guard hook. | inside |
| 9 | The role lock is held, and lingering is on. | outside |
| 10 | A current qualification record exists for this runtime, version and profile digest. | outside |

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
- **Assertion 7** shows that each of the three paths is on a read-only mount, that
  no read-write mount lies at or below it, and that a write is refused. The probe
  checks that `.git` is a folder; that the tree is the lead's own clone is the
  check of `mountpoints`. Where the kernel gives no mount ID (before Linux 5.8)
  the result is not-checked.
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
caller opens a folder handle (`OpenRoot`) once, on a path it owns, and every
access goes through `openat2` with `RESOLVE_BENEATH`, `RESOLVE_NO_SYMLINKS`,
`RESOLVE_NO_MAGICLINKS` and `RESOLVE_NO_XDEV`, relative to that handle. It offers
reads (a regular file up to a cap, a folder listing up to a count, `Lstat`) and
the writes the mount-point work needs (`MkdirAll`, `WriteFileExclusive` which only
creates, and `AppendFile` for an existing regular file), `Sync` (fsync of a file
or folder) and `RenameNoReplace` (one name to another in the handle's folder,
which never replaces), and no replacing rename, remove or truncate.

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
it), `OpenRoot` fails with `ErrUnsupported`; there is no fallback to a path-based
open.

**What the caller must still do.** Pass a path whose parents it owns: only the last
component is checked. Keep the owner's state folders on a file system mounted
`nodev`, because a device node is opened before `fstat` can refuse it. Check again,
right before use, a path it will later use by name (a bind): the end check of
`MkdirAll` holds only at that moment, and folders made after a move stay where the
moved folder went. Expect that a raw error number (`EACCES`, `ENXIO`, `EPERM`,
`EAGAIN` for a leased file) can come instead of a package error from the open of a
device or a leased file. The package does not protect against changes made by
someone with owner rights to the folder behind the handle.

## The profile digest

`profile.Digest` is the SHA-256 of a canonical, versioned encoding. It covers the
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
   available inside, a larger kernel surface than the first profile. Case S8
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
    [the lead intake guide](lead-intake.md) says what the built intake does with
    a lead's tree.
14. **Checks at one moment (Q).** The mount-point and source checks are made right
    before the start; nothing here closes the gap between a check and the launch
    except an empty control group, which the holder has to prove.
15. **The agent's exit 98 (Q).** A holder cannot tell an agent that exited 98 from
    a probe that failed to execute it after a good report.
16. **Owner-side state (O).** The owner's state folders should lie on a file
    system mounted `nodev`, and the holder makes the records folder (in the
    lead's state folder) with mode 0700 and checks its mode and owner.
17. **One state root for every lead (Q).** Rule G1 across leads rests on every
    lead on the host using the same state root, and on every source and
    everything above it being Ataniq's. No code checks either: the rules about
    another lead's state know only the state root of their own inputs, and the
    other checks cover links and the lexical and real folder rules. The holder
    has to guarantee both.

## Qualification

The harness `qualify` launches the generated plan once, for real, on a host a
coordinator chose, and records facts: a result is `ok`, `failed` or `not-checked`,
and no field says that the profile passed. It is opt-in: `make test` does not
reach it, and building it with `make qualification-leadsandbox-build` runs
nothing. bubblewrap and pasta are qualification subjects, not developer tools. The
automated and attended cases (S1 to S20 of the design, section 7.3), the
configuration and the R17 note for S8 are in
[the qualification harness guide](qualification-lead-sandbox.md). Until a
coordinator has run them and the owner has decided on the profile, everything in
this guide is a proposal.

Copyright (c) 2026 Everett C. Haimes III. Licensed under the MIT License;
see [LICENSE](../LICENSE).
