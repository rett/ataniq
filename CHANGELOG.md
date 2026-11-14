# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
