- The seat release's `share/validators.list` has `test -L
  /etc/systemd/system/ataniq-seat.service`: the switch refuses the release
  before anything changes when the owner's link of the seat's unit (setup
  step 5) is missing, instead of failing the reload's restart and the stop
  list's stop (exit 5) after `current` moved. DEPLOY.md has the
  troubleshooting row.
