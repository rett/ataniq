# Lead intake: the exchange mirror and the reviewed intake

This guide is for the owner and for contributors. It describes how work leaves
a managed lead and enters the owner's upstream branches, and what the command
`ataniq-intake` does at each step. It describes what is built on this branch
and nothing else.

## What this is, and its status

A managed lead works in its own clone, inside a sandbox. What the lead writes
is untrusted. The owner's design decision R18 says how it comes out:

- The exchange mirror is the only way out of a lead. It is a bare git
  repository that Ataniq keeps in its state folder.
- The lead's branches in the mirror are untrusted.
- Work enters an upstream branch only by a reviewed intake. The intake is
  fast-forward only. The owner signs off the execution-config paths one by
  one, and the gates run in a sandbox.

`ataniq-intake` is the owner's command for this. It has six subcommands:
`init`, `fetch`, `branches`, `review`, `verify` and `integrate`. It never
asks a question and never reads a terminal. The sign-off is made of
arguments you type.

**Status: provisional.** Read these limits before you rely on it.

- No sandbox is shipped or qualified here. `fetch` and `verify` run commands
  from a template file that you write (see [The sandbox template](#the-sandbox-template)).
  Without a usable template they refuse and run nothing. The command does not
  check that the template confines anything.
- The holder, the daemon and the lead's own sandbox are separate work, and
  none of them calls this command: see [the lead holder guide](lead-holder.md),
  [the lead daemon guide](lead-daemon.md) and
  [the lead sandbox guide](lead-sandbox.md). Nothing here starts a lead. In a
  build with the `ataniq_canary` tag, under `--canary`, the daemon starts a
  throwaway lead whose commit the owner takes in with this command by hand,
  with a template of its own (see [the canary slice guide](canary-slice.md)).
- Qualification of a sandbox is separate. See
  [the lead mirror qualification manual](qualification-lead-mirror.md).

The code is in `cmd/ataniq-intake` and in these packages: `internal/mirror`
(the mirror and the fetch), `internal/intake` (review, sign-off, records and
integration), `internal/intake/verify` (the gates),
`internal/intake/execconfig` (the execution-config set) and
`internal/exchange` (git environment, sandbox plan, safe file access, text
escaping and bounded processes). Each package has a package comment that is
the exact specification.

## The layout

Everything is under one state root, `STATE`, for example `/srv/ataniq-state`.
`STATE` must be a real folder that you own, and neither its group nor others
may write it.

```text
STATE/
  mirrors/
    myproject.git/            the exchange mirror (bare repository)
    myproject.git.lock        lock file of the fetch, beside the mirror
    myproject.git.staging-*   staging repository of a running fetch
  leads/
    coder/
      tree/                   the clone of the lead "coder"
```

- `mirrors` has mode 0700. `init` creates it, and the mirror inside it.
  The mirror holds a marker file, `ataniq-mirror`, that says which project it
  belongs to.
- `leads/<role>/tree` is the lead's clone. The lead writes all of it, `.git`
  included. Owner-side code never runs git in it.
- The lock file and the staging folders sit beside the mirror, not inside it.
  A staging folder exists only while a fetch runs, or after a crash (see
  [What a refused fetch and a crash leave](#what-a-refused-fetch-and-a-crash-leave)).

The mirror has two ref namespaces:

| Namespace | Who writes it | Meaning |
| --- | --- | --- |
| `refs/heads/*` and `refs/tags/*` | The owner only: a push, or `integrate` | The upstream branches and tags. |
| `refs/leads/<role>/heads/*` | `fetch`, from the lead's branches | The lead's branches. Untrusted. |

A lead's branch `develop` is `refs/leads/coder/heads/develop`. It is a
different ref from the upstream `refs/heads/develop`. Lead tags are not
carried: a lead can never replace an upstream tag.

The records folder and the work folder of `verify` are not in the state root.
You give them as options. They must exist, be absolute, be real folders that
you own with mode 0700, and not be the state root or inside it. For `verify`,
the records folder and the work folder must also not be the same folder or lie
inside each other, with symbolic links resolved.

## The fetch, step by step

`ataniq-intake fetch` brings the branches of one lead from `STATE/leads/<role>/tree`
into the mirror, under `refs/leads/<role>/heads/`. It fetches branches only
and no tag. The steps below follow `Mirror.Fetch` in `internal/mirror`.

1. **Lock.** The fetch takes an exclusive lock on the lock file. A second
   fetch into the same mirror stops with "busy". A staging folder left by a
   crashed fetch is removed.
2. **Check the clone as the owner.** Starting from the state root, without
   following any symlink, the fetch checks that `leads`, `leads/<role>`,
   `leads/<role>/tree`, `.git` and `.git/objects` are real folders. It refuses
   the clone ("hostile tree") when `.git` is a file or a symlink, when
   `.git/objects/info/alternates` or `http-alternates` exists, when
   `.git/commondir`, `.git/gitdir` or `.git/shallow` exists, or when a
   check cannot be made. This is a way to refuse the obvious cases early. A
   running lead can change the clone right after the check, so the owner
   relies on the sandbox and on step 7, not on this check.
3. **Ask for the sandbox plan.** The template gives the command that runs
   `git-upload-pack` on the clone, and the command that measures the clone's
   object store. With no template, or a template without a `serve` section,
   the fetch stops here. Nothing has run. The plan's repository path
   (`serve.repository`) is then checked on the host, because the owner's git
   is given that path and looks at it before it starts the serving half: a git
   bundle file there is read by that git itself, as the owner, and the sandboxed
   command never runs. The path is refused ("the template's repository path
   exists on the host") when anything is there: a regular file, a symlink (also
   a dangling one, found without following it), a folder of any kind (a bare
   repository, a mirror, a clone), or a file of another kind. It is also
   refused when its state cannot be told (a permission error, a file where a
   folder should be on the way), so the rule fails closed. Two things pass: a
   path that does not exist on the host, which is a path inside the sandbox
   and the case the template is for, and the lead's own clone path
   (`STATE/leads/<role>/tree`), which step 2 has just checked to be a real
   folder and which no bundle can be. Nothing has run when the path is refused.
4. **Measure.** The measure command runs in the sandbox. Its number is the
   lead side's own statement, a first check. More than the source limit is a
   limit error. The owner also measures the mirror: a mirror already at its
   limit stops the fetch.
5. **Staging repository.** A fresh bare repository is made beside the mirror.
   It has the mirror's objects as its only alternate, so git does not fetch
   again what the mirror has. The mirror's refs are noted.
6. **Receive.** The repository path is checked again, right before git starts,
   to keep the window of a path that appears in between small. Git runs as the
   owner, in the staging repository. The serving
   half is the sandboxed command, named with git's `--upload-pack` option. Only
   the receiving half runs as the owner. A watchdog stops the fetch when the
   staging repository passes its size limit or its entry limit, and the whole
   fetch has a time limit.
7. **Check what arrived**, in the staging repository, before the mirror is
   touched: the sizes, the number of refs, and every ref. Every ref must be in
   the lead's namespace, have a name this code accepts, and point at a commit.
   A ref that fails refuses the whole fetch. The error gives the count and
   never the names. The staging repository must still be what this code made.
8. **Move, in two parts.** First the objects the mirror lacks are fetched from
   the staging repository by object id, so that no ref is written. Then the
   lead's namespace, and only it, is changed to what the staging repository
   holds, in two compare-and-swap transactions of `git update-ref`: first
   every delete, then every create and update. Two are needed because a lead's
   rename of a branch across a `/` (`a/b` to `a`) cannot be done in one. A
   failure of the second puts back what the first deleted. From this step on
   the fetch is not cancelled by its time limit, nor by an interrupt,
   terminate or hang-up signal, so that refs are not left half moved.
9. **Prove that other namespaces did not change.** The mirror's refs are listed
   again. The move names only the lead's namespace, so it cannot write
   anything else. A ref outside it that holds a tip this fetch staged can only
   be a bug or an attack. That, or an alternates file in the mirror, is an
   error, and the lead's namespace is put back as it was. A ref outside the
   namespace that differs for another reason (an intake or a push by the
   owner while the fetch ran) is only printed as a note.
10. **Clean up.** The staging repository is deleted, also after an error or a
    panic, and the mirror is measured again.

The command prints the four sizes (the lead's object store, the mirror
before, what the fetch added and the mirror after), the duration, each ref
update and any note from step 9. Lead text in it is escaped (see
[How lead text is shown](#how-lead-text-is-shown)).

### The limits

A zero or missing limit means the default. The command has an option for the
first four. Sizes are in bytes and each option must be positive.

| Limit | Option | Default |
| --- | --- | --- |
| Time for the whole fetch | `--timeout` (a duration such as `10m`) | 10 minutes |
| Size of the lead's object store, as the sandbox measures it | `--max-source` | 2 GiB |
| What one fetch adds | `--max-staged` | 1 GiB |
| Size of the mirror | `--max-mirror` | 8 GiB |
| Files and folders in any measure | none | 2 000 000 |
| Branches of one lead | none | 2 000 (never more than 20 000) |

The size of the mirror after the move is a check and not a quota. When the
mirror is over its limit after the move, the refs have already moved. The
command prints the report and exits with status 1.

### The git environment

Every git run by the owner on content a lead wrote goes through the `gitenv`
package. The environment is complete and nothing is inherited from the calling
process. The git executable is an absolute path and `$PATH` is never searched.
No system or global configuration is read, no attributes outside the
repository and no credentials. Hooks, file system monitors, pagers, editors,
askpass programs, credential helpers, external diff programs and submodule
recursion are off. Every transport is refused except the one the call names,
which is `file`. Object checking (fsck) is on for transfers. No option is
accepted before the subcommand. After it, up to the first `--` or
`--end-of-options`, every option must be written `--name=value` or be one of a
short list of flags that take no value. Options that name a program to run are
refused in every form, abbreviated too; the one upload-pack command of the
fetch is set by the package itself, not as an option the caller writes. The
package does not make git safe in a repository an attacker controls, and does
not confine the process. Owner-side code runs git only on bare repositories
that Ataniq made, with an explicit `--git-dir`. The package comment of
`internal/exchange/gitenv` has the details.

### What a refused fetch and a crash leave

- **A failure before step 8** leaves the refs and the objects of the mirror as
  they were. This includes a refused clone, a missing template, a limit, a
  time-out and a refused ref. The staging repository is deleted. The lock file
  stays; it is an empty file.
- **A move that fails** leaves no ref changed. The objects it brought stay in
  the mirror with no ref that reaches them. They are harmless. The owner's
  garbage collection removes them, and nothing here runs it.
- **A crash** of the owner process is another matter. An interrupt, terminate
  or hang-up signal is not a crash: before step 8 the command cancels like a
  time-out and leaves the mirror as it was, and from step 8 on the signal
  cancels nothing and the fetch runs to its end. An interrupt or a hang-up
  that was ignored when the command started (under `nohup`, for example)
  changes nothing at any step. A kill that cannot be caught or a power loss is
  a crash. The namespace is changed in two git runs, and nothing makes them one
  step. A crash between the two leaves the lead's namespace half changed. The
  next fetch takes the namespace as it finds it and brings it to the lead's
  branches as they are then. It does not repair or notice the half-done state,
  and its report is relative to what it found, so a report can miss changes.
  A crash can also leave a staging folder, which the next fetch removes. A git
  killed in the middle of a transaction can leave a stale `<ref>.lock` file in
  the mirror. Every later move that writes that ref then fails until you
  remove the file by hand.

## The sandbox template

The template is a JSON file that you write. It says which commands run, and
the command trusts it. It is not checked to be a sandbox: the package fills in
the argument lists and does nothing more. You vouch for it. The file must not
be in the state root, which is kept for the host's records and journal and free
of configuration (a lead's sandbox cannot write the host's folders), is read through a safe open (a
symlink is refused) and is at most 64 KiB.

Both sections are optional. A step whose section is absent refuses and runs
nothing. This is the whole format, version 1:

```text
{
  "version": 1,
  "serve":  { "upload_pack", "repository", "measure", "measure_env" },
  "verify": { "prefix", "env", "mirror", "scratch", "home" }
}
```

- `serve.upload_pack` is the argument list that runs `git-upload-pack`
  sandboxed, with no network and the clone read-only. Git appends the
  repository path as one more argument. Git starts this command, through
  `sh`, with the environment of the process that fetches, which may hold
  owner credentials. The plan has no environment for it, so the list itself
  must clear the environment (for example `bwrap --clearenv`, or `env -i`).
- `serve.repository` is the path of the clone as the sandbox sees it. The
  owner's git is given this path as the source of the fetch, and it looks at
  the path on the host before it starts the serving half: when a regular file
  is there and it is a git bundle, git reads that file itself and never starts
  `upload_pack`. Choose a path that does not exist on the host, such as
  `/work/{project}`, and never one where a sandbox could put a file. The fetch
  checks it and refuses a path that exists there, whatever it is (see step 3 of
  [the fetch](#the-fetch-step-by-step)); a path that does not exist is the
  expected case. The check cannot stop a file that appears after it, so keep
  every folder on the way to the path out of the reach of any sandbox.
- `serve.measure` and `serve.measure_env` are the command and the
  environment that print the size of the clone's object store. The first
  token of the first output line must be a decimal byte count.
- `verify.prefix` and `verify.env` are the argument list and the environment
  that run an inner command in the verification sandbox. The command appends
  `/bin/sh -c SCRIPT`. The sandbox needs a shell and git.
- `verify.mirror`, `verify.scratch` and `verify.home` are the paths of the
  mirror (read-only), the scratch clone and the home folder as the inner
  command sees them.

Rules for what a string may hold:

- The first element of every argument list is the launcher. It runs as the
  owner. It must be an absolute, clean path written out in full, with no
  placeholder.
- In `serve`, the placeholders are `{role}`, `{project}` and `{tree}`. In
  `verify` they are `{project}`, `{mirror}`, `{scratch}` and `{home}`. Any
  other brace is refused, so a literal brace cannot be written.
- `{role}` and `{project}` stand for a name. A name has no slash and no dot, so
  they may be used inside a longer string.
- `{tree}`, `{mirror}`, `{scratch}` and `{home}` each stand for a root folder
  that the caller vouches for. Each must be a **whole element** of an argument
  list: the element is exactly the placeholder, with nothing before or
  after it. A template can then never name a path below a root or around
  it. A path placeholder is refused in `measure_env`, in `env` and in the
  fields `repository`, `mirror`, `scratch` and `home`. Those fields are paths
  as the sandbox sees them and hold only names and fixed text.
- Unknown fields, a field name that differs in case, a field written twice, a
  version other than 1 and trailing data are refused.

What you vouch for:

- The launcher confines what it says. Nothing here checks it.
- No sandbox can write any folder on the way to the clone, to the mirror or
  to the verification folders, except the content of the clone, the scratch
  folder and the home folder, which sandboxed code writes. The clone is never
  under a folder a lead can write.
- Fixed text in the template can still spell out any host path. Do not name a
  path below a folder a sandbox can write, other than through a whole-element
  placeholder.

The example below uses `bubblewrap` arguments. It is an illustration only. No
one has qualified it, it has not been run against a hostile clone, and the
exact arguments that a host needs (library folders, user mapping, and git's
ownership check inside a user namespace) depend on the host. Do not take it as
a safe default. The paths `/work`, `/work/home` and `/ataniq/mirror` exist
only inside the sandbox.

```json
{
  "version": 1,
  "serve": {
    "upload_pack": [
      "/usr/bin/bwrap", "--unshare-all", "--clearenv", "--die-with-parent",
      "--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin",
      "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64",
      "--proc", "/proc", "--dev", "/dev",
      "--ro-bind", "{tree}", "/work/{project}",
      "/usr/bin/git-upload-pack"
    ],
    "repository": "/work/{project}",
    "measure": [
      "/usr/bin/bwrap", "--unshare-all", "--clearenv", "--die-with-parent",
      "--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin",
      "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64",
      "--proc", "/proc", "--dev", "/dev",
      "--ro-bind", "{tree}", "/work/{project}",
      "/usr/bin/du", "-s", "-B1", "/work/{project}/.git/objects"
    ],
    "measure_env": ["PATH=/usr/bin:/bin"]
  },
  "verify": {
    "prefix": [
      "/usr/bin/bwrap", "--unshare-all", "--clearenv", "--die-with-parent",
      "--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin",
      "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64",
      "--proc", "/proc", "--dev", "/dev",
      "--ro-bind", "{mirror}", "/ataniq/mirror/{project}.git",
      "--bind", "{scratch}", "/work/verify",
      "--bind", "{home}", "/work/home",
      "--setenv", "PATH", "/usr/bin:/bin",
      "--setenv", "HOME", "/work/home"
    ],
    "env": ["PATH=/usr/bin:/bin"],
    "mirror": "/ataniq/mirror/{project}.git",
    "scratch": "/work/verify",
    "home": "/work/home"
  }
}
```

The command accepts this file as a template. That shows only that it parses.

## The intake, step by step

The steps are `review`, `verify` and `integrate`. `ataniq-intake help` and
`ataniq-intake help SUBCOMMAND` print the exact flags and exit statuses of
each; this guide does not repeat them. Every subcommand takes `--state` and
`--project`.

### review

`review` shows what a lead branch changes against an upstream branch. Nothing
is checked out: the review reads the change with git plumbing in the mirror,
and writes nothing. The upstream commit must be an ancestor of the lead's tip,
so a branch that cannot be fast-forwarded is refused.

It prints:

- the base and tip commit ids and the **review digest**, a hash over the base,
  the tip, the two ref names, the changed paths with their modes and ids, and
  the flags (kind, path and matching pattern). A change in any of them changes
  the digest;
- the commits between the base and the tip, newest first;
- the changed paths of the net change, with status and modes;
- with the optional `--records DIR`, the newest three verification records of
  the project for this tip, newest first, each with its outcome, its start
  time, its plan digest and its **gates** (name and command), before the
  sign-off, and a count of the earlier ones that are not shown; or a line that
  says that none is recorded. The newest record decides, as for `integrate`;
- the section **Needs your sign-off**: each path that needs its own sign-off,
  with the kind of each flag, its reason and the exact `--accept-path`
  argument to type.

`--diff` also prints the patch of the net change. The review has limits that
are not options: 5000 commits, 20 000 paths and 16 MiB of patch.

**How the commits are read.** The commits are lead text. When git prints a
message, it converts the text from the charset that the commit's own
`encoding` header names, with a converter the lead chose, in the owner's
process. The review therefore never asks git to print a message. It lists the
commits with `git rev-list`, checks the size of each object with
`git cat-file --batch-check`, reads the raw objects with
`git cat-file --batch` and parses the headers itself:

- The headers are `tree`, the `parent` lines, `author` and `committer`, in that
  order, and then any others. A header that continues on lines starting with a
  space (`gpgsig`, `mergetag`) is one header. A header out of order, without a
  value, not ended by a newline, a second `encoding` header, or an `author` or
  `committer` that is not `Name <address> seconds zone` (the seconds as digits,
  the zone as a sign and four digits) refuses the review: "a commit object is
  malformed", with the commit id.
- Only the `encoding` header is looked at, and only to refuse. A value of
  `UTF-8` or `utf8`, in any case, is UTF-8; any other value, a charset git
  knows (`ISO-8859-1`, `SHIFT_JIS`) or not, and an empty one, refuses the
  review: "a commit declares an encoding other than UTF-8", with the commit id
  and the value escaped. Nothing is converted and nothing of that commit is
  shown. The lead has to commit the branch again in UTF-8. Refusing was chosen
  over showing such a commit as raw bytes: a commit that the owner cannot read
  is not one to sign off, and a refusal adds no code that handles a charset.
- The names, the addresses and the subject are the stored bytes. A message
  with bytes that are not UTF-8 and no encoding header is not refused; it is
  shown escaped, as any lead text (see
  [How lead text is shown](#how-lead-text-is-shown)). The subject is what
  `git log --format=%s` prints: the first paragraph, its lines joined by single
  spaces, white space at the end of each line dropped, and a NUL byte ending the
  message.
- Each header with its continuation lines, a name, and the subject may be at
  most 32 KiB, a whole commit object at most 64 KiB, and all the commits
  together at most 8 KiB for each commit plus one commit object of 64 KiB.
  More is a limit error, with the commit id; an object over its size limit is
  not read. Of the message only the subject is kept. A header of many
  continuation lines is parsed in time linear in its size, and the time limit
  of the call and the caller's context are checked between the commits that
  are parsed, so a branch of such commits is refused when the time is used up.

A path can carry several flags. These are the kinds (`odd-path` is for the merge mode only, see [merge mode](#merge-mode-of-verification)):

| Kind | Why it exists |
| --- | --- |
| `exec-config` | The path is in the execution-config set (see [the next section](#the-execution-config-set)). Code at that path can run as the owner when the tree is next used without a sandbox. |
| `symlink` | The entry is, or was, a symbolic link. A link can make a checkout reach an execution-config path through another name. |
| `submodule` | The entry is, or was, a submodule. A submodule fetches and runs other code. |
| `aliased` | A symbolic link whose own path is execution config stands for a folder, and this path lies in that folder. A link that points out of the repository, at an absolute path, or through too long a chain of links, is flagged on its own path. |
| `history-only` | The path is execution config, a link or a submodule, or lies in a folder that a link stands for, in the change of some commit of the branch, and the net change does not show it: it was added and removed again, or changed and changed back, or it was a link or a submodule in a commit and is neither at the tip. It still enters upstream history, so it needs a sign-off too. |
| `odd-path` | The matcher cannot be trusted with the path: it holds bytes that are not UTF-8, a control or format character, a colon, a `~` followed by a digit, or it is not in Unicode normalization form C, so another file system could read it as a different name. Merge mode only: the review of a merge the host built flags it, and the review of a lead branch never does. |

### verify

`verify` runs gates on the tip of a lead branch. Each `--gate NAME=COMMAND`
runs as `/bin/sh -c COMMAND`. The gates run in order, and the first failure
ends the run.

- **Where.** In the verification sandbox of your template, never as you and
  never in your checkout. The command makes two new folders in `--work`, a
  scratch folder and a home folder, and removes both afterwards, also when an
  interrupt, terminate or hang-up signal stops the run. The sandbox makes a
  scratch clone from the mirror, fetches the tip into it (no tags, no
  hardlinks, no alternates), checks that it is exactly the reviewed tip,
  checks it out detached and runs each gate from the root of the tree. The
  home folder is empty: there is no provider login and no lead credential.
- **Limits.** 30 minutes (`--timeout`) and 4 MiB of output. A gate that ends
  with an output over the cap, or past the time, stops the run and the run is
  not a pass.
- **What a pass means.** The gates ran inside the sandbox on the tip's tree,
  and each exited 0 within the limits. The tip's own build files decide what a
  gate does. A pass is evidence for the reviewer. It is not proof that the
  code is correct, safe or free of hostile behaviour. A line
  `ataniq-verify: gate NAME` in the output can be imitated by a gate and
  proves nothing.
- **The records.** The result is written to the records folder as a JSON file
  named `<UTC time>-verification-<id>.json`. It holds the project, the lead ref,
  the tip, the gates, the digest of the plan, the outcome and the hash and
  length of the output. The raw output, if there was any, is written beside it
  with the suffix `.out`. Nothing ever overwrites or deletes a record. The
  command prints the outcome and the last 40 lines of the output, escaped. A
  failed run is recorded too: the newest record decides, so a later failure
  withdraws an earlier pass.

The exit status is 0 only for a pass. A pass whose record was stored but could
not be synced to disk is still a pass: the command exits 0 and warns on
standard error.

#### Merge mode of verification

Merge mode is the mode of the intake's review as well as of its verification: the owner channel (tagged build only, not qualified) reviews a merge the host built, with the flag kind `odd-path` and a fuller summary, and verifies it.
The package `internal/intake/verify` and the records store also verify a merge
that the host built, which sits under a pin in `refs/ataniq/pending/`. No
command offers this mode: the host adapter of the owner channel (package
`internal/approval/hostadapter`, tagged build only) calls it for each merge it
builds, with the pin, the merge and the target's tip it was built on, and counts
the records of this mode by the rules below before it moves a target. It differs
from the verification of a lead branch in these ways:

- **The request.** `Merge` is set, `TipRef` is the pin (a name under
  `refs/ataniq/pending/` only) and `Base` is the target's tip the merge was
  built against. A pin is refused in lead mode, a lead ref in merge mode, a
  base in lead mode, and merge mode without a full-length base.
- **The script.** It fetches the pin by its exact name, checks that it is
  exactly the merge that was reviewed, and then checks that this commit has
  exactly two parents and that the first is the base. Any other shape (one
  parent, three, none, the parents the other way round, another first parent)
  ends the run with status 93 before any gate runs. The run is not a pass.
- **The record.** A record of merge mode holds `"merge": true` and the `"base"`
  next to the pin, as `tip_ref`, and the merge, as `tip`. A record of a lead
  branch has neither key and is written and read as before; the store refuses
  a pin in a lead-mode result and a lead ref in a merge-mode one.
- **The evidence.** `integrate` of a merge counts only merge-mode records of
  its project whose merge is the tip being integrated and whose base is the
  target's tip the review was made against. A record of a lead branch for the
  same commit id, and a merge record made against another base, are not
  evidence and do not withdraw a pass either. Among the records that count, the
  newest decides, as for a lead branch: a later failed run withdraws an earlier
  pass. `integrate` of a lead branch ignores merge-mode records in the same
  way.

### integrate

`integrate` moves the upstream branch to the lead's tip. You sign off by
typing arguments:

- `--tip` and `--digest`, as `review` printed them. They bind the sign-off to
  the review you saw.
- `--accept-change`, which accepts the change as a whole.
- one `--accept-path` for each path under "Needs your sign-off", as printed.
- the same `--path-rule` list you gave to `review`. The digest covers the
  flags, not the list: a list that flags a different set of paths changes the
  digest, and `integrate` refuses; a rule that flags nothing in this change
  goes unnoticed.

The command then does these things, in this order, and stops at the first that
fails:

1. It reviews again, with the refs as they are now. A tip that does not
   contain the upstream branch, or a missing upstream branch, stops here. The
   command never creates a branch. A lead branch that is already at the
   upstream branch is refused: there is nothing to integrate.
2. The fresh review's tip and digest must be the ones you signed. If either
   moved, it refuses and names the old and new commit ids.
3. The sign-off must be complete: the change accepted, every flagged path
   accepted, and no accepted path that is not flagged. It lists everything
   wrong at once, with the exact arguments to add or to remove.
4. The newest verification record for this project and this tip must be a
   clean pass. A pass whose output was cut, or that was stopped, is not clean.
   Any pass for this tip counts, whatever gates it ran: the command does not
   compare the record's gates with anything. `review --records` shows the
   gates of the records before you sign off, and `integrate` prints the gates
   of the pass it used after the move. Read them; the command cannot judge
   them.
5. The upstream branch moves to the tip with one compare-and-swap of
   `git update-ref` in the mirror, expecting the branch to be at the base the
   review saw. It is fast-forward only. The command never deletes a ref, never
   forces, and writes no ref but the target.
6. An intake record is written to the records folder, with the digest, the
   signed paths and the verification record that allowed the move.

If the branch moved and the record cannot be written, the command says so and
prints the record: keep that output.

If the update of the branch ends unclearly (a time-out, a signal or a kill of
git), the command reads the branch again. When it finds the branch at the tip,
it writes the record, prints it and exits 0 with a warning; do not run it
again. A record that was written but could not be synced to disk is the same:
exit 0 with a warning. When the command cannot read the branch, it says that
the outcome is unknown and exits with status 3, and the branch may have moved
without an intake record (see the limits below). `ataniq-intake help` lists
the exit statuses.

State plainly what this does and does not do:

- **Your working checkout is never touched.** The command writes no checkout,
  merge, pull, reset or push. It moves a branch in the bare mirror. Your
  checkout gets the commits later, by your own `git fetch` from the mirror.
- **After the sign-off the code runs as you,** as any merged code does. The
  review is the boundary. What you sign is what enters the history.

## The execution-config set

A path is in the set when one rule matches it. The set has three built-in
lists and any additions you give. Each rule has a short reason that `review`
prints. The lists come from `internal/intake/execconfig`.

**Design** rules, which the design requires:

| Pattern | Reason |
| --- | --- |
| `.claude/` | agent settings and hooks |
| `.codex/` | agent settings and hooks |
| `.mcp.json` | MCP servers start as programs |
| `.envrc` | runs on direnv allow |
| `.vscode/` | editor tasks and settings run programs |
| `.idea/` | editor run configurations run programs |
| `.githooks/` | git hooks run on git commands |
| `Makefile` | build entry point |
| `scripts/` | scripts the build and the tools run |
| `.gitattributes` | git filters and drivers run programs |
| `.gitmodules` | submodules fetch and run other code |
| `go.mod` | selects the Go toolchain and dependencies |

**Feature scope** rules, added by the lead-mirror feature:

| Pattern | Reason |
| --- | --- |
| `.git` | git config and hooks run programs |
| `.husky/` | git hooks run on git commands |
| `.pre-commit-config.yaml` | git hooks run on git commands |
| `lefthook.yml` | git hooks run on git commands |
| `.agents/` | agent settings and skills |
| `.github/` | CI workflows and actions |
| `.gitlab-ci.yml` | CI pipeline |
| `.circleci/` | CI pipeline |
| `.woodpecker/` | CI pipeline |
| `.woodpecker.yml` | CI pipeline |
| `Jenkinsfile` | CI pipeline |

**Added relatives**, close relatives of the two lists above:

| Pattern | Reason |
| --- | --- |
| `.cursor/` | agent settings and rules |
| `.gemini/` | agent settings and hooks |
| `.aider.conf.yml` | agent settings |
| `CLAUDE.md` | agent instructions |
| `CLAUDE.local.md` | agent instructions |
| `AGENTS.md` | agent instructions |
| `AGENTS.override.md` | agent instructions |
| `GEMINI.md` | agent instructions |
| `.cursorrules` | agent instructions |
| `GNUmakefile` | build entry point |
| `*.mk` | included by make |
| `Justfile` | task entry point |
| `Taskfile.yml` | task entry point |
| `Taskfile.yaml` | task entry point |
| `magefile.go` | task entry point |
| `lefthook.yaml` | git hooks run on git commands |
| `go.work` | selects Go modules and toolchain |
| `go.sum` | selects dependency contents |
| `go.work.sum` | selects dependency contents |
| `.go-version` | selects the Go toolchain |
| `.tool-versions` | selects tool versions |
| `.mise.toml` | selects tools and runs tasks |
| `mise.toml` | selects tools and runs tasks |
| `.npmrc` | package manager settings and scripts |
| `package.json` | package scripts and dependencies |
| `.devcontainer/` | container setup commands |
| `.devcontainer.json` | container setup commands |
| `devcontainer.json` | container setup commands |
| `Dockerfile` | container build commands |
| `.env` | environment of every command |
| `.env.*` | environment of every command |

### Project additions

`--path-rule PATTERN` adds a pattern to the set, after the built-in ones. It
may be given more than once, up to 256 times. Its reason is "project
addition". Give the same list to `review` and to `integrate`.

The syntax is the same as for the built-in rules. A pattern is split at `/`
into elements.

- `name` matches any path that has an element `name`, at any depth, the last
  element included. A tracked file can become a folder and the reverse, so
  both are matched.
- `name/` matches the same paths as `name`. The matcher sees no file modes, so
  a folder rule cannot ask for a folder. The trailing `/` is for the reader.
  `scripts/` therefore also matches a file named `scripts`.
- A leading `/` anchors the pattern at the root of the repository.
- `*` matches any run of bytes inside one element, and `?` matches one
  character inside one element. Nothing else is special. Brackets and
  backslashes are refused.
- A pattern with several elements, such as `a/b`, matches a run of consecutive
  path elements.
- An element must not end in a dot or a space, and a pattern may not hold an
  empty element, `.` or `..`. A pattern is at most 255 bytes.

Matching ignores ASCII case: only `A` to `Z` fold. `makefile` matches the rule
`Makefile`. An element is also tried without its trailing dots and spaces, so
`.claude.` matches `.claude`. Other folding, such as Unicode normalisation and
other file system short names, is not done, except that the element `git~1`
also matches as `.git`.

A symbolic link is matched by its own name only. A link named `cfg` that points
at `.claude` does not match a rule. That is why the review flags symbolic links
and what they alias.

## How lead text is shown

Everything a lead wrote is hostile: ref names, paths, commit subjects, author
names, symlink targets, diff content and the output of gates. The command
shows it only through the `leadtext` package.

- **Escaping.** Printable ASCII passes, except the backslash, which is shown
  as two. Newline, return and tab are `\n`, `\r` and `\t`. Other control
  bytes and bytes that are not UTF-8 are `\xNN`. Invisible and dangerous
  characters become `\u{HEX}`: bidirectional controls, zero-width characters,
  variation selectors, line and paragraph separators, the braille blank,
  private use and unassigned code points. The escaping can be undone. For
  `--branch`, `--target` and `--accept-path`, you type a name as printed, and
  the command unescapes it, so a name with odd bytes can be named exactly.
- **Bytes that are not UTF-8.** A commit message, name or path that is not
  valid UTF-8 is shown escaped and never crashes the command. A commit that
  declares an encoding other than UTF-8 is not shown at all: the review is
  refused (see [review](#review)).
- **Layout.** A line that starts in the first column is the command's own.
  Lead text is on indented lines, under a heading that carries the label
  `unverified lead data`. The output of a gate is shown on lines that start
  with a bar after the indent.
- **The label.** Keep it near the text. Escaped text is still the lead's text:
  a lead can write a sentence that reads as an instruction or a status.

What is not handled: look-alike letters (homoglyphs). Two names that differ
only by look-alike letters stay possible. A tool that compares names must
compare bytes, not the escaped text.

## A worked example

This is a toy: one project, one lead and one small change. The output below was
produced by the command built from this branch. To run it without a real
sandbox, the template used a stand-in launcher that confines nothing, and the
`STATE` and other paths are shortened to the names used in this guide. Never
use a launcher like that for a real lead.

Create the mirror. Then put the upstream branch `develop` in it. Here that is
a fetch of your own repository into the mirror, which is a write by the owner:

```text
$ ataniq-intake init --state /srv/ataniq-state --project myproject
Created the exchange mirror of project myproject.
Mirror: /srv/ataniq-state/mirrors/myproject.git
Next: fetch a lead's branches into it with 'ataniq-intake fetch'.
$ git --git-dir=/srv/ataniq-state/mirrors/myproject.git fetch /path/to/my/repo develop:refs/heads/develop
```

The lead `coder` has made a commit on a branch `feature` in its clone at
`/srv/ataniq-state/leads/coder/tree`. It changed `Makefile` and added
`docs/notes.md`. Its commit subject holds a terminal escape sequence. Fetch it:

```text
$ ataniq-intake fetch --state /srv/ataniq-state --project myproject --role coder --sandbox /srv/ataniq-sandbox.json
Fetched the branches of the lead "coder" into the mirror.
Lead object store (the lead's own statement): 32768 bytes
Mirror before: 36864 bytes
Added by this fetch: 32768 bytes
Mirror after: 57344 bytes
Duration: 46ms
Ref updates (unverified lead data):
  created refs/leads/coder/heads/feature 30205c7864875913553896f203f0900b33718534
```

Review it. With `--records` the review also says what verification the tip
has; here there is none yet:

```text
$ ataniq-intake review --state /srv/ataniq-state --project myproject --role coder --branch feature --target develop \
    --records /srv/ataniq-records
Review of a lead branch against an upstream branch
Branches (unverified lead data):
  lead branch: coder/feature
  upstream branch: develop
Base: 737e1bd27db924f3bce4a7c054c43ab9b53c6431
Tip: 30205c7864875913553896f203f0900b33718534
Digest: ea76d76545258473b87fe4c472a8a6dbc3a8b5b714e901f98800c13ef4cacbbe
Commits, newest first (unverified lead data):
  30205c7864875913553896f203f0900b33718534  2026-11-17T15:39:05Z  Owner <owner@example.org>  add notes\x1b[31m and a build step
Changed paths: status, old mode, new mode, path (unverified lead data):
  M  100644  100644  Makefile
  A  000000  100644  docs/notes.md
Verification: no verification recorded for this tip
Needs your sign-off (unverified lead data):
  path: Makefile
    exec-config: build entry point
    to accept it, add: --accept-path 'Makefile'
To sign off the change as a whole, add: --accept-change
To integrate, give integrate this tip and this digest: --tip 30205c78... --digest ea76d765...
```

(The last line is shortened here. The command prints the full ids.) Only
`Makefile` needs its own sign-off. The escape sequence in the subject is shown
as `\x1b[31m`, and nothing reached the terminal.

Run a gate. The gate quotes its command, because it is passed as one argument:

```text
$ ataniq-intake verify --state /srv/ataniq-state --project myproject --role coder --branch feature \
    --gate check='test -f docs/notes.md && make' --sandbox /srv/ataniq-sandbox.json \
    --records /srv/ataniq-records --work /srv/ataniq-work
Verification of a lead branch
Branch (unverified lead data):
  coder/feature
Tip: 30205c7864875913553896f203f0900b33718534
Gates: 1, plan digest 505d2fcf3018c152a40f1342611bee07e1cee200d76afa8ef1f43ee293d6e7b6
Outcome: pass
Exit status: 0
Duration: 33ms
Stored: a verification record for the tip, output 58 bytes, sha256 a0c1fbd3...
Output tail (unverified lead data):
  | ataniq-verify: gate check
  | echo build
  | build
  | echo more
  | more
```

Integrate without the path sign-off. The command refuses, moves nothing, and
says what to add:

```text
$ ataniq-intake integrate ... --accept-change
ataniq-intake integrate: refused: the sign-off is incomplete. Nothing was moved.
  - a flagged path is not accepted (exec-config): add --accept-path 'Makefile'
Paths above are unverified lead data, shown escaped.
```

The exit status is 1. Add the path:

```text
$ ataniq-intake integrate --state /srv/ataniq-state --project myproject --role coder --branch feature \
    --target develop --tip 30205c78... --digest ea76d765... \
    --accept-change --accept-path Makefile --records /srv/ataniq-records
Integrated: the upstream branch moved, fast-forward only.
Branches (unverified lead data):
  upstream branch: develop
  lead branch: coder/feature
Old: 737e1bd27db924f3bce4a7c054c43ab9b53c6431
New: 30205c7864875913553896f203f0900b33718534
Record: intake of 2026-11-17T15:39:21Z, 1 commits, 2 changed paths, 1 signed paths
Review digest: ea76d76545258473b87fe4c472a8a6dbc3a8b5b714e901f98800c13ef4cacbbe
Verification: a pass of 2026-11-17T15:39:20Z, plan digest 505d2fcf..., output sha256 a0c1fbd3...
Gates that the pass stands for (your records, shown escaped):
  gate check: test -f docs/notes.md && make
Signed paths (unverified lead data):
  Makefile
Your working checkout was not touched. It gets the commits with its own fetch from the mirror at /srv/ataniq-state/mirrors/myproject.git.
```

Reviewing again with `--records` now shows the pass and its gate:

```text
Verification of this tip (your records, shown escaped):
  pass of 2026-11-17T15:39:20Z, plan digest 505d2fcf..., the newest, which decides
    gate check: test -f docs/notes.md && make
```

A second `integrate` with the same arguments moves nothing and exits with
status 1: "nothing to integrate: the lead branch coder/feature is at the
upstream branch develop already".

In the `integrate` lines above, `30205c78...` and the other shortened values
stand for the full ids and digests, which the command requires in full. Your
own checkout then gets the commit with its own fetch from the mirror.

The records folder now holds a verification record, its `.out` file and an
intake record.

## Limits, and what is not built

- **No quota.** The size of the mirror is a check made before and after a
  fetch, not a quota. A fetch that ends over the limit has already moved the
  refs.
- **"Offered", not "authored".** A ref under a lead's namespace says that the
  role offered that commit. It does not say that the role wrote it or that its
  objects came from that role's clone. A lead can point a ref at any commit in
  the mirror, such as another lead's, because the mirror is readable by every
  lead and a check would protect nothing. Commit authorship is lead text.
- **A pass is evidence, not proof.** The record of a pass is not compared with
  your project's gates. `review --records` shows the gates before the sign-off
  and `integrate` prints them after the move. Read them yourself.
- **After "outcome unknown", the branch may have moved without a record.** When
  `integrate` says the outcome of the update is unknown (exit status 3), look
  at the upstream branch with `ataniq-intake branches`. If it is at the tip,
  the change is in and no intake record was written: keep the output of the
  command, because running `integrate` again only says "nothing to integrate".
- **Intermediate commits enter history.** A fast-forward brings every commit of
  the branch, not only the net change. That is why `history-only` exists.
- **No project registry.** The execution-config additions are options you must
  repeat on `review` and `integrate`. Nothing stores them for a project.
- **No owner channel.** The sign-off is typed arguments, and the command cannot
  tell who typed them. The lead cannot run `integrate`, because the mirror is
  read-only in its sandbox and the records folder is not mounted there. That is
  a job of the sandbox profile and is not checked here. The profile that
  [the lead sandbox guide](lead-sandbox.md) proposes binds the mirror
  read-only; it is unqualified, and nothing launches a lead in it, except in a build with
  the `ataniq_canary` tag, under `--canary`.
- **Same-user processes outside a sandbox are not fenced.** The state root, the
  mirror and the records are protected by file modes of one user. A process of
  the same user that is not in a sandbox can write them.
- **Not wired.** The fetch is not yet called by a daemon or a handoff: the
  daemon skeleton `ataniqd` is read-only and calls nothing here (see
  [the lead daemon guide](lead-daemon.md)). You run the commands by hand. The
  canary slice, which exists only in a build with the `ataniq_canary` tag and
  under `--canary`, does not change that: it joins the daemon, the holder, the
  sandbox and an injected message to a throwaway lead, and the owner still
  types `fetch`, `review`, `verify` and `integrate` (see
  [the canary slice guide](canary-slice.md)).
- **Qualification is separate.** Nothing here qualifies a sandbox. The opt-in
  harness and its manual are in
  [the lead mirror qualification manual](qualification-lead-mirror.md).
