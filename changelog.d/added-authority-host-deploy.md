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
