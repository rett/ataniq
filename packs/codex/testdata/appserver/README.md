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

To replay a permission decision, first encode `thread/start` and decode its
matching result, then decode the turn notification and permission request.
Pass the original request bytes to `DecodeGrant` and `EncodeGrant`. Encoding a
valid reply consumes the request. A turn change discards its outstanding
requests.

For a stale permission request or an unsupported server request, `Decode`
returns a `*protocol.Decline`. The holder can use `errors.As` to retrieve it,
frame its `Reply` with a line feed, send it and continue. Command and file
approvals and MCP elicitations have native decline results. User input and
unknown methods receive request errors. A stale permission reply grants an
empty profile for the turn.
