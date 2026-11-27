# Checkpoint home identity

The Linux command reads a JSON configuration from the clean absolute path
given by `--config FILE`. Field names are case sensitive and each occurs
once. Pre-create the home account's 0700 state directory. Use absolute paths
through real directories owned by root or the home account.

```json
{
  "state": "/var/lib/ataniq-home/state",
  "home_key": "/var/lib/ataniq-home/seed",
  "tls_dir": "/var/lib/ataniq-home/tls",
  "listen": "192.0.2.20:7444",
  "epoch": "1",
  "slot": {"witness": "WITNESS_ID", "authority": "LEDGER_AUTHORITY", "store": "LEDGER_STORE"},
  "trust_sources": ["192.0.2.10/32"]
}
```

Use the witness's observed source prefix, exact slot and nonzero decimal
activation epoch. The listen address takes a canonical IP literal and a
decimal port. Bracket IPv6 addresses. Slot identifiers use printable ASCII
from space through tilde. The optional `pending_journal` and `binding` fields
reserve absolute file paths.
