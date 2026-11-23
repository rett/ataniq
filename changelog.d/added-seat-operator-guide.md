- Add `docs/seat-gate.md`, the operator guide of the phone approval path, for
  the scratch build with the owner channel's tag: what runs on the lead host and
  on the authority host, the keys of both ends (`ataniqd seat-gate init|pins`,
  `ataniq-seat gate-init`) and the pin file on each side, the owner-local origin
  file on both hosts, the firewall rule (one gate port, from the seat host only)
  and the reverse proxy rules (original Host kept, `Origin` and
  `Sec-Fetch-Site` untouched, the client-address header overwritten on every
  request, no caching, no redirects), rotation of a seat key, the gate key, the
  origin and a passkey, with what breaks when the order is wrong, pairing,
  login and the approve or deny of a card with a fresh passkey, the credential
  kinds and the accepted-kinds policy, what the STAND-IN banner and "scratch
  only" mean, the known limits and a troubleshooting table of the refusals the
  app shows. `docs/lead-daemon.md` no longer says the daemon has no network
  listener: the seat gate is its one, only with `--owner-channel --seat-gate`,
  and the accepted-kinds policy is applied, as the guide now says. Provisional
  and unqualified, for scratch state only.
