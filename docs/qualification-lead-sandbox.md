# Lead sandbox qualification harness

This is qualification infrastructure for a proposal: the lead sandbox profile of
the lead-hosting design, which awaits the owner's decision on the profile. It
launches the generated launch plan once, for real, on a host a coordinator
chose, and records facts. It does not qualify, approve or support anything, and
no part of its output says that the profile passed, is qualified or is
supported. A reader draws conclusions from the facts and the attended cases
below; the owner decides on the profile.

The harness is for a coordinator. Nobody runs it as part of a checkout's checks.
`make test` does not reach a real launch: the harness's tests use a fake
launcher, and the tests of the real launcher start only the test binary itself,
under a pseudo-terminal (to check its terminal, session, process group,
environment and signal handling) and as the stand-in for a tool asked for its
version. No test starts pasta, bubblewrap, the probe or a namespace. See [the host manual](qualification-host.md) for the
conventions this follows: facts, status and errno; no pass or support field;
explicit configuration; evidence in a private campaign folder; subjects that are
not developer prerequisites.

## What it is and is not

The command `qualify` (in `qualification/leadsandbox/cmd/qualify`, logic in
`qualification/leadsandbox/internal/harness`) does, in order, for one attempt:

1. validates the configuration and looks at the host for what it names;
2. makes an attempt folder with a random name under the campaign folder;
3. records the versions of the tools (each question has a time limit, and does
   not wait for the output of a process the tool left behind longer than a
   second after the tool has ended; such a case is recorded in the step);
4. checks that the lead's tree is its own full clone, makes the mount points
   and Ataniq's copy of the tree's git files (the git metadata folder, see
   [rule G1](#rule-g1-and-git-inside-the-sandbox)) when the configuration says
   no lead has run, and checks them;
5. derives the sandbox's `resolv.conf` from the host and writes it into a folder
   of its own;
6. resolves every bind source to its real path and puts the real paths into the
   profile inputs;
7. makes the report channel (a FIFO in a folder of the attempt that holds nothing
   else, its reader opened first) and starts the canary (a loopback listener
   and an abstract socket that count connections);
8. builds the plan and checks it with the plan package's own verifier, collects
   the identities (device and inode) of the owner's and the holder's paths and
   of their ancestors, and writes the probe spec with them;
9. checks the mount points and sources again, right before the launch, and
   then, as a step of its own (`check-bind-sources`), opens every bind source of
   the plan from the root of the file system without following a symbolic link;
   a link on any component of a source refuses the launch, naming it;
10. launches pasta, bubblewrap, the probe and a scratch command through a
    launcher (a session and process group of its own, with a pseudo-terminal as
    its standard streams and no controlling terminal, as a holder under a unit
    would run it), reads the probe's report line from the channel with a time limit
    (one line, taken as the start without waiting for end-of-file), and waits
    for the exit;
11. runs the checks that can only be made outside (the canary's count, the
    sysctl, and the checks that need a role lock, lingering or a qualification
    record, which have no source here and are recorded as not-checked);
12. stops anything left (SIGTERM, wait, SIGKILL, with the stop-rule constants of
    the assert package), asks the report channel whether any byte followed the
    line (a failed step `report-trailing`, and `report_trailing_bytes` in
    `report.json`), and closes what it opened;
13. writes the evidence.

It does not:

- create a systemd unit. The cases that need a unit (S9, the stop rule as the
  design states it for a unit and its control group, resource limits) belong to
  the holder's qualification. The harness stops its own process group only.
- review a seccomp filter, check that the probe is a static executable, narrow
  outbound network access, or hold a role lock, lingering state or a
  qualification record store.
- run a real `claude` or `codex`. The command inside is a scratch command.
- change anything in the owner's checkout or real provider folders. It never
  reads them, and it refuses a source that touches a never-mounted path.

Nested user namespaces stay available (owner decision R17): the plan passes
neither `--disable-userns` nor `--assert-userns-disabled`, which is a larger
kernel surface than with them disabled. See [the R17 note](#the-r17-note-for-s8).

## Prerequisites

bubblewrap and pasta are qualification subjects, not developer tools. The
bootstrap installs neither and the doctor does not check them. Install the
subject versions on the host the coordinator chose, record where they are, and
name them in the configuration with absolute paths. Each must be a regular file
that is not group- or other-writable; the harness records the SHA-256 of each,
and of the probe, in `summary.json`. The probe is built from this checkout:

```sh
make doctor
make qualification-leadsandbox-build
```

This builds the static Linux/amd64 probe `build/ataniq-probe` (from
`cmd/ataniq-probe`) and the harness `build/qualification-leadsandbox`, through
the same offline Go helper as the other targets. The target is defined in
[the Makefile](../Makefile). Building runs nothing.

The host also needs a kernel that lets the user create the namespaces the plan
asks for, pseudo-terminals (`/dev/ptmx`), and openat2 (Linux 5.6 or newer, as
the safe-file helpers require). The harness does not check whether the kernel
policy of the host allows the namespaces: a launch that bubblewrap or pasta
refuses is recorded as it happened.

## Prepare the scratch folders

Everything below is created by the coordinator, on the chosen host, outside every
repository. Paths here are illustrative; none is discovered or defaulted from the
caller's environment or home.

- **Campaign folder**, for example `/owned/qualification/campaign`: mode 0700,
  owned by the caller, a real path, empty or holding earlier attempts. Evidence
  goes here and nowhere else. Do not put credentials or unrelated state in it.
  It must not be, lie below or contain a never-mounted path, and must not lie in
  or contain the state root, any source folder or file of the profile (the agent
  install, managed settings, skills, instruction file, git identity, Qargi
  install and records, assignment and projection) or the probe, because two
  bind sources of the harness lie below it. Put it outside every never-mounted
  folder rather than shortening the never-mounted list: a configuration that
  lists a whole home folder as never-mounted cannot keep its campaign inside it.
- **State root**, for example `/owned/qualification/state`: a real path holding
  the lead's layout, all folders mode 0700:

  ```text
  leads/<role>/home
  leads/<role>/config/<runtime>
  leads/<role>/tree
  mirrors/<project>.git
  ```

  The run makes one more folder, `leads/<role>/gitmeta`, when the configuration
  says no lead has run. It is always that folder: the configuration has no key
  for another one, because a folder elsewhere could lie beside or above a
  folder the lead writes, and nothing would look at its parents. Do not make it
  by hand: its parent (`leads/<role>`) must exist and be the caller's, and the
  folder itself must not exist before the first run.

  The tree is a full clone (a `.git` folder, not a worktree, with no
  alternates), not below another repository. Create it with an ordinary
  `git clone` outside any sandbox. Role and project names are 1 to 63
  lower-case letters, digits and hyphens, starting with a letter or digit. Use
  a scratch state root: the harness never reads a real lead's folders.
- **Sources**: the agent's install folder for the pinned version; the managed
  settings file and its SHA-256 (for claude it must contain the text named by
  `managed_marker`); the lead skills folder; the instruction file; the git
  identity file; the Qargi install; the assignment and projection folders.
  Optional: the lead's working Qargi records, named by the path they appear at
  inside (`sources.qargi_records_inside`); their folder is always
  `leads/<role>/records` in the state root, which you make (mode 0700, owned by
  the caller, a real path with no symbolic link in it) before the run, and
  there is no key for another place (rule G1: a folder a lead writes elsewhere
  could be, or lie above, a source of another lead). Also optional: extra tree
  placeholders, and a seccomp file with its SHA-256.
- **Never-mounted paths**: the owner's checkout, the real provider folders, and
  `.ssh`, as real paths. Give the paths even if they do not exist on the host.
  The other runtime's config folder is added by the profile.
- **Executables**: pasta, bubblewrap and the probe, as above.

A seccomp file is refused unless the configuration states `"reviewed_bpf": true`
in the `seccomp` object. That statement is the coordinator's: nothing in the
harness reviews a filter, and none is generated.

## The configuration

One JSON file, mode 0600, owned by the caller, at most 64 KiB, valid UTF-8,
decoded strictly: an unknown key, a key in another case, a key written twice and
trailing data are refused. Every path is absolute and clean. Illustrative
example for a claude lead:

```json
{
  "version": 1,
  "campaign": "/owned/qualification/campaign",
  "coordinator_reviewed": true,
  "no_lead_has_run": true,
  "executables": {
    "pasta": "/owned/qualification/bin/pasta",
    "bwrap": "/owned/qualification/bin/bwrap",
    "probe": "/owned/qualification/bin/ataniq-probe"
  },
  "runtime": "claude",
  "runtime_version": "2.1.0",
  "state": {
    "root": "/owned/qualification/state",
    "role": "qual-lead",
    "project": "qualproj"
  },
  "sources": {
    "agent_install": "/owned/qualification/agent",
    "managed_settings": "/owned/qualification/managed/managed-settings.json",
    "managed_settings_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
    "managed_marker": "qargi-guard",
    "lead_skills": "/owned/qualification/skills",
    "instruction_file": "/owned/qualification/instructions/CLAUDE.md",
    "git_identity_file": "/owned/qualification/git/config",
    "qargi_install": "/owned/qualification/qargi",
    "assignment_dir": "/owned/qualification/assignment",
    "projection_dir": "/owned/qualification/projection"
  },
  "never_mounted": [
    "/owned/owner-checkout",
    "/owned/owner-home/.claude",
    "/owned/owner-home/.codex",
    "/owned/owner-home/.ssh"
  ],
  "scratch_command": ["/bin/sh", "-c", "echo ataniq-leadsandbox-scratch-marker"],
  "provider_host": "api.example.test",
  "terminal": {
    "term": "xterm-256color",
    "lang": "C.UTF-8",
    "rows": 24,
    "cols": 80
  },
  "time_limit_seconds": 120,
  "cases": ["S1", "S2", "S3", "S4", "S7", "S11", "G1"]
}
```

The digest shown is a placeholder: the harness compares the real SHA-256 of the
managed-settings file with it and refuses a difference.

| Key | Meaning |
| --- | --- |
| `version` | Format version, 1. |
| `campaign` | The private campaign folder; must exist. |
| `coordinator_reviewed` | The statement that a coordinator reviewed this configuration and the plan it gives. Without `true`, `--run` is refused. |
| `no_lead_has_run` | The statement that no lead has ever run in these scratch folders. Only then does a run make the mount points (`Prepare`); otherwise it checks them and fails when they are missing. Never set it for a folder a lead has run in. |
| `executables` | `pasta`, `bwrap` and `probe`: absolute, regular, not group- or other-writable. Three different files. |
| `runtime`, `runtime_version` | `claude` or `codex`, and the pinned version string. |
| `state` | The scratch state root, the role and the project. |
| `identity`, `owner_user` | `root` (the default) or `owner`, which needs the owner's login name; `owner_user` is given for `owner` only. |
| `sources` | Every host source of the profile (see above). There is no key for the folder that holds the copy of the tree's `.git/config`, `.git/hooks` and `.git/info`: it is `<state root>/leads/<role>/gitmeta`, and a `git_meta` key is refused as an unknown key. `managed_settings_inside` is required for codex, whose managed-configuration path the design does not name, and must be absent or the provider's own path for claude. `seccomp` is the optional object with `file`, `sha256` and `reviewed_bpf`. `extra_placeholders` lists extra tree bind targets (`rel`, `source`, `dir`). |
| `never_mounted` | Host paths no bind source may equal, lie below or lie above. At least one. |
| `scratch_command` | The command run inside, as an absolute argv. Absent: `/bin/sh -c 'echo ataniq-leadsandbox-scratch-marker'`. |
| `provider_host` | The host name the DNS assertion resolves inside. Not an address. |
| `terminal` | `term` (required), optional `colorterm`, `lang`, `tz`, and the size of the pseudo-terminal (`rows`, `cols`). These are the values the profile's environment takes from a terminal. |
| `time_limit_seconds` | 1 to 3600. Bounds the wait for the report line and the wait for the exit, counted from the launch. |
| `cases` | The automated cases to record, from `S1`, `S2`, `S3`, `S4`, `S7`, `S11`, `G1`. |

The harness passes the launcher an empty environment: no variable of the
caller's environment reaches pasta, bubblewrap or the probe, and the environment
inside is the profile's allowlist. The report channel uses descriptor 4, and the
seccomp file, when named, descriptor 3.

## The dry run

```sh
build/qualification-leadsandbox --config /owned/qualification/lead.json
```

Without `--run` the command validates the configuration, looks at the host,
makes an attempt folder, runs the read-only checks, derives and writes the
resolv.conf, resolves the sources, builds the plan, verifies it, prints it with
its profile digest, and starts nothing: no process, no listener, no FIFO, no
check of the bind sources without following links (that is a step of the run),
and no write into the scratch folders (the mount points and the git metadata
folder are not made; when the configuration states that no lead has run, the
sources in the folder do not exist yet, and a note in `summary.json` says so). It writes
`plan.json` and `summary.json` into the attempt folder, and the harness's own
`bound` folder. When the configuration states that no lead has run, the dry run
skips the mount-point check, because the run itself would make them. The
coordinator reads the plan before any run: the argument vector, the binds with
their sources, the descriptors and the notes (the R17 note, the seccomp state and
the optional rows left out).

## The real run

```sh
build/qualification-leadsandbox --config /owned/qualification/lead.json --run
```

Two gates stand before a launch. The flag `--run`, and `"coordinator_reviewed":
true` in the configuration. With `--run` the command also refuses when standard
input is not a terminal, unless `--unattended` is given. A refusal creates
nothing.

Run it on a host the owner chose, as an unprivileged user, with fake credentials
and no real provider login. The scratch command ends the run: the default one
prints a marker and exits. Press the interrupt key to end a run early; the
harness stops the process group and writes the evidence.

Exit status:

| Status | Meaning |
| --- | --- |
| 0 | Every step did its work. The probe's own exit status and report are facts in the evidence, not a step failure. |
| 3 | A step failed, timed out or was interrupted. The evidence is still written. |
| 2 | Usage error, refused configuration or host, or a closed gate. Nothing was written. |
| 4 | The attempt folder or the evidence could not be written. |

The status says whether the harness did what it set out to do. It never says
whether the profile is acceptable.

## The evidence

The attempt folder is `<campaign>/<random name>`, mode 0700, made exclusively.
Every file is mode 0600, created exclusively (never replaced), written, and
synced with its folder. The folder is private: it holds host paths, the plan and
the terminal output. Do not copy it into a repository.

| File | Content |
| --- | --- |
| `plan.json` | The plan: argument vector, the names of the environment allowlist, the mounts (kind, inside path, source, whether the source lies below a folder the lead can write, optional), the descriptors the pass-through step opens, the notes, and the profile digest. |
| `spec.json` | The probe's spec, as written for it (also bound read-only inside, in `bound/spec`). |
| `report.json` | What the probe reported: its results with their statuses, or what came instead (`line-read`, `refused-line` with the parser's reason and an escaped excerpt, `time-limit`, `exited-before-report`, `interrupted`, `not-launched`). After a line was read, `report_trailing_bytes` says whether the channel held any byte after it when it was asked (`true` is a failure of the start and of the step `report-trailing`). |
| `outside.json` | The results of the checks the harness makes outside, and the canary's connection counts. |
| `terminal.log` | The first 1 MiB of what the processes wrote to the pseudo-terminal, raw and unescaped; the rest is counted in `summary.json`. The command inside controls it: it is display only, never evidence of a check (a report line seen there authenticates nothing, and `marker_seen` only says the text is there). It may hold escape sequences: read it with `cat -v`. |
| `summary.json` | The versions (kernel release, bubblewrap, pasta), the SHA-256 of each executable, every step with its start, end, status and error text, the exit status (when the first process was still there at the time limit, `exit.exited` is false and `exit.after_stop` says how the stop ended it: its code, or the signal), how the stop went, the canary's hit counts, the terminal counts, the resolv.conf derivation, notes about sources whose real path differs from their name, `bind_sources` (every bind source of the plan with the facts of rule G1: see case G1), and the requested cases with the results each speaks to. |
| `bound/` | What the plan binds from the harness: the derived `resolv.conf` and the spec folder. |

A step status is `ok`, `failed`, `timeout`, `interrupted` or `skipped`. The report
channel's FIFO and its folder exist only during the run. The summary is written last; its own
write is not one of its steps.

No file has a field that says the profile passed, is qualified or is supported,
and a test checks that no key of the evidence contains such a word. A result is
`ok`, `failed` or `not-checked`, with a detail. A `not-checked` result is not an
`ok` one: it says the check could not be made, and why.

## Rule G1 and git inside the sandbox

Rule G1: no bind source has a component that a sandbox can write, and every bind
source is checked, before a start, without following a symbolic link.
bubblewrap resolves a bind source on the host, outside every sandbox, and
follows a link there, so a link that a lead planted in a folder it writes would
bring a file or folder of the owner into the next start. The tree's
`.git/config`, `.git/hooks` and `.git/info` are bound read-only over the tree, so
their sources are not the tree's own files: they are the entries of the git
metadata folder, which lies outside the lead's home, config folder and tree, and
which the lead cannot write. The run fills it once, before the lead's first run
(the configuration states `no_lead_has_run`): `config` is a copy of the clone's
`.git/config`, `hooks` is empty, and `info` holds a copy of the clone's
`.git/info` with one anchored line in `exclude` for each tree placeholder. The
mount points in the tree stay and are checked as before; they are only targets.
A later start does not fill the folder again: it checks it, resolves the
sources, and opens each source, last of all, with `check-bind-sources`. A row of
the host whose own name is a link (`/etc/localtime` often is) is checked by its
real path, because the plan cannot name another and no lead writes the host's
folders; the evidence names it as `checked_path`. The check says what the path was at that moment: a lead that could write above a source could change it
afterwards, which is why every source and everything above it must be the
caller's.

What changes for git inside the sandbox: it reads Ataniq's copy of `config` and
`info`, which it cannot change (they are read-only binds of files the lead cannot
write), and the hooks folder is empty, so no hook runs. A change to the clone's
git configuration is made outside, in the copy, not by the lead. The lead can
still rename `.git` in its tree and build another: the binds keep their targets
in the old one, the start-up check of the mount points refuses a link at them,
and the rule that no owner-side tool uses a lead's tree is not enforced here.

## Cases

The design (section 7.3) lists the sandbox cases. The harness records facts for
the automated ones; the others are attended, and the coordinator makes and
records them by hand. The harness passes no input to the sandbox: it only reads
the pseudo-terminal, so a scratch command that waits for the keyboard (a shell,
for example) cannot be driven from it and is stopped at the time limit. Run an
attended case either as a non-interactive scratch command (a command in
`scratch_command` that does the steps and prints what happened; it runs inside
the sandbox, so its program and files must be reachable there, in the lead's
tree for example; its output goes to `terminal.log` as display only), or in the holder's own qualification.

### Automated cases

| Case | What the harness records |
| --- | --- |
| S1 | The probe's `holder-hidden` and `owner-paths-absent` results (it looks for the holder's paths, the owner's checkout and provider folders, and the other runtime's config folder inside), and the harness's check that no bind source touches a path the harness owns. |
| S2 | The probe's dial of a host loopback port and abstract socket, and the canary's counts, which do not depend on the probe's judgement. |
| S3 | The probe's comparison of its environment with the allowlist (plus `PWD`, which bubblewrap sets). |
| S4 | The probe's resolution of the provider host, and this run's resolv.conf derivation: its source, the nameservers kept and the loopback ones dropped. A derivation refused for lack of any non-loopback server ends the run before a launch, with the reason in the step; that is the second half of S4. |
| S7 | The sysctl `dev.tty.legacy_tiocsti` as the probe and the harness read it, or the seccomp filter the probe finds loaded. The harness never issues TIOCSTI. |
| S11 | Through the record interface: no store exists here, so the result is `not-checked` with "no qualification record store to ask". The stale-record start refusal belongs to the holder. |
| G1 | Rule G1 (see [Rule G1 and git inside the sandbox](#rule-g1-and-git-inside-the-sandbox)); not a case of the design's table. In `summary.json`, `bind_sources` lists every bind source of the plan, in the order of its mounts, with `no_symbolic_link_component`: the fact, from `mountpoints.CheckBindSources` right before the launch, that the path was opened from the root of the file system without following a link and no component of it, the last one included, is a link (`false` with the reason when one is, absent when the check could not say). `checked_path` is set for a row of the host whose own name is a link: the real path was checked. `links_checked` says the step `check-bind-sources` ran; without it the list is empty and nothing is claimed. `no_source_in_read_write_source` is the fact, from `plan.Verify`, that no bind source lies in or below the source of a read-write bind. The case's results are `bind-sources-no-symbolic-link` and `no-bind-source-in-read-write-source`, each `ok`, `failed` or `not-checked`. The attended variant is [below](#case-g1-attended-variant). |

### Attended cases

For each case below, record the date, the plan digest and attempt name, the
versions from `summary.json`, the exact command, what happened (output and exit
status), and a screenshot or transcript when it helps. Keep it in the campaign
folder. The expected result is the design's; a different result is a finding to
record, not a failure of the harness.

| Case | What to do | What to record |
| --- | --- | --- |
| S5 | In a clone of this repository inside the sandbox: run the bootstrap, the doctor and one test target; edit and commit a file; run Qargi's lead flow verbs; start claude in the tree with the seeded memory and the read-only instruction file. | Each command's result, the author of the commit, whether the memory and instructions loaded, and the environment variables each tool needed (the list that settles the allowlist). |
| S6 | With a reviewed seccomp filter named (and `"reviewed_bpf": true`), run one prompt per agent. | The filter's SHA-256, whether each agent reached its prompt and answered, and the probe's seccomp lines. |
| S8 | See [the R17 note](#the-r17-note-for-s8). | See the note. |
| S9 | Belongs to the holder's qualification (it needs a unit and its control group): stop the unit while a codex daemon and a background task run. | The control group's content over the stop time, and the sockets left in the host's `/tmp`. |
| S10 | Take the manifest (names, sizes and times, never contents) of the real provider config before and after a full session, with a real login on a disposable host. | The two manifests and their difference. |
| S12 | Turn Remote Control on inside the sandbox, with the managed setting on and off. | Whether it works with the setting on and whether it can be turned on with the setting off. |
| S13 | Start two leads with two state roots and configurations. | Whether each has its own `/tmp`, home, clone and codex daemon, and whether either sees the other's. |
| S14 | Connect to a service bound to all interfaces on the host's LAN address, and to one LAN address. | What is reachable, to compare with the network policy the owner chose. |
| S15 | Sign in inside the sandbox: the pasted-code flow for claude, the device-code flow for codex. | Whether each works and which files change, and that only the lead's config folder is written. |
| S16 | From inside, try to write `.git/config`, a hook, `.git/info`, `.claude/settings.json`, `.mcp.json`, `.codex/config.toml` and `.envrc`; rename `.git` and build another; create a lead branch named `develop`, a lead tag and an oversized object store. Then run the owner-side steps. | Each attempt's result, what the mirror's `refs/heads` and `refs/tags` hold afterwards, and whether anything the lead wrote ran outside the sandbox. |
| S17 | Replace the conversation file, the state file, the read-back and a working Qargi record with symbolic links to an outside file and with a file over the size cap; plant a symbolic link at a bind target in the config folder, and start again. | Each owner-side read's result, whether anything outside was disclosed or written, and whether the start was refused (the harness's `prepare` or `check` step must fail on a planted link). |
| S18 | From inside, remove the guard hook, add an allow rule and raise the permission mode in the lead's own config; restart. Repeat on an account with server-delivered policy, if one is available. | Whether the managed settings still apply and the guard hook still runs, and any policy source that outranks the file. |
| S19 | A lead commits each execution-config path of the design's list; run the documented intake up to the sign-off, and a plain pull in an owner checkout. | Which paths were marked, whether anything the lead wrote ran outside a sandbox, and whether the upstream branch moved without the sign-off. |
| S20 | Run codex with `danger-full-access` and the `untrusted` approval policy inside the sandbox, and ask for a command outside its trusted set. | Whether it prompts, and what the holder would read. |

### Case G1, attended variant

The automated G1 records the facts about the plan's sources. The attended variant
checks what a lead can do about them from inside, and what the next start then
does. It needs two runs on the same scratch folders.

1. The coordinator makes a canary under the campaign folder, never in a real
   owner folder: a folder `g1-canary` with one file `canary` that holds the line
   `ataniq-g1-canary-text`. It is on the host and is not bound into the sandbox, so
   a link to it dangles inside; if a bind source were ever followed through a
   link to it, its text would show inside.
2. First run, normally (`no_lead_has_run` true), with a scratch command that
   tries to replace `.git/config`, `.git/hooks`, `.git/info` and `.git` itself in
   its tree, and the mount points in its config folder, with links to the canary,
   and prints what each attempt did. The three `.git` entries and the config
   folder's mount points are bound read-only, so a rename of those is expected to
   be refused; renaming `.git` itself is allowed (the parent of a mount point
   may be renamed). What is refused and what is not is a fact to record. This
   script is an example for the coordinator's configuration, to be adapted to the
   project and runtime of the scratch folders; nothing in this repository runs it:

   ```sh
   C=/owned/qualification/campaign/g1-canary
   try() { if "$@"; then echo "done: $*"; else echo "refused: $*"; fi; }
   cd /work/qualproj || exit 1
   for p in .git/config .git/hooks .git/info; do
     try mv "$p" "$p.old"
     try ln -s "$C/canary" "$p"
   done
   try mv .git .git.old
   try ln -s "$C" .git
   cd /home/lead/.claude || exit 1   # codex: /home/lead/.codex, and AGENTS.md
   for p in skills CLAUDE.md; do     # the mount points of the config folder
     try mv "$p" "$p.old"
     try ln -s "$C/canary" "$p"
   done
   ```

   It goes into the configuration as `"scratch_command": ["/bin/sh", "-c", "<the script>"]`.
3. Second run, on the same scratch folders, with a scratch command that looks for
   the canary's text under the places the first run tried, and with
   `"no_lead_has_run": false` (the lead has run: nothing is made, the folders are
   only checked):

   ```sh
   if grep -rs ataniq-g1-canary-text /work/qualproj/.git /home/lead/.claude; then
     echo canary-visible
   else
     echo canary-not-visible
   fi
   ```

Record, for each run: the attempt name, the plan digest, the terminal output
(`cat -v terminal.log`), the steps of `summary.json`, and what the lead's tree and
config folder hold on the host afterwards (`ls -la`, never following the links).
For the second run, record which of two things happened: the start proceeded and
the canary's text was not visible inside; or the start was refused, at which step
(`own-clone`, `check` and `check-bind-sources` are the ones that can refuse it)
and with what reason. A start that proceeds with the canary's text visible
inside is a finding against rule G1. A refusal is not a failure of the harness: a
link where the tree's `.git` should be is not a clone, and the check of the mount
points refuses a link at a mount point.

### The R17 note for S8

The design wrote S8 for a profile that disables nested user namespaces. Under owner
decision R17, nested sandboxes are allowed for now, and the plan passes neither
`--disable-userns` nor `--assert-userns-disabled`. S8 is therefore rewritten. A
nested user namespace and codex's own sandbox are expected to start. The case
records that they do, and what the nested sandbox can reach. The larger kernel
surface is the thing being measured.

What to do: inside the sandbox, create a user namespace (and, in it, a mount, a
network and a pid namespace); start codex's own sandbox. What to record: the exact
commands and exit statuses; `/proc/sys/user/max_user_namespaces` and the other
user-namespace sysctls as the sandbox sees them; what the nested sandbox can reach
(the work tree, the home, `/tmp`, the network, the lead's provider config, and the
sockets and files listed for S1 and S2); whether the nested sandbox can change a
read-only bind of the outer one; and the kernel release. If a nested sandbox does
not start, record that too: it is a fact about this host, and the plan's R17 note
(nested sandboxes stay available) then does not describe it.

## Cleaning up

A run closes the canary, removes the report FIFO and its folder, releases the pseudo-terminal
and stops the process group (SIGTERM, wait, SIGKILL, wait, with the stop-rule
constants). The first process is kept unreaped until the stop has found the group
empty, so that its process group id cannot be given to another process while
the stop signals it; members that are zombies do not count as left. A group still
there after that is recorded as `unknown`; look for
leftover processes with the ordinary host tools before the next attempt, and
do not start another attempt on a campaign while one may still run. The harness
removes nothing else.

To remove an attempt, delete its folder by hand after the coordinator has taken
what is needed from it. The mount points that `Prepare` made in the scratch
state folders (skills folder, instruction file placeholder, `.git/hooks` if the
clone had none) and the git metadata folder with its copy of the clone's git
files stay: use a fresh
scratch state root for the next first run. Remove the scratch state root, the
campaign folder and the subjects when the qualification is finished.
