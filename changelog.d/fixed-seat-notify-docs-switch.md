- The authority host's README and DEPLOY.md no longer say that a notifier that
  cannot start fails the switch's reload: the unit is `Type=simple`, so the
  restart succeeds once the process is forked, the health check is the seat's,
  and the switch ends `ok` while systemd restarts the notifier every 5
  seconds. They say how the owner sees it (`systemctl status`, the journal, the
  missing socket, `send-test`, the page's Enable notices failing) and what to
  do. The design document's limits also say what a taken-over seat can do to
  the notifier within its host policy: subscribe its own push-host endpoints up
  to the four places, replace or remove the owner's subscription, and cause up
  to 30 pushes an hour.
