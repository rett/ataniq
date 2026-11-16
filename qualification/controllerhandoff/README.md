# Controller handoff model (source-only)

This synthetic qualification model and its independent verifier cover only the
frozen L01–L32 and C01–C04 slice. They are not a production gate or authority.
The source has not received final independent exact-source review or been
executed: zero assertions have been executed. Do not run it until that review
is complete. Frozen ledger, ranges, refusals, outcomes and physical sink bytes
are checked beside the verifier; proof-only records are retained evidence, not
physical writes. Sink labels and the gone-sink mark retain L25's evidence.
Native capability, framing and semantic delivery remain unqualified.

Fable's source review identified broader model deviations that remain
unqualified: grant events ignore payload identity; settlement is not bound to
the full grant tuple; results after multiple switches and the default outcome
are not generally guarded; INCOMPLETE is not a model outcome; state labels can
be stale; controller attachment currency is not modeled; proofs can mutate
following TRACE_INVALID; restart-lost retains excess state; request and
capture budgets have limitations; and issues do not identify delivery kind.
The verifier does not establish correctness outside the supported slice.

The design must resolve whether an identical request after commit is idempotent
or stale, whether a valid proof for a still-pending write is refused, and
whether a tick outside FENCED is refused. These ambiguities are not resolved by
this source. Six old-return holds, live input, credentials, durable admission,
integration/release gates and real references remain outside this qualification.

After completion of independent review, the intended execution command is
`sh scripts/go-tool.sh test ./qualification/controllerhandoff`. It has not been
run in this source-only phase.
