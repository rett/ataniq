# Checkpoint home

On Linux, `make home-build` builds `build/home/ataniq-home`. The home accepts
authenticated witness polls and keeps the ledger at its enrolled path. Run the
attended pilot under a dedicated, unprivileged home account.

## Home configuration

Pass a clean absolute path to `--config FILE`. JSON field names are case
sensitive and each occurs once. Pre-create the empty, home-owned 0700 state
directory. Keep the seed, TLS identity, transfer files and binding outside it.
Keep the binding in a separately protected restore domain with a directory the
home account can write. Use absolute paths through real directories owned by
root or the home account. The config and witness certificate must each be a
regular file with one hard link and write access restricted to its owner.

```json
{
  "state": "/var/lib/ataniq-home/state",
  "home_key": "/var/lib/ataniq-home/seed",
  "tls_dir": "/var/lib/ataniq-home/tls",
  "binding": "/var/lib/ataniq-home-records/binding.json",
  "listen": "192.0.2.20:7444",
  "epoch": "1",
  "slot": {"witness": "aw-01", "authority": "home-ledger-1", "store": "home-slot-1"},
  "trust_sources": ["192.0.2.10/32"]
}
```

Replace the example addresses and slot locally. Use a canonical address
literal, brackets around IPv6, a nonzero decimal port and a nonzero decimal
activation epoch. Slot identifiers use printable ASCII from space through
tilde. The optional `pending_journal` path is reserved. The endpoint restores
an outstanding advance from the existing signed intent in `state/advance.pending`.

## Owner ceremony

Follow these steps after establishing the authority-host installation baseline.
Keep managed admission closed throughout the pilot.

2. **Create the home identity.** Give the seed a home-owned 0700 parent and the
   TLS directory a parent protected against group and other writes. Both
   destination names must be free. As the dedicated home account, run

   ```sh
   ataniq-home identity-init --config /etc/ataniq-home/config.json
   ataniq-home pins --config /etc/ataniq-home/config.json
   ```

   Verify the raw 32-byte Ed25519 seed is 0600 and the TLS fingerprint is
   64 lowercase hex digits. The new 0700 TLS directory contains 0600
   `cert.pem` and `key.pem`. Record `home_tls_pin` and `home_public_key`.

3. **Create the witness keys.** Generate the witness Ed25519 seed and witness
   TLS identity under the witness account. Generate the owner's TLS identity
   as root in `/etc/authority-host/witness/owner-tls`. Record all three TLS
   fingerprints and both signing public keys in the owner's pairing record.

4. **Exchange pins in both directions.** Over an independently authenticated
   channel, provision the witness with `home_tls_pin`, `home_public_key`,
   `home_endpoint` and the owner TLS pin. Add the witness certificate in DER
   format and its two 64-digit lowercase hex public values to the home config.

   ```json
   "witness_cert": "/etc/ataniq-home/witness.der",
   "witness_tls_pin": "WITNESS_SPKI_SHA256",
   "witness_public_key": "WITNESS_ED25519_PUBLIC_HEX"
   ```

   Compare each value on both devices, including the slot, epoch and expected
   source prefix. Run `ataniq-home check --config /etc/ataniq-home/config.json`
   or `ataniq-home serve --check --config /etc/ataniq-home/config.json`.
   Both construct the endpoint and print `home check ok` without binding a
   port. The witness's local check also requires its initialized store in step 6.

5. **Sign the activation record.** Record the witness identity, signing key,
   activation epoch and directory commitment. Keep the owner's signed copy
   outside the witness backup.

6. **Install and activate the witness.** As the owner, run
   `runuser -u ataniq-witness -- WITNESS init --config CONFIG`, then
   `runuser -u ataniq-witness -- WITNESS check --config CONFIG`, using the
   verified executable and witness config. Install nftables rules allowing the
   witness UID to reach only the home address and TCP port (here
   `192.0.2.20:7444`), the matching `IPAddressAllow` drop-in and the unit link.
   Activate through `ah-switch`. As root, verify `WITNESS status --config CONFIG`
   reports sequence 1 and zero slots, receipts and fences. Record the genesis
   head off-host. [Authority-host witness guide](authority-host-witness.md)

7. **Start the home by hand.** Run the listener in the foreground under the
   home account, leaving the unit disabled.

   ```sh
   ataniq-home serve --config /etc/ataniq-home/config.json
   ```

   The witness can poll before enrollment and receives Idle when the queue is
   empty. Confirm the poll at the home firewall, record the observed source
   address and set the inbound rule and `trust_sources` to that host prefix.
   If the config changes, stop with SIGINT or SIGTERM, check and restart.
   The command prints one line on startup and one on shutdown.

8. **Enroll the disposable mirror.** In a protected transfer directory on the
   home, prepare the ledger at its final path.

   ```sh
   ataniq-home enroll prepare --config /etc/ataniq-home/config.json --out prepared.json
   ```

   Carry the exact JSON bytes over an authenticated channel. On the authority
   host, compare the file's SHA-256 with the home's `prepared_sha256`. Use the
   local owner client to call `enrollment.Submit` through the witness owner
   socket. This checkout supplies the owner library API. Return its raw signed
   receipt to the home. As the home account, run

   ```sh
   ataniq-home enroll finalize --config /etc/ataniq-home/config.json --prepared prepared.json --receipt receipt.bin
   ataniq-home status --config /etc/ataniq-home/config.json
   ```

   Finalization verifies the request, witness receipt and original ledger
   identity, publishes the 0600 binding, and prints `ledger_id`,
   `enrollment_id` and `receipt_digest`. Archive the exact receipt off-host.
   Check that witness status reports one slot, the receipt matches the request,
   signing key and epoch, and local replay agrees with a fresh signed read.

   A second prepare refuses an occupied root. After interruption or a failed
   export, recover the same signed request to a new file with
   `ataniq-home enroll resume --config /etc/ataniq-home/config.json --out resumed.json`.
   Compare the digest before replaying an uncertain submission. Repeating
   finalize at genesis recovers the same binding. Prepared exports and binding
   files use exclusive, atomic publication with Linux `O_TMPFILE` and
   `/proc/self/fd`.

`status` works offline, including before identity creation. It reports the
presence of the signing key and TLS files, prepared record, binding validation
and ledger head sequence from local replay. A fresh header has sequence 0.
Exit 0 indicates success, 1 indicates refusal, and 2 indicates incorrect usage
or the reserved command `retire`.

## Observe the mirror

Stop the foreground listener before running
`ataniq-home observe --config FILE [--events N] [--archive FILE]`. Observe
serves witness polls on the configured address, claims a fresh session and
appends synthetic signed assignment events for project `mirror-1`. The default
is three events and the maximum is sixteen. Use `--events 0` after a previous
advance to compare an already archived head. Each invocation claims a session.
Before the next invocation, fence the previous writer and release the claim
through the witness owner channel.

A completed comparison prints the replayed head, retained advance receipt
head, freshly challenged witness head and optional owner archive. It records
the result in the private state file `observation.json`. Differences give
`observation: DISAGREE` and exit 1. The last output line counts managed starts
and launches. This producer has no execution or process-management capability.

Supply a protected regular archive file with exactly one ASCII line,
`sequence=N head=HEX`, terminated by LF. Use the slot's unsigned decimal
assignment sequence (no leading zeroes) and its 64 lowercase digest digits.
Keep the original signed evidence off-host. The witness CLI in this checkout
prints directory journal position and digest in its status summary. Obtain
the slot head from the owner client's authenticated slot read. Copying the
directory position or an enrollment receipt digest will give a disagreement.
