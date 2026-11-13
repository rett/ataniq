# Experimental Codex mock qualification

Run `make doctor`, then `sh scripts/go-tool.sh test -v ./qualification/codex/...`.
With no `ATANIQ_QUALIFY_CODEX`, the external test is skipped and no binary is
qualified. To exercise the recorded candidate in a disposable, separately
isolated environment with loopback, set `ATANIQ_QUALIFY_CODEX` to the absolute
path of a regular executable (a final-component symlink is rejected), then run
the same command. The subject is not a developer tool installed by this test.
The test accepts the research-observed SHA-256 and version only; a new binary or
catalog requires review and requalification. Logs include the digest, reported
version, generated configuration, advertised catalog and mock request results.
Every request's root `tools` and each `input` `additional_tools.tools` array
are compared with the recorded profile, including routing and schemas; prose
descriptions are excluded. A changed profile fails pending review.
The subject and any helper binaries must be trusted for the test environment;
the fixture is not a sandbox and does not establish helper provenance.

A local mock, with no real upstream and `requires_openai_auth = false`, scripts
an experimental dynamic callback, native patch, command and child attempts,
and raw JavaScript import, require, fetch, process and arbitrary-helper
attempts. It checks the callback, returned tool results, sentinel files and
mock network receipts. The advertised catalog and the code-mode helper list
remain incomplete evidence of all possible effects; helpers including patch and
MCP resource functions were observed in research. Exact behavioral results may
change between versions. Unknown versions and advertised catalogs fail closed;
passing this mock does not authorize a provider or prove complete containment.

A separate in-memory upstream observes the request-gate fixture. It exercises
an in-flight bounded response completing after authority loss, a held request,
stale epoch rejection, fresh admission, cancellation and errors. The external
probe also holds a native provider continuation at its local gate until a fresh
current-epoch decision. The fixture holds its authority mutex until upstream
response headers arrive, so revocation cannot commit during a blocked header
admission; cancellation can release it. The proxy assigns a waiting request
the current epoch after restoration, so that external probe does not establish
stale-origin binding. The separate fixture checks explicitly supplied stale
epochs. This is a test fixture for future broker semantics,
not a production boundary; retries, compaction and all internal provider paths
are not comprehensively covered. Durable grants, actual effect start and
delivery evidence are distinct; a missing journal entry does not prove
non-delivery.

No real authentication, hostile-workload containment, recovery or deployment
is tested. The intended threat model trusts the kernel and administrator, uses
rootless isolation and deny-by-default workload egress, and separately authorizes
dependency setup. Managed ChatGPT sign-in is intended first, API keys later.
The mock does not exercise either and does not use internal external-token APIs.
