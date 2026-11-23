- Pairing a phone refuses a credential id of 1024 bytes, which WebAuthn does
  not allow and the assertion verifier refuses: the owner wire and the list of
  deciding keys now take at most 1023 bytes, as the verifier does, so no
  credential is paired that could never approve.
