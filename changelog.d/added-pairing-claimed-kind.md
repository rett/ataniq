- Pairing a phone records the kind of its passkey as a claim. The seat relays
  the registration's authenticator data and relying party id with `seat.pair`;
  the daemon registers the credential from them, refusing one whose data does
  not name the same credential and key or lacks user verification, and keeps
  the kind its AAGUID claims (`platform-passkey`, `vault-passkey` or
  `passkey`), the AAGUID, the backup flags and the sign counter. The pending
  `owner.pair-status` and the key list show the kind, the AAGUID and the
  backup flags as optional members, and not the sign counter, which is kept
  internally: `keys.log` keeps all of them, with a counter record that only
  raises the highest sign counter. A key paired without them stays of the kind
  `unverified`, which can neither log in nor decide.
