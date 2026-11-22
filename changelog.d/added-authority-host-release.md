- Add `ah-release`, the release builder of the authority host.
  `ah-release build` makes the release folder `ah-switch` imports,
  `DIR/<counter>-<short commit>`, from the given binaries (`ah-noaicheck` and
  `ah-switch` are required) and the `etc/` and `share/` of
  `deploy/authority-host`: folders 0755, files 0644 (0755 for `bin/*`), no
  link, a `MANIFEST` and a `SHA256SUMS` in the format the switch reads, and no
  time in any content, so two builds of one input are byte-identical. It
  refuses an existing release, a counter below 1, a commit or value with
  characters the switch would not read, and a link, fifo, device, socket or
  odd name in the source. `ah-release check DIR/<id>` reads a built folder
  back: its sums, manifest and shape, the coverage of its command lists that
  the switch leaves to the builder (a validator for every shipped Caddy,
  nftables and systemd file, a restart or reload of each shipped family, a
  stop and a restart of every decision service, no decision service in a
  static release), and the release's own no-AI check, which must exit 0 and
  print no hit. Both print only fixed reasons and plain names, never a
  file's content. `deploy/authority-host` now holds the source tree of the
  first, static release: a hardened Caddyfile (admin endpoint off, server
  timeouts, security headers, `/healthz`, a static site) and its validator,
  reload, health and stop lists. See `deploy/authority-host/README.md`, "The
  release builder".
