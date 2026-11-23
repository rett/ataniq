- The daemon built with the owner channel's tag can serve the seat gate, the
  link of the phone's seat app, with `ataniqd run --owner-channel --seat-gate
  ADDR`. ADDR is one IP address of the host and a port; a host name, a wildcard,
  multicast or broadcast address and a zero port are refused. The gate is TLS
  1.3 with pinned keys: `ataniqd seat-gate init` makes the gate's key in
  `owner/gate/` of the state folder and prints its fingerprint, and the seat
  keys it lets in are pinned in `owner/gate/seat-pins` (mode 0600, one
  fingerprint per line, at most 4), which `ataniqd seat-gate pins` lists and the
  daemon reads at start. It serves the pairing call and the reads of the cards
  in frames of up to 257 KiB, so the largest card fits. With the seat origin
  configured it also serves a login and a decision from the phone, and without
  one it serves only the pairing call and the reads and decides nothing (see
  the entry on the login and the decision). A seat whose key is not pinned
  never reaches the service. A seat refused 16 times within 10 minutes for what
  it sent is answered `too-many-refusals` until those refusals age out; a
  refused pairing, which the seat relays for anyone who reaches its pairing
  page, is not counted, so a stranger cannot shut the owner's seat out with
  wrong codes. At start one line
  gives the address and the gate's key fingerprint, never a pin. Without the
  flag nothing listens on the network. Provisional and unqualified, for scratch
  state only. See `docs/lead-daemon.md`.
