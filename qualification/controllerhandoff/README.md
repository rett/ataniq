# Controller handoff model (synthetic, finite slice)

This synthetic qualification model and its independent verifier cover only the
frozen L01–L32 and C01–C04 slice. They are not a production gate or authority.
The exact source was independently reviewed through packet
`bc5ec4a326a3ececa0870e2de69fee8d1e1c200b24e9733752788c2de77b386d`.
The 36 frozen rows were then executed. All 32 lawful rows (L01–L32,
`ASSERT_PASS`) and four mutated-evidence controls (C01–C04, expected
`ASSERT_VIOLATION`) matched every frozen expectation, including ledger, ranges,
refusals, dispositions, checkpoints, outcome, sink bytes, assertion and violation index.
Results bind the exact reviewed executable source. Comments in the test files
and this README were corrected afterwards without changing executable
statements, so their file hashes differ from the packet in comments only.
Source changes require their own review and checks. The initial frozen-row run
excluded the five supplemental verifier checks (`TestPartialEvidenceVerifier`).
Those checks received separate desk review for first execution at the
whole-feature offline checkpoint; their execution result belongs to that
checkpoint receipt. They use hand-built evidence and do not widen the slice. Proof-only records are retained evidence, separate from physical
writes. Sink labels and the gone-sink mark retain L25's evidence.
Native capability, framing and semantic delivery remain unqualified.

Fable's source review identified broader model deviations that remain
unqualified: grant events ignore payload identity; settlement is not bound to
the full grant tuple; results after multiple switches and the default outcome
are not generally guarded; INCOMPLETE is not a model outcome; state labels can
be stale; controller attachment currency is not modeled; proofs can mutate
following TRACE_INVALID; request and
capture budgets have limitations; and issues do not identify delivery kind.
The verifier does not establish correctness outside the supported slice.

The design must resolve whether an identical request after commit is idempotent
or stale, whether a valid proof for a still-pending write is refused, and
whether a tick outside FENCED is refused. These ambiguities are not resolved by
this source. Six old-return holds, live input, credentials, durable admission,
integration/release gates and real references remain outside this qualification.

The 36 frozen rows were executed with
`sh scripts/go-tool.sh test -count=1 -run '^TestFrozenControllerHandoff$' ./qualification/controllerhandoff`.
Ordinary `make test` runs doctor and then `scripts/go-tool.sh test ./...`,
which covers this whole package and therefore also the five supplemental
checks. The whole-feature offline checkpoint records that result.

The verifier supports at most three request events per generation as a conservative
finite input subset. This is not a concurrency budget: the model has one pending
winner and immediate losers, and more than three simultaneous requests remain
unrepresented and unqualified. Unsupported inputs yield ASSERT_INCOMPLETE.
The binding labels syn-beta-1 and syn-beta-2 stand for contract sink labels
syn-sink-1 and syn-sink-2 respectively. L23 retains only a contested boolean;
it does not establish the contract's contested range [2,6). V11 has no event
representing a forbidden claim and therefore is not a negative test of one.
