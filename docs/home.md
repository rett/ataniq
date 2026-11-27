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
If a crash tears the first stage write at the confirmed enrollment head, the
endpoint can restart. Observe clears the unsent stage after confirming its
fresh session claim. Preserve a torn stage with a moved head or an advance
receipt for owner reconciliation.

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
   `home_endpoint` and the owner TLS pin. Add the witness certificate (one raw DER certificate or
   the single `cert.pem` CERTIFICATE block from identity creation) and its two 64-digit lowercase hex public values to the home config.

   ```json
   "witness_cert": "/etc/ataniq-home/witness.pem",
   "witness_tls_pin": "WITNESS_SPKI_SHA256",
   "witness_public_key": "WITNESS_ED25519_PUBLIC_HEX"
   ```

   Compare each value on both devices, including the slot, epoch and expected
   source prefix. Run `ataniq-home check --config /etc/ataniq-home/config.json`
   or `ataniq-home serve --check --config /etc/ataniq-home/config.json`.
   Both check retirement and the protected installation directory, construct
   the endpoint and print `home check ok` without binding a port. The witness's local check also requires its initialized store in step 6.

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
   The command prints one line on startup and one on shutdown. For the
   partition drill, stop `serve`, confirm that the witness loses the poll
   path, then restart it with the same config and confirm that polls resume.
   Keep the ledger and pairing in place throughout the interruption.

8. **Enroll the disposable mirror.** In a protected transfer directory on the
   home, prepare the ledger at its final path.

   ```sh
   ataniq-home enroll prepare --config /etc/ataniq-home/config.json --out prepared.json
   ```

   Carry the exact JSON bytes over an authenticated channel. As local root on
   the authority host, use the `prepared_sha256` read on the home device for
   `HOME_DIGEST` and run

   ```sh
   ataniq-witness enroll-submit --config /etc/authority-host/witness/config.json --prepared prepared.json --receipt-out receipt.bin --expect-digest HOME_DIGEST
   ```

   Archive the printed digests, witness status line and raw signed receipt
   off-host. Return the exact receipt to the home. As the home account, run

   ```sh
   ataniq-home enroll finalize --config /etc/ataniq-home/config.json --prepared prepared.json --receipt receipt.bin
   ataniq-home status --config /etc/ataniq-home/config.json
   ```

   Finalization verifies the request, witness receipt and original ledger
   identity, publishes the 0600 binding, and prints `ledger_id`,
   `enrollment_id` and `receipt_digest`. Compare this digest with the owner's.
   Check that witness status reports one slot, the receipt matches the request,
   signing key and epoch, and local replay agrees with a fresh signed read.

   A second prepare refuses an occupied root. After interruption or a failed
   export, recover the same signed request to a new file with
   `ataniq-home enroll resume --config /etc/ataniq-home/config.json --out resumed.json`.
   Compare the digest before replaying an uncertain submission. Repeating
   finalize at genesis recovers the same binding. Prepared exports and binding
   files use exclusive, atomic publication with Linux `O_TMPFILE` and
   `/proc/self/fd`.

9. **Observe, interrupt and replace the mirror.** Stop the foreground
   `serve` process to free its listener. Under the home account, run

   ```sh
   ataniq-home observe --config FILE --events 3
   ```

   Save the output. Verify that the replayed home head, the witness
   receipt and the challenged read have the same sequence and digest,
   followed by `observation: agree` and `managed_starts=0 launches=0`.

   After the observer exits, fence its writer. As local root on the authority
   host, record witness status, archive the challenged slot head, and release
   the claimed session. Substitute the witness configuration for CONFIG and
   describe the fencing evidence in WORDS.

   ```sh
   ataniq-witness status --config CONFIG
   ataniq-witness slot-read --config CONFIG --archive-out ARCHIVE
   ataniq-witness slot-release --config CONFIG --asked "WORDS"
   ```

   Save the result lines off-host, transfer the archive to the home, and run

   ```sh
   ataniq-home observe --config FILE --events 0 --archive ARCHIVE
   ataniq-home status --config FILE
   ```

   Save all three records, the verdict, the counted line and the exit status.
   A three-way comparison requires an archive from the same assignment head.
   A stale or conflicting archive gives `observation: DISAGREE` and exit 1.

   **Partition during observation.** Interrupt witness polling on the
   disposable pair. An unavailable witness gives a refusal and an owner
   action. Restore polling. A stopped append closes the store and exits
   with the original failure and its owner action. A committed advance
   can recover its receipt once the old writer is fenced and the owner
   releases its session. An occupied session or a local suffix without a
   verified witness commit needs owner reconciliation. Preserve the pending
   intent and both journals. Compare the recovered head with the owner
   record before appending another sample.

   **Restore drill.** Take snapshots of the disposable pair at two witnessed
   heads and keep the binding and latest owner archive outside both backups.
   With the writer fenced, restore one side at a time at its enrolled path.
   Preserve the pinned root and ledger inodes when testing rollback. Run
   `observe --config FILE --events 0 --archive ARCHIVE` and retain the refusal.
   An older home prefix gives `home behind`, an older witness head gives
   `home ahead`, and different digests at the same sequence give `fork`.
   A plain copy can give `store identity mismatch` before head comparison.
   Restoring both sides to the same earlier advance can leave their heads
   equal. The newer independent archive then detects the rollback. Stop on
   unexplained evidence and retain the original files.

   **Retire and enroll at epoch 2.** Stop and fence the disposable writer.
   Run `ataniq-home retire --config FILE` and check `retired=true` in local
   status. As local root on the authority host, run
   `ataniq-witness slot-retire --config CONFIG --asked "WORDS"`. Describe
   the disposition of the fenced writer in WORDS. Retain the printed
   retirement receipt digest and exact disposition off-host.

   Preserve the old state and installation records. Prepare an empty home
   root, new seed and TLS paths, and a fresh binding directory. Configure
   the replacement witness in its own fresh directory at **epoch 2**
   (previous epoch plus one). Repeat `identity-init`, pin exchange, the
   outside activation record and prepare/submit/finalize for the replacement.
   Check the new genesis and enrollment receipt, observe the fresh mirror
   and retain its counted line as separate evidence.
   [Witness retirement and replacement](authority-host-witness.md#retire-and-enroll-a-replacement)

`status` works offline, including before identity creation. It reports the
presence of the signing key and TLS files, prepared record, binding validation
and ledger head sequence from local replay. A fresh header has sequence 0.
Exit 0 indicates success, 1 indicates refusal, and 2 indicates incorrect usage.

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
`observation: DISAGREE` and exit 1. Observe prints the managed-start and launch counts only after
printing a comparison verdict. This producer has no execution or process-management capability.

Supply a protected regular archive file with exactly one ASCII line,
`sequence=N head=HEX`, terminated by LF. Use the slot's unsigned decimal
assignment sequence (no leading zeroes) and its 64 lowercase digest digits.
Keep the original signed evidence off-host. The witness CLI in this checkout
prints directory journal position and digest in its status summary. Obtain
the slot head with `ataniq-witness slot-read --config CONFIG --archive-out ARCHIVE`. Copying the
directory position or an enrollment receipt digest will give a disagreement.

## Retire the local home

Stop the active foreground commands. Under the home account, run
`ataniq-home retire --config FILE`. It writes and syncs the empty 0600
`retired` marker in the protected directory containing `binding`. Dedicate
that directory to this installation and keep it outside the restore domain.
The same operation creates `retired` in the old state root and
`HOME_KEY.retired` beside the old signing seed. These two empty 0600 files guard the old root and key path if
the binding configuration changes. Repeat an interrupted retirement to sync
all three markers. Retirement blocks serve (including its preflight),
observe and all three enrollment steps for this installation. Keep the ledger,
receipt, binding, keys and marker together as evidence of the old pairing.

`ataniq-home status --config FILE` still replays the ledger offline. It prints
`retired=true` or `retired=false` and `last_observation=none` until there is
a recorded result. A completed comparison shows `last_observation=agree` or
`last_observation=DISAGREE` with the differences. The suffix
`(stale: ledger head moved)` means the recorded home head differs from local
replay. If observe refuses a home behind, home ahead, fork or retirement, it
records the reason in `observation.json`. Status then shows
`last_observation=refused:` followed by that reason.
