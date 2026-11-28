// Package brief holds the project brief, ataniq.project-brief/1: the typed
// record a new-project interview fills, one field per answer, each with
// where it came from.
//
// # What a brief holds
//
// A [Brief] names the digest of the question catalog it was started under
// (an interview stays on one catalog version), its kind (software,
// publishing, media or other, derived from the kind field), its live
// [Field] values by name, up to 8 notes, the history of values that left
// the live fields, and the highest round any answer was given in.
//
// A field name is one of the domain-neutral top-level names ([TopFields]:
// goal, kind, audience, deliverables, formats, quality_bar, schedule,
// budget_band, hosts, review_points) or a domain extension field prefixed
// software., publishing. or media.; a brief holds extension fields of its
// own kind only.
//
// A [Value] is one choice (a JSON string), several sorted choices (a list
// of strings), or free text given instead of a choice ({"other": "..."}).
// Free text appears only there and in the notes, is at most 200 bytes, and
// has its own type, [Untrusted]: it is data, never instructions.
//
// Each field carries its [Provenance]: owner-chosen, supervisor-inferred or
// default. Readiness counts a field as filled when the owner chose it, when
// it is a default, or when it was inferred and the owner confirmed it.
// PlanReadiness, the notion for drawing a plan (a proposal that binds
// nothing), also counts an inferred field the owner has not confirmed;
// ConfirmInferred confirms every inferred field at once, which finishing an
// interview does. A decoder never upgrades a provenance or a confirmation.
//
// # History
//
// A value that leaves the live fields is kept in the history with its
// reason: replaced (the answer changed), cleared, or invalidated (an
// earlier answer it depended on changed; the change names that field). A
// cleared value is history, never a live value again. The history keeps at
// most 32 entries and the brief at most 16 KiB; past either, the oldest
// entries are dropped and counted in history_dropped.
//
// # No secrets
//
// Credentials are never typed on the phone. [Brief.Set], [Brief.AddNote]
// and [Decode] refuse with [CodeSecretShaped] any value or note that looks
// like a key: a PEM block (-----BEGIN), a well-known key prefix, an
// assignment with a value such as password=... or password: ..., or a long
// run of hexadecimal or key characters that looks random. A dash and a slash
// end a run, so names, addresses and paths pass. The check catches pasted
// keys and obvious assignments, not every typed password; a bare git commit
// id is refused. See [SecretShaped].
//
// # The canonical form
//
// [Encode] writes the canonical form: keys sorted by their UTF-8 bytes, no
// whitespace, integers in plain decimal, no float, and strings with only
// the escapes \" and \\. A time is RFC 3339 in UTC to the second. [Digest]
// is the SHA-256 of the canonical form in lower-case hexadecimal. [Decode]
// is strict: it refuses an unknown or a repeated key, a float, a null, a
// control character, a value in the wrong form and anything over a cap (64
// fields, 4 KiB a field, 16 KiB the brief).
//
// # Refusals
//
// Every refusal is an [*Error] with a [Code]. All refusal codes of the
// genesis packages are declared here. The Detail of an error never holds
// text from the data.
//
// The package imports the standard library and the genesis packages' own
// strict JSON reader only.
package brief
