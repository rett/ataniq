- Add `ah-release`, the release builder of the authority host.
  `ah-release build` makes the release folder `ah-switch` imports,
  `DIR/<counter>-<short commit>`, from the given binaries (`ah-noaicheck` and
  `ah-switch` are required) and the `etc/` and `share/` of
  `deploy/authority-host`: folders 0755, files 0644 (0755 for `bin/*`), no
  link, a `MANIFEST` and a `SHA256SUMS` in the format the switch reads, and no
  time in any content, so two builds of one input are byte-identical. It
  refuses an existing release, a counter below 1, a commit or value with
  characters the switch would not read, and a link, fifo, device, socket or
  odd name in the source. A `--components static` build writes a no-op
  `bin/ah-pkgstep` of its own (a POSIX `sh`
  script that checks its arguments and exits 0 on a first switch or an
  unchanged snapshot, and refuses with exit 1 a snapshot change it cannot
  make, which leaves the package state unknown until `ah-switch recover
  --package-state`), so a first switch of a static release needs no
  hand-made package step; the build refuses a `--bin ah-pkgstep` with a
  static release (`static-package-step`, exit 2), because a release that
  must move the package snapshot is not static. `ah-release check DIR/<id>` reads a built folder
  back: its sums, manifest and shape, the coverage of its command lists that
  the switch leaves to the builder (a validator for every shipped Caddy,
  nftables and systemd file, a restart or reload of each shipped family, a
  stop and a restart of every decision service, no decision service in a
  static release), and the release's own no-AI check, which must exit 0 and
  print no hit and is run only when nothing else was found (a checker whose
  hash does not match `SHA256SUMS` is never run; what runs is a private
  copy of the bytes it verified, started as the open file that was hashed
  and not as a path, so a renamed temporary folder cannot swap it; a checker
  changed since is not run, `noaicheck-changed`; a relative `DIR/<id>`, a
  relative `--root` and a relative `TMPDIR` are made absolute, since the
  checker runs from the folder `/`). Both print only fixed reasons and plain names, never a
  file's content. `ah-release baseline --src DIR --units FILE --ports FILE
  --packages FILE --nft FILE [--out DIR] [--stamp TEXT]
  [--require-default-deny]` makes a scratch copy
  of the source tree whose allowlists are filled from files the owner
  collected on the host (the unit names of the no-AI check's `HIT unit`
  lines, `ss -H -ltn` ports, `pacman -Qq`, `nft list ruleset`): it refuses
  anything but a regular file of at most 2 MiB, an invalid line, an empty
  result and an output folder that exists or lies in the source, drops
  duplicates, sorts, takes `nft.digest` with the function of `ah-noaicheck
  nft-digest`, and never writes into the source. It enrolls any firewall
  listing, but for each of the hooks input, output and forward without a
  base chain of type `filter` with `policy drop` for IPv4 and IPv6 (a `nat`
  or `route` chain does not count, nor one whose first rule accepts every
  packet, with a plain or a named counter; no other rule is read; a listing
  that ends inside a block counts none) it prints a `WARNING:
  no-default-deny HOOK` line and writes the same words into the header of
  `nft.digest`; `--require-default-deny` refuses such a listing instead. See the README, "Filling the
  allowlists". `deploy/authority-host` now holds the source tree of the
  first, static release: a hardened Caddyfile (admin endpoint off, server
  timeouts, security headers, `/healthz`, a static site) and its validator,
  reload, health and stop lists. See `deploy/authority-host/README.md`, "The
  release builder".
