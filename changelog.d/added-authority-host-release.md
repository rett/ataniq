- Add `ah-release build`, the release builder of the authority host. It
  makes the release folder `ah-switch` imports, `DIR/<counter>-<short
  commit>`, from the given binaries (`ah-noaicheck` and `ah-switch` are
  required) and the `etc/` and `share/` of `deploy/authority-host`: folders
  0755, files 0644 (0755 for `bin/*`), no link, a `MANIFEST` and a
  `SHA256SUMS` in the format the switch reads, and no time in any content,
  so two builds of one input are byte-identical. It refuses an existing
  release, a counter below 1, a commit or value with characters the switch
  would not read, and a link, fifo, device, socket or odd name in the
  source, printing only fixed reasons and plain names. See
  `deploy/authority-host/README.md`, "The release builder".
