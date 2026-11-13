# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Confine documentation source reads to the checked root.
- Record the verified bootstrap toolchain for offline checks with older Go drivers.
- Isolate Go module mode and confine documentation link checks to their root.
- Enforce the exact Go pin and isolate offline gates from inherited Go settings.

### Added

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
