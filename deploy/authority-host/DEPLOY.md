# Deploying the authority host by hand

This is the manual sequence for the first deploys of the authority host and for
every later one: set the host up once (`bootstrap/host-bootstrap.sh`), then put
each release on it (`deploy.sh`). The layout, the switch and its policy are
described in [the README](README.md); this page only says what to run, in what
order, and what to do when a step fails. Nothing here is automatic: every step
is run by the owner, or with the owner's word, and every script shows what it
will do first (`--dry`).

The host is never named in a file. The deploy driver reads it from the
environment variable `AUTHORITY_HOST_SSH` (`USER@HOST` or an ssh alias), set in
your own shell, and prints only `the authority host`:

```
export AUTHORITY_HOST_SSH=USER@HOST     # your value; not in any file of this repository
```

## What you need first

- On the development host: Git, Make, `sh`, `rsync`, `ssh`, the repository's
  toolchain (`make doctor` passes) and a way to run `ah-release` (see step 2).
- On the authority host: the deploying user can log in with a key, without a
  password (`ssh -o BatchMode=yes` never asks), has a folder it owns for
  incoming releases (default `~/incoming`, outside `/opt/authority-host`; make it
  once with `mkdir -m 0700 ~/incoming`) and may run
  `sudo /usr/local/sbin/ah-switch` (see "Who may switch" below).
- Root on the authority host, your own way in, for the bootstrap and for
  anything the switch asks the owner to do.

## The first deploy

### 1. Build the binaries, static

Both programs that run on the host are built without cgo, so they depend on
nothing installed there. From the repository root on the development host:

```
mkdir -p build/authority-host
CGO_ENABLED=0 sh scripts/go-tool.sh build -trimpath -o build/authority-host/ah-switch ./cmd/ah-switch
CGO_ENABLED=0 sh scripts/go-tool.sh build -trimpath -o build/authority-host/ah-noaicheck ./cmd/ah-noaicheck
ldd build/authority-host/ah-switch      # "not a dynamic executable"
sha256sum build/authority-host/ah-switch
```

Keep the `sha256sum` line: you type that hash on the host in step 3. Do not copy
it from the host.

### 2. Build the release

`ah-release build` makes the release folder, named for its id
(`COUNTER-COMMIT`, for example `0007-ab12cd3`): the `MANIFEST`, `SHA256SUMS`,
the `bin/`, `etc/` and `share/` of [the layout](README.md#layout), and, in
signed mode, `SHA256SUMS.sig`. Run `ah-release build --help` for its inputs (the
binaries from step 1, the counter, where to write). The counter must be above
every counter the host has seen. Then check it:

```
ah-release check --no-noaicheck DIR/ID
```

`--no-noaicheck` is how the development host checks a release: it reads the
folder back (sums, manifest, shape, the coverage of the lists) without running
the release's own no-AI check, which would look at this machine, not at the
authority host, and report its agent tools. Run the no-AI check where it is
meant to pass: on the authority host, where the switch runs it itself on the
verified copy of the release before anything changes (see step 4), or here
with `--root` pointing at a copy of the host's tree. `deploy.sh` runs the same
`ah-release check --no-noaicheck` again before it copies anything.

If the release is to pass the no-AI check under `--require-policy`, fill the
allowlists of the source tree from the host's baseline first, with
`ah-release baseline` (see [Filling the
allowlists](README.md#filling-the-allowlists)): collect four files on the host
(`HIT unit` names, `ss -H -ltn` ports, `pacman -Qq`, `nft list ruleset`), run
`ah-release baseline --src deploy/authority-host ... --out DIR` and build with
`--src DIR`.

### 3. Set the host up, once: bootstrap, dry then apply

Copy the script and the switch binary to the authority host (any way you trust;
this one uses your own ssh), from the development host:

```
rsync -a -e 'ssh -o BatchMode=yes' deploy/authority-host/bootstrap/host-bootstrap.sh \
  build/authority-host/ah-switch "$AUTHORITY_HOST_SSH":incoming/
```

Then, on the host, as root: read the script, copy both files to a folder only
root can write (for example `/root`) and run them from there, because the
incoming folder belongs to the deploying user, who could change a file in it
after you read it.

```
sh /root/host-bootstrap.sh --dry   --switch-bin /root/ah-switch --switch-sha256 HASH_FROM_STEP_1
sh /root/host-bootstrap.sh --apply --switch-bin /root/ah-switch --switch-sha256 HASH_FROM_STEP_1
```

`--dry` is the default and changes nothing; it prints every step and checks the
hash and every folder above what the script touches. `--apply` then:

- creates `/opt/authority-host`, `/opt/authority-host/releases`,
  `/etc/authority-host` and `/var/lib/authority-host` (0755, root:root) and
  leaves `/srv/authority/public` alone;
- installs `/etc/authority-host/stop.list` with one command, `/usr/bin/true`
  (the static host has no decision service yet; replace the line when one exists;
  a list that is already there is kept);
- installs the switch as `/usr/local/sbin/ah-switch` (0755, root:root), only when
  its SHA-256 is the one you gave;
- writes no `switch-policy.json`: without it the switch is in manual mode, which
  the first scratch deploys may use (see "Owner-only items").

With `--link-caddy` it also makes `/etc/caddy/Caddyfile` a link to
`/opt/authority-host/current/etc/caddy/Caddyfile`, keeping a regular Caddyfile
once as `/etc/caddy/Caddyfile.pre-authority-host`. Do not use it before the
first switch has made `current`: until then the link dangles and caddy cannot
read its configuration. The script never starts, restarts, reloads or enables a
service; do that yourself, after the first switch (step 5). It is safe to run
again: what is already right is left as it is.

### 4. Deploy: dry, then go

Before the first deploy read the release's `share/reload.list` and
`share/health.cmd`: they say whether the switch reloads caddy and whether its
health check needs caddy to be serving the release's own Caddyfile. The first
deploy has to follow an order that satisfies them (step 5 says what to do when
caddy has to be pointed at `current` after the switch).

On the development host:

```
sh deploy/authority-host/deploy.sh --dry --release DIR/ID
sh deploy/authority-host/deploy.sh --go  --release DIR/ID --confirm ID
```

`--dry` checks the release with `ah-release check --no-noaicheck`, runs
`rsync -n` (a list of what would be copied; nothing is copied) and prints the
exact ssh command that `--go` runs afterwards:

```
sudo /usr/local/sbin/ah-switch --from "$HOME/incoming/ID" ID
```

`--go` repeats the check, copies the release to the host's incoming folder with
`rsync -a --delete --itemize-changes` (what it prints is rsync's list of
changes), runs that command over `ssh -o BatchMode=yes` and streams its output.
It is refused unless `--confirm` is the release id, so a pasted command cannot
deploy another release. `--incoming-base PATH` changes where on the host the
release goes, `--ah-release FILE` names the release tool, and `--skip-check`
skips `ah-release check` with a warning (use it only when you checked the
release another way). A failing step stops the later ones: when the copy
fails nothing is switched.

The driver never runs a file of the incoming folder: that folder belongs to
the deploying user, and a program in it would run as root before anyone had
verified it. The no-AI check is run by the switch itself: after it has
imported and verified the release (sums, and the signature in signed mode) into
the root-owned `releases/ID`, and before anything changes, it runs that copy's
no-AI check, phase `pre`, with `--require-policy`, and refuses the switch when
it finds anything (see [the switch's steps](README.md#steps)). `--require-policy`
turns an empty allowlist into a hit, so fill the allowlists first (see [Filling
the allowlists](README.md#filling-the-allowlists)). A line starting `HIT` in
the switch's output says what it found.

The switch imports the release into `/opt/authority-host/releases/ID` as new,
read-only, root-owned files, runs its checks, no-AI check, validators and reloads
as [the README](README.md#steps) describes, and exits 0 only when the release is
healthy.

### 5. Verify

On the host:

```
sudo /usr/local/sbin/ah-switch status
curl -fsS http://127.0.0.1/healthz      # the address and port your Caddyfile serves it on
```

`status` shows `current` as `releases/ID`, the highest counter, the trust mode
(`manual` or signed), no transaction to resume, decisions not inhibited and the
last outcome from `deploy.json` as `ok`. The release's `share/health.cmd`
checks the same things the switch checked; read it to see what "healthy" means.

If the Caddyfile is still the old one, link it now (run the bootstrap again with
`--link-caddy`, dry first) and restart caddy yourself, then run `status` and
the health check again. Between the first switch and that restart the host
keeps serving whatever caddy already has loaded.

### 6. Roll back

To go back to the release `previous` points at (it is there after the second
switch; the first switch has nothing to go back to):

```
sudo /usr/local/sbin/ah-switch --dry --rollback-to previous
sudo /usr/local/sbin/ah-switch --rollback-to previous
```

It accepts that release's id too and refuses any other. A rollback does not need
the incoming folder. A rollback skips the rule that a counter must be higher,
but the release it goes back to is checked and qualified again like any other.

## Owner-only items

These are written by the owner, on the host, never by a script of this folder
and never in this repository:

- **The signers file and the switch policy.** Signed mode needs an allowed
  signers file (for example `/etc/authority-host/allowed_signers`) and
  `/etc/authority-host/switch-policy.json` with `min_mode`, `allowed_signers` and
  `principal`, both root-owned and not writable by group or others, with every
  folder above them. See [the switch policy](README.md#the-switch-policy). The
  release is then signed with the owner's key, which stays off both hosts that
  build and deploy: `ssh-keygen -Y sign -f KEY -n authority-host-release SHA256SUMS`.
  Without the policy every switch is in manual mode and accepts an unsigned release.
- **The host's stop list.** `/etc/authority-host/stop.list` is the owner's own;
  the bootstrap installs the single line `/usr/bin/true` only when the file is
  missing, and never changes a list that exists. Put the real stop commands in it
  when a decision service exists.
- **The word for the switch's owner commands.** `ah-switch inhibit-clear --asked WORDS`
  and `ah-switch ack --asked WORDS` act on the owner's word; nobody types it for
  the owner. See [the decision inhibit](README.md#the-decision-inhibit) and
  [the journal and recover](README.md#the-journal-and-recover).
- **Who may switch.** The driver needs `sudo /usr/local/sbin/ah-switch` without a
  password for the deploying user (`BatchMode`, no terminal). The switch runs
  what a release contains as root, so in manual mode whoever can put a folder in
  the incoming folder and run the switch holds root on the host. Grant that
  sudo right only for that program, only to the deploying user, and move to
  signed mode before the host is relied on. Without such a rule `--go` copies the
  release and then fails at the ssh step; run the printed command yourself, in a
  terminal on the host, instead.

## When a step fails

Nothing in this sequence runs on its own, so a failed step leaves a known
state; read the message, fix the cause, run the step again.

| Step | What happened | What to do |
| --- | --- | --- |
| Build | The binary is not static, or `ldd` lists libraries | Build with `CGO_ENABLED=0`; do not deploy it |
| `ah-release check` | The release does not check | Nothing was copied. Rebuild the release; do not use `--skip-check` to get past a failure |
| Bootstrap | `refused: ... writable by its group or by others`, `is a symbolic link` or `not owned by root` | Nothing was changed. Fix that folder (it must be a real folder, root-owned, mode without group or other write) and run the dry run again |
| Bootstrap | `refused: sha256 of the switch binary is ... not ...` | Nothing was changed. The file on the host is not the one you built; copy it again and compare, do not paste the host's hash |
| Bootstrap | `Caddyfile.pre-authority-host exists and is never overwritten`, or the Caddyfile is a link to elsewhere | Nothing was changed. Move the old backup (or link) away by hand, then run again |
| `deploy.sh` | `AUTHORITY_HOST_SSH is not set`, a bad release id or a bad `--incoming-base` | Nothing was run. Set the variable, or name a release folder `COUNTER-COMMIT` and a safe incoming folder |
| `deploy.sh --dry` | `rsync -n failed` | Nothing was copied. Check that the incoming folder exists on the host and belongs to the deploying user, and that ssh works in batch mode |
| `deploy.sh --go` | rsync failed | The switch was not run. The partial copy is only in the incoming folder; run `--go` again (rsync resumes) |
| `deploy.sh --go` | the switch step failed (the output above it is the switch's) | The release is in the incoming folder, possibly already imported. Read the switch's `status` and its outcome, then see below |
| Switch | the no-AI check found something (`HIT` lines) | Nothing was changed. Read the `HIT` lines (and the README's [no-AI check](README.md#the-no-ai-check)), fix the host or the release, and run `--go` again |
| Switch | `rolled-back` (a reload or health check failed) | The old release is live again. Read why in `ah-switch status`, fix the release and deploy a new one with a higher counter |
| Switch | `failed` or `failed-recovery` | The decision services were stopped and decisions stay inhibited; the console is needed. Follow [the failure cases](README.md#failure-cases), [the decision inhibit](README.md#the-decision-inhibit) and [the journal and recover](README.md#the-journal-and-recover); `inhibit-clear` and `ack` are the owner's words |
| Switch | exit 3 | Another switch holds the lock; wait for it, then run again. Nothing was done |
| Switch | exit 2 | Bad arguments or a root other than `/` without `--allow-exec`; nothing was done |
| Any | You are not sure what the host holds | `sudo /usr/local/sbin/ah-switch status` changes nothing and says what is live, what is owed and what is to resume |

A release that was imported stays in `releases/` when a later step fails: switch
to it again without `--from` (`ah-switch ID`), or go back with `--rollback-to
previous`. Never copy a folder into `releases/` by hand: the switch refuses it
as `provenance-missing` and it is not a release.
