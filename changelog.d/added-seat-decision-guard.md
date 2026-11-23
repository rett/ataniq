- `ataniq-seat` follows the authority host's decision contract: before it
  relays a decision's challenge or a decision to the gate, every time, it
  asks `decisionguard.Check` of the release switch's state folder,
  `--decision-guard DIR` (an absolute folder, `/var/lib/authority-host` when
  not given). While `decisions.inhibit` is there, or the folder or the file
  cannot be looked up, nothing is relayed and the phone shows "Decisions are
  paused on this host. Nothing was sent." under the code
  `decisions-inhibited`, and may tap again once decisions are taken again; a
  refused decision's room at the gate is given back. It fails closed: on a
  host without that folder no decision is relayed. Pairing, login and the
  reading of cards decide nothing and go on. Each refusal is logged with the
  guard's fixed code.
