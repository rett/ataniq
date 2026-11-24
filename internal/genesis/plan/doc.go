// Package plan holds the project plan, ataniq.project-plan/1, and the
// deterministic compiler that turns a finished project brief into one, with
// a template of the brief's kind. It is a pure library: no input or output,
// no wire and no pages.
//
// # What a plan is
//
// A plan is a proposal. It binds nothing and grants nothing: no field of it
// is, or leads to, a decision, a contract, a project's creation or an
// approval. Three things bind, each approved on its own, and none is here:
// the creation of the project, the charter, and the delegation contracts.
//
// A [Plan] names its revision (1 for a first compile), the digests of the
// brief and the catalog it was compiled from, its template (id, version and
// digest), a title (a label of the catalog, never the owner's text), up to 8
// milestones of up to 6 tasks each (40 in all), the gates required at every
// milestone's end, the first demo's definition of done (one sentence of the
// template's), the review points, where the files live, every external
// service the work would use (by name, from a closed list), the monthly
// spending ceiling (none, low or medium: an id, never a number), an estimate
// band, always marked "an estimate, not a promise", the never-list
// (publish, release, push, credentials, raise-a-cap, spend-past-ceiling)
// and the owner's notes. A task has an id, a title, a kind (build, write,
// review, test), a tier (fast, balanced, deep: a size class, not a model
// name), the tasks it waits for, its scope (area names such as repo:src or
// store:manuscript) and its checks (ids such as unit-tests or
// rights-manifest). A milestone and a task keep their place in the
// template as their id (m1, t4), so that an id means the same thing in
// every revision of a template version.
//
// # The injection rule
//
// The owner's own text (the text of an "other" answer, a note of the brief)
// is quoted in the plan's notes, each {field, text, untrusted: true}, at
// most 200 bytes and 8 notes, and is never used to choose anything else in
// the plan. A template's condition may hold for "other" (the owner gave
// text of their own), but never reads the text. Whoever shows a note
// escapes it; whoever hands it to an agent quotes it as data.
//
// # The canonical form
//
// [Encode] writes the canonical form under the rules of the brief: keys
// sorted by their UTF-8 bytes, no whitespace, integers in plain decimal, no
// float, strings with only the escapes \" and \\. [Digest] is the SHA-256 of
// it in lower-case hexadecimal; the revision number is part of it. The time
// of the compile ([Plan.CompiledAt], UTC to the second) is not: it is kept
// in the envelope ([EncodeEnvelope]: compiled_at, the digest and the plan),
// so two compiles of one brief at different times have one digest. [Decode]
// is strict: an unknown or a repeated key, a null, a float, a control
// character and anything over a cap are refused, and the plan is held to
// [Check] (ids unique and in order, every dependency a task of the plan, no
// cycle, every closed list closed).
//
// # Templates
//
// A template is data, ataniq.genesis-plan-template/1, embedded from
// templates/*.json in the canonical form, versioned and reviewed like code;
// [LoadTemplate] reads one strictly. A changed template is a new version: a
// test pins the digest of each. There are three, one per kind: software
// (web app and one-page tool), publishing (newsletter and article) and
// media (podcast and book). Each holds the same security block (no push,
// no outward step, secrets by reference). A task, a gate, a service or a
// demo may carry a condition over one field of the brief, from a closed set:
// eq, in, contains and set. A template's fields and options are held to the
// catalog when a plan is compiled: a field that is not a required field of
// the kind, an option no answer can set, or a kind with no template is a
// refusal, never a silent default.
//
// # Compile, Next and Diff
//
// [Compile] refuses a brief that is not complete, a brief of another
// catalog and a kind with no template, each with a [*brief.Error] whose code
// is declared in the brief package. It is deterministic: the same brief
// gives the same bytes. [Next] compiles the revision after a plan, and
// [Diff] says what changed between two revisions, by id and by field name,
// with short texts and a cap, so that a later revision can be shown against
// the one the owner last saw.
//
// # Test data
//
// The golden plans and diffs in testdata are written by the tests. After a
// change to a template or to the compiler, regenerate them with
//
//	go test ./internal/genesis/plan/ -run Golden -update
//
// and review the change to the files as part of the change.
//
// The package imports the standard library and the genesis packages only.
package plan
