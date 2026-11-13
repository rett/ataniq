# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

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
