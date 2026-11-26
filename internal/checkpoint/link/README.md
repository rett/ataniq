# Checkpoint witness link v1

The witness dials the home over TLS 1.3 with ALPN `ataniq-checkpoint/1`. At the home, construct a `NewEndpoint` with the home TLS identity, independently provisioned witness client certificate (DER), witness Ed25519 public key and expected epoch. Run `Endpoint.Serve` on the home's listener. `Endpoint.Connect` takes the exact slot, home private key and process session private key. Its signed grant check can succeed before enrollment.

On the authority host, construct a `NewServer` with the witness TLS identity and signing public key. Run `Server.Poll(ctx, home, witness)` with the home address and pinned home TLS SPKI fingerprint in a `Remote`. Each connection carries a poll followed by one signed home request or an idle indication. The witness returns the result on the same connection. Cancellation stops polling. The home bounds waiting and active calls to eight and hands off at most one mutation at a time per enrollment.

Implementations check the provisioned key in `CheckHome` under the request context (including cancellation and shutdown), recheck the live grant while taking the read snapshot in `ReadHome`, and verify the signed claim or advance and current write grant as part of the transaction in `RawSubmit`. A successful mutation returns its committed receipt. A backend that loses certainty about commit returns `checkpoint.ErrUnknownOutcome`.

For owner calls, use `ServeOwner` on a separate Unix-domain or loopback listener. Supply the administrative capability and the pinned SPKI fingerprint of the owner's client certificate. Dial that endpoint with `Remote.Administration` and the matching TLS identity. The authenticated owner can read, enroll, release and retire. Enrollment also carries the home's proof of possession.

## Framing and bounds

One connection carries one request. The four-byte unsigned big-endian length covers the entire payload. The payload starts with version byte 1 and a one-byte message kind. Each field has a four-byte unsigned big-endian length followed by its exact bytes. Each kind has a fixed field count. Both sides enforce `checkpoint.MaxFrame` on each frame before allocating the payload. The numeric kinds, field order and decoder live together in [frame.go](internal/frame/frame.go).

Mutation requests carry the canonical bytes from `checkpoint.Intent.Body`. Home writes carry the scoped slot, home public key and detached home and session signatures. Home reads and grant checks sign canonical JSON containing the operation, schema, witness key ID, slot, challenge and receipt ID in the home namespace. Both IDs use 64 lowercase hex digits. An all-zero receipt ID requests current state only.

A mutation returns one receipt frame. A read returns three ordered frames carrying current state, the fresh signed receipt and the optional historical receipt. Two empty fields in the last frame mean no receipt matched. Each frame fits the same ceiling even when both receipts contain maximum-length identifiers. The client verifies the fresh challenge, slot, guard, epoch and historical evidence before returning the view. A signed inhibited view accompanies `ErrUnavailable`.

The home accepts the registered witness leaf certificate through TLS client-certificate verification and checks its P-256 SPKI pin. Provision a currently valid witness client certificate. Each listener permits four simultaneous handshakes, up to two per source IP or Linux Unix peer UID. Unix peers share a source limit on platforms without peer credentials. A handshake has five seconds. After authentication the home permits eight active connections. The witness reserves two connections per owner listener independently of its six outbound polls. The first frame length must arrive within two seconds. A valid length opens the remainder of the 30-second exchange deadline. Each authenticated principal has a rolling limit of 60 calls per minute shared across mutations, current reads and grant checks, plus 60 historical reads per minute. Budgets survive reconnects. Closing the listener cancels its workers.

## Lost acknowledgements

The client uses jittered reconnect delays capped at one second on transient connection failures before sending, up to the caller's deadline or 30 seconds. A permanent TLS refusal ends the call, including a certificate alert received before any reply bytes. A rejected write that transferred zero mutation bytes returns `link.ErrNotSent`. A broken exchange after a mutation write begins, an unrecognized reply or a receipt that fails `checkpoint.MatchReceipt` leaves the outcome unknown.

Keep the original intent. Reconnect and issue a fresh challenged read with its mutation ID in `ReceiptID`. Verify the prior receipt against the saved intent and compare the current state and fence before publication. An authorized retry of the exact latest mutation can recover the original acknowledgement. A superseded receipt is historical evidence. Unresolved outcomes stay suspended for operator reconciliation.

For qualification, `linktest.Proxy.Forward` relays one exchange between two authenticated streams. Arm `DropNextReply` before the mutation and close both streams when `Forward` returns. The proxy consumes the complete successful receipt before dropping it, so a subsequent read can establish what committed.
