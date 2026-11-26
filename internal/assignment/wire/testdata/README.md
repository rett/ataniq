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

The independent historical-review pair extends the hold at revision 13. Its
starting journal checkpoint is
`72446f6adbd7e561e9c8064ba3574ad06b1028b23169503ebba56d5d23e386d2`.
Three hand-specified commands freeze the reverse transfer, inventory it with
SHA-256 of the literal `reverse inventory`, and review the original hold.
The reference used sorted compact ASCII JSON for the private and public
objects, SHA-256 with each specified domain for commitments, and OpenSSH
SSHSIG with the public fixture seed. The review at revision 16 names
`move` and projects the inventory and active ID of `reverse`. Its fixed
signed-envelope SHA-256 is
`495b74e904214d94c61c3619d577ec469440fcbada8123659c5acfe4d2da5947`.

Regeneration checks the independent body, detached signature and signed cursor
before writing any generated fixture. The historical-review pair is read-only
in both modes. Regenerate the other fixtures from the repository root with
`sh scripts/go-tool.sh test ./internal/assignment/wire -run '^TestGoldenEvents$' -update`.
