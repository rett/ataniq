# Deploying the authority host by hand

This is the manual sequence for the first deploys of the authority host and for
every later one: set the host up once (`bootstrap/host-bootstrap.sh`), then put
each release on it (`deploy.sh`). The layout, the switch and its policy are
described in [the README](README.md); this page only says what to run, in what
order, and what to do when a step fails. Nothing here is automatic: every step
is run by the owner, or with the owner's word, and every script shows what it
will do first (`--dry`).

The host is never named in a file. The deploy driver's connection reads no
ssh configuration of the development host (`-F /dev/null`, see "The deploy
key"), so everything it needs comes from your own shell's environment, never
from this repository, and it prints only `the authority host`:

```
export AUTHORITY_HOST_SSH=USER@HOST                           # your value: a host name or an address, not an ssh alias
export AUTHORITY_HOST_KNOWN_HOSTS=/ABSOLUTE/PATH/known_hosts  # a file you keep, with the authority host's key
export AUTHORITY_HOST_PORT=22                                 # only when the port is not 22
```

Put the authority host's key in that known-hosts file from a source you
trust: read its fingerprint on the host itself, from your root session
(`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`), and compare it with the
line you add. The driver accepts no other host key (`StrictHostKeyChecking
yes`, and no other known-hosts file).

## What you need first

- On the development host: Git, Make, `sh`, `rsync`, `ssh`, the repository's
  toolchain (`make doctor` passes) and a way to run `ah-release` (see step 2).
- On the authority host: the deploying user can log in with the owner's deploy
  key from the laptop's agent, forwarded to the development host for the
  session (see "The deploy key" below), never with a key file and never with a
  password (`ssh -o BatchMode=yes` never asks), has a folder it owns for
  incoming releases (default `~/incoming`, outside `/opt/authority-host`; make it
  once with `mkdir -m 0700 ~/incoming`) and may run the switch form of
  `sudo /usr/local/sbin/ah-switch`, and nothing else of it (see "Who may
  switch" below).
- Root on the authority host, your own way in, for the bootstrap and for
  anything the switch asks the owner to do.

## The deploy key

The deploy key never lives on the development host (the AI host). It stays on
the owner's laptop, in the laptop's ssh-agent, which asks on the laptop before
each use; the agent is forwarded to the development host only for a deploy
session. `deploy.sh` logs in with the agent's key only. `deploy.sh --self-test`
prints its exact ssh options (here without `AUTHORITY_HOST_KNOWN_HOSTS`, which
it shows as `/KNOWN-HOSTS-FILE`):

```
-F /dev/null -o BatchMode=yes -o IdentityFile=none -o IdentitiesOnly=no -o IdentityAgent=SSH_AUTH_SOCK -o PreferredAuthentications=publickey -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o GSSAPIAuthentication=no -o HostbasedAuthentication=no -o PKCS11Provider=none -o ProxyCommand=none -o ProxyJump=none -o ControlMaster=no -o ControlPath=none -o ForwardAgent=no -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o UserKnownHostsFile=/KNOWN-HOSTS-FILE
```

and `-o Port=N` after them when `AUTHORITY_HOST_PORT` is set. No ssh
configuration file is read at all (`-F /dev/null`: no `IdentityFile`,
`IdentityAgent`, `ProxyJump`, `Match` or alias of yours or of the system's
applies); no key file; only the agent of `SSH_AUTH_SOCK`, the one the driver
checked; public-key authentication only (no password, keyboard-interactive,
Kerberos, host-based login or token library); a direct connection with no
proxy; no shared connection, so every step is a new login and a new
confirmation; the agent goes no further than the development host; and only
the host key of your known-hosts file. It refuses to start when
`AUTHORITY_HOST_KNOWN_HOSTS` is not a regular file, or when `SSH_AUTH_SOCK` is
unset or its agent holds no key. No private key for the authority host, the
git remote or signing is kept on the development host; push and signing keys
follow the same rules (see [outward keys](../../docs/outward-keys.md)).

Placeholders: `AI-HOST` is the development host and `USER@AUTHORITY-HOST` the
deploying user on the authority host, each named as the laptop's `known_hosts`
names it; `192.0.2.10` is the development host's address as the authority host
sees it; `DEPLOY-KEY` is the key's file on the laptop.

1. **On the laptop, make the key (or keep the one you have there)**, with a
   passphrase. It is never copied to another host.

   ```
   ssh-keygen -t ed25519 -f DEPLOY-KEY -C deploy-key
   ```

2. **Load it for the session**, with a confirmation for each use (`-c`), usable
   only from the laptop to the development host and from there to the
   authority host (`-h`), for 30 minutes (`-t`):

   ```
   ssh-add -c -t 30m -h AI-HOST -h 'AI-HOST>USER@AUTHORITY-HOST' DEPLOY-KEY
   ```

   `-c` needs an askpass program on the laptop (`SSH_ASKPASS`); without one
   every use is refused. `-h` looks both hosts up in the laptop's
   `known_hosts`, so both must be there. A destination constraint (`-h`)
   needs OpenSSH 8.9 or newer at both ends of every hop, client and server:
   the laptop's `ssh` and `ssh-agent`, the development host's `sshd` (which
   receives the forwarded agent) and its `ssh` (which tells the agent which
   host it logs in to), and the authority host's `sshd` (which proves to the
   agent which host it is). Check each with `ssh -V`, and on the hosts with
   `sshd -V` (or the package version) from a root session; with an older one
   anywhere the agent refuses the key, or the constraint is not enforced
   there, so do not deploy until all of them are 8.9 or newer. The agent
   refuses the key on any other hop. Forward only an agent whose
   keys all carry `-h` (the signing key aside, see [outward
   keys](../../docs/outward-keys.md)): a key added without it is usable from the
   development host to anywhere. A second agent on the laptop for these
   sessions keeps the everyday keys out of it:

   ```
   ssh-agent -a ~/.ssh/outward-agent.sock
   SSH_AUTH_SOCK=~/.ssh/outward-agent.sock ssh-add -c -t 30m -h AI-HOST -h 'AI-HOST>USER@AUTHORITY-HOST' DEPLOY-KEY
   ```

3. **Forward it for the deploy session only**, never for an everyday one, on
   a connection of its own:

   ```
   ssh -o ControlMaster=no -o ControlPath=none -o ForwardAgent=~/.ssh/outward-agent.sock AI-HOST
   ```

   `ControlMaster=no` and `ControlPath=none` keep this login off any shared
   connection of your ssh configuration: a login that reuses an existing
   master connection gets that master's forwarded agent (often none, or the
   everyday one), not the one named here. With a single agent that holds
   nothing but such keys, `ssh -o ControlMaster=no -o ControlPath=none -A
   AI-HOST` does the same. For an alias, set these on an alias of its own
   that you use only for deploy, push and release sessions, never on the
   everyday one:

   ```
   Host ai-outward
       HostName AI-HOST
       ForwardAgent ~/.ssh/outward-agent.sock
       ControlMaster no
       ControlPath none
   ```

   In that session `ssh-add -l` lists the key; `deploy.sh` runs there.

   **Which confirmations to accept.** While the agent is forwarded, any
   process of the deploying account on the development host can ask it to
   sign, every agent session included; within the `-h` limits that is a
   login, and the signing key, which has no `-h`, signs anything. So accept a
   confirmation only right after you started the step that causes it, one for
   each login it makes: the `--dry` run's `rsync -n`, then `--go`'s copy and
   its switch (see "Deploy: dry, then go"), a `git push`, or a signature you
   asked for. Refuse any other prompt, and any prompt beyond that count, and
   treat it as an incident: something on the development host is using the
   forwarded agent; stop, close the session and find out what asked before
   the next one. When the step is done, tear the forwarding down at once
   instead of waiting for `-t`: on the laptop remove every key from the
   outward agent,

   ```
   SSH_AUTH_SOCK=~/.ssh/outward-agent.sock ssh-add -D
   ```

   and close the forwarded login (`exit`).

4. **On the authority host**, from your root session, the deploying user's
   `~/.ssh/authorized_keys` holds this key's line, and no key whose private
   part is on the development host:

   ```
   restrict,from="192.0.2.10" ssh-ed25519 AAAA...PUBLIC-KEY deploy-key
   ```

   `restrict` turns off forwarding of ports, of the agent and of X11, the
   terminal and `~/.ssh/rc`; `from=` takes the key only from the development
   host's address. There is no forced command (`command=`): the copy is
   `rsync --server` with flags that the sending rsync's version chooses, which
   a small fixed command cannot check (`rrsync`, which can, is a Python
   program), and the switch would need a second dispatcher in front of `sudo`:
   both would be code on the login path that no release verifies. The boundary
   is `restrict` and `from=` here, the confirmation on the laptop for each use,
   and the sudo rule of "Who may switch" below: `sudo -l -U DEPLOYUSER` lists
   the one `ah-switch` form and nothing else. A confirmed use can still run
   any command as the deploying user, so that account owns nothing else and
   has no other sudo rule.

5. **Prove it**, once it is set up and after any change to it:

   - In the forwarded session, run `sh deploy/authority-host/deploy.sh --dry
     --release DIR/ID` and refuse the confirmation on the laptop (or let it
     time out): it ends `rsync -n failed` and nothing is copied. Run it again
     and accept: it passes. If no confirmation popped, the key is not loaded
     with `-c`: remove it (`ssh-add -d DEPLOY-KEY` on the laptop), load it as
     in step 2, and deploy nothing until it pops.
   - In a session without the agent, `deploy.sh --dry` refuses (`SSH_AUTH_SOCK
     is not set`). Then test each identity under review, one at a time, on
     the very connection the driver makes: the same destination
     (`AUTHORITY_HOST_SSH`), port, known-hosts file and direct connection, no
     ssh configuration file (which could change the destination, the port or
     add a proxy), no agent, and only the one key file `KEY` offered. The
     identities under review are every private key file on the development
     host (each file of `~/.ssh` that holds one, and any other place a key
     may be kept) and the old deploy key wherever a copy may remain. Run this
     once for each, with `KEY` its path:

     ```
     env -u SSH_AUTH_SOCK ssh -F /dev/null -o BatchMode=yes -o IdentityFile=KEY -o IdentitiesOnly=yes -o IdentityAgent=none -o PreferredAuthentications=publickey -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o GSSAPIAuthentication=no -o HostbasedAuthentication=no -o PKCS11Provider=none -o ProxyCommand=none -o ProxyJump=none -o ControlMaster=no -o ControlPath=none -o ForwardAgent=no -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o UserKnownHostsFile="$AUTHORITY_HOST_KNOWN_HOSTS" -o Port="${AUTHORITY_HOST_PORT:-22}" "$AUTHORITY_HOST_SSH" true
     ```

     Each must fail with `Permission denied (publickey)`. A key file with a
     passphrase cannot be used in batch mode, so its failure alone proves
     nothing: add `-v` and look for `Server accepts key`, which means the
     authority host would take that key; its line must then go from
     `authorized_keys`. The command makes a connection of its own, so that
     no shared connection, which may carry a forwarded agent, logs in for it.

   - From your root session on the authority host, `ssh-keygen -lf
     /home/DEPLOYUSER/.ssh/authorized_keys` lists only fingerprints that
     `ssh-keygen -lf DEPLOY-KEY` prints on the laptop.

6. **Later, a FIDO2 key** (`ed25519-sk`): only the confirmation becomes a touch.
   The file on the laptop is then a handle; the private key stays on the
   security key, which signs only when touched, so `-c` is no longer needed:

   ```
   ssh-keygen -t ed25519-sk -f DEPLOY-KEY-SK -C deploy-key
   ssh-add -t 30m -h AI-HOST -h 'AI-HOST>USER@AUTHORITY-HOST' DEPLOY-KEY-SK
   ```

   The line on the authority host becomes this one, never with
   `no-touch-required`:

   ```
   restrict,from="192.0.2.10" sk-ssh-ed25519@openssh.com AAAA...PUBLIC-KEY deploy-key
   ```

   A FIDO2 key follows its own minimum: OpenSSH 8.2 or newer on the laptop
   (with its FIDO library, for `ed25519-sk`) and on the authority host's
   `sshd` (which must accept the `sk-ssh-ed25519@openssh.com` key type). It
   does not lower the minimum of step 2: with `-h`, every hop still needs
   OpenSSH 8.9 or newer, client and server. Nothing else changes:
   `deploy.sh`, the forwarding, the confirmations to accept and the proof
   stay the same, and a deploy without the touch fails.

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
this one uses the forwarded deploy key with the options of `deploy.sh`, see "The
deploy key"), from the development host:

```
rsync -a -e "ssh -F /dev/null -o BatchMode=yes -o IdentityFile=none -o IdentitiesOnly=no -o IdentityAgent=SSH_AUTH_SOCK -o PreferredAuthentications=publickey -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o GSSAPIAuthentication=no -o HostbasedAuthentication=no -o PKCS11Provider=none -o ProxyCommand=none -o ProxyJump=none -o ControlMaster=no -o ControlPath=none -o ForwardAgent=no -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o UserKnownHostsFile=$AUTHORITY_HOST_KNOWN_HOSTS" \
  deploy/authority-host/bootstrap/host-bootstrap.sh build/authority-host/ah-switch "$AUTHORITY_HOST_SSH":incoming/
```

(add `-o Port=$AUTHORITY_HOST_PORT` inside the quotes when the port is not
22).

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
changes), runs that command over ssh with the options of "The deploy key" and
streams its output. Each of the two is a login with the deploy key, so `--go`
asks for two confirmations on the laptop, and `--dry` for one; `--dry` ends
with a reminder to stop if none popped.
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

## Deploying the seat app

The seat app, `ataniq-seat`, the phone's page, is deployed as a release that
is not static: this folder with `seat/` laid over it. What it installs and
why is in [the README](README.md#the-seat-app); this is the order. Nothing
of it is automatic either, and no value of the host is in a file of this
repository: the placeholders below are `seat.example` for the seat's name and
`203.0.113.20` for the gate's address.

1. **Build the seat's binary, static**, beside the other two (step 1), and
   keep its hash:

   ```
   CGO_ENABLED=0 sh scripts/go-tool.sh build -trimpath -o build/authority-host/ataniq-seat ./cmd/ataniq-seat
   ldd build/authority-host/ataniq-seat      # "not a dynamic executable"
   sha256sum build/authority-host/ataniq-seat
   ```

2. **Make the release.** Lay the seat's part over a copy of this folder,
   fill the allowlists as in step 2 (`ah-release baseline --src SRC ... --out
   FILLED`), and build without `--components static`, with the binary and
   the package snapshot the host records (`ah-switch status` shows it; the
   release then needs no package step):

   ```
   mkdir SRC
   cp -R deploy/authority-host/etc deploy/authority-host/share SRC/
   cp -R deploy/authority-host/seat/. SRC/
   ah-release build --out DIR --counter N --commit SHA --go-version V --built-on CLASS --package-snapshot S \
     --bin build/authority-host/ah-switch --bin build/authority-host/ah-noaicheck --bin build/authority-host/ataniq-seat \
     --src FILLED
   ah-release check --no-noaicheck DIR/ID
   ```

3. **Set the host up for the seat, once, by hand**, from your own root
   session, as [the README](README.md#setting-the-host-up-for-it-once-by-hand)
   says: the user `seat` and Caddy in its group, and the group `seat-notify`
   (`groupadd --system seat-notify`, which the seat's unit names as a
   supplementary group, so it must exist before the first switch even
   without the notifier); the folder
   `/etc/authority-host/seat` with `origin` (`https://seat.example`),
   `gate.pin`, `seat.env` (`SEAT_GATE=203.0.113.20:GATEPORT`) and
   `site.caddy` (the site `http://seat.example` that imports `seat`); the
   drop-in `/etc/systemd/system/ataniq-seat.service.d/gate.conf` with
   `IPAddressAllow=203.0.113.20`, and your firewall's rule that keeps the
   user `seat` to the gate's port; the seat's key, made with `gate-init`
   from a root-owned copy of the release you verified, and its fingerprint
   carried to the gate's pin list; and the unit's link,
   `ln -s /opt/authority-host/current/etc/systemd/system/ataniq-seat.service /etc/systemd/system/ataniq-seat.service`.
   The proxy in front of the host must pass `Host`, `Origin` and
   `Sec-Fetch-Site` unchanged and name the client in `X-Forwarded-For`.
   Your firewall must admit TCP port 80 of the authority host from the
   front proxy's address only. The Caddyfile trusts every private address
   as a proxy (`trusted_proxies static private_ranges`), so any client that
   reaches port 80 directly from a private range sends its own
   `X-Forwarded-For` and chooses the address the seat counts its
   per-source limits by (5 pairing tries and 10 login challenges in 15
   minutes). The limits for all sources together still hold, but the
   per-source ones do not without that rule. Check it from a host other
   than the front proxy: `curl --max-time 5 http://AUTHORITY_HOST/healthz`
   must fail.
   The phone's pages hold a request to `/api/watch` open for up to 25
   seconds, so the front proxy's read timeout must be longer than 30 seconds
   and its buffering must be off for that path (for nginx, `proxy_read_timeout
   60s;` and `proxy_buffering off;` in the location of `/api/watch`; a CDN or
   cloud proxy in front has a limit of its own, which must also be above 30
   seconds). Otherwise every poll fails and the pages say "Not live". This
   host's Caddy needs nothing (see [the README](README.md#setting-the-host-up-for-it-once-by-hand),
   step 7).

4. **Deploy: dry, then go**, as in step 4. The switch runs the release's
   no-AI check (it reads `seat.env`, and `site.caddy`, which the
   Caddyfile imports), its validators (`caddy validate`, which
   reads `site.caddy`, `systemd-analyze verify` of the unit and `test -L` of
   the unit's link), moves
   `current`, and its reload list restarts systemd's view of the unit, the
   seat and Caddy while decisions are inhibited. Its health check needs both
   the front end and the seat on its socket.

5. **Verify**, as in step 5, and also:

   ```
   systemctl status ataniq-seat.service
   curl -fsS --unix-socket /run/seat/http.sock http://localhost/healthz
   ```

   Then acknowledge the outcome as in step 5.

6. **The host's stop list.** Once that switch ended `ok`, replace the line
   `/usr/bin/true` of `/etc/authority-host/stop.list` with
   `systemctl stop ataniq-seat.service`, so that the host's own list stops
   the seat whatever release is live. Not before: `systemctl stop` of a unit
   systemd does not know fails.

To roll back, take step 6 of the first deploy (`ack`, then `--rollback-to
previous`): the seat release's own stop list stops the seat first. Going
back to the first, static, release leaves the seat stopped and its unit's
link dangling: put `/usr/bin/true` back in the host's stop list until a seat
release is live again (see [the README](README.md#rollback)).

## Deploying the notifier

The notifier, `ataniq-seat-notify`, which sends the phone the notice that
cards wait, is part of the seat's release when the owner wants notices: this
folder with `seat/` and then `notify/` laid over it. What it installs and why
is in [the README](README.md#the-notifier); this is the order. It follows
"Deploying the seat app" (the seat is set up and deployed first), and nothing
of it is automatic. The placeholders are `203.0.113.53` for the resolver,
`198.51.100.0/24` for the push service's range and `owner@example.org` for
the contact. It needs the owner's words on one thing: outbound access to
Apple's push service for this unit, and nothing else.

1. **Build both binaries, static**, and keep their hashes:

   ```
   CGO_ENABLED=0 sh scripts/go-tool.sh build -trimpath -o build/authority-host/ataniq-seat ./cmd/ataniq-seat
   CGO_ENABLED=0 sh scripts/go-tool.sh build -trimpath -o build/authority-host/ataniq-seat-notify ./cmd/ataniq-seat-notify
   ldd build/authority-host/ataniq-seat-notify      # "not a dynamic executable"
   sha256sum build/authority-host/ataniq-seat build/authority-host/ataniq-seat-notify
   ```

2. **Make the release.** Lay both parts over a copy of this folder, in this
   order, fill the allowlists and build as in step 2 of the seat's, with the
   notifier's binary added:

   ```
   mkdir SRC
   cp -R deploy/authority-host/etc deploy/authority-host/share SRC/
   cp -R deploy/authority-host/seat/. SRC/
   cp -R deploy/authority-host/notify/. SRC/
   ah-release build --out DIR --counter N --commit SHA --go-version V --built-on CLASS --package-snapshot S \
     --bin build/authority-host/ah-switch --bin build/authority-host/ah-noaicheck \
     --bin build/authority-host/ataniq-seat --bin build/authority-host/ataniq-seat-notify \
     --src FILLED
   ah-release check --no-noaicheck DIR/ID
   ```

3. **Set the host up for the notifier, once, by hand**, from your own root
   session, as [the README](README.md#setting-the-host-up-for-it-once-by-hand-1)
   says, in this order: the group and the user `seat-notify` and the seat's
   user in the group (the group is made in the seat's step 3, which the
   seat's unit needs, so make it before that first switch); the folder
   `/var/lib/seat-notify` and the VAPID key, made with `vapid-init` from a
   root-owned copy of the release you verified (it prints the public key
   only); the folder `/etc/authority-host/seat-notify` with
   `seat-notify.env` (`NOTIFY_SUBJECT=mailto:owner@example.org`, the
   contact of your own); the drop-in
   `/etc/systemd/system/ataniq-seat-notify.service.d/egress.conf` with the
   resolver and the push range (`IPAddressAllow=203.0.113.53` and
   `IPAddressAllow=198.51.100.0/24` in place of the real values); the unit's
   link, `ln -s /opt/authority-host/current/etc/systemd/system/ataniq-seat-notify.service /etc/systemd/system/ataniq-seat-notify.service`;
   the line `SEAT_NOTIFY_ARGS=--notify-socket /run/seat-notify/notify.sock` in
   `/etc/authority-host/seat/seat.env`; and your firewall's rule that keeps
   the user `seat-notify` to the resolver and to TCP port 443 of the push
   range, outbound, and to nothing else.

4. **Deploy: dry, then go**, as in step 4. The switch runs the release's
   no-AI check (it reads `seat.env` and `seat-notify.env`), its validators
   (`systemd-analyze verify` and `test -L` of both units), moves `current`,
   and its reload list restarts the notifier, then the seat and Caddy while
   decisions are inhibited. The health check is the seat's: the notifier
   decides nothing, and is not part of it, so a notifier that cannot start
   (the unit is `Type=simple`) does not fail the switch: it is restarted every
   5 seconds while the switch ends `ok`. Step 5 is how you see it.

5. **Verify**, as in step 5, and also:

   ```
   systemctl status ataniq-seat-notify.service
   ls -l /run/seat-notify
   sudo -u seat RELEASE/bin/ataniq-seat-notify send-test --socket /run/seat-notify/notify.sock 3
   ```

   The socket is `seat-notify:seat-notify`, mode 0660; the test prints `sent K`
   (0 until a phone has subscribed). `systemctl status` must say
   `active (running)`; `activating (auto-restart)`, no socket in
   `/run/seat-notify` or a `send-test` that says the notifier is not reachable
   means the notifier is exiting at its start and being restarted every 5
   seconds, whatever the switch said (see the table below, and
   [the README](README.md#a-notifier-that-cannot-start)). Then acknowledge the outcome as in
   step 5. The host's own stop list is not changed: the notifier decides
   nothing.

To roll back, take step 6 of the first deploy (`ack`, then `--rollback-to
previous`). A release without the notifier leaves it running from the earlier
release's binary until `systemctl stop ataniq-seat-notify.service`; see
[the README](README.md#rollback-1). If the notifier is down, nothing breaks but
the notices.

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
  when a decision service exists: for the seat app,
  `systemctl stop ataniq-seat.service`, once a switch to its release ended `ok`
  (see "Deploying the seat app").
- **The seat app's files.** `/etc/authority-host/seat/` (`origin`, `gate.pin`,
  `seat.env`, `site.caddy`), the drop-in that allows the gate's address, the
  user `seat`, the seat's key in `/var/lib/seat/identity` and the unit's link
  are the owner's, made by hand (see [the README](README.md#setting-the-host-up-for-it-once-by-hand)).
- **The notifier's files.** `/etc/authority-host/seat-notify/seat-notify.env`
  (the contact the push service may reach you by), the drop-in that allows the
  resolver and the push service's range, the user `seat-notify`, the VAPID key
  and the subscriptions in `/var/lib/seat-notify`, the unit's link, the
  `SEAT_NOTIFY_ARGS` line of `seat.env` and the firewall's rule for the
  user's outbound port 443 are the owner's, made by hand (see [the
  README](README.md#setting-the-host-up-for-it-once-by-hand-1)). No release
  holds the contact or a key.
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
  them to the deploy key, whose every use is made from the development host.
  So give the deploying user this rule and no other for the switch, in a file of
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
| `deploy.sh` | `AUTHORITY_HOST_SSH is not set`, `must be USER@HOST`, a bad release id or a bad `--incoming-base` | Nothing was run. Set the variable to `USER@HOST` (an ssh alias does not work: the driver reads no ssh configuration), or name a release folder `COUNTER-COMMIT` and a safe incoming folder |
| `deploy.sh` | `AUTHORITY_HOST_KNOWN_HOSTS is not set` or `must name a regular file`, or `AUTHORITY_HOST_PORT must be a port number` | Nothing was run. Point the variable at your known-hosts file (an absolute path, not a link) and set the port only when it is not 22 |
| `deploy.sh --dry` | `Host key verification failed.` | Nothing was copied. Your known-hosts file has no key, or another key, for the authority host. Do not accept a key you have not checked: read the fingerprint on the host itself, from your root session, compare, then add the line |
| `deploy.sh` | `SSH_AUTH_SOCK is not set`, `the agent of SSH_AUTH_SOCK holds no key` or `cannot reach the agent` | Nothing was run but `ssh-add -l`. Forward the laptop's agent for this session and load the key there, as in "The deploy key"; never put a key file on the development host instead |
| `deploy.sh --dry` | `rsync -n failed` | Nothing was copied. If the laptop asked and you refused, or it timed out, this is the confirmation working. Otherwise check that the incoming folder exists on the host and belongs to the deploying user, and that the key is in the forwarded agent (`ssh-add -l`) and allowed on this hop (`-h`, see "The deploy key") |
| `deploy.sh --go` | rsync failed | The switch was not run. The partial copy is only in the incoming folder; run `--go` again (rsync resumes) |
| `deploy.sh --go` | the switch step failed (the output above it is the switch's) | The release is in the incoming folder, possibly already imported. Read the switch's `status` and its outcome, then see below |
| Switch | `step 4/9 noaicheck: FAILED: noaicheck-failed: exit status N (run it by hand to see its hits)`: the no-AI check found something before activation (`HIT` lines that only the checker prints, step `noaicheck`, phase `pre`) | The switch prints no `HIT` line, here or in phase `post`, and `ah-release check` prints only codes: when the checker finds a hit it exits 1, so the builder prints both `noaicheck-exit` and `noaicheck-hit` (and `noaicheck-hit` alone only for a checker that prints a hit and exits 0); the `HIT` lines themselves exist only when you run the checker. The release is imported by now (`releases/ID`), so, from a real root login, run `/opt/authority-host/releases/ID/bin/ah-noaicheck --phase pre --root / --release /opt/authority-host/releases/ID --require-policy` (the switch's own arguments) and read its `HIT` lines (`HIT policy ... empty` means an allowlist was not filled; the README's [no-AI check](README.md#the-no-ai-check) lists the kinds). If the result line says `result: failed at noaicheck; current unchanged (...)` and does not say the decision services are stopped, nothing was changed: fix the host or the release and deploy again (a release that must change is a new build with a higher counter, see the `release-exists` row). If it says they are stopped, the release's package step ran before the check: take the failed rows below |
| `deploy.sh --go` | the same release again: `step 0/9 import: FAILED: release-exists: releases/ID exists; --from imports only a new release (switch to it without --from)` | An earlier `--go` imported that release and stopped later, and the import is kept; this run imported nothing and moved nothing (rsync ran again, the switch refused at the import). It does rewrite the record: `ah-switch status` now shows this refusal as the last switch (`failed step: import`, `error: release-exists`) in place of how the earlier attempt ended, so read that from the output of the earlier `--go`; the `decisions:` line and any owed outcome in `status` still describe the host. If the earlier attempt's outcome is still owed (not acknowledged), the same request prints `settled: this switch to ID has been settled already; it is not done again` instead, and the outcome stays owed until you acknowledge it. Never `--go` the same id a second time. If the earlier attempt was refused before anything began (`result: failed at ... current unchanged`, such as the no-AI check of phase `pre`, a validator or a static release refused at packages) and the cause was on the host and is fixed, switch to the imported release from your own root session, without `--from` (`ah-switch ID`; the deploy key's sudo rule does not allow that form). Otherwise (the cause is in the release, or the switch step ran and used its counter, as after a `rolled-back` result, or the host was changed and the decision services stopped, as after a `failed` or `failed-recovery` one: take those rows below first) build a new release with a higher counter and deploy that |
| Switch | `phase post: ... (run it by hand to see its hits)`, `failed_step: postcheck` | The release was already activated and caddy restarted: this is not "nothing was changed", and the switch does not print the hits. The result depends on what there was to go back to. On the first switch there is no old release, so the result is `failed`: `current` is still the new release, the stop lists ran and decisions stay inhibited. On a later switch the old release is put back and qualified again (reload, health check and its own no-AI check of phase `post`): if that passes, the result is `rolled-back` (`health after going back: ok`, decisions not inhibited); if the fallback also fails, the result is `failed-recovery`, no release is known healthy, the decision services are stopped and decisions stay inhibited, and you acknowledge the owed outcome before any other switch (take the `failed-recovery` row below). Read which one `status` shows before you act. Do not run `--go` for the same id again: its counter is used and a new import of an existing id is refused. From your root session: `ah-switch status` (result, failed step, inhibit); verify the installed copy (`cd /opt/authority-host/releases/ID && sha256sum -c SHA256SUMS`); save the firewall with `nft list ruleset` into a file only root can read (`/root/nft-now.txt`), then, from a real root login, run `/opt/authority-host/releases/ID/bin/ah-noaicheck --phase post --root / --release /opt/authority-host/releases/ID --require-policy --nft-listing /root/nft-now.txt` to read the `HIT` lines (a `HIT port` means `ports.allow` lacks a listener the host now has). Fix the lists, acknowledge the owed outcome as in step 5 (a switch to another release is refused until you do), then build a release with a higher counter and deploy it: a healthy switch removes the inhibit (see [the decision inhibit](README.md#the-decision-inhibit) and [the failure cases](README.md#failure-cases)) |
| Switch | `result: rolled-back at STEP; current -> OLD; health after going back: ok`: a reload, the health check or the check of phase `post` failed, and the old release was put back and qualified again | The old release is live and was qualified again (reload, health check and no-AI check of phase `post`), and decisions are no longer inhibited. `status` shows the last switch as `rolled-back`, `health after going back: ok` and `decisions: not inhibited`. Read why (`failed step`, `error`, `detail`), acknowledge the outcome as in step 5, fix the release and deploy a new one with a higher counter: the counter of the failed one is used |
| Switch | `result: failed-recovery at STEP; current -> ID; health after going back: failed; the decision services are stopped`: going back did not work either (or the old release could not be used, then the `health after going back` part may be missing) | Not rolled back: no release is known healthy. The host's stop list and the releases' ran, the decision services are stopped and decisions stay inhibited (across a reboot). `status` shows the last switch as `failed-recovery`, with `decision services: stopped`, `health after going back: failed` and `decisions: inhibited`. The console is needed: from your own root session read the `back:` and `stop:` lines of the output and `detail` in `status`, look at what `current` points at and at the services by hand, and repair the host. Then acknowledge the owed outcome first (`ack`, as in step 5): a switch to any other release, one with a higher counter included, is refused while it is owed. After that the host is qualified again by a healthy switch (a new release with a higher counter, which removes the inhibit), or by your word, `inhibit-clear --asked WORDS` after you looked at the host. See [the failure cases](README.md#failure-cases), [the decision inhibit](README.md#the-decision-inhibit) and [the journal and recover](README.md#the-journal-and-recover) |
| Switch | `result: failed at STEP; current -> ID; the decision services are stopped`, with no `health after going back` part: a first switch (no old release to go back to), or a failure after the package step changed the host | As `failed-recovery`: the decision services are stopped and decisions stay inhibited, and on a first switch `current` stays the new release. Repair the host, acknowledge the owed outcome first (`ack`, as in step 5: a switch to another release is refused while it is owed), then a healthy switch of a higher counter, or `inhibit-clear`; `status` shows `failed` and `decision services: stopped` |
| Switch | `refused: an outcome is owed` | Nothing was done. Read the outcomes it printed and `status`, then run the `ah-switch ack --txn ID[,ID...] --asked WORDS` that `status` prints (the `--txn` copied from it, exactly the ids owed) from your own root session, and run the request again |
| Switch (seat release) | `step 4/9 noaicheck: FAILED` and, run by hand, `HIT incomplete env-file /etc/authority-host/seat/seat.env` | Nothing was changed. The seat's environment file is missing: write it (see "Deploying the seat app", step 3) and switch to the imported release again from your root session, without `--from` |
| Switch (seat release) | `step 4/9 noaicheck: FAILED` and, run by hand, `HIT incomplete caddy-import /etc/authority-host/seat/site.caddy`, or `HIT env` or `HIT proxy ... outbound-target` naming that file | Nothing was changed. The owner's site file is missing (write it, see "Deploying the seat app", step 3), names a variable like a provider key, or proxies to something that is not this host: the file is to hold the site address and `import seat` only. Fix it and switch to the imported release again from your root session, without `--from` |
| Switch (seat release) | `step 5/9 validate: FAILED: validator-failed: validator 1 of 3, ...` (`caddy validate`) | Nothing was changed. Usually `/etc/authority-host/seat/site.caddy` is wrong (the no-AI check reads it first and says when it is missing); fix it and switch to the imported release again without `--from` |
| Switch (seat release) | `step 5/9 validate: FAILED: validator-failed: validator 3 of 3, ...` (`test -L /etc/systemd/system/ataniq-seat.service`) | Nothing was changed. The unit's link (step 3 of "Deploying the seat app") is missing or is not a link: make it with `ln -s /opt/authority-host/current/etc/systemd/system/ataniq-seat.service /etc/systemd/system/ataniq-seat.service` and switch to the imported release again without `--from`. Without the check, the reload's `systemctl restart ataniq-seat.service` would fail with `Unit not found` after `current` moved, and the stop list's `systemctl stop` would fail with exit 5 and leave the transaction in `stopping` |
| Switch (seat release) | the health check fails on the seat's socket (`curl ... --unix-socket /run/seat/http.sock ...`) | The release was activated: the result is `rolled-back` or `failed`, as in the rows above. Read `journalctl -u ataniq-seat.service`: a missing key (`--identity`), origin or pin file, or a gate address the drop-in does not allow, stops the seat at its start. Fix it, acknowledge, and deploy a release with a higher counter |
| Switch (release with the notifier) | `step 4/9 noaicheck: FAILED` and, run by hand, `HIT incomplete env-file /etc/authority-host/seat-notify/seat-notify.env` | Nothing was changed. The notifier's environment file is missing: write it (see "Deploying the notifier", step 3) and switch to the imported release again from your root session, without `--from` |
| Switch (release with the notifier) | `step 5/9 validate: FAILED: validator-failed: validator 5 of 5, ...` (`test -L /etc/systemd/system/ataniq-seat-notify.service`) | Nothing was changed. The notifier's unit link is missing or is not a link: make it with `ln -s /opt/authority-host/current/etc/systemd/system/ataniq-seat-notify.service /etc/systemd/system/ataniq-seat-notify.service` and switch to the imported release again without `--from`. The seat's own link is validator 4 of 5 |
| After a switch (release with the notifier) | The switch ended `ok`, but `systemctl status ataniq-seat-notify.service` says `activating (auto-restart)`, there is no `/run/seat-notify/notify.sock`, or `send-test` says the notifier is not reachable | The switch did not see it and did not go back: with `Type=simple` the restart in the reload list succeeds once the process is forked, the health check is the seat's, and `Restart=on-failure` restarts the notifier every 5 seconds without end. Read `journalctl -u ataniq-seat-notify.service`: a missing or unsafe key (`--vapid-key`), a subscriptions file that is not as the command writes it (`--subscriptions`; also one that holds a host `--push-hosts` no longer allows), a user `--seat-user` the host does not know, or a subject that is not a `mailto:` or `https:` value stops it at its start. Fix the file and `systemctl restart ataniq-seat-notify.service`; no new release is needed unless the fix is in the release. Until then there are no notices and nothing else is affected |
| Switch | exit 3 | Another switch holds the lock; wait for it, then run again. Nothing was done |
| Switch | exit 2 | Bad arguments or a root other than `/` without `--allow-exec`; nothing was done |
| Any | You are not sure what the host holds | `sudo /usr/local/sbin/ah-switch status` changes nothing and says what is live, what is owed and what is to resume |

A release that was imported stays in `releases/` when a later step fails: switch
to it again without `--from` (`ah-switch ID`), or go back with `--rollback-to
previous`. Never copy a folder into `releases/` by hand: the switch refuses it
as `provenance-missing` and it is not a release.
