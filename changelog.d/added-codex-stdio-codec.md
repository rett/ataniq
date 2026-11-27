- The Codex holder pack includes an app-server stdio codec for the pinned
  0.172.0 schema. The codec requests a read-only thread with on-request
  approval and binds to the thread the server returns. Each message needs an
  idle thread and reserves its turn start until the server rejects the request
  or names a turn in its answer and reports that turn completed. Forgetting a
  timed-out start keeps the reservation for a late answer. If the outcome never
  arrives, further messages return `ErrTurn` until the session restarts.
  Closing the thread or reporting a system error prevents further messages on
  that thread. Steering checks the target turn.
  A turn-scoped permission reply grants only the selected subset
  and answers the original, unaltered request once. The codec returns an
  ID-matched decline for stale, invalid or excess permission requests, command
  and file approvals, user input, MCP elicitation and unsupported requests.
  Protocol sessions escape backslashes and non-printable characters in event
  text, including message deltas and error details, while preserving newlines
  and tabs.
