# Assignment wire vectors

`test-key.seed` is a throwaway Ed25519 test key. Its hex seed is public.
`allowed_signers` pins its OpenSSH public key for the identity `assignment-fixture`.

The header and signed public object use the format
`ataniq.assignment.schema-1.v2`. This deliberate pre-storage cutover
keeps schema number 1 and starts new header hashes, pins and cursors.
Readers refuse the previous format.

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
`b4be73a7ac3f98b44b5e983d095f3cd8ee73c57255651a83d3a9bbb52df0d68a`.
Three hand-specified commands freeze the reverse transfer, inventory it with
SHA-256 of the literal `reverse inventory`, and review the original hold.
The reference used sorted compact ASCII JSON for the private and public
objects, SHA-256 with each specified domain for commitments, and OpenSSH
SSHSIG with the public fixture seed. The review at revision 16 names
`move` and projects the inventory and active ID of `reverse`. Its fixed
signed-envelope SHA-256 is
`de1e8d0ab38529b39846d8a4c9421e4fb27ea583b5a81b1f63149d6def8a8c26`.

Run `python3 internal/assignment/wire/testdata/check_vector.py` from the
repository root to reproduce the historical pair from the header, 16 fixture
commands and the test key using Python's standard library and OpenSSH. Its
`--write` option replaces only the independent pair for a deliberate cutover.

Go regeneration checks the independent body, detached signature and signed cursor
before writing any generated fixture. The historical-review pair is read-only
in both modes. Regenerate the other fixtures from the repository root with
`sh scripts/go-tool.sh test ./internal/assignment/wire -run '^TestGoldenEvents$' -update`.
