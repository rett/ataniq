- Link the lead guides from `README.md` and `docs/architecture.md`, which now
  name the lead injection guide, the lead intake guide and the two opt-in
  qualification manuals (lead sandbox, lead mirror) beside the holder, sandbox
  and daemon guides. The guides point at each other and no longer say that a
  holder or a daemon does not exist: each says what the others built, and that
  none of these parts calls another yet. `docs/lead-intake.md` adds that the
  owner's git reads a git bundle file found on the host at the template's
  `serve.repository` path, so that path must not exist on the host.
  `docs/lead-daemon.md` no longer says that the daemon's packages build on
  Linux only: the daemon runs on Linux only, and for another system `ataniqd`
  builds as a stub and the packages compile without their Linux files.
  The status notes of the two amendments of 2026-11-17 in `docs/design` no
  longer say that the scratch holder and attach client are being built and that
  no recogniser exists: they name what is built, and say that it is not joined
  and that nothing sets `ready`. No contract text changes.
  The maturity section of `README.md` describes the tree without naming a
  release; it named 0.0.8.
  Documentation only.
