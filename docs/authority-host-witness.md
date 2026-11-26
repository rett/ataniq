# Authority-host checkpoint witness

On Linux, use `ataniq-witness COMMAND --config FILE`. Give a clean absolute
path to one JSON object. Unknown, duplicate and wrong-case field names are
errors. Store independent public pins in this file. Keep private keys in
separate files under protected directories.

| Command | Account | Result |
| --- | --- | --- |
| `init` | Witness service | Creates signed genesis in a pre-provisioned, service-owned 0700 directory. Prints `witness store initialized` on success. Refuses an existing or interrupted store. |
| `check` | Witness service | Checks local configuration, signing and TLS identity, and published file modes without opening the journal for replay. Prints one line and exits. |
| `run` | Witness service | Opens and re-proves the store, grants the configured home access to one slot, polls outbound and serves the owner socket. |
| `status` | Local root | Verifies a fresh signed status and prints the epoch, directory sequence, predecessor digest, counts and inhibition reason. |
| `acknowledge` | Local root | Sends a pinned, epoch-bound acknowledgement over the owner socket. |
| `recover` | Witness service, under owner control | Re-proves storage and serves only the owner socket with decisions inhibited. |
| `complete-publication` | Witness service, offline | Finishes publication of one unambiguous staged signed genesis pair and prints `witness publication completed`. |

Exit 0 indicates success, 1 refusal and 2 incorrect usage. A verified status
reporting storage failure, an unclean end or interrupted publication also exits
1. The switch's ordinary decision inhibit allows status to succeed. Local
root needs its own pinned TLS key to administer the witness. The socket's
parent must be a service-owned 0700 directory. Keep that directory, state,
seed and both TLS directories separate from each other and from the fixed switch
guard directory, `/var/lib/authority-host`. Remove the former `guard` key
from existing configurations. Preflight requires a readable guard directory
even when the switch has inhibited decisions. Before startup,
`check` verifies the socket path fits in 107 bytes, its private parent is
writable and the name is free. It leaves the socket untouched. A bind can
still fail at startup. Both cases report `owner socket already present or unusable`.

Build the witness for Linux with the release toolchain. On the development
host, compose the base, seat and notifier release trees as described in
the authority-host README, then copy `deploy/authority-host/witness/.` over the
result before filling the baseline. Add `--bin build/authority-host/ataniq-witness`
to the release build and check the resulting release. The switch uses the
witness's read-only preflight at startup and a challenged local status after
restart. The main process performs preflight before opening the store. A refusal
(exit 1), including a bad configuration, unsafe files, a pin mismatch or a
poisoned store, leaves the service failed. Other failures eligible for restart
are limited to five starts in 300 seconds. After repairing the cause, perform
any required storage recovery, then run `systemctl reset-failed ataniq-witness.service`
and `systemctl restart ataniq-witness.service` by hand. The witness needs no additional enabled unit, port or package.

## Firewall and home listener

The owner installs these two rules, in order, in the host's existing nftables
`inet` output filter. Place them **before** any general accept, including
established traffic and loopback. Substitute the dedicated account's numeric
UID, one home IP address and one TCP port. For IPv6 change `ip daddr` to
`ip6 daddr`. Install only the rule for the chosen family. The final drop
covers both families and all protocols for that UID.

```nft
meta skuid WITNESS_UID ip daddr HOME_ADDRESS tcp dport HOME_PORT accept
meta skuid WITNESS_UID drop
```

In the owner's `ataniq-witness.service.d/egress.conf`, add the corresponding
single address (`/32` for IPv4 or `/128` for IPv6).

```ini
[Service]
IPAddressAllow=HOME_ADDRESS
```

Configure `home_endpoint` as that literal address and decimal port, bracketing
IPv6. Use the canonical address spelling and decimal port without leading
zeros. Use a unicast destination outside loopback and link-local space.
IPv4-mapped IPv6 addresses and zones are refused. Only the firewall restricts
the TCP port. The host no-AI check and witness `check` read the owner’s unit
drop-ins across the system manager’s unit search path, including
`ataniq-witness.service.d`, `ataniq-.service.d` and `service.d`. Together
these files may set one `IPAddressAllow` matching the home address. On a
running systemd host both checks query the loaded unit with
`systemctl show -p IPAddressAllow,IPAddressDeny ataniq-witness.service`.
Its deny list must cover all IPv4 and IPv6 addresses, and its allow list must
match the files on disk. After changing a drop-in, run
`systemctl daemon-reload` before checking it. The owner verifies the live
rules, their order and counters, then records the reviewed ruleset digest
with the release baseline. No DNS allowance is needed.

On the home firewall allow inbound TCP to that listener **only from the
witness's observed source address** and drop other sources for the port.
Set the home link's `TrustSources` to that same host prefix. The home checks
the witness's pinned TLS identity on every connection.

## Provision once, by hand

**Installation is the owner's step.** Keep managed admission closed throughout
setup. Prepare the signed release on the development host, including the
binary built with the following command.

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 sh scripts/go-tool.sh build -trimpath -o build/authority-host/ataniq-witness ./cmd/ataniq-witness
```

1. On the authority host create a system account and group named
   `ataniq-witness`, with a locked password, no login shell and no
   supplementary groups. Give neither the seat nor the deploy account group
   membership, write access to unit/configuration files, or access to a
   signing socket. Use a local filesystem supporting flock, keep-size
   fallocate, and durable file and directory fsync.
2. Pre-create `/var/lib/ataniq-witness` and its `store` subdirectory as
   `ataniq-witness:ataniq-witness`, mode 0700. Sync the new directories and
   their parents before provisioning genesis. Keep the signing seed and TLS
   identities outside `store`. Create `/etc/authority-host/witness` under
   root control, traversable by the service. Give the service read access to
   `config.json` (root-owned 0640, group `ataniq-witness`). All ancestor
   directories must be real, protected directories.
3. Create a new Ed25519 witness signing identity using the owner's trusted
   key provisioner. Store the **raw 32-byte seed** in `seed`, owned by the
   service with mode 0600 and one hard link. Record the raw 32-byte public
   key in lowercase hex independently. On the home, provision a different
   Ed25519 ledger signing key under its protected signer account. Transfer
   only its public key to the witness.
4. Create three distinct P-256 TLS identities for the witness, the local
   owner and the home. The verified seat binary's `gate-init --identity DIR`
   produces the supported `cert.pem` and `key.pem` format. Run it under the
   account that will use each key. Direct its fingerprint output to the
   owner's protected pairing record. Use new 0700 folders and 0600 keys. Both TLS directories must deny
   writes by group and others, including when the sticky bit is set.
   The local owner's key and directory belong to root. Use separate keys
   from the seat, gate, notifier and deploy tools.
5. Exchange pins over an independent authenticated channel. At the witness
   record the home's TLS SPKI SHA-256 fingerprint and Ed25519 public key.
   At the home provision the witness TLS certificate (DER for
   `link.NewEndpoint`), verify its SPKI fingerprint, and pin the witness
   Ed25519 public key, exact slot and activation epoch. The witness pins the
   local owner's TLS fingerprint. The local owner pins the witness's TLS
   fingerprint, Ed25519 key and epoch. Verify the values at both devices
   before proceeding. Private keys stay with their principals.
6. On a device outside the witness backup, retain the owner's signed activation
   record with witness identity, signing key, monotonically increasing epoch
   and directory commitment. Record the home key, slot, TLS pins and the
   exact genesis head after initialization. Prepare the host and home
   firewall rules above. Allow the witness to read the switch state
   directory `/var/lib/authority-host` and reserve writes there for the switch.

### Configuration file

Replace the uppercase values locally. The five public-key and fingerprint
values each use 64 lowercase hex digits. The witness directory identity
and the home ledger slot have separate store identifiers. The witness name
is shared. Use nonempty printable ASCII (space through tilde) for witness,
store, incarnation and authority identifiers. Encode the nonzero activation epoch as a decimal string.

```json
{
  "identity": {
    "witness": "WITNESS_ID",
    "store": "DIRECTORY_ID",
    "incarnation": "DIRECTORY_INCARNATION",
    "epoch": "1"
  },
  "slot": {
    "witness": "WITNESS_ID",
    "authority": "LEDGER_AUTHORITY",
    "store": "LEDGER_STORE"
  },
  "state": "/var/lib/ataniq-witness/store",
  "seed": "/var/lib/ataniq-witness/seed",
  "socket": "/run/ataniq-witness/owner.sock",
  "service_tls": "/var/lib/ataniq-witness/tls",
  "owner_tls": "/etc/authority-host/witness/owner-tls",
  "service_tls_pin": "WITNESS_SPKI_SHA256",
  "owner_tls_pin": "OWNER_SPKI_SHA256",
  "home_tls_pin": "HOME_SPKI_SHA256",
  "home_endpoint": "192.0.2.20:7443",
  "signing_public_key": "WITNESS_ED25519_PUBLIC_HEX",
  "home_public_key": "HOME_ED25519_PUBLIC_HEX"
}
```

### First activation

Use a verified copy of the candidate release's executable for these first
two calls, before switching `current`. In the examples, `WITNESS` stands for
its absolute path, and `CONFIG` for `/etc/authority-host/witness/config.json`.
Before running `check` by hand, create `/run/ataniq-witness` owned by the
witness account with mode 0700. Systemd creates it on subsequent unit starts.

```sh
runuser -u ataniq-witness -- WITNESS init --config CONFIG
runuser -u ataniq-witness -- WITNESS check --config CONFIG
```

Link the release-managed unit and create the firewall drop-in as local root.

```sh
ln -s /opt/authority-host/current/etc/systemd/system/ataniq-witness.service /etc/systemd/system/ataniq-witness.service
```

Before activation, add this exact line to the host emergency stop list
`/etc/authority-host/stop.list`, retaining the other service stops. The
witness validator requires the line even on the first switch.

```text
systemctl stop ataniq-witness.service
```

Activate the signed release with the existing switch procedure. Leave the
unit disabled in systemd. The switch's validator checks the unit and its
exact link target under `/opt/authority-host/current`, even on the first
switch while the link dangles. Its reload list starts the unit under the decision inhibit.
The unit's `run` process checks the egress files on disk. The health command
verifies signed local status over the owner socket even if the home is offline.
Neither operation calls `systemctl`. The post-switch host no-AI check queries
systemd for the loaded unit's effective grants before decisions resume. Without the egress drop-in the service can pass this local health
check while the unit denies all IP traffic. The host no-AI check reports
`NOTE witness egress drop-in missing: home unreachable`.
Verify the effective account and restrictions, the output filter and the
absence of a witness IP listener. Run `WITNESS status --config CONFIG` as
local root. Fresh genesis reports sequence 1, zero slots, receipts and fences.

Store initialization and home enrollment are separate steps. For the attended
enrollment use the trusted local owner client of
`link.Remote.Administration` on the configured Unix socket. This command
exposes status and storage acknowledgement as CLI operations. The link's
typed owner API also accepts enrollment, release, retirement and settlement.
Submit `Enroll` with the exact slot, fresh mutation ID, the immutable home
header bytes, the pinned home public key and the home's detached proof over
the canonical enrollment body. Keep the exact request before sending.
Check the returned receipt against the request, witness key and epoch.
Archive it independently. A fresh signed read must agree with home replay
before any managed admission. Provisioning this service alone starts no
managed work. [Owner link protocol](../internal/checkpoint/link/README.md)

## Outage or uncertain delivery

If the witness, home endpoint or private link is down, suspend new managed
admission and publication that require a fresh witnessed head. The seat and
notifier continue their own functions. An existing route may still be active.
An offline writer or a lost reply gives no authority to replace it.

Retain the exact staged intent, mutation ID, digest and expected state. On
reconnect, use a new challenged read for that ID and match any returned
receipt to the saved intent. A historical receipt records a past commit.
Check the current fence and head before publication. If a read cannot
establish the outcome, use the authorized link's `Settle` for that ID and
epoch, or preserve the ambiguity for an owner decision. Never invent a new
request ID to bypass an unresolved mutation.

## Storage error and interrupted publication

1. Close home admission, fence any active routes and preserve an evidence
   copy including hidden files, `journal.v1`, `witness.lock`, and both marker
   files if present. With an unclean stop, normal startup can re-prove the
   store and sign status with reason `unclean process end`. A live write
   error can stop the signer itself. A poisoned store refuses normal startup.
2. Stop `ataniq-witness.service` and check the storage device, filesystem,
   space and durability before attempting repair. Keep the original evidence
   and all external receipts. Leave markers and partial data intact.
   To inspect a provable store, start the following command under the witness
   UID, attended, with the regular unit stopped. If systemd removed its runtime
   directory, recreate `/run/ataniq-witness` owned by the service, mode 0700.
   Confirm any stale Unix socket has no live owner before removing **only**
   that socket. The recovery process holds the store and key locks and serves
   only local owner requests.

   ```sh
   runuser -u ataniq-witness -- WITNESS recover --config CONFIG
   ```

3. From a second local root terminal, run `WITNESS status --config CONFIG`.
   Compare its verified head, reason and counts with the independent record.
   If signed status reports `interrupted publication`, preserve the staged
   files and keep home admission closed. `acknowledge` always refuses staged publication.
   Stop the attended recovery process and keep the regular unit stopped.
   After identifying exactly one staged journal and its staged or
   already-published index, run `WITNESS complete-publication --config CONFIG`
   offline as the witness account. It verifies the pinned signed genesis and
   exact index, publishes the pair, then reopens and verifies the published
   names. Run `check`, start the verified unit and compare fresh signed status
   with the independent record before resuming admission. This completes the
   interrupted-publication procedure. An ambiguous pair, torn frame, corrupt
   receipt, missing partner or legacy format needs preservation and owner
   reconciliation before fresh provisioning. A second `init` cannot repair
   an existing store.

4. For a storage error or unclean process end, repair storage and reconcile
   the evidence. Keep home admission independently suspended. In the owner's
   root session run `ah-switch status`.
   If a transaction is open, including a journal held in the inhibit, end the
   attended witness process before running `ah-switch recover`. Recovery can
   reload services. Follow the switch's recovery procedure until the transaction
   settles, inspect the outcome, then re-establish attended witness recovery
   with the regular unit stopped.

   A healthy switched release normally has its guard cleared by the switch.
   If a plain inhibit remains, first verify the active release and that it is
   safe for **all** decision services to resume. With no open transaction, the
   owner can run `ah-switch inhibit-clear --asked "WORDS"`, recording what was
   checked. Never remove `decisions.inhibit` by hand. Confirm `ah-switch status`
   reports `decisions: not inhibited` before running
   `WITNESS acknowledge --config CONFIG`, then request fresh signed status.
   A lost acknowledgement reply requires a fresh status read before deciding
   to retry. Recovery mode remains service-inhibited after acknowledgement.
   End the recovery process cleanly, start the verified unit and check status
   again before restoring home admission.

## Backup, restore and rollback

Stop the service and back up the complete store, including hidden files.
Back up the signing seed separately, encrypted and in the owner's offline custody.
Keep private TLS keys out of the store backup as well. Retain verification keys and
owner-signed activation records outside that backup. Keep signed receipts
and challenged sequence/digest observations outside the witness after each
accepted pilot transition. A status line is a verified summary. Retain the
actual signed receipts when independently verifiable evidence is needed.

After a known restore, isolate all copies of the signer and store, keep
managed admission closed, and fence the old identity before using any key
to inspect the evidence through attended `recover`. Never reactivate the old
seed beside a live instance. Returning a restored store to service requires
a fresh identity and a higher externally recorded epoch. A directory lock
and key-file lock protect only those particular filesystem objects. A copied
directory and copied key can sign conflicting histories. Replay detects a
journal suffix missing from just one file. Matching truncation of **both**
files at a commit boundary, or a whole-directory restore, can hide later
mutations, releases, retirements and settlement fences. A forgotten fence
may allow the same mutation ID to commit later.

A lower signed sequence or a different digest at the same observed position
exposes a conflict with the outside record. A larger sequence alone does
not establish continuity. If all surviving evidence ends at the same older
prefix, later activity is undetectable. Reconcile the home journal, exact
receipts, tombstones, fences and owner record. Preserve the evidence, fence
the old identity and provision a fresh identity at a higher externally
recorded epoch. A new epoch fences future requests. It cannot reconstruct
the missing interval. [Durable storage contract](../internal/checkpoint/durable/doc.go)

Before rolling back across the change that pinned the guard directory, edit
`/etc/authority-host/witness/config.json` for the target binary. The older
`ataniq-witness` requires `"guard": "/var/lib/authority-host"`. Restore that
key before switching to the older witness. Remove it before switching back
to the binary with the pinned guard.

For a software rollback use the signed switch procedure
`ah-switch --rollback-to previous`. The switch runs the host stop list before reloading the target.
It also runs the departing release’s stop list when that release still verifies. If a stop fails, decisions
stay inhibited. Correct the cause and retry the rollback. A target that carries the witness restarts its own
binary against the existing store. Verify storage-format compatibility,
pins, epoch and signed status before resuming admission. When returning to a
release without the witness, confirm it is stopped. Keep its single-unit
stop command in the host list. A stop reporting exit 5 still requires
the manager to confirm that the canonical unit is inactive or absent.
The switch records the verified service names in the transaction journal
and the current release’s names in `deploy.json`. Older state can use the
installed release unit links. A retry repeats the stops and checks. Preserve witness state and keep dependent homes
suspended until an appropriate release is active again. The no-AI listener
check continues to check the witness account after its unit link is removed.

## Retire and enroll a replacement

Drain the old home, fence every former writer and route, and record the
disposition of outstanding work. On the authenticated local owner link,
`Read` the exact slot with a fresh challenge. If a session needs explicit
release, submit `Release` against the verified state and read again.
Submit `Retire` with a fresh mutation ID, the exact current state and the
digest of the owner's disposition record. Keep the saved intent, verified
retirement receipt and a new challenged view showing the permanent
tombstone. The owner can retire after losing the home's key.

Stop the old instance cleanly and preserve its store and verification keys.
For the replacement prepare a fresh home key, store, incarnation and ledger
ID. Use a fresh witness directory and identity with a higher activation epoch
for the new home pin. Repeat both directions of the pin exchange and the
create-only enrollment above. Check the new genesis and receipt against the
owner's outside record before reopening admission.
