# Record vectors

These files are test vectors for the three records of an approval: the
request, the pending record and the decision. They are a proposal for the
joint contract: an implementation in another language can check itself
against them. All names in them are neutral samples.

The rules they exercise, in short: a record is a UTF-8 JSON object with no
float, no null, no control character in a string (U+0000 to U+001F, U+007F,
U+0080 to U+009F), no unknown field, no repeated key, and keys matched byte
for byte. The section "The rules in full", at the end, states each of them
and the content rules of the three forms.
A path, the plan text and the note are a JSON string when their bytes are
valid UTF-8 without a control character, and `{"b64": "..."}` (standard
base64 with padding) otherwise; the other form is refused. The canonical
form has the keys of every object sorted by their UTF-8 bytes, no
whitespace, integers in plain decimal, and strings with only `\"` and `\\`
escaped, so `<`, `>`, `&`, U+2028 and non-ASCII characters are written as
they are. A digest is SHA-256 in lower-case hexadecimal.
The note a lead sends with a request travels beside it, not in it: the
request carries only `note_sha256`, the digest of the note's raw bytes (after
base64 decoding when the note is written as `b64`, not of its JSON form), and
the pending record carries the note itself. So the `note` of a pending record
must digest to the `note_sha256` of the request it answers; in these vectors
the `note` of `pending-valid.json` digests to the `note_sha256` of
`request-valid.json`, and a test holds the pair.

No file ends with a newline unless it says so below.

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
| `decision-valid-passkey.json` | The approve of `decision-valid.json` confirmed on the owner's phone: `confirmed` is `passkey`, and a seat block stands where the console block was. Its `assertion_sha256` is the digest of the sample assertion below, and its `credential` the display id of that assertion's credential id |

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

## The rules in full

This section states every rule an invalid vector rests on, so that an
implementation can find the code of each vector from this file alone. The
`why` texts of `invalid.json` only say which rule a vector breaks.

### The order of the checks

A record is checked in five steps, and the first step that fails gives the
code. Below, the *kind's code* is `request-invalid` for a request,
`record-invalid` for a pending record and `decision-invalid` for a decision.

1. **Size.** A record of more bytes than its cap is `record-too-large`,
   before anything of it is read: 8192 bytes for a request, 65536 for a
   pending record, 8192 for a decision. Bytes over the cap that are not JSON
   at all are too large, not invalid.
2. **JSON.** A record that breaks a rule of "Reading the JSON" has the kind's
   code.
3. **Form.** A record whose fields are not those of its form ("The forms"),
   with the type each has there, has the kind's code. So has a text in the
   wrong one of its two forms.
4. **Content.** A record that breaks a content rule of its form has the
   kind's code, with three exceptions, which have a code of their own:
   - a `verb` outside the closed list is `verb-not-requestable`, in a request
     and in a pending record;
   - a feature finish whose `argv` is not exactly one feature name is
     `feature-name`, in a request and in a pending record;
   - a pending record whose note is more than 2048 bytes is
     `record-too-large`. The cap is on the note's raw bytes: for a note
     written as `b64`, on the bytes after base64 decoding and not on the
     base64 text.
5. **Digests**, for a pending record only, and only when steps 1 to 4 found
   nothing: `binding.plan_sha256` must be the digest of the raw bytes of
   `plan_text`, and `binding_sha256` the digest of the canonical form of
   `binding`. Either one wrong is `binding-mismatch`. So a record with a
   broken form is never reported as a mismatch, whatever its digests are.

Each vector has one thing wrong, so the vectors do not fix which code a
record with several faults has, beyond the order of the five steps.

### Reading the JSON

- The bytes are valid UTF-8, with no byte order mark. The record is one JSON
  object. Before it, after it and between its tokens only space, tab, line
  feed and carriage return may stand; anything else after the object is
  refused.
- No `null` anywhere. Every number is an integer in plain decimal, from
  -9223372036854775808 to 9223372036854775807: an optional minus sign, then
  `0` or digits that do not start with `0`. A fraction or an exponent is
  refused even when the value is whole (`7.0`, `7e0`), and so are `-0`, a
  plus sign and a leading zero.
- A string holds no control character: U+0000 to U+001F, U+007F, and U+0080
  to U+009F. It is refused whether the character is written raw or as an
  escape, in a key as in a value. The escapes are those of JSON. A `\u`
  escape of a surrogate must be a high one followed at once by a `\u` escape
  of a low one; any other surrogate escape is refused.
- Keys are compared after their escapes are resolved, byte for byte. A key
  that appears twice in one object is refused, whatever its values are and
  however either one is spelled (so `epoch` and the same key with its first
  letter as a `\u` escape are one key, twice). A key the form does not name
  is refused, and so is one that differs from a name of the form by case
  only.

### Values

- **Present and optional.** Every field of a form must be present, a list
  that is empty included (`"argv":[]`). The optional fields are the eight
  marked so below: `feature_sha`, `merge`, `summary`, `holder`,
  `merged_into`, `reason`, `console` and `seat`. An optional field that has
  no value is left out. It is never written as an empty string:
  `"feature_sha":""` and `"reason":""` are refused. Nor is an optional
  object written with every field empty (empty strings, `0`, `false`): a
  seat block whose four fields are empty strings is refused. This is what
  makes the canonical bytes of a value the only ones.
- **Text.** A path, the plan text and the note are a JSON string when their
  bytes are valid UTF-8 with no control character, and otherwise the object
  `{"b64": "..."}` with that one key, in standard base64 with padding and
  no loose bits. A `b64` whose bytes could have been written as a string is
  refused; so is a `b64` of no bytes, since no bytes are an empty string.
- **Filled string.** A string that is not empty.
- **Count.** An integer that is 0 or more.
- **Digest.** 64 lower-case hexadecimal digits: the SHA-256 of some bytes.
- **Object id.** 40 or 64 lower-case hexadecimal digits. An id of all zeros
  is an object id.
- **Time.** `YYYY-MM-DDThh:mm:ssZ`, 20 bytes: a date and a time of day that
  exist, in UTC, to the second, with the capital letters `T` and `Z`. No
  fraction of a second, no offset (`+00:00` is refused), no lower-case
  letters.
- **Name.** 1 to 48 bytes of lower-case ASCII letters, digits and dashes:
  `[a-z0-9-]{1,48}`. A feature name is a name. A request id is `rq-` and
  then a name; a pending id is `ap-` and then a name.
- **Flag kind.** One of `exec-config`, `symlink`, `submodule`, `aliased`,
  `history-only`, `odd-path`.

Every list of words below is closed, and the words are matched byte for
byte: `Feature Finish` is not a verb.

### The forms

A field marked *optional* may be absent; every other field must be there.

**Request**, schema `qargi.approval-request/1`:

| Field | Rule |
| --- | --- |
| `schema` | The schema name |
| `request_id` | A request id |
| `epoch` | A count |
| `verb` | `feature finish` or `branch prune` |
| `argv` | A list of strings. For a feature finish, exactly one feature name; for a prune, empty |
| `feature_sha` | *Optional.* For a feature finish it must be there, an object id. For a prune it must be absent |
| `note_sha256` | A digest: that of the raw bytes of the note sent beside the request. A request with no note carries the digest of zero bytes, `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`, as `request-valid-prune.json` does |
| `at` | A time |

**Pending record**, schema `ataniq.approval-pending/1`:

| Field | Rule |
| --- | --- |
| `schema` | The schema name |
| `id` | A pending id |
| `planner` | A filled string |
| `request_id` | A request id |
| `request_sha256` | A digest: that of the request's bytes as they arrived |
| `binding` | The binding, below |
| `binding_sha256` | A digest: that of the canonical form of `binding` (step 5) |
| `plan_text` | A text. It may be empty |
| `note` | A text of at most 2048 raw bytes. It may be empty |

The binding:

| Field | Rule |
| --- | --- |
| `project` | A filled string |
| `subject` | The subject, below, with its `holder` |
| `verb`, `argv` | As in a request |
| `class` | `ref-move` |
| `plan_token` | A filled string |
| `plan_sha256` | A digest: that of the raw bytes of `plan_text` (step 5) |
| `effects` | A list of 1 to 200 effects, below |
| `merge` | *Optional.* For a feature finish it must be there; for a prune it must be absent. `commit`: an object id. `parents`: a list of exactly two object ids. `built_by`: `host` |
| `summary` | *Optional.* For a feature finish it must be there; for a prune it must be absent. The summary, below |
| `evidence` | A list of `{kind, record_id, record_sha256}`: `kind` is `suite`, `record_id` a filled string, `record_sha256` a digest. A feature finish has at least one; a prune may have none |
| `requested_at`, `pending_expires_at` | Times |

The subject, in a binding and in a decision:

| Field | Rule |
| --- | --- |
| `kind` | `seat` |
| `role_name` | A filled string |
| `epoch`, `seat_rev` | Counts |
| `holder` | *Optional.* In a binding it must be there: `kind`, `display_id` and `runtime`, each a filled string. In a decision it must be absent |

An effect:

| Field | Rule |
| --- | --- |
| `kind` | `ref` |
| `ref` | A branch: see the rules after the summary |
| `old`, `new` | Object ids of the same length. A `new` of all zeros deletes the ref |
| `merged_into` | *Optional.* An effect that deletes must have it: `ref`, a filled string, and `tip`, an object id. An effect that does not delete must not have it |

The summary:

| Field | Rule |
| --- | --- |
| `review_digest` | A digest |
| `base`, `tip` | Object ids |
| `files`, `added`, `removed`, `new_bytes` | Counts |
| `paths` | A list of `{path, status, old_mode, new_mode, added, removed, new_bytes}`: `path` is a text; `status` is `A`, `M`, `D` or `T`; `old_mode` and `new_mode` are six octal digits each (`000000` where there is no entry); `added`, `removed` and `new_bytes` are counts |
| `paths_truncated` | `true` or `false` |
| `flagged` | A list of `{kind, path, pattern}`: `kind` is a flag kind, `path` a text, `pattern` a string that may be empty. No two entries have the same `kind` and `path` |
| `binary`, `symlinks`, `mode_changes`, `submodules` | Lists of texts |
| `exec_config_set` | `matcher`: a filled string. `sha256`: a digest. `additions`: a count |

The parts of a binding must agree with each other. These are content rules
(step 4), with the code `record-invalid`:

- Every effect is on a branch: its `ref` starts with `refs/heads/` and has
  at least one more byte.
- A feature finish has exactly one effect. That effect does not delete: its
  `new` is not all zeros. Its `old` is the first of `merge.parents` and its
  `new` is `merge.commit`. `summary.base` is the first of `merge.parents`
  and `summary.tip` is `merge.commit`. So the effect, the merge and the
  summary name the same two commits: the target's tip and the merge the
  host built.
- A prune has only effects that delete: every `new` is all zeros, and so
  every effect has `merged_into`.

**Decision**, schema `ataniq.owner-decision/1`:

| Field | Rule |
| --- | --- |
| `schema` | The schema name |
| `id` | A pending id |
| `planner` | A filled string |
| `decision` | `approve` or `deny` |
| `binding_sha256` | A digest |
| `project` | A filled string |
| `subject` | The subject, above, without `holder` |
| `plan_token` | A filled string |
| `signed_off` | A list of `{kind, path}`: `kind` is a flag kind, `path` a text. No two entries are the same |
| `uses` | 1 for an approve, 0 for a deny |
| `decided_at`, `expires_at` | Times |
| `confirmed` | `code` or `passkey`: how the owner confirmed. It says which one of `console` and `seat` the decision has |
| `remark` | A string. It may be empty |
| `reason` | *Optional.* An approve must not have it. A deny may have it: at most 500 bytes. A longer one is `decision-invalid`, not `record-too-large` |
| `console` | *Optional.* For `code` it must be there; for `passkey` it must be absent. `pid`: an integer of 1 or more. `tty`: a string that may be empty. `client` and `surface`: filled strings. `decider`: `true` or `false`. `session`: 16 lower-case hexadecimal digits |
| `seat` | *Optional.* For `passkey` it must be there; for `code` it must be absent. `assertion_sha256`: a digest, that of the assertion below. `challenge_id`: 16 lower-case hexadecimal digits. `credential`: 16 lower-case hexadecimal digits, the display id of the credential. `session`: 16 lower-case hexadecimal digits |

So a decision carries exactly one of the two blocks: a decision with both,
with neither, or with the block its `confirmed` does not name is
`decision-invalid`, and so is a `confirmed` that is neither word.

The `assertion_sha256` of a seat block is the digest of the four fields of
the WebAuthn assertion that confirmed the decision, each with its length in
front as four bytes, big-endian: `credential_id`, `authenticator_data`,
`client_data_json` and `signature`, in that order. For the sample assertion
whose credential id is the 16 bytes 0x00 to 0x0f, whose authenticator data
is 37 bytes of 0xaa, whose client data is `{"type":"webauthn.get"}` and
whose signature is the 8 bytes `3006020101020101` (hex), it is
`7d6c723c90db49417f74b3e73ebc5c8abd774f0e0d942f82ebd4fb6784b60ac0`. The
display id of a credential is the first 8 bytes of the SHA-256 of its id,
in lower-case hexadecimal: `be45cb2605bf36be` for that credential id.

No record has a field for a verdict or for a held mark; a record that brings
one has a field its form does not name.
