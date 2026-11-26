- Ship `ataniq-witness` as an optional authority-host release component. It
  polls one pinned home, records durable checkpoints and serves signed status
  and storage acknowledgement over a private owner socket. The release switch
  checks its health and stops it on rollback. The operator guide covers pin
  exchange, recovery, backup limits and re-enrollment.
