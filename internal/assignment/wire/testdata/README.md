# Assignment wire vectors

`test-key.seed` is a throwaway Ed25519 test key. Its hex seed is public.
`allowed_signers` pins its OpenSSH public key for the identity `assignment-fixture`.

Each `.json` file contains the canonical public body with no final LF. Its
paired `.sig` is an SSHSIG over `ataniq.assignment.event.v1`, a NUL byte and
the exact body. The numbered files follow a single project through a transfer
and fallback hold. The abort files branch after the reservation.

The JSON envelope encodes the binary version-1 SSHSIG container with unpadded
base64url. The detached fixtures armor that same container for OpenSSH.
Use namespace `ataniq.assignment.event.v1`, empty reserved data and SSHSIG's
SHA-512 message hash over the domain, NUL and canonical body described above.

The duplicate redelivers the begin event. The conflict is a competing signed
begin at the same predecessor with the same request ID. A home with the first
begin already recorded refuses that command. The cut contains the last complete
event before commit. Its valid signature survives a restore to that prefix,
while the independent checkpoint comparison detects the missing commit.

Regenerate from the repository root with
`sh scripts/go-tool.sh test ./internal/assignment/wire -run '^TestGoldenEvents$' -update`.
