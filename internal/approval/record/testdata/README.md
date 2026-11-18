# Record vectors

These files are test vectors for the three records of an approval: the
request, the pending record and the decision. They are a proposal for the
joint contract: an implementation in another language can check itself
against them. All names in them are neutral samples.

The rules they exercise: a record is a UTF-8 JSON object with no float, no
null, no control character in a string (U+0000 to U+001F, U+007F, U+0080 to
U+009F), no unknown field, no repeated key, and keys matched byte for byte.
A path, the plan text and the note are a JSON string when their bytes are
valid UTF-8 without a control character, and `{"b64": "..."}` (standard
base64 with padding) otherwise; the other form is refused. The canonical
form has the keys of every object sorted by their UTF-8 bytes, no
whitespace, integers in plain decimal, and strings with only `\"` and `\\`
escaped, so `<`, `>`, `&`, U+2028 and non-ASCII characters are written as
they are. A digest is SHA-256 in lower-case hexadecimal. No file ends with
a newline unless it says so below.

## Valid records

Each of these is the canonical form, byte for byte: decoding it and encoding
the result must give the same bytes.

| File | What it is |
| --- | --- |
| `request-valid.json` | A request for a feature finish |
| `request-valid-prune.json` | A request for a prune: no argument and no `feature_sha` |
| `pending-valid.json` | The pending record for that feature finish. Its plan text has several lines, so it is `b64`. Its paths include one that is not UTF-8 (`data/caf`, the byte E9, then `.bin`), one with an escape byte, and one with `<`, `>`, `&`, U+2028 and non-ASCII letters, written as it is; its note holds the same characters |
| `pending-valid-prune.json` | The pending record for that prune, with the id of `request-valid-prune.json` and the digest of its bytes: one effect that deletes a ref and says where it was merged, no merge, no summary, no evidence |
| `decision-valid.json` | The approve that answers `pending-valid.json`, with its two sign-offs in the order of the flagged list |
| `decision-valid-deny.json` | A deny of the same record, with a reason |

## Beside the valid records

| File | What it is |
| --- | --- |
| `request-valid.pretty.json` | The same record as `request-valid.json`, indented, with the keys in the order of the design, every non-ASCII character as a `\u` escape, and a final newline. It must decode to the same value, and encoding that value must give the bytes of `request-valid.json` |
| `pending-valid.pretty.json` | The same for `pending-valid.json` |
| `decision-valid.pretty.json` | The same for `decision-valid.json` |
| `request-valid.digests.json` | `record_sha256`: the digest of the bytes of `request-valid.json`. `note_sha256`: the digest of the note sent with the request, which is the record's `note_sha256`. Indented, with a final newline, as the other digest files are |
| `pending-valid.binding.json` | The canonical form of the `binding` object of `pending-valid.json`, alone. Its digest is the record's `binding_sha256` |
| `pending-valid.digests.json` | `record_sha256`: the digest of the bytes of `pending-valid.json`. `binding_sha256`: the digest of `pending-valid.binding.json`. `plan_sha256`: the digest of the raw bytes of the plan text (after base64 decoding, not of its JSON form). `request_sha256`: the digest of the bytes of `request-valid.json` |
| `decision-valid.digests.json` | `record_sha256`: the digest of the bytes of `decision-valid.json`. `binding_sha256`: the digest the decision is bound to, the same as the pending record's |

## Invalid records

| File | What it is |
| --- | --- |
| `invalid.json` | For each invalid vector, the refusal code a decoder of its kind must answer (`code`) and what is wrong with the vector (`why`). Indented, with a final newline |
| `request-invalid-*.json` | Requests that must be refused |
| `pending-invalid-*.json` | Pending records that must be refused |
| `decision-invalid-*.json` | Decisions that must be refused |

Each invalid vector is a valid one with one thing wrong, named by the file.
The codes: `request-invalid`, `record-invalid` (a pending record) and
`decision-invalid` for a record that breaks the rules or its form;
`feature-name` for a feature finish that does not name exactly one feature
of 1 to 48 lower-case letters, digits and dashes, and `verb-not-requestable`
for a verb outside the closed list, each in a request and in a pending
record alike; `binding-mismatch` for a pending record
whose `binding_sha256` or `plan_sha256` is not the digest it should be;
`record-too-large` for a record over its cap (request 8 KiB, pending 64 KiB,
decision 8 KiB) or a note over 2 KiB.
