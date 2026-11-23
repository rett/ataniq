- The authority host's release source has the seat app's part in
  `deploy/authority-host/seat/`, laid over the base tree to make a release
  that is not static: `ataniq-seat.service` (user `seat`, socket
  `/run/seat/http.sock` in `RuntimeDirectory=seat`, its key in
  `/var/lib/seat`, no network but the gate's address from the owner's
  drop-in, no listener, the design's hardening, never enabled: the reload
  list starts it), a Caddyfile with the snippet `(seat)`
  (`reverse_proxy unix//run/seat/http.sock`, the Host passed as it came,
  `Origin` and `Sec-Fetch-Site` untouched, `X-Seat-Client-Addr` set on every
  request, nothing cached) and the import of the owner's site file
  `/etc/authority-host/seat/site.caddy`, and lists that validate the unit,
  restart the seat, check it on its socket and stop it
  (`systemctl stop ataniq-seat.service`). The README ("The seat app") and
  DEPLOY.md ("Deploying the seat app") say how to make the release, the
  owner's one-time setup of the host, how the switch starts and stops the
  seat, and how to roll back.
