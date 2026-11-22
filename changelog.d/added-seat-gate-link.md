- `ataniq-seat` can relay pairing to the daemon through the seat gate, with
  `--gate HOST:PORT`, `--gate-pin-file PATH` and `--identity DIR`, given all
  three or none. The pin file holds the gate key's fingerprint, exactly one, 64
  lower-case hex digits; it must be a regular file, not a link, of at most
  1 KiB, that group and other cannot write. `--identity` is the folder
  `ataniq-seat gate-init` wrote. The link uses pinned-key TLS at both ends and
  redials when it drops; a wrong pin never connects. While the link is down the
  phone is told `daemon_unavailable`; a code the daemon refused is
  `code_refused`, and any other refusal is `daemon_error`. The pairing code is
  never printed. Without the three flags pairing answers `daemon_unavailable`,
  as before.
