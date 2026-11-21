# Lead mirror qualification harness

This is the manual of `qualification/leadmirror`, an opt-in harness that
records what a real sandbox template does with a hostile lead clone. It
looks at two things: the fetch of a lead's branches into the exchange mirror
(design case S16) and the intake review of a lead branch (design case S19).
The fetch and the intake themselves are described in
[the lead intake guide](lead-intake.md). The harness for the sandbox proposal
of a lead is a different one, with its own manual,
[the lead sandbox qualification harness](qualification-lead-sandbox.md): that
one launches a generated plan, and this one runs a template that you write.

The harness records facts. It does not qualify a sandbox, and nothing here does
by itself. No field of its report says pass, supported or qualified. A person
reads the facts and decides.

## Who runs it, and when

The lead or the coordinator runs it, by hand, on a host they have chosen, with
a sandbox template they name. It is not part of `make test` or any ordinary
test run, no Makefile target builds it, and no test of the repository runs it
against a sandbox. Its own tests use a fake planner that runs plain
`git-upload-pack` with no namespace, and they check the shape of the facts and
the refusals only.

It never touches a real lead's state. It works in a scratch state root that you
name, which must not exist yet or must be an empty folder. It does not delete
anything: remove the scratch root yourself when you have read the report.

## Build and run

Run `make doctor` first, then build with the repository's Go wrapper:

```sh
sh scripts/go-tool.sh build -o build/qualify-mirror ./qualification/leadmirror/cmd/qualify-mirror
```

Run it with a config and a new output file:

```sh
build/qualify-mirror --config /path/to/config.json --out /path/to/report.json
```

The targeted check of the harness itself, which starts no sandbox, is:

```sh
sh scripts/go-tool.sh test ./qualification/leadmirror/... ./tests/...
```

The command refuses to run, with exit status 2 and nothing created, when:

- `--config` or `--out` is missing, or there is another argument;
- the config is missing, unreadable, has an unknown field or lacks a field;
- the config does not say `"i_run_this_on_purpose": true`;
- the template is missing, unreadable or does not parse;
- the scratch state root exists and holds anything, is a symlink, is not a
  folder, or can be written by group or others;
- the output file exists, its folder does not exist, or it lies inside the
  scratch state root. The command follows the symlinks of the output file's
  folder and of the state root's parent folder before it compares, so a link
  into the state root is refused too.

If the run is interrupted after it has started, by an interrupt (SIGINT), a
terminate (SIGTERM) or a hang-up (SIGHUP) signal, the command writes the report
it has made so far, then says that the run did not finish and exits with
status 1. The report then holds the facts of the cases that ran, and the case
that was running may hold fewer facts than a finished run. An interrupt or a
hang-up that was ignored when the command started (under `nohup`, for example)
does not interrupt the run.

## The config

One JSON object. Every field is required, and an unknown field is refused.

```json
{
  "state_root": "/abs/path/to/scratch/state",
  "template": "/abs/path/to/sandbox-template.json",
  "role": "coder",
  "project": "proj",
  "i_run_this_on_purpose": true
}
```

- `state_root`: an absolute, clean path made of letters, digits, dot, dash,
  underscore and slash, not at the top of the file system. The harness writes
  this path into shell commands and git config values, so it takes no other
  character. Choose a folder that is not under your home and that no other
  process uses.
- `template`: an absolute path to a sandbox template file in the format that
  `internal/exchange/sandboxplan` documents (`ParseTemplate`). The digest in
  the report is the SHA-256 of this file's bytes.
- `role` and `project`: names of the scratch lead and project. Each is a
  lower-case name of letters, digits and dashes, up to 63 characters, that does
  not start with a dash.
- `i_run_this_on_purpose`: must be `true`. It says that you mean to run this
  on this host with this template.

## What the template must provide

The harness runs everything that touches the lead's clone through the template,
as the real fetch and intake do. The template is yours; the harness checks
nothing about it. For the facts to mean something, it must provide the
following.

**The serving half** (`serve` section):

- `upload_pack` runs `git-upload-pack` on the whole element `{tree}`, with
  the clone **read-only** and **no network**. Git starts this command through
  `sh` with the environment of the harness, so the argv must clear the
  environment itself (for example `bwrap --clearenv`).
- `repository` is the clone's path inside the sandbox, and a path that does not
  exist on the host. The owner-side git of the fetch is given it as a
  `file://` URL, so it starts `upload_pack` and never reads a git bundle file
  at that path itself (see
  [the sandbox template](lead-intake.md#the-sandbox-template)). As a second
  layer the fetch refuses a path that exists on the host, a regular file, a
  symlink or a folder alike, before anything runs (`mirror.ErrRepositoryPath`),
  so a template with such a path gives a refused `s16-fetch.fetch` and no other
  fact of the fetch. A path that does not exist, and the clone's own path (which a fake
  planner gives), pass.
- `measure` prints the size of the clone's object store, and runs in the same
  kind of sandbox.
- The sandbox gives `git-upload-pack` what it needs to run and nothing else.

**The verification sandbox** (`verify` section). It is optional: without it
the verification case ends after one fact. When present:

- `prefix` runs the inner command in a sandbox with `{mirror}` read-only,
  `{scratch}` and `{home}` writable, and its own home, with no provider login
  and no credential of the lead or of the person who runs the harness. The
  inner command is `/bin/sh -c <script>`, and the script uses `git`, so the
  sandbox needs a shell and git. The gates use `/bin/sh` and `env`.

**What the template must not bind**, in either section, for the canary facts to
mean something:

- the scratch state root, or any folder above it, writable;
- any `canary` folder (`<root>/canary` and `<root>/cases/<case>/canary`),
  writable. Bind nothing of the scratch root but what the placeholders name:
  `{tree}` (read-only), `{mirror}` (read-only), `{scratch}` and `{home}`;
- the `fixture` folders or the mirrors of the other cases.

A canary file is a file that a planted hook or gate would write if it ran with
access to the host's folder. If the template binds the canary folder writable,
a file in it shows only that the template allows the write.

## The report

One JSON document, written with mode 0600 to a new file:

```text
{ "format": 1,
  "tools":    { "git": "git version 2.x", "go": "go1.x", "goos": "linux", "goarch": "amd64" },
  "template": { "sha256": "..." },
  "role": "...", "project": "...",
  "cases": [ { "id": "...", "title": "...",
               "facts": [ { "id": "<case>.<name>", "detail": "...", "observed": { ... } } ] } ] }
```

`tools.git` is the output of `git version` of the git that the harness and the
mirror run. `tools.go` is the Go version the harness was built with.

Text that a lead wrote (ref names, paths, commit subjects, gate output) is
passed through `leadtext.Escape` before it is written. Error texts come from
the packages, which put no output of git and no lead text in them; the one
exception is the error of `s16-fetch.control`, which holds the first line of
git's own message. The harness puts no scratch path in a fact of its own,
except `repository_inside` in `s16-fetch.clone`, which is the template's own
value for the clone's path as the serving half sees it, and that value is a
host path when the template uses `{tree}` there. The text of an error from a
package or from the control's git may name a folder.

A fact never says that something is good. Where a fact compares two values it
says whether they are `identical`, and nothing more.

An error is recorded as `result: "error"` with `error` (the text),
`error_classes` (the sentinel errors it matches, for example
`mirror.ErrHostileTree` or `sandboxplan.ErrNotConfigured`) and, for a limit
error, `limit` with `what`, `got` and `max`. A call that returned no error has
`result: "ok"`.

A problem of the harness itself (a fixture it could not make) is a fact
`<case>.harness-error`, and the run goes on with the next case.

### Case `s16-fetch` (design case S16)

The harness builds a hostile lead clone at `<state_root>/leads/<role>/tree`
and an exchange mirror at `<state_root>/mirrors/<project>.git`, with the
upstream branch `main` and an annotated tag `v1`. The clone has:

- a branch `main` that has the upstream branch's name and different content,
  and a branch `feature`;
- a tag `v1` that has the upstream tag's name and points elsewhere, and a tag
  `lead-only`;
- in `.git/hooks`, a script for each hook name in the harness (the git sample
  names and the other documented hooks), and the same set in `.git/lead-hooks`;
- in `.git/config`, `core.fsmonitor`, `core.hooksPath` (naming
  `.git/lead-hooks`, with the clone's path as the serving half sees it),
  `uploadpack.packObjectsHook` and `core.alternateRefsCommand`.

Every hook and command writes one file in the canary folder
`<state_root>/canary`. The harness then runs `Mirror.Fetch` of the lead's
branches through the template, with the default limits.

| Fact | Observed values |
| --- | --- |
| `s16-fetch.clone` | What was planted; `repository_inside`, the clone's path as the serving half sees it; `repository_on_host`, what the host has at that path, as the fetch's own check sees it (the entry itself, no symlink followed at the last name): `absent`, `the clone's own path`, `exists: a regular file`, `exists: a symbolic link`, `exists: a folder`, `exists: a file of another kind`, `cannot be checked: <cause>`, or `unknown: the planner gave no serve plan`; the number of canary names. Anything that `exists` or `cannot be checked` makes the fetch refuse the path. |
| `s16-fetch.fetch` | The fetch result and error; `updates` (lead ref, old and new id); `source_bytes` (the sandbox's measure), `mirror_before_bytes`, `staged_bytes`, `mirror_after_bytes`; `outside_changed`; `serve_requests` (planner requests the fetch made). |
| `s16-fetch.upstream-refs` | `refs/heads/*` and `refs/tags/*` of the mirror `before` and `after` (name, id, type), and whether they are `identical`. |
| `s16-fetch.lead-refs` | The lead's refs in the mirror `before` and `after`. |
| `s16-fetch.mirror-size` | The mirror folder's allocated bytes and entries before and after. |
| `s16-fetch.canaries` | `before` and `after`: which planted canary names exist on the host (`present`), other names in the canary folder, and the number of entries. |
| `s16-fetch.control` | The control. After the facts above are taken, the harness runs plain `git status` (its own explicit environment, not through the template, no sandbox) in a second clone made the same way, planted the same way, with a canary folder of its own, in the fixture folder. The harness made it before any sandbox served the lead's clone. `result` and `error` of that git; `present`: the canaries that fired; the other names in the control's canary folder. |

### Case `oversize`

A fresh clone, mirror and canary folder under `<state_root>/cases/oversize`.
The clone has a branch `big` with about 300 KiB of text that does not compress.
Three fetches run, in this order, on one mirror:

| Fact | What runs | Observed values |
| --- | --- | --- |
| `oversize.measure` | A fetch with `MaxSource` of 512 bytes (the sandbox's measure is compared with it). | The fetch result as above, with `mirror_before` and `mirror_after` (upstream refs, lead refs, size), `mirrors_folder` (the names left beside the mirror), `serve_requests`. |
| `oversize.staged` | A fetch with `MaxStaged` of 64 KiB. | The same keys. |
| `oversize.control` | A fetch with the default limits. | The same keys. It shows that the limits and not the clone caused the two errors. |

### Case `planted`

A fresh clone under `<state_root>/cases/planted`, fetched through the template
once as it is, then one fetch for each plant, then once more with a new clone
made as the first one was. Each plant is made in a new clone before anything
serves it, and that clone is put in the place of the lead's clone; a clone that
the template has served is moved away whole and the harness writes nothing into
it. The owner-side check of the clone refuses the three plants before the
planner is asked, so these facts show `serve_requests` of 0.

| Fact | What runs |
| --- | --- |
| `planted.control-before`, `planted.control-after` | A fetch of a clone as made: the first clone, and a new one made the same way after the plants. |
| `planted.alternates` | A fetch with `.git/objects/info/alternates` present. |
| `planted.git-symlink` | A fetch with `.git` replaced by a symlink to the real folder. |
| `planted.tree-symlink` | A fetch with `leads/<role>/tree` replaced by a symlink to the real folder. |

Each has the fetch values, `lead_refs_before` and `lead_refs_after`, and
`upstream_refs_identical`.

### Case `s19-review` (design case S19)

A fresh clone and mirror under `<state_root>/cases/review`. The lead branch
`review` has nine commits on top of the upstream branch: one each for
`.claude/settings.json` (with a hook), `.mcp.json`, `.codex/config.toml`,
`.envrc`, `.vscode/tasks.json`, a change of `Makefile`, a change of a file in
`scripts/`, a `toolchain` line in `go.mod`, and a last one for `docs/notes.md`,
which is not execution config. The harness fetches it through the template and
runs `intake.Inspect` against the upstream branch with the default
execution-config rules.

| Fact | Observed values |
| --- | --- |
| `s19-review.fetch` | The fetch values, and the lead refs after. |
| `s19-review.inspect` | The result (`error_classes` can hold `intake.ErrCommitEncoding` or `intake.ErrCommitFormat`: see the notes below); `base` and `tip`; `commits` (id and subject); `changes` (path, status, modes, flag kinds); `flagged` (kind, path, pattern, reason); `flagged_paths`; `unflagged_changed_paths`; `rule_count`. |
| `s19-review.upstream-refs` | The upstream refs before the fetch and after the review, and whether they are `identical`. They show that neither the fetch nor the review moved the upstream branch. |

### Case `verification`

Only when the template has a `verify` section. Two runs of `verify.Run` on the
tip of the review branch, one gate each:

| Fact | What runs | Observed values |
| --- | --- | --- |
| `verification.canary-gate` | A gate that tries to write a file in the canary folder, outside the scratch folder, prints `canary-write-succeeded` or `canary-write-failed`, and exits 0. | `exit`, `stopped`, `output_cut`, `output` (escaped lines), `canary_exists_on_host`, `verify_requests`. |
| `verification.environment-gate` | A gate that prints its environment with `env`. | `exit`, `stopped`, `environment` (escaped lines), `environment_names`, `same_as_harness_env`. |

A value in the gate's environment that equals the same name in the harness's
own environment is not recorded, except for `PATH`, `LANG`, `LC_ALL`, `TERM`
and `SHLVL`; its name is listed in `same_as_harness_env`. Other values are
recorded as the gate saw them, and the file is mode 0600: treat it as
sensitive and read it before you share it.

Without a `verify` section the case holds one fact,
`verification.no-verify-section`. If the review case did not give a fetched
tip, it holds `verification.not-run`.

## What a fact does and does not show

- A fact says what one call returned and what the harness saw on the host, in
  one run, on one host, with one template file (named by its digest), one git
  and one Go version. A different template, kernel, git or run can give other
  facts.
- The S16 canaries show that owner-side git ran nothing from the clone: no
  hook and no configured command of the clone was started by the fetch. They
  show nothing about the sandbox. A serving command that runs only
  `git-upload-pack` gives the same S16 canary facts with a sandbox and without
  one, because `git-upload-pack` does not run these hooks or these configured
  commands in ordinary use. The harness does not claim otherwise. A canary
  that does not exist does not show that a hook or a command was started and
  then blocked, or never started.
- What the harness holds as controls: `s16-fetch.control` is the control of the
  S16 canaries. It runs plain `git status` in a second clone, made the same
  way and planted the same way, in the fixture folder with its own canary
  folder, with no sandbox. The canaries it lists in `present` are the ones that
  fire when git runs in a clone on this host, so a reader can tell that the
  fixture was armed. Without a fired canary in the control, an absent canary in
  `s16-fetch.canaries` means little. The package's tests also run `git status`
  and `git commit` in a planted clone and require the canaries, and require the
  canary of the verification gate to be written when there is no sandbox. The
  control of the verification canary is in the tests only; the report holds no
  control for it.
- What does show something about the sandbox: the verification canary
  (`verification.canary-gate`, with `canary_exists_on_host`), the environment
  facts (`verification.environment-gate`), and the review of the template file
  itself, which a person does by reading what each section binds, hides and
  sets.
- A canary that exists is a real finding: something wrote outside the places
  the template gave. A template that does not bind the canary folder makes a
  write fail for a plain reason, and that is the intended reading of the
  verification canary: nothing reached the host.
- `canary-write-failed` in the output of the canary gate is the sandbox's own
  statement. The fact `canary_exists_on_host` is the harness's check.
- `identical` upstream refs show that the refs `refs/heads/*` and
  `refs/tags/*` had the same ids before and after, as read through the mirror
  package. They do not show that the objects of the mirror are unchanged: the
  fetch adds objects, and the size facts show that.
- Limit errors show that the code compared a number with a limit. The measure
  is the sandbox's statement and a first check; the staged size is the owner's
  own measure.
- Flagged paths show what the intake's rules mark for a separate sign-off. A
  path that is not flagged is not shown to be harmless. The harness does not
  run the sign-off, the integration or the lead's code.
- `repository_on_host` is the harness's reading of the template's `repository`
  path on the host, taken before the fetch with the same rule as the fetch. It
  is not a report on the sandbox: it says whether the second layer of the fetch
  would refuse the path. A race (a file that appears after it) is not covered
  by it, and is not read as a bundle either, because the source is a `file://`
  URL.
- The intake reads the commits of the review branch as raw objects and
  converts no text, whatever the commit's `encoding` header says (see
  [how the commits are read](lead-intake.md#review)). The harness has no
  commit with such a header, so no fact here shows that refusal; the package's
  tests do.
- `serve_requests` shows how many times the planner was asked for a serve
  plan during that call. The harness asks the planner once per case, before the
  fetches, only to learn the clone's path as the sandbox sees it.
- The fixtures (the upstream repository, and the clone before it is made
  hostile) are made with git by the harness, in a folder that no sandbox is
  given, with an explicit environment. After the clone is moved to
  `leads/<role>/tree` git never runs in it. The mirror is seeded and read
  through the exchange packages' own owner-side git.
- The control (`s16-fetch.control`) is the one place where git runs in a
  planted clone, unsandboxed, on the host. It is safe only while the template
  does not bind the state root or its `fixture` folder writable. A template
  that breaks that rule lets sandboxed code change the control's hooks, which
  the harness would then run on the host.

Nothing here qualifies a sandbox by itself. The facts are inputs for a reader
who knows the template, the host and the design cases S16 and S19: that person
reads them, decides whether they are enough, and records the decision
elsewhere. A report that holds no surprise is not a support statement, and one
that does is not a verdict on the code: it is a reason to look.
