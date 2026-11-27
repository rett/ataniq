The J1 pin is the four `KIND/v1` directories from Qargi 0.3.9 under
`tests/fixtures/json/`. Both ZIP files preserve the upstream paths and bytes.
The decoder embeds the four schema files from [registries.zip](../schema/registries.zip).
`cases.zip` holds the four case lists and all their inputs, including malformed
JSON and refusal lines with their wire prefix.

`MANIFEST.sha256` covers the uncompressed contents of both archives. The tests
check its digest, verify every entry and run every case in memory. A separate
test compares the same manifest against an installed Qargi fixture tree when
`/opt/qargi/VERSION` identifies exactly 0.3.9. Qargi uses the same MIT license as Ataniq.

The decoder checks the registries’ conditional field groups when assessing
evidence. The pin leaves some source facts implicit.

- Status and check omit the project's integrity mode. A row missing any of
  `integrity`, `integrity_note` and `signer` cannot establish completeness.
  Handoff supplies the mode, so all three are absent when it is off.
- A lead without a recorded worktree has incomplete identity evidence.
  `succeeds` identifies a predecessor only when the lead is a successor.
- An unreadable suite record makes that feature incomplete. For a recorded
  environment check or apply, each conditional field depends on what the source
  record held. Missing fields make that handoff incomplete.
