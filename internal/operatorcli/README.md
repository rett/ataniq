# Provisional operator CLI adapter

This package has no executable or transport. `Run` receives a context, a
backend, arguments, input and output streams. The backend supplies trusted
authentication and session context; `--client` is only the lookup target.
`--authority` and `--id` are also lookup references, not credentials. No flag
authenticates a caller. On every Submit, Show and exact retry, the backend must
revalidate authentication, current permissions, exact target scope and
disclosure. Hidden and absent results must remain nonrevealing. The backend must treat context cancellation as
detachment from observation, never as cancellation of durable work. There are
no automatic retries; callers retain the exact command identity for any later
reconciliation. These ports and outputs require reconciliation with the real
authenticated service and read ports before merge.

The provisional port has `Submit(ctx, Envelope) (AdmissionReceipt, error)` and
`Show(ctx, CommandKey) (CurrentStatus, error)`. It returns a domain error for
an authoritative result code, `ErrUnavailable` only when it can prove the
service was not invoked, and any other error for an unknown observation. A
domain code does not by itself confirm admission or nonadmission, and
`storage_unknown` remains uncertain. Ambiguous joined errors require a policy
when the backend is reconciled. A pre-cancelled context is unavailable before
invocation; cancellation after
invocation is unknown unless the backend proves otherwise. The adapter checks
pre-cancellation before opening or reading input, but arbitrary in-flight
Reader and Writer cancellation belongs to stream composition. Receipt values are
not proof of real durability. The admitted revision is the mutation resource's
revision at admission, not current status. Status states are queued, held,
cancelled and applied. Identifiers and revisions use appmodel's ID syntax and
maximum length. A cursor is opaque, nonempty UTF-8 of at most 512 bytes. This
provisional adapter bound needs reconciliation with the actual backend. The
backend must bind receipt identity and digest to the exact submitted envelope
and server-derived enrollment. Show returns current status, not a full
reconciliation record or the original receipt.

`command submit --file PATH --format json` reads at most appmodel.MaxWireBytes;
`-` reads stdin. `command show --authority A --client C --id ID --format json`
looks up that exact key. Flags are required once, as separate arguments. `help`,
`--help`, and `completion bash|zsh|fish` emit static text without a backend.

Machine output is one JSON object followed by LF. Success is `kind: receipt`
with `command` (`authority`, `client_id`, `id`), lowercase hex `digest`,
`admitted_revision`, and `cursor`; or `kind: status` with `command`, `revision`,
and `state`. Errors have `kind: domain_error` and an appmodel `code`, or
`kind: observation_error` and code `unavailable` or `unknown`. Unrecognized
backend errors, malformed backend responses and unknown domain codes are
`unknown`. An invalid or oversized encoded response is also `unknown` before
any output write. Diagnostics never include backend error strings or input text.
Exit codes are 0 for success, 1 for backend/observation outcomes, 2 for input
or usage errors, and 3 for failed or short output writes. A write failure after
submission cannot establish whether admission happened; reconcile the original
identity. Partial output is possible when the output stream fails.

The counting fake used in tests is an adapter test double, not authentication
or durability qualification.
