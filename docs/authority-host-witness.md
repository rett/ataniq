# Authority-host checkpoint witness

On Linux, use `ataniq-witness COMMAND --config FILE`. Give a clean absolute
path to one JSON object. Unknown, duplicate and wrong-case field names are
errors. Store independent public pins in this file. Keep private keys in
separate files under protected directories.

| Command | Account | Result |
| --- | --- | --- |
| `init` | Witness service | Creates signed genesis in a pre-provisioned, service-owned 0700 directory. Refuses an existing or interrupted store. |
| `check` | Witness service | Checks local configuration, signing and TLS identity, and published file modes without opening the journal for replay. Prints one line and exits. |
| `run` | Witness service | Opens and re-proves the store, grants the configured home access to one slot, polls outbound and serves the owner socket. |
| `status` | Local root | Verifies a fresh signed status and prints the epoch, directory sequence, predecessor digest, counts and inhibition reason. |
| `acknowledge` | Local root | Sends a pinned, epoch-bound acknowledgement over the owner socket. |
| `recover` | Witness service, under owner control | Re-proves storage and serves only the owner socket with decisions inhibited. |
| `complete-publication` | Witness service, offline | Finishes publication of one unambiguous staged signed genesis pair. |

Exit 0 indicates success, 1 refusal and 2 incorrect usage. A verified status
reporting storage failure, an unclean end or interrupted publication also exits
1. The switch's ordinary decision inhibit allows status to succeed. Local
root needs its own pinned TLS key to administer the witness. The socket's
parent must be a service-owned 0700 directory.

Build the witness for Linux with the release toolchain. On the development
host, compose the base, seat and notifier release trees as described in
the authority-host README, then copy `deploy/authority-host/witness/.` over the
result before filling the baseline. Add `--bin build/authority-host/ataniq-witness`
to the release build and check the resulting release. The switch uses the
witness's read-only preflight at startup and a challenged local status after
restart. The witness needs no additional enabled unit, port or package.

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
IPv6. Both `check` and `run` validate it locally. The owner verifies the live
rules, their order and counters, then records the reviewed ruleset digest
with the release baseline. No DNS allowance is needed.

On the home firewall allow inbound TCP to that listener **only from the
witness's observed source address** and drop other sources for the port.
Set the home link's `TrustSources` to that same host prefix. The home checks
the witness's pinned TLS identity on every connection.
