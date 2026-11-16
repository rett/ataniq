# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add a provisional `ataniq-console --demo [--theme dark|light]` terminal
  cockpit that shows fictional, read-only fixture projects, persistent leads,
  subagents, tasks and conversations within bounded terminal sizes. It is
  fixture-only: without `--demo` it reports that the backend is unavailable,
  and its control keys only explain why live controls are unavailable. Live
  control, provider integration, use over SSH and terminal restoration after a
  startup error remain unqualified.
- Pin the Bubble Tea v2.1.1 terminal console toolkit and its module
  dependencies, and preserve their dependency notices.

### Changed

- The terminal console's toolkit now uses patched, in-tree copies of
  `charmbracelet/x/vt` v0.1.0 and `charmbracelet/x/ansi` v0.11.9 under
  `third_party/patched`, selected by `replace` lines in `go.mod`. The patches
  keep a grapheme cluster whole when it arrives in two writes, and keep a
  UTF-8 character such as U+2733 from ending a window title early; `x/vt`,
  the terminal emulator the console's session pane is built on, is a new
  dependency. Each copy has a `NOTICE.md`; it is dropped once an upstream
  release contains the fixes. `go mod verify` does not cover directory
  replacements, so the doctor says so and git protects the copies, and
  `make test` now also runs their own tests (`make test-patched` runs only
  those; it covers x/vt and x/ansi with its parser, kitty and iterm2
  subpackages, not the sixel subpackage, which needs an uncached module).
- Record the order of work in the design documents. Qualification comes first
  and sandbox qualification precedes any Ataniq-managed lead; a provider's
  remote control stays an optional launch profile during the transition; a
  Codex lead is an interactive `codex` in its own session. Session hosting is
  decided by an attended spike and documented after it. Nothing here is
  implemented.
- Add a design document for approvals and delegation. Only the owner's direct
  words, or a delegation-chain grant whose signatures verify back to the
  owner's root key, approve anything. Grants have scope, expiry, a use count
  (single use for a sub-grant), revocation and audit; a message is never the
  authority, though a signed grant may travel by message and the gate verifies
  it; an owner channel is unreachable by leads; and relayed direction is never
  an approval. Delegation grants are kept in one qargi store and are honoured
  for qargi gates only in the first version. The designated-approver wording in
  the design baseline and client protocol is corrected to match. Specification
  only; nothing is implemented.
- Add a design document for roles and the authority split. Each project has
  exactly one assigned lead and a single supervisor spans projects; both are
  assignable roles separate from agent identity, provider, model and effort.
  A role changes hands only by a verified handoff (reconcile, exclusive
  ownership, fence the old holder) that grants no authority. If any step
  fails the outgoing holder keeps the role and an unresolvable outcome goes
  to the owner; who verifies each step is left to the handoff contract. The
  supervisor never acts on its own project's gates. Ataniq owns processes,
  sessions, lifecycle, attach, messaging, scheduling and escalation routing,
  qargi owns development tasks, evidence and gates, and each record has one
  authoritative system. Specification only; nothing is implemented.
- Record the owner-approved interface direction in the design documents: a
  terminal-first Bubble Tea console is the primary operator client, a web
  console with the same information architecture comes later, a shared typed
  command and event contract sits under both with MCP as the agent-facing
  surface, dark is the default theme, and unavailable or unqualified controls
  are shown with their reason. This replaces the earlier assistant-ui web
  console first direction. Only the fixture console is implemented.
- `make doctor` checks offline that every package and test for the host
  platform loads from the module cache with hashes matching `go.sum`, and that
  cached modules are unchanged since download. It prints the Go diagnostic,
  which names the failing module when the failure is in a module, and leaves
  out Go's `go: downloading` lines, which are misleading offline; when nothing
  is left it prints `(no diagnostic from go)`. A missing module is fixed with
  `make bootstrap`. For a checksum mismatch, restore `go.sum`, or run
  `chmod -R u+w` on the named module directory in the module cache, remove it
  and run `make bootstrap`. A modified module is handled the same way: its
  files are read-only, so run `chmod -R u+w` on it first, remove it, then run
  `make bootstrap`. Any other load error is in the failing package and is
  fixed there. It also checks that `grep` is installed, which the Go helper
  uses. When it is not, doctor prints `missing: grep` and skips the Go checks,
  and bootstrap, test and build stop with `missing: grep`.

## [0.0.8] - 2026-11-15

### Added

- Add a finite synthetic controller-handoff ledger, proof and generation model,
  independent evidence verifier and 36 frozen rows, including four
  mutated-evidence controls, plus five supplemental verifier checks.
  Real controller authority, receiving-gate exclusivity, native attachment,
  credential mediation and durable native admission remain unqualified.

### Changed

- Update README maturity and qualification scope to match this release.

## [0.0.7] - 2026-11-15

### Added

- Add private internal administration, resource, status and conflict contracts,
  bounded canonical bindings and projection helpers, preserving the public v1
  command contract and vectors.
- Add a private v2 encrypted cursor codec with canonical bounded decoding,
  bounded per-codec epoch reservations, rotation, failed-mint and fresh-construction tests.
  Authentication, service authorization and durable receipt integration remain
  pending.
- Extend SQLite default-VFS qualification with checked directory synchronization,
  explicit FULL checkpoint and close checks, descriptor-selected sync denial on
  the harness's pinned directory alias and SQLite's live main descriptor, and
  bounded lifecycle, identity-replacement and refusal controls. The directory
  denial target is the harness-owned alias. Directory-entry persistence for main-file creation and WAL creation/removal, production
  lifecycle, pooling, power-loss behavior and broader durability remain unqualified.

### Changed

- Update the README to describe the internal contracts, cursor codec and bounded
  SQLite lifecycle checks in this release, with production limits and native
  terminal priority retained.

## [0.0.6] - 2026-11-14

### Added

- Add a bounded Linux/amd64 SQLite default-VFS settlement qualification harness with
  child-only sync-failure injection, strict capability controls and relational
  evidence. Production durability remains unqualified, including directory lifecycle,
  pooling and live admission. See the qualification guide for the full scope.

### Changed

- Add a review of README and any website claims for every release, including hotfixes,
  to the contributing workflow.
- Correct the README maturity overview and clarify qualification limits.

## [0.0.5] - 2026-11-14

### Added

- Document journal recognition's read-only and recovery boundaries and its
  synthetic qualification fixtures.

- Add bounded trusted internal journal v1 recognition under existing-store
  ownership, preserving committed WAL state and pending recovery without writer
  SQL or journal recovery. Share the read-only preflight with ordinary Open;
  recognition grants no migration or public disclosure authority.

## [0.0.4] - 2026-11-14

### Added

- Add pure typed application command contracts for queued input and exact queued
  cancellation, bounded strict JSON codecs, owned canonical binary bindings and
  eight portable golden vectors. Keep authentication, durable admission and
  provider execution outside this package.

- Document application wire and canonical contracts, parser limits, immutable
  receipts and current status, and the boundary to future authentication and
  durable admission, including owner disposition for held queued work.

### Fixed

- Distinguish applied cancellation commands from cancelled queued inputs and
  specify the resource revision and historical cursor in admission receipts.

- Clarify authority ownership per independent Unix-user instance and the journal
  schema v1 shipped in 0.0.3, retaining explicit future schema evolution and
  unchanged ordinary opening behavior.

## [0.0.3] - 2026-11-14

### Fixed

- Retain one exact grant-conflict event per immutable attempt, preserve context
  errors on retiring Release retries, and preserve proven retired generations
  during Close without changing their evidence or durable uncertainty.

- Preserve exact empty journal BLOB outcomes when saving Prepared observations;
  reconcile a lost Release response only from the exact retired generation.
  Return unknown on failed grant-conflict commits, verify the pinned DQS probe
  diagnostic, and reject new lifetime admission promptly during Close.
  Add actual-driver, retirement, conflict, cancellation and explicit bounded
  capacity-recovery regressions without changing the schema or dispatch policy.

- Update the design index to describe the implemented guarded dispatch journal.

- Validate journal policy and grant expiry bounds by UTC instant across caller
  and host timezones.

- Preserve admitted journal failures across Release/Close retirement, reject
  late calls without changing retired activations, and capture coherent bounded
  snapshot failure projections. Reject foreign Prepare bindings before waiting
  or cancellation, retaining foreign Save isolation. Cover retirement response
  loss, cancellation, dirty reopen and independently counted no-replay behavior.

- Require native main-file OFD locking before journal ownership acquisition,
  and verify it after a consumed SQLite schema read on preflight and every
  physical connection before writer settings. Rejected competing opens and
  unrelated main-file descriptor closure preserve the live owner's SQL locks;
  processes already using POSIX SQLite locking are refused before acquisition.

### Added

- Document exact-generation Release response reconciliation, empty receipt BLOB
  preservation, failed conflict status, precise DQS diagnostics and prompt Close
  admission refusal. Describe fixed-bound FULL recovery refusal and the existing
  explicit larger-MaxBytes reopen path with preserved evidence and no automatic
  bound increase or capacity guarantee.

- Document journal retirement admission/publication ordering, coherent volatile
  snapshot capture, foreign-call isolation and the narrow settled-Denied
  retirement response-loss case, distinguishing draft and final regressions
  and graceful-close evidence from waited SIGKILL recovery.
- Document guarded journal composition, exact policy/grant binding, failure and
  retirement semantics, bounded eager inspection, and offline crash evidence
  with its limits; retain the accepted lifecycle and OFD ownership contract.
- Add an internal offline guarded dispatch journal on the owned SQLite lifecycle:
  exact-attempt capabilities, immutable trusted policy registration, atomic
  one-use grants, monotonic revocation, durable activation/recovery fences,
  bounded retirement, exact receipts and contradictions, and eager inspection
  with stable event identities. Exercise the frozen coordinator with fake
  boundaries, bounded SQLite faults and waited SIGKILL/reopen fixtures.
- Document journal OFD startup ordering, post-read verification and the separate
  SQLite-managed POSIX shared-memory lock domain, with caller path/filesystem
  responsibilities and the limits of the lifecycle regression evidence.
- Pin the SQLite journal driver and preserve its dependency notices.
- Add Linux journal storage Create/Open/Close/Info with exclusive inode ownership,
  transactional authority and stable identities, read-only identity preflight,
  verified SQLite connection settings, and finite operation and page bounds.
  This lifecycle foundation does not yet implement guarded dispatch journaling.
- Document journal lifecycle APIs, defaults, ownership, connection verification,
  refusal behavior and the distinction between ordinary reopen and unsupported
  restore; link the bounded foundation from the design contract index.

## [0.0.2] - 2026-11-14

### Added

- Add an internal deterministic effect-dispatch coordinator with explicit
  journal and current-authority start boundary ports. This is source-only
  infrastructure and does not qualify a provider or supply durable storage.
- Document the coordinator entrypoint, port obligations and integration limits.

### Changed

- Reject ASCII and Unicode whitespace in dispatch identities and approval IDs.
- Keep cancellation-independent saving an optional integration strategy.
- Clarify dispatch uncertainty projections and future durable recovery obligations.
- Cover cancelled outcome saves and separately retained completion evidence.
- Fence ambiguous dispatch preparation, retain conflicting receipt evidence,
  and recheck grant authority at the actual start boundary.
- Document receipt identity, recovery fencing and fresh grant checks.
- Cover grant target, payload and epoch binding conflicts.
- Validate dispatch journal receipts and isolate payload ownership across ports.
- Clarify unresolved dispatch evidence and the bounded boundary test scope.

## [0.0.1] - 2026-11-14

### Changed

- Clarify inspection marker ordering, cleanup authority and persistence limits.

- Keep the validated FD1 attempt directory open while inspection records an
  integrity incident, avoiding a second acquisition before marker publication.

- Clarify that host inspection can durably record an integrity incident and
  describe the separate durability-failure limitation.
- Persist inspection-detected typed FD1 integrity incidents in the owned attempt
  directory, preserving the start fence and original cleanup authority after
  artifact restoration and reopening.
- Record sanitized source-pinned FD1 and no-pressure entry host observations
  in the host manual while retaining their separate historical provenance.
- Describe the developer qualification foundation and pending production work
  in the README, including the provider-specific Codex mock tests.
- Add the opt-in `--r1-memory` probe mode with separate strict fixed R1
  request/context/control/evidence schemas, exact byte-hash binding, pinned
  read-only control reads, bounded anonymous-memory helper and private self-exec
  child protocol. Test ordering, retention, accounting, deadlines and failures
  with fake allocation and bounded child-only handshake checks. Ordinary probe,
  hold, FD1 and host launch behavior remained unchanged at that source stage.
  Pressure R1 host launches remain unsupported. The later `r1-entry` host case
  admits no-pressure child entry. No pressure run or runtime qualification
  support is claimed by this source change.
- Preserve nonblocking mode on R1 input/output and private child pipes by caching
  raw descriptors before setup. A bounded stalled-handshake regression covers
  pipe timeout and actual abort/reap without running the pressure actor.
- Reject contradictory R1 failure phases, consumed hashes and touch accounting
  in both wire directions while preserving partial touch and failed cleanup.
  Withhold uncorroborated child handshake identities on metadata failure while
  retaining the owned process for bounded cleanup. Document the R1-only
  `/dev/null` qualification dependency for future host integration; no launcher,
  bind, actual pressure run or runtime support claim is added.
- Latch typed FD1 release/collection authentication incidents before fallback
  saves or finalizer reads, so immediate artifact restoration cannot erase a
  detected failure. Keep the original authenticated cleanup authority and start
  fence across restart without trusting never-adopted evidence. Preserve both
  the original cause and save error when an unknown transition cannot persist;
  a failed save cannot establish cleanup. No live qualification claim is added.
- Retain every detected typed FD1 collection integrity incident across failed
  observations, entry authentication and both completion saves, preserving raw
  evidence and original errors. Retain authenticated stop authority before
  budget/handle checks and through refused bound/released saves. Exact cleanup
  remains available with a persistent start fence; failed durable saves retain
  filesystem poisoning. These recovery corrections add no live matrix claim.
- Preserve authenticated typed FD1 bindings during cleanup-only recovery of a
  lost service-start acknowledgement, including interruption during cancellation.
  Reject inconsistent typed cleanup saves. Retain original inspect/stop authority
  after artifact loss or change, exposing `fd1_integrity_failure` and reconciliation
  reason `typed-runtime-integrity-failed`; keep new starts fenced after cleanup and
  restart. Artifact restoration cannot clear the recorded failure or qualify the
  attempt. Recheck integrity after durable observation saves so mid-save artifact
  changes remain inconclusive while retaining raw evidence. This adds no live FD1
  matrix claim.
- Add the explicit optional `descriptor_profile: fd1-typed` host selection,
  frozen directory/leaf plan challenge and metadata-only read-only bind.
  Authenticate separate runtime artifact evidence before durable binding and
  release, preserving historical absent-profile encodings and plan hashes.
  This supplies no live qualification or dynamic backend closure claim.
- Integrate fixed directory128/pipe256/Unix-stream384/regular512 capabilities
  through the owned injector, released final wrapper and typed observation
  collection. Authenticate and retain runtime bindings separately from the
  immutable plan; close every temporary alias/peer before publication, validate
  without channel consumption and preserve strict regular-file link checks.
  Require scoped fresh direct controls, exact typed inventories and complete
  operation evidence. Retain failed observations and explicit socket-admission
  test gaps; the reviewed live FD1 matrix remains a separate lead action.

- Require exact descriptor inventory rows and durable owned stdio regular-file
  identities before accepting complete post-exec host qualification evidence.
  Reject empty or contradictory tables while retaining failed raw observations
  separately from lifecycle cleanup; this adds no runtime support claim.
- Add a versioned final descriptor policy to new bounded host qualification
  attempts: use a private static-driver reexec, pinned-thread close_range
  CLOEXEC/UNSHARE, raw role revalidation and fail-closed syscall.Exec. Keep
  sparse synthetic aliases and restricted weakened controls, separate planned
  receipts from post-exec fake/BPF identity observations, and preserve legacy
  command digests and failed isolation evidence for inspect and exact cleanup.
  This is backend-chain hardening infrastructure, not runtime/provider support.
- Reject qualification ELF runtime metadata touched by overlapping PT_LOAD
  mapping pages, including logically disjoint segments; check host-page rounding
  overflow for both dynamic and runtime string extents.
- Keep qualification campaign fences sticky across early save/create failures
  and later successful completions. Publish phase/reason only after durable
  replacement, validate ownership before failure markers, and retain restart
  fencing without changing cleanup or launch authority.
- Read qualification subject ELF dependencies from loader-visible program headers
  and bounded runtime tables; reject RPATH/RUNPATH tags and malformed dependency
  strings even with absent or mismatched section headers, sharing checks across
  static and dynamic subjects and every frozen launch-chain check.
- Distinguish sandbox probe directory pin and Unix socket/setup failures from
  target open/connect results; reject overlong Unix paths and insufficient nonce
  budgets before fixture access, and require exact nonce replies followed by EOF
  under a bounded deadline while preserving successful open/connect facts.
- Preserve sandbox probe open/connect effects and exact failed stages with
  known errno, while keeping incomplete exchanges inconclusive; specify Unix
  fixture UID projection and complete frozen-path caller requirements.
- Preserve Codex fixture ledger diagnostics on failure and strengthen shutdown
  and oversized request regression checks.
- Check Codex proxy attempts and upstream receipts after bounded fixture shutdown,
  and detect continuations that cross the offline gate before observation.
- Bound Codex qualification subprocess output drains and report cleanup failures.
- Reject extra or malformed Codex mock receipts and unsuccessful terminal turns.
- Check every Codex mock request against the recorded complete tool profile,
  and document the request-gate fixture's admission limits.
- Restore fixture checks for isolated Go environment settings.
- Confine documentation source reads to the checked root.
- Record the verified bootstrap toolchain for offline checks with older Go drivers.
- Isolate Go module mode and confine documentation link checks to their root.
- Enforce the exact Go pin and isolate offline gates from inherited Go settings.

### Added

- Raw `fake-dir-fd`, `fake-pipe-fd` and `fake-socket-fd` observations with an
  optional immutable `fd1-typed` challenge and a strict bounded four-record
  `inherited-bindings.json` schema. Validate context and fixed object identities
  before content access, bound nonblocking channel reads and require exact
  synthetic frames plus EOF while preserving intermediate effects. Owned child
  regressions cover rejection without consumption. The opt-in host integration
  is described above; live FD1 qualification remains separate and existing
  serialized request meanings are preserved.
- Explicit Linux/amd64 bubblewrap qualification driver with durable campaign
  ownership and restart fencing, exact service/slice reconciliation, private
  reexec fake-FD injection, finite ready/hold cancellation evidence and offline
  static builds. Deterministic transcript checks cover crash/identity failures;
  live host behavior and full profile support remain separate verification gates.
- Bounded Linux/amd64 sandbox observation protocol and static probe for passive
  inventory and owned fake file, descriptor and Unix fixtures, plus a flat
  trusted-side reader requiring independent workload-stop and frozen-tree proof.
  Backend execution and qualification decisions remain separate gates.
- An experimental offline Codex mock probe and request-gate fixture; the
  exact external subject is opt-in and remains unqualified.
- Offline checks for local documentation links, references and heading anchors.
- Pinned Go development toolchain, bootstrap, offline doctor and test gates.
- Clarified dispatch fencing, single-use approvals, authorization projections,
  operations views and federation limits in the proposed design contract.
- README entry point to the design contract and its selected client and runtime direction.
- Design contract baseline for authority, runtime recovery, client protocol,
  security qualification, work relationships and executable acceptance scenarios.
- MIT license and copyright attribution to Everett C. Haimes III.
- Initial MCP-first architecture brief and Go core/TypeScript UI decision.
- Federation architecture with directional grants, MCP task and message
  exchange, local admission, revocation, recovery, and acceptance criteria.
- Initial two-host federation target and load-balancing requirements covering
  eligibility, shared quotas, capacity reservations, and safe reassignment.
