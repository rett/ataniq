# Checkpoint vectors

Each Ed25519 key is a throwaway test key. Its seed is SHA-256 of the ASCII
string `checkpoint fixture ` followed by its role, `home`, `session`, or
`witness`. The public keys are in `derive.py`.

Each line of `transcript.jsonl` contains a name, an exact canonical body
(without LF), and the detached signatures indexed by role. Owner-local
release and retirement requests have no peer signature. The signed read
retrieves the historical advance receipt. Each acknowledgement carries the
previous receipt digest in its result. Hashing the completed acknowledgement
produces the digest for the next CAS.

The transcript was derived independently with sorted compact JSON in Python
and OpenSSH's Ed25519 signing implementation. Run
`python3 internal/checkpoint/testdata/derive.py` to print it. The Go tests
compare each body and signature and check the OpenSSH profile independently.

The SSHSIG binary primitive is generated from the assignment wire source.
The profile test checks it for drift. After reviewing an upstream change, run
`sh scripts/go-tool.sh test ./internal/checkpoint -run '^TestProfileMatchesAssignmentWire$' -update-checkpoint-profile`
to regenerate it.
