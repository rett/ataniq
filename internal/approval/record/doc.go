// Package record holds the record forms an approval card is made of, and
// their digests.
//
// Three records carry an approval. A lead writes a request ([Request]): it
// asks for one gated verb. The host answers it with a pending record
// ([Pending]): what the host itself planned, built and reviewed, with a
// digest over the part the owner approves (the binding). The owner's answer
// is a decision ([Decision]), bound to that digest. Each record is one JSON
// object, and each kind has a schema name and a size cap.
//
// # Rules for every record
//
// A record is a UTF-8 JSON object. It holds no float and no null. No string
// in it holds a control character (U+0000 to U+001F, U+007F, U+0080 to
// U+009F). A field the form does not name is refused, and so is a key that
// appears twice in one object, or a key that differs from the form's only by
// case. Every list of allowed words (verbs, kinds, states) is closed. A time
// is RFC 3339 in UTC to the second, ending in Z.
//
// The decoders check all of this themselves and do not use encoding/json,
// whose decoder matches keys without regard to case and keeps the last of a
// repeated key.
//
// # Text
//
// A path, the plan text and the lead's note may hold any byte. They are a
// [Text]: a JSON string when the bytes are valid UTF-8 without a control
// character, and {"b64": "..."} otherwise. Exactly one of the two forms is
// right for given bytes, and the other is refused.
//
// # The canonical form and the digests
//
// The Encode functions write the canonical form: the keys of every object
// sorted by their UTF-8 bytes, no whitespace, integers in plain decimal, and
// strings with the two escapes \" and \\ and no other. So "<", ">", "&",
// U+2028, U+2029 and every non-ASCII character are written as they are,
// which is not what encoding/json does by default. A digest is the SHA-256
// of bytes in lower-case hexadecimal: [BindingDigest] is taken over the
// canonical form of the binding, [PlanDigest] over the bytes of the plan
// text, and [Digest] over any bytes, such as a record as it travelled.
//
// A decoder accepts any JSON spelling of a valid record (whitespace, keys in
// any order, \u escapes), because a record is checked by the digest of its
// own bytes and is never written again on its way. Decoding a canonical
// record and encoding the result gives the same bytes.
//
// # Reading API for other packages
//
// A package that carries payloads under the same rules (the owner
// connection's frames) reads and writes them with [Reader], [Marshal],
// [AppendMarshal] and [TextLen], and checks them with [ValidFlagKind],
// [ValidMode], [IsLowerHex] and [IsControl], instead of keeping a copy that
// could drift from the records' own.
//
// # Refusals
//
// Every refusal is an [*Error] with a [Code]. All refusal codes of the
// approval packages are declared here. The Detail of an error is written by
// this package and never holds text from the record, so it can be shown
// without escaping.
//
// The package imports the standard library only. Everything here is
// provisional until the joint contract with the planner's project is agreed;
// the files under testdata are the vectors proposed for it.
package record
