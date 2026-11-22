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
  once with `mkdir -m 0700 ~/incoming`) and may run the switch form of
  `sudo /usr/local/sbin/ah-switch`, and nothing else of it (see "Who may
  switch" below).
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
sha256sum build/authority-host/ah-switch deploy/authority-host/bootstrap/host-bootstrap.sh
```

Keep the two `sha256sum` lines (the switch binary and the bootstrap script): you
type those hashes on the host in step 3. Do not copy them from the host.

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
(`HIT unit` names, `ss -H -ltn` ports, `pacman -Qq`, `nft list ruleset`; the
unit names come from running the unfilled release's own checker as root, so
verify a root-owned copy of that release first, as the README says there), run
`ah-release baseline --src deploy/authority-host ... --out DIR` and build with
`--src DIR`. `baseline` enrolls any firewall listing, but it prints a `WARNING:
no-default-deny HOOK` line (and writes the same words into the header of
`share/nft.digest`) for each of the hooks `input`, `output` and `forward` that has
no active base chain of type `filter` with `policy drop` (a chain in a table that
is not dormant, covering IPv4 and IPv6); read them before you build.
`--require-default-deny` makes it refuse such a listing instead. It reads only
the first rule of such a chain: a first rule that is an unconditional `accept`
makes the chain not count, while later accepts and jumps are not read, so a
chain with a drop policy whose rules accept everything in another way still
counts. The warning therefore says that a base chain drops by policy, never
that its rules drop anything: read the rules yourself. The host's firewall is
the owner's: the baseline pins the rules as they are and does not judge them.

The baseline records what listens while you collect it, and step 3 leaves
caddy stopped, so on a fresh host `ports.txt` holds the ssh port and not the
front end's. The switch restarts caddy and then runs the no-AI check of phase
`post`, which reports every listening port that `ports.allow` does not name:
the first switch would end `failed` on its own service. So read `ports.txt`
and add the line 80 to ports.txt (the port of the shipped Caddyfile's `:80`
site; use the ports your own Caddyfile serves) before you run `ah-release
baseline`, and check that it is in `share/ports.allow` of the release you
build.

### 3. Set the host up, once: bootstrap, dry then apply

Copy the script and the switch binary to the authority host (any way you trust;
this one uses your own ssh), from the development host:

```
rsync -a -e 'ssh -o BatchMode=yes' deploy/authority-host/bootstrap/host-bootstrap.sh \
  build/authority-host/ah-switch "$AUTHORITY_HOST_SSH":incoming/
```

Then, on the host, as root, copy both files into a new folder only root can
write, and judge and run only those copies. The incoming folder belongs to the
deploying user, who can change a file in it at any moment, so nothing in it is
read, hashed or run in place:

```
mkdir -m 0700 /root/ah-bootstrap
cp /home/DEPLOYUSER/incoming/host-bootstrap.sh /home/DEPLOYUSER/incoming/ah-switch /root/ah-bootstrap/
sha256sum /root/ah-bootstrap/host-bootstrap.sh /root/ah-bootstrap/ah-switch
```

Compare the two hashes with the lines you kept in step 1, typed by hand: they
must be equal, and then the copies are the files you built. Read
`/root/ah-bootstrap/host-bootstrap.sh` (the copy, not the incoming file). From
here on run only the copies, even for `--dry`:

```
sh /root/ah-bootstrap/host-bootstrap.sh --dry   --switch-bin /root/ah-bootstrap/ah-switch --switch-sha256 HASH_FROM_STEP_1 --link-caddy
sh /root/ah-bootstrap/host-bootstrap.sh --apply --switch-bin /root/ah-bootstrap/ah-switch --switch-sha256 HASH_FROM_STEP_1 --link-caddy
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

`--link-caddy`, which the two commands above carry, also makes `/etc/caddy/Caddyfile` a link to
`/opt/authority-host/current/etc/caddy/Caddyfile`, keeping a regular Caddyfile
once as `/etc/caddy/Caddyfile.pre-authority-host`. Link the Caddyfile before the
first switch, in this step. The shipped release's `share/reload.list` restarts
caddy inside the switch (its Caddyfile has `admin off`, so there is nothing to
reload) and `share/health.cmd` then requires `/healthz` from it, so caddy has to
read the release's own Caddyfile when the switch restarts it. With the old
Caddyfile still in place the restart brings up the old configuration: the health
check fails, and the first switch has no previous release to go back to, so the
host is left in the failed state (or, if the old configuration happens to answer
`/healthz`, the switch qualifies it, which is no proof of the release). Until the
first switch the link dangles, because `current` does not exist yet, and caddy
cannot read its configuration: leave caddy stopped (do not start, restart or
enable it) until the first switch creates `current` and restarts it itself. If
it is already serving the old configuration, leave it alone: it keeps what it
loaded until that restart, and a reboot before the first switch would leave it
without a readable configuration. The script never starts, restarts, reloads or
enables a service. It is safe to run again: what is already right is left as it
is.

### 4. Deploy: dry, then go

Before the first deploy read the release's `share/reload.list` and
`share/health.cmd`: they say whether the switch reloads caddy and whether its
health check needs caddy to be serving the release's own Caddyfile. The shipped
ones need the Caddyfile link of step 3 to be in place before this step; if your
release's lists differ, make the order satisfy them. Read its `share/ports.allow`
too: it must name every port the release's services listen on (the shipped
front end: 80), because the switch's check after the reload refuses a listener
it does not name.

On the development host:

```
sh deploy/authority-host/deploy.sh --dry --release DIR/ID
sh deploy/authority-host/deploy.sh --go  --release DIR/ID --confirm ID
```

`--dry` checks the release with `ah-release check --no-noaicheck`, runs
`rsync -n` (a list of what would be copied; nothing is copied) and prints the
exact ssh command that `--go` runs afterwards. `--dry` contacts the host,
read-only (ssh and `rsync -n`). `rsync -n` starts a remote rsync to read the
incoming folder, so the host must be reachable; the remote rsync reads the
destination, no files are copied and the switch is not run. To see the plan without touching the network, `--self-test`
starts nothing: it computes and prints the same commands (it does not need
`AUTHORITY_HOST_SSH`, and runs neither `ah-release`, `rsync` nor `ssh`).

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
the allowlists](README.md#filling-the-allowlists)). The switch never prints the
check's `HIT` lines, in either phase: a refusal shows only `step 4/9 noaicheck:
FAILED: noaicheck-failed: exit status N (run it by hand to see its hits)`. The
hits are read by running the checker yourself (see "When a step fails").

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

Then acknowledge the outcome, from your own root session on the host (the
deploying user cannot run this, see "Who may switch"). Every switch owes its
outcome until you do, and while an outcome is owed the switch refuses a switch
to another release or a rollback (it prints the outcomes owed and does nothing
else). Running the same request again only reports the outcome and leaves it
owed; only your `ack` takes it back, so after every switch you acknowledge
before the next different switch or a rollback. `ack` names the transactions
it acknowledges: `status` prints the whole command, with the ids filled in
(`ah-switch ack --txn ID[,ID...] --asked WORDS`), and so do the switch's
result lines. Read the outcome in `status`, then copy the `--txn` value from
that output, never from a script or from an earlier session:

```
sudo /usr/local/sbin/ah-switch ack --txn TXN --asked "WHAT YOU ASKED OR SAW"
```

`ack` prints every outcome owed, records the time, your words and the
transactions in `deploy.json`, and removes `owed.json`. An `ack` whose `--txn`
is not exactly the transactions owed now is refused and changes nothing, so
words given before an outcome was seen never acknowledge it. Repeating an `ack`
for transactions already recorded keeps the first receipt (your first words and
time) and exits 0. Run `status` first and read what it says: the words are
yours, and nobody types them for you (see [the journal and
recover](README.md#the-journal-and-recover)).

Verification of the first deploy is not finished until the boot unit is
installed. Neither script installs it, and without it nothing requalifies
the host after a reboot: `ah-switch boot` runs from
`ah-switch-boot.service` at every boot, and the unit is the owner's to
install by hand. Install and enable it with the steps of [Boot
requalification](README.md#boot-requalification) in the README (the
unit's installation, the hash comparison, `systemctl enable`), then
confirm it with `systemctl status ah-switch-boot.service` and `ah-switch
status` after a reboot. `share/units.allow` of the release must name
`ah-switch-boot.service` before the unit is enabled: the no-AI check
finds every enabled unit its list does not name, so enabling it first
makes that check fail, at the next switch and at the boot itself. Add
the line to `share/units.allow` in the release source and deploy a
release built with it first.

### 6. Roll back

To go back to the release `previous` points at (it is there after the second
switch; the first switch has nothing to go back to). First acknowledge the last
outcome, if `status` shows one owed, as at the end of step 5 (the `--txn` value
is the one `status` prints): a rollback while an outcome is owed is refused.

```
sudo /usr/local/sbin/ah-switch ack --txn TXN --asked "WHAT YOU ASKED OR SAW"
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
  and `ah-switch ack --txn ID[,ID...] --asked WORDS` act on the owner's word;
  nobody types it for the owner, and the owner copies the `--txn` value from
  `ah-switch status` after reading the outcome, never from a script. See [the decision inhibit](README.md#the-decision-inhibit) and
  [the journal and recover](README.md#the-journal-and-recover).
- **Who may switch.** The driver needs `sudo /usr/local/sbin/ah-switch --from
  INCOMING/ID ID` without a password for the deploying user (`BatchMode`, no
  terminal), and only that form. The switch does not check who calls it, and
  it also has the owner's verbs: `inhibit-clear`, `ack`, `recover` with its
  options, `--rollback-to`, and a caller-chosen checker (`--noaicheck`, which
  the switch refuses when `SUDO_UID` or `SUDO_USER` is set, so the owner needs a
  real root login for it, not a root shell started with `sudo`) or alternate
  root (`--root`). A rule for the whole program would hand all of
  them to the deploy key, which lives on the development host. So give the
  deploying user this rule and no other for the switch, in a file of
  `/etc/sudoers.d` (made with `visudo -f`), with the user's name and home
  folder in place of `DEPLOYUSER` and `/home/DEPLOYUSER/incoming` (sudo 1.9.10
  or newer, for the regular expression that limits the arguments):

  ```
  DEPLOYUSER ALL=(root) NOPASSWD: /usr/local/sbin/ah-switch ^--from /home/DEPLOYUSER/incoming/[0-9]{4}[0-9]*-[0-9a-f]{7}[0-9a-f]* [0-9]{4}[0-9]*-[0-9a-f]{7}[0-9a-f]*$
  ```

  Check it from your own root session with `sudo -l -U DEPLOYUSER`: it must
  list that one line for the switch, and no other rule that lets the account
  run `ah-switch` or become root. `inhibit-clear`, `ack` and `recover` run
  only from the owner's own root session, never through this rule and never
  over the deploy key; `status`, `--dry` and `--rollback-to` are run there
  too. Any broader rule for the same account defeats the separation, for
  example a blanket `NOPASSWD: ALL`, which some desktop distributions install
  for the first user: the deploying account must not have one (or be in a
  group that has one). The rule does not make the switch safe on its own: the
  switch runs what a release contains as root, so in manual mode whoever can
  put a folder in the incoming folder holds root on the host. Move to signed
  mode before the host is relied on. Without the rule `--go` copies the
  release and then fails at the ssh step; run the printed command yourself, in
  a terminal on the host, instead.

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
| Switch | `step 4/9 noaicheck: FAILED: noaicheck-failed: exit status N (run it by hand to see its hits)`: the no-AI check found something before activation (`HIT` lines that only the checker prints, step `noaicheck`, phase `pre`) | The switch prints no `HIT` line, here or in phase `post`, and `ah-release check` prints only codes: when the checker finds a hit it exits 1, so the builder prints both `noaicheck-exit` and `noaicheck-hit` (and `noaicheck-hit` alone only for a checker that prints a hit and exits 0); the `HIT` lines themselves exist only when you run the checker. The release is imported by now (`releases/ID`), so, from a real root login, run `/opt/authority-host/releases/ID/bin/ah-noaicheck --phase pre --root / --release /opt/authority-host/releases/ID --require-policy` (the switch's own arguments) and read its `HIT` lines (`HIT policy ... empty` means an allowlist was not filled; the README's [no-AI check](README.md#the-no-ai-check) lists the kinds). If the result line says `result: failed at noaicheck; current unchanged (...)` and does not say the decision services are stopped, nothing was changed: fix the host or the release and deploy again (a release that must change is a new build with a higher counter, see the `release-exists` row). If it says they are stopped, the release's package step ran before the check: take the failed rows below |
| `deploy.sh --go` | the same release again: `step 0/9 import: FAILED: release-exists: releases/ID exists; --from imports only a new release (switch to it without --from)` | An earlier `--go` imported that release and stopped later, and the import is kept; this run imported nothing and moved nothing (rsync ran again, the switch refused at the import). It does rewrite the record: `ah-switch status` now shows this refusal as the last switch (`failed step: import`, `error: release-exists`) in place of how the earlier attempt ended, so read that from the output of the earlier `--go`; the `decisions:` line and any owed outcome in `status` still describe the host. If the earlier attempt's outcome is still owed (not acknowledged), the same request prints `settled: this switch to ID has been settled already; it is not done again` instead, and the outcome stays owed until you acknowledge it. Never `--go` the same id a second time. If the earlier attempt was refused before anything began (`result: failed at ... current unchanged`, such as the no-AI check of phase `pre`, a validator or a static release refused at packages) and the cause was on the host and is fixed, switch to the imported release from your own root session, without `--from` (`ah-switch ID`; the deploy key's sudo rule does not allow that form). Otherwise (the cause is in the release, or the switch step ran and used its counter, as after a `rolled-back` result, or the host was changed and the decision services stopped, as after a `failed` or `failed-recovery` one: take those rows below first) build a new release with a higher counter and deploy that |
| Switch | `phase post: ... (run it by hand to see its hits)`, `failed_step: postcheck` | The release was already activated and caddy restarted: this is not "nothing was changed", and the switch does not print the hits. The result depends on what there was to go back to. On the first switch there is no old release, so the result is `failed`: `current` is still the new release, the stop lists ran and decisions stay inhibited. On a later switch the old release is put back and qualified again (reload, health check and its own no-AI check of phase `post`): if that passes, the result is `rolled-back` (`health after going back: ok`, decisions not inhibited); if the fallback also fails, the result is `failed-recovery`, no release is known healthy, the decision services are stopped and decisions stay inhibited, and you acknowledge the owed outcome before any other switch (take the `failed-recovery` row below). Read which one `status` shows before you act. Do not run `--go` for the same id again: its counter is used and a new import of an existing id is refused. From your root session: `ah-switch status` (result, failed step, inhibit); verify the installed copy (`cd /opt/authority-host/releases/ID && sha256sum -c SHA256SUMS`); save the firewall with `nft list ruleset` into a file only root can read (`/root/nft-now.txt`), then, from a real root login, run `/opt/authority-host/releases/ID/bin/ah-noaicheck --phase post --root / --release /opt/authority-host/releases/ID --require-policy --nft-listing /root/nft-now.txt` to read the `HIT` lines (a `HIT port` means `ports.allow` lacks a listener the host now has). Fix the lists, acknowledge the owed outcome as in step 5 (a switch to another release is refused until you do), then build a release with a higher counter and deploy it: a healthy switch removes the inhibit (see [the decision inhibit](README.md#the-decision-inhibit) and [the failure cases](README.md#failure-cases)) |
| Switch | `result: rolled-back at STEP; current -> OLD; health after going back: ok`: a reload, the health check or the check of phase `post` failed, and the old release was put back and qualified again | The old release is live and was qualified again (reload, health check and no-AI check of phase `post`), and decisions are no longer inhibited. `status` shows the last switch as `rolled-back`, `health after going back: ok` and `decisions: not inhibited`. Read why (`failed step`, `error`, `detail`), acknowledge the outcome as in step 5, fix the release and deploy a new one with a higher counter: the counter of the failed one is used |
| Switch | `result: failed-recovery at STEP; current -> ID; health after going back: failed; the decision services are stopped`: going back did not work either (or the old release could not be used, then the `health after going back` part may be missing) | Not rolled back: no release is known healthy. The host's stop list and the releases' ran, the decision services are stopped and decisions stay inhibited (across a reboot). `status` shows the last switch as `failed-recovery`, with `decision services: stopped`, `health after going back: failed` and `decisions: inhibited`. The console is needed: from your own root session read the `back:` and `stop:` lines of the output and `detail` in `status`, look at what `current` points at and at the services by hand, and repair the host. Then acknowledge the owed outcome first (`ack`, as in step 5): a switch to any other release, one with a higher counter included, is refused while it is owed. After that the host is qualified again by a healthy switch (a new release with a higher counter, which removes the inhibit), or by your word, `inhibit-clear --asked WORDS` after you looked at the host. See [the failure cases](README.md#failure-cases), [the decision inhibit](README.md#the-decision-inhibit) and [the journal and recover](README.md#the-journal-and-recover) |
| Switch | `result: failed at STEP; current -> ID; the decision services are stopped`, with no `health after going back` part: a first switch (no old release to go back to), or a failure after the package step changed the host | As `failed-recovery`: the decision services are stopped and decisions stay inhibited, and on a first switch `current` stays the new release. Repair the host, acknowledge the owed outcome first (`ack`, as in step 5: a switch to another release is refused while it is owed), then a healthy switch of a higher counter, or `inhibit-clear`; `status` shows `failed` and `decision services: stopped` |
| Switch | `refused: an outcome is owed` | Nothing was done. Read the outcomes it printed and `status`, then run the `ah-switch ack --txn ID[,ID...] --asked WORDS` that `status` prints (the `--txn` copied from it, exactly the ids owed) from your own root session, and run the request again |
| Switch | exit 3 | Another switch holds the lock; wait for it, then run again. Nothing was done |
| Switch | exit 2 | Bad arguments or a root other than `/` without `--allow-exec`; nothing was done |
| Any | You are not sure what the host holds | `sudo /usr/local/sbin/ah-switch status` changes nothing and says what is live, what is owed and what is to resume |

A release that was imported stays in `releases/` when a later step fails: switch
to it again without `--from` (`ah-switch ID`), or go back with `--rollback-to
previous`. Never copy a folder into `releases/` by hand: the switch refuses it
as `provenance-missing` and it is not a release.
