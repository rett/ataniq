# Checkpoint home identity

On Linux, run `make home-build` to build `build/home/ataniq-home`. The
examples below use that executable as `ataniq-home`.

The command reads a JSON configuration from the clean absolute path
given by `--config FILE`. Field names are case sensitive and each occurs
once. Pre-create the home account's 0700 state directory. Use absolute paths
through real directories owned by root or the home account. The config and
witness certificate must each be a regular file with one hard link, owned by
root or the home account, with write access restricted to the owner.

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

## Create the home keys

Run these commands as the dedicated, unprivileged home account. Give the
seed a real, home-owned 0700 parent directory. The TLS directory's parent
must deny group and other writes. Both destination names must be free.

```sh
ataniq-home identity-init --config /etc/ataniq-home/config.json
ataniq-home pins --config /etc/ataniq-home/config.json
```

Initialization creates the raw 32-byte Ed25519 seed (0600) and the new TLS
directory (0700) with `cert.pem` and `key.pem` (0600). Keep both private
keys under the home account's control. Send the two public values printed by
`pins`, `home_tls_pin` and `home_public_key`, to the witness operator over
an independently authenticated channel.

## Verify the witness

Obtain the witness certificate in DER format and its two public values over
the operator's authenticated exchange. Add all three fields to the same JSON
object and replace the placeholders with the recorded values.

```json
"witness_cert": "/etc/ataniq-home/witness.der",
"witness_tls_pin": "WITNESS_SPKI_SHA256",
"witness_public_key": "WITNESS_ED25519_PUBLIC_HEX"
```

Both public values use 64 lowercase hex digits. The home and witness need
distinct TLS and ledger signing keys. Run the read-only preflight as the home
account. It verifies the local identity, witness certificate fingerprint and
link configuration, then prints `home check ok` on success.

```sh
ataniq-home check --config /etc/ataniq-home/config.json
```

Exit 0 indicates success, 1 indicates refusal, and 2 indicates incorrect
usage or a reserved command. The reserved commands `serve`, `enroll`,
`observe`, `status` and `retire` print `not built in this release`.

The witness operator records the home's public values in the witness
configuration. [Authority-host witness guide](authority-host-witness.md)
