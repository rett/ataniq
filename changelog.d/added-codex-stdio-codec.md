- The Codex holder pack includes an app-server stdio codec for the pinned
  0.172.0 schema. The codec requests a read-only thread with on-request
  approval and binds to the thread the server returns. An idle notification
  received while a turn start is pending keeps that start reserved.
  A rejected start restores idle. A timed-out start may have reached the
  server, so forgetting it blocks another start until the server reports idle
  or completes the observed turn. Steering checks the target turn.
  A turn-scoped permission reply grants only the selected subset
  and answers the original, unaltered request once. The codec returns an
  ID-matched decline for stale, invalid or excess permission requests, command
  and file approvals, user input, MCP elicitation and unsupported requests.
