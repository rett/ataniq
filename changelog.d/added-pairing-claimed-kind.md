- Pairing a phone records the kind of its passkey as a claim. The seat relays
  the registration's authenticator data and relying party id with `seat.pair`;
  the daemon registers the credential from them, refusing one whose data does
  not name the same credential and key or lacks user verification, and, on a
  seat gate with a seat origin, one for another relying party than the
  origin's host (`credential-invalid`, the code left unused), and keeps
  the kind its AAGUID claims (`platform-passkey`, `vault-passkey` or
  `passkey`), the AAGUID, the backup flags and the sign counter. The pending
  `owner.pair-status` and the key list show them as optional members, and
  `keys.log` keeps them, with a counter record that only raises the highest
  sign counter. A key paired without them stays of the kind `unverified`.
