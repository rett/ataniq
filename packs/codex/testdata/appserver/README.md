# App-server stdio fixtures

`schema.json.gz` contains the complete, unmodified
`codex_app_server_protocol.schemas.json` from the local 0.172.0 CLI. To
reproduce the capture offline, run the following in a temporary directory.

```sh
codex --version
codex app-server generate-json-schema --out schema
sha256sum schema/codex_app_server_protocol.schemas.json
gzip -n -9 -c schema/codex_app_server_protocol.schemas.json > schema.json.gz
```

The codec checks the SHA-256 of the **uncompressed bytes**. The capture
uses the default schema surface.

`events.jsonl` includes the turn start and completion frames from the first
run in the earlier 0.169.0 mock qualification recording
(`codex-provider-mock-observations.json`), with disposable thread and turn
IDs. Both use the notification timestamp field present in the pinned schema.
The other frames, including the permission decisions in `grants.jsonl`, are
synthetic examples of the 0.172.0 wire shapes. Tests replay them in memory.
