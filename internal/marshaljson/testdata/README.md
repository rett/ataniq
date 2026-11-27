The J1 pin is the four `KIND/v1` directories from Qargi 0.3.9 under
`tests/fixtures/json/`. Both ZIP files preserve the upstream paths and bytes.
`registries.zip` holds just the four schema files embedded by the decoder.
`cases.zip` holds the four case lists and all their inputs, including malformed
JSON and refusal lines with their wire prefix.

`MANIFEST.sha256` covers the uncompressed contents of both archives. The tests
check its digest, verify every entry and run every case in memory. A separate
test compares the same manifest against an installed Qargi fixture tree when
`/opt/qargi` exists. Qargi uses the same MIT license as Ataniq.
