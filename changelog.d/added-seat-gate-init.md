- `ataniq-seat gate-init --identity DIR` makes the seat's own key for the seat
  gate: `cert.pem` and `key.pem` in DIR, the key with mode 0600, DIR made with
  mode 0700 when it is missing. It prints one line, the key's fingerprint (64
  lower-case hex digits) for the gate's pin list, and nothing else. It never
  overwrites: an existing key file, or a folder that others can write, is
  refused and nothing is written.
