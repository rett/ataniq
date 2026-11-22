- Add `deploy/authority-host/bootstrap/host-bootstrap.sh`, the one-time
  setup of the authority host, run on the host as root. `--dry` (the
  default) prints every step and changes nothing; `--apply` creates
  `/opt/authority-host/releases`, `/etc/authority-host` and
  `/var/lib/authority-host` (0755, root:root), installs the host's
  `stop.list` (the single line `/usr/bin/true`, an existing list is kept)
  and installs the switch binary given with `--switch-bin` as
  `/usr/local/sbin/ah-switch` only when its SHA-256 is the one given with
  `--switch-sha256`. It writes no switch policy (the first scratch deploys
  run in manual mode), links the Caddyfile to the current release only with
  `--link-caddy` (keeping the old file once as
  `Caddyfile.pre-authority-host`), refuses any folder above what it touches
  that is not a real folder owned by root and closed to group and others,
  and never starts, restarts or enables a service.
  Add `deploy/authority-host/deploy.sh`, the deploy driver run on the
  development host: with `AUTHORITY_HOST_SSH` (never printed; the output says
  `the authority host`) it runs `ah-release check --no-noaicheck` on a built
  release folder (never the release's no-AI check, which would report this
  machine's own agent tools),
  copies it to the host's incoming folder with `rsync -a --delete`, and runs
  `sudo /usr/local/sbin/ah-switch --from INCOMING/ID ID` over `ssh -o
  BatchMode=yes`. `--dry` (the default) only lists what rsync would copy and
  prints the ssh command (it contacts the host read-only, through ssh and
  `rsync -n`), `--go` does it and is refused unless
  `--confirm ID` equals the release id, and `--self-test` computes the
  commands and starts nothing (no ah-release, rsync or ssh).
  The driver never runs a file of the incoming folder: the no-AI check is
  the switch's own, on the verified copy it imports, before anything
  changes.
  `DEPLOY.md` links the Caddyfile before the first switch (the shipped
  release restarts caddy in the switch and requires `/healthz` from it) and
  has the owner acknowledge the outcome (`ah-switch ack --asked WORDS`)
  before a later switch or a rollback, which the switch refuses while an
  outcome is owed.
  `DEPLOY.md` ("Who may switch") gives the sudo rule for the deploying
  user, limited to the switch form (`--from INCOMING/ID ID`, an argument
  regex for sudo 1.9.10 or newer) and checked with `sudo -l -U`:
  `inhibit-clear`, `ack` and `recover` run only from the owner's own root
  session, and a broader rule for the same account defeats that.
  `DEPLOY.md` has root copy the bootstrap script and the switch binary
  into a new root-only folder, compare their hashes with those typed from the
  build host, and run only those copies (even for `--dry`); the README's
  baseline collection does the same for the release whose checker it runs
  before any switch has verified a release.
  `DEPLOY.md` adds the front end's port 80 to the collected listeners before
  the baseline (caddy is stopped while it is taken, and the switch's check
  after the reload refuses a listener `ports.allow` does not name), carries
  `--link-caddy` in both bootstrap commands, and says that a failed check
  after the reload happens after the activation: inspect `status` and the
  installed release, run the check by hand, acknowledge, deploy a higher
  counter.
  `deploy/authority-host/DEPLOY.md` gives the first-deploy sequence, the
  rollback and what to do when a step fails.
