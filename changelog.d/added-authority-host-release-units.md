- `ah-release build` replaces `{release}` in every file under the source's
  `etc/systemd` (units and drop-ins) with the release's own folder on the
  host, `/opt/authority-host/releases/<id>`, so a decision service's unit can
  run the binary of its own release (`ExecStart={release}/bin/NAME`). A unit
  that ran `/opt/authority-host/current/bin/NAME` failed the switch's
  `systemd-analyze verify` on the first switch to a release shipping `NAME`,
  since `current` was still the release before it. Such a file over 4 MiB is
  refused (`too-large`), and `ah-release check` reports a unit that still
  holds `{release}` (`unit-placeholder`).
