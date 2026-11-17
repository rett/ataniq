# Lead sandbox qualification harness

This is qualification infrastructure for a proposal: the lead sandbox profile of
the lead-hosting design, which awaits the owner's decision on the profile. It
launches the generated launch plan once, for real, on a host a coordinator
chose, and records facts. It does not qualify, approve or support anything, and
no part of its output says that the profile passed, is qualified or is
supported. A reader draws conclusions from the facts and the attended cases
below; the owner decides on the profile.

The harness is for a coordinator. Nobody runs it as part of a checkout's checks,
`make test` does not reach a real launch, and every test in the repository uses
a fake launcher. One test starts the test binary itself under a pseudo-terminal
to check the launcher's terminal, process group and signal handling; it starts
no sandbox tool. See [the host manual](qualification-host.md) for the
conventions this follows: facts, status and errno; no pass or support field;
explicit configuration; evidence in a private campaign folder; subjects that are
not developer prerequisites.

## What it is and is not

The command `qualify` (in `qualification/leadsandbox/cmd/qualify`, logic in
`qualification/leadsandbox/internal/harness`) does, in order, for one attempt:

1. validates the configuration and looks at the host for what it names;
2. makes an attempt folder with a random name under the campaign folder;
3. records the versions of the tools;
4. checks that the lead's tree is its own full clone, makes the mount points
   when the configuration says no lead has run, and checks them;
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
9. checks the mount points and sources again, right before the launch;
10. launches pasta, bubblewrap, the probe and a scratch command through a
    launcher, reads the probe's report line from the channel with a time limit
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
  It must not be listed as never-mounted and must not lie in or contain the state
  root or any source, because two bind sources of the harness lie below it.
- **State root**, for example `/owned/qualification/state`: a real path holding
  the lead's layout, all folders mode 0700:

  ```text
  leads/<role>/home
  leads/<role>/config/<runtime>
  leads/<role>/tree
  mirrors/<project>.git
  ```

  The tree is a full clone (a `.git` folder, not a worktree, with no
  alternates), not below another repository. Create it with an ordinary
  `git clone` outside any sandbox. Role and project names are 1 to 63
  lower-case letters, digits and hyphens, starting with a letter or digit. Use
  a scratch state root: the harness never reads a real lead's folders.
- **Sources**: the agent's install folder for the pinned version; the managed
  settings file and its SHA-256 (for claude it must contain the text named by
  `managed_marker`); the lead skills folder; the instruction file; the git
  identity file; the Qargi install; the assignment and projection folders.
  Optional: a records folder (mode 0700, owned by the caller) with the path it
  appears at, extra tree placeholders, and a seccomp file with its SHA-256.
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
  "cases": ["S1", "S2", "S3", "S4", "S7", "S11"]
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
| `sources` | Every host source of the profile (see above). `managed_settings_inside` is required for codex, whose managed-configuration path the design does not name, and must be absent or the provider's own path for claude. `seccomp` is the optional object with `file`, `sha256` and `reviewed_bpf`. `extra_placeholders` lists extra tree bind targets (`rel`, `source`, `dir`). |
| `never_mounted` | Host paths no bind source may equal, lie below or lie above. At least one. |
| `scratch_command` | The command run inside, as an absolute argv. Absent: `/bin/sh -c 'echo ataniq-leadsandbox-scratch-marker'`. |
| `provider_host` | The host name the DNS assertion resolves inside. Not an address. |
| `terminal` | `term` (required), optional `colorterm`, `lang`, `tz`, and the size of the pseudo-terminal (`rows`, `cols`). These are the values the profile's environment takes from a terminal. |
| `time_limit_seconds` | 1 to 3600. Bounds the wait for the report line and the wait for the exit, counted from the launch. |
| `cases` | The automated cases to record, from `S1`, `S2`, `S3`, `S4`, `S7`, `S11`. |

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
its profile digest, and starts nothing: no process, no listener, no FIFO, and no
write into the scratch folders (the mount points are not made). It writes
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
| `terminal.log` | The first 1 MiB of what the process wrote to the pseudo-terminal; the rest is counted in `summary.json`. It may hold escape sequences: read it with `cat -v`. |
| `summary.json` | The versions (kernel release, bubblewrap, pasta), the SHA-256 of each executable, every step with its start, end, status and error text, the exit status, how the stop went, the canary's hit counts, the terminal counts, the resolv.conf derivation, notes about sources whose real path differs from their name, and the requested cases with the results each speaks to. |
| `bound/` | What the plan binds from the harness: the derived `resolv.conf` and the spec folder. |

A step status is `ok`, `failed`, `timeout`, `interrupted` or `skipped`. The report
channel's FIFO and its folder exist only during the run. The summary is written last; its own
write is not one of its steps.

No file has a field that says the profile passed, is qualified or is supported,
and a test checks that no key of the evidence contains such a word. A result is
`ok`, `failed` or `not-checked`, with a detail. A `not-checked` result is not an
`ok` one: it says the check could not be made, and why.

## Cases

The design (section 7.3) lists the sandbox cases. The harness records facts for
the automated ones; the others are attended, and the coordinator makes and
records them by hand, inside a sandbox launched by this harness (with a scratch
command that waits, for example a shell) or by the holder's own qualification.

### Automated cases

| Case | What the harness records |
| --- | --- |
| S1 | The probe's `holder-hidden` and `owner-paths-absent` results (it looks for the holder's paths, the owner's checkout and provider folders, and the other runtime's config folder inside), and the harness's check that no bind source touches a path the harness owns. |
| S2 | The probe's dial of a host loopback port and abstract socket, and the canary's counts, which do not depend on the probe's judgement. |
| S3 | The probe's comparison of its environment with the allowlist (plus `PWD`, which bubblewrap sets). |
| S4 | The probe's resolution of the provider host, and this run's resolv.conf derivation: its source, the nameservers kept and the loopback ones dropped. A derivation refused for lack of any non-loopback server ends the run before a launch, with the reason in the step; that is the second half of S4. |
| S7 | The sysctl `dev.tty.legacy_tiocsti` as the probe and the harness read it, or the seccomp filter the probe finds loaded. The harness never issues TIOCSTI. |
| S11 | Through the record interface: no store exists here, so the result is `not-checked` with "no qualification record store to ask". The stale-record start refusal belongs to the holder. |

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
constants). A group still there after that is recorded as `unknown`; look for
leftover processes with the ordinary host tools before the next attempt, and
do not start another attempt on a campaign while one may still run. The harness
removes nothing else.

To remove an attempt, delete its folder by hand after the coordinator has taken
what is needed from it. The mount points that `Prepare` made in the scratch
state folders (skills folder, instruction file placeholder, `.git/hooks` if the
clone had none, and the lines it added to `.git/info/exclude`) stay: use a fresh
scratch state root for the next first run. Remove the scratch state root, the
campaign folder and the subjects when the qualification is finished.
