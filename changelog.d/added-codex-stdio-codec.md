- The Codex holder pack supports app-server stdio for the pinned 0.172.0
  schema. It starts a read-only thread with on-request approval and binds to
  the thread the server returns. Each idle observation permits one turn start.
  A rejected or abandoned start releases its reservation. Steering checks the
  target turn. A turn-scoped permission reply grants only the selected subset
  and answers the original, unaltered request once. The codec returns an
  ID-matched decline for stale, invalid or excess permission requests, command
  and file approvals, user input, MCP elicitation and unsupported requests.
