// Package plan holds the project plan, ataniq.project-plan/1, and the
// deterministic compiler that turns a finished project brief into one, with
// a template of the brief's kind. It is a pure library: no input or output,
// no clock, no wire and no pages.
//
// # What a plan is
//
// A plan is a proposal. It binds nothing and grants nothing: no field of it
// is, or leads to, a decision, a contract, a project's creation or an
// approval. Three things bind, each approved on its own, and none is here:
// the creation of the project, the charter, and the delegation contracts.
// What always waits for the owner (publishing, a release, a push,
// credentials, raising a cap) is not a field of the plan: the page that
// shows a plan says it in fixed words, whatever the plan holds.
//
// A [Plan] names its revision (1 for a first compile) and the digest of the
// revision before (empty for 1), the digests of the brief and the catalog
// it was compiled from, its template (id and version) and the digest of the
// template's data, a title (a label of the catalog, never the owner's
// text), up to 4 milestones of up to 6 tasks each (12 in all), the gates
// required at every milestone's end, the first demo's definition of done
// (one sentence of the template's), the review points exactly as the owner
// chose them, where the files live, every external service the work would
// use (by name, from a closed list), the monthly budget band (none, low or
// medium: an id, never a number, and advisory), an estimate band derived
// from the tasks' sizes and always marked "an estimate, not a promise",
// warnings from a closed list, and the owner's notes. A task has an id, a
// title, a kind (build, write, review, test), a size (small, medium, large:
// it chooses no model), the tasks it waits for, its scope (area names such
// as repo:src or store:manuscript) and its checks (ids such as unit-tests
// or rights-manifest). A milestone and a task keep their place in the
// template as their id (m1, t4), so that an id means the same thing in
// every revision of a template version; a plan's ids may skip numbers.
//
// A plan holds no time: when it was made is the store's to keep. The same
// brief gives the same bytes and digest whenever it is compiled.
//
// # The owner's words
//
// The owner's own text (the text of an "other" answer, a note of the brief)
// is kept in the plan's notes, each {field, text, untrusted: true}, at most
// 200 bytes and 8 notes, for the owner's eyes only. It is never served to an
// implementation agent: [Plan.ForAgent] is the projection an agent may be
// given, and it holds no note; for a plan Compile made, every string in it
// is a constant of a template, of the catalog or of this package. A decoded
// plan is held to grammar only, so a plan from a store must be re-checked
// against its template before it is handed to an agent. The text chooses
// nothing in the plan: a template's condition may hold for an "other"
// answer (the closed predicate other), never for what it says, and two
// briefs that differ only in such texts give plans that differ only in
// their notes and the brief's digest.
//
// # The canonical form
//
// [Encode] writes the canonical form under the rules of the brief: keys
// sorted by their UTF-8 bytes, no whitespace, integers in plain decimal, no
// float, strings with only the escapes \" and \\, at most [MaxPlanBytes].
// [Digest] is the SHA-256 of it in lower-case hexadecimal. [Decode] is
// strict: an unknown or a repeated key, a null, a float, a control
// character and anything over a cap are refused, and the plan is held to
// [Check]: its grammar, its caps and its own consistency (ids unique and in
// order, every dependency a task before it, so no cycle), never the
// templates embedded now, so that a plan of a template version no longer
// embedded still reads. A changed template never changes a plan.
//
// # Templates
//
// A template is data, ataniq.genesis-plan-template/1, embedded from
// templates/*.json in the canonical form, versioned and reviewed like code;
// [LoadTemplate] reads one strictly. A changed template is a new version: a
// test pins the digest of each by id@version, fails when the digest of a
// pinned id@version changes, and fails on an id@version with no pin. To
// change a template, raise its "version" in templates/ID.json (v2 after v1),
// run the plan tests with -update, which reports the pin to add, and add
// that line to templateDigests in template_test.go without removing the old
// version's line, which stays on record because a stored plan names the
// digest it was compiled with; then regenerate the golden plans as below.
// There are three, one per kind: software
// (web app and one-page tool), publishing (newsletter and article) and
// media (podcast and book). Each holds the same security block (no push,
// no outward step, secrets by reference). A task, a gate, a service, a
// warning or a demo may carry a condition over one field of the brief, from
// a closed set: eq, in, contains, set and other. A field a template reads
// that takes the owner's own text needs an explicit other branch (an other
// language selects a task to settle it at the desk). The fields and options
// a template names are held to the catalog when the embedded templates are
// loaded and when a plan is compiled: a field that is not a required field
// of the kind, an option no answer can set, a missing other branch, or a
// kind with no template is a refusal, never a silent default.
//
// # Compile, Next and Diff
//
// [Compile] refuses a brief that is not complete, a brief of another
// catalog and a kind with no template (no_template, the kind other among
// them), each with a [*brief.Error] whose code is declared in the brief
// package. [Next] compiles the revision after a plan, and refuses
// no_change when the plan would be the one before ([ContentEqual]: the
// brief's digest differs for the same answers given again). [Diff] says
// what changed between two revisions, by id and by field name, with short
// texts and a cap, and never quotes the owner.
//
// # Test data
//
// The golden plans and diffs in testdata are written by the tests. After a
// change to a template or to the compiler, regenerate them with
//
//	go test ./internal/genesis/plan/ -run 'Golden|Kinds|Diff' -update
//
// and review the change to the files as part of the change.
//
// The package imports the standard library and the genesis packages only.
package plan
