Two Qargi releases are pinned, each in its own folder. A pin holds the
`KIND/vN` fixture directories of `tests/fixtures/json/` of that release, with
the upstream paths and bytes preserved.

| Pin | Folder | Covers |
| --- | --- | --- |
| Qargi 0.3.9 (J1) | `qargi-0.3.9/` | `v1` of status, check, handoff and refusal |
| Qargi 0.3.10 (J2) | `qargi-0.3.10/` | `v1` and `v2` of status and check, `v1` of handoff and refusal |

Each folder holds `cases.zip` (the case lists and all inputs of the pin,
including malformed JSON and refusal lines with their wire prefix, but not the
registries) and `MANIFEST.sha256`. The decoder embeds the registries from
[registries.zip](../schema/registries.zip), which holds every pinned integer:
the six `KIND/vN/registry.json` files of the 0.3.10 tag. The `v1` registries
are the 0.3.9 ones byte for byte, which the 0.3.9 manifest still proves. The
0.3.10 folder also keeps `kinds-MANIFEST.json`, the upstream
`lib/kinds/MANIFEST.json` at the tag, whose SHA-256 is
`5cfeb8f32c80bc23d66ea7df2c9d64294399d6b09144458aa3e4cdbc100ed3fb`, the digest
the Qargi lead reported for the release. Its entries for the four kinds name
the embedded registries, and a test checks both.

## What changed in 0.3.10 (J2)

- `marshal status` and `marshal check` are schema 2. The top-level `integrity`
  (`off`, `advisory` or `strict`) is required, and a document without it, with
  `null`, or with another word is refused.
- In `marshal check`, a warning is an object `{text, severity}` with severity
  exactly `info` or `damaging`; a bare string, another severity or a missing
  member is refused. `damaging` means the record's evidence cannot be trusted
  as stated, as when its shared ref vanished from the remote.
- Handoff and refusal are unchanged (schema 1). Upstream changed only one
  broken file of the handoff `v1` set, `broken/enum-unknown.json`, which now
  puts the unknown word in a record status rather than a gate tier.

## Both versions decode

The decoder reads the document's `schema` and checks it against the registry of
that integer, so status and check of Qargi 0.3.9 (schema 1) and of 0.3.10
(schema 2) both decode, each against its own registry, and a lead may run
either version during the upgrade. The two registries allow it, because schema
2 only adds one required field and changes the type of the warning items. Each
version keeps its own fixture set and its own manifest. A schema 1 document
with a field of schema 2 does not decode as schema 2, and the reverse; the
`schema-unknown` fixtures of `v1` (schema 2 with a schema 1 body) are refused
for their missing `integrity`, and any other integer is refused outright.

- `Document.Schema()` tells which one was read.
- `Document.Warnings()` returns typed `Warning{Feature, Text, Severity}` values.
  A schema 2 severity is `SeverityInfo` or `SeverityDamaging`. A schema 1
  warning carries text only and reads as `SeverityUnclassified`, never as
  `SeverityInfo`. `Document.Damaging()` reports a damaging warning. Callers
  read the severity, never the text.
- Any warning still makes the document, or for a row its feature, incomplete,
  so a damaging warning never gives usable evidence.

## Tests

`MANIFEST.sha256` of each folder covers the uncompressed contents of that
pin's `cases.zip` and of the registries it covers. The tests check each
manifest's digest, verify every entry and run every case in memory. A separate
test compares the manifest of the installed Qargi with its fixture tree when
`/opt/qargi/VERSION` names a pinned release (0.3.9 or 0.3.10), and for 0.3.10
also its `lib/kinds/MANIFEST.json`; for any other version it skips. Qargi uses
the same MIT license as Ataniq.

## What the pins leave implicit

The decoder checks the registries' conditional field groups when assessing
evidence. The pins leave some source facts implicit.

- Schema 2 status and check state the project's integrity mode, so a row
  must have all of `integrity`, `integrity_note` and `signer` unless the mode
  is `off`, when it must have none. Schema 1 omits the mode: a row missing any
  of the three cannot establish completeness. Handoff supplies the mode, so all
  three are absent when it is off.
- A lead without a recorded worktree has incomplete identity evidence.
  `succeeds` identifies a predecessor only when the lead is a successor.
- An unreadable suite record makes that feature incomplete. For a recorded
  environment check or apply, each conditional field depends on what the source
  record held. Missing fields make that handoff incomplete.
