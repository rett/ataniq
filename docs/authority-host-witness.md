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
