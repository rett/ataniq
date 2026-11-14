# Guarded dispatch journal

Status: implemented internal offline Linux journal with guarded exact-attempt
views implementing the frozen `dispatch.Journal` port, immutable recorded
policy, one-use grants, receipt evidence and causal events.
It supplies no provider, service, client or production authentication path.
Read it alongside the [runtime](runtime.md), [security](security.md) and
[client protocol](client-protocol.md) contracts; none of their external
execution or disclosure guarantees follow from opening a database.

## API and identity

The internal [journal package](../../internal/journal/store_linux.go) exposes:

| Operation | Behavior |
| --- | --- |
| `Create(ctx, path, Options)` | Exclusively creates a fresh database file in an existing private directory. Refuses existing files and orphan SQLite sidecars. |
| `Open(ctx, path, Options)` | Opens existing recognized storage for the configured authority. Never creates a missing main file or migrates state. |
| `Store.Info(ctx)` | Returns bounded owned values describing authority, store/incarnation IDs, schema and engine identity, page count/size and configured page budget. |
| `Store.Close()` | Waits for local operations, closes SQL and then releases the inode lock. Idempotent; repeated calls return the same close result. |
| `Store.Register(ctx, Policy)` | Trusted composition records the full immutable intent and observed permission facts, atomically activating a durable guard before returning an Attempt. Existing IDs cannot be refreshed. |
| `Store.Activate(ctx, Intent)` | Activates an exact existing binding for duplicate/evidence handling; grants no new execution approval. Refuses a competing live view. |
| `Attempt.Prepare(ctx, Intent)` | Looks up existing state first; only a confirmed new atomic preparation and grant consumption returns PrepareNew. |
| `Attempt.Save(ctx, Receipt)` | Retains exact evidence, including contradictions, with its causal event in one transaction. |
| `Attempt.Revoke(ctx)` | Orders monotonic permission revocation with preparation under the active guard. |
| `Attempt.Release(ctx)` | Invalidates this activation and retires its guard atomically with required uncertainty; an exact-generation retry can reconcile a lost committed retirement response. |
| `Store.Inspect(ctx, id)` / `Store.Snapshot(ctx)` | Eager bounded trusted reads of exact bindings, receipts, consumed grants and events. |

Paths must be clean absolute filesystem paths supplied by trusted composition.
The package constructs an escaped `file:` URI; it accepts neither user DSNs
nor SQL. The SQL pool and connection handles are private.

Creation uses one immediate transaction for the exact journal schema,
application ID `0x41544a4c`, schema version 1, configured authority and two
independent cryptographically random 128-bit IDs. Store and incarnation IDs
are stored as BLOBs and exposed as hexadecimal strings. Authority is also a
BLOB, preserving its exact bytes. This local DDL initialization has no
provider conversation bootstrap meaning. Strict recognition covers exactly
`journal_identity`, `attempts`, `grants`, `evidence` and `events`, including
their complete DDL. This schema shipped as version 1 in 0.0.3. Future schema
evolution requires an explicit reviewed path; ordinary opening does not migrate
state. Earlier metadata-only layouts are refused before writer settings.
The parent directory is synced after the initialization commit.

Ordinary reopen preserves both identities. An interrupted initialization is
refused unless its complete transaction committed. Create failure can leave
an incomplete file or a committed database whose success was not returned;
Create never removes or overwrites either. Open accepts only a complete,
recognized identity. Missing, zero, ambiguous, foreign, older or newer state
is refused without automatic adoption, repair or migration.

Info is trusted local storage inspection. It contains no snapshot watermark,
event cursor, principal enrollment or authorization assertion. There are no
unbounded result sets or public SQL accessors.

## Exact capabilities and recorded policy

The [Attempt implementation](../../internal/journal/attempt_linux.go) keeps the
raw Store out of the coordinator's Journal port. A view contains one owned
immutable binding and a random writer activation generation. It supports
concurrent calls. Neither an intent string nor a persisted approval boolean
establishes authority: Register is a capability reserved for trusted internal
composition and owned fixtures. It makes no network authentication claim.

`Policy.Intent` supplies the complete binding. `Policy.ValidUntil` bounds all
recorded permission facts. `RequireGrant=true` requires a nonempty intent
grant and a `Grant` with the exact same ID and entire target binding, including
payload. Grant validity has its own bound as well. Grant-free permission must
be explicit through a policy with RequireGrant false and no grant. Times must
be nonzero and within UTC years 1970–9999, independent of the caller or host
timezone. Stored milliseconds conservatively round expiry down. Policy/source/grant IDs remain opaque strings, not parsed tokens.
No later registration can refresh expiry, replace policy, undo consumption or
revocation, or clear uncertainty for an existing intent ID.

The store-local `Intent.ID` is the key. Authority, project, attempt, command,
runtime, epoch, action, kind, policy, qualification, authorization, limits,
grant and payload are all immutable. Commands are not unique: a command may
have multiple distinct effects. There is no sibling retry policy. Scalar
strings use length-prefixed exact bytes, including admitted NUL and non-UTF8;
payload is binary and nil/empty payloads are equivalent. Returned payloads
are owned copies. No JSON normalization or exported dispatch-private helper
is involved.

Prepare first verifies the exact binding and reads the existing record.
Prepared or uncertain attempts return the current duplicate projection even
when the recorded policy has since expired or been revoked. New preparation
checks expiry and revocation, then atomically inserts any exact-target grant
consumption, marks prepared, and appends its event. A consumed grant ID cannot
be used by another binding. The grant row and original intent never change.
Only this call's confirmed commit returns PrepareNew. A proved policy denial,
binding/grant conflict, duplicate, and unknown commit remain distinct outcomes.
Prepare and Save reject a foreign binding before admission, waiting or
cancellation can latch a failure on the legitimate activation. Prepare returns
PrepareConflict without an error; Save returns ErrConflict. Such rejection
does not append a binding-conflict event. Grant-consumption conflicts discovered
for an exact binding retain their first exact transactional conflict evidence.
An immutable attempt fixes its grant and the consumed grant binding is stable,
so repeated clean conflicts reuse that event.
If that event cannot be confirmed committed, Prepare returns PrepareUnknown
with a nonnil error and fences the activation. A failed-before-commit event is
absent; a committed event whose response was lost retains the original consumed
grant binding. Neither case changes or releases grant consumption. The frozen
Coordinator already handles errors before interpreting PrepareStatus.
An error before lookup for an admitted exact call, including cancellation or
busy timeout, returns unknown; it cannot falsely assert that already-started
work was denied.

PrepareRejected proves only that this invocation committed no preparation.
It is not a permanent denial latch. Expiry checks use the observed current
time; a later observation of an earlier valid instant can pass that check.
This does not remove the independent current-authority check at Boundary.Start.

The actual Boundary independently obtains current authority at the real effect
seam. Recorded policy cannot permit a stale external start. For example, a
policy revoked after Prepare must still be rejected by Boundary.Start. The
journal has no provider implementation and does not serialize an external
authority source itself.

## Guards, failure and retirement

Registration and initial activation commit together before a Journal view can
escape. Every Prepare, Save and Revoke therefore already has a durable dirty
guard when storage or cancellation can fail. A per-view failure latch also
prevents a later call through the same activation from treating absent prepared
state as new work after the fault clears. Safety does not depend on successfully
writing an extra fence after a storage error. Ambiguous revocation is an error
and fences execution; it is never acknowledged as successful or silently used
to restore permission.

Open atomically appends recovery events and marks all previous active
registrations uncertain before exposing a Store. It preserves original
bindings, receipts and grant consumption. An active registration with no
prepared row is still fenced. Failed recovery exposes no Store; a later Open
must complete recovery. Successfully recovered unrelated work is not subject
to a permanent store-wide quarantine. Activate can expose a known binding for
duplicate/evidence handling, but an unprepared existing binding becomes
uncertain and cannot receive a new execution approval through that operation.

Trusted composition calls Release after Dispatch returns, with a fresh bounded
context when the dispatch context has been cancelled. Release invalidates the
activation before clearing its guard; late Journal calls require a new Activate.
A stale view or release cannot clear a newer generation. New exact Prepare,
Save and Revoke calls after retirement publication return ErrInactive without
changing either activation's failure latch. Calls already admitted are
accounted for conservatively. Prepared-only and Started records remain
unresolved on retirement: Started proves a start acknowledgement, not completion.
A definitive Denied record without other uncertainty can retire settled.
Clearing a guard and recording required uncertainty commit in the same
transaction with a release event. A failed release can leave the guard in place;
Release may be retried while the durable activation remains active, or
Close/reopen performs conservative retirement. If the response was lost after
the release committed, a retry through the still-current view reads the exact
stored binding and generation. Only an exact match with an inactive durable
guard allows it to remove that view and return success. It changes no durable
row, uncertainty, receipt or event. An active guard still requires the ordinary
retirement transaction; a mismatch is refused. Once the view is removed,
concurrent or stale retries return ErrInactive and cannot retire a newer view.
Fresh guarded Activate then permits duplicate handling and late contradictory
evidence in the same Store, without Close/Open or fresh execution permission.

Both Release and Close publish retirement before sampling pending operations.
Each exact Prepare, Save and Revoke increments its pending count before checking
retirement, and publishes any admitted failure before decrementing that count.
Retirement reads pending **before** the failure latch. If an admitted call has
not finished, a nonzero count requires durable uncertainty. If it has finished,
its failure publication precedes the zero count and the subsequent latch read.
Reading the latch first would allow a failure and decrement between the two
reads to escape both checks. A caller that increments after retirement's sample
sees retirement and exits without attempting storage. The additional early
retirement check avoids touching the count for an already inactive view.

Close publishes retirement for all its views under its lifetime write lock,
before starting the retirement transaction, including when that transaction
later fails. A caller that passed admission but has not acquired the lifetime
read lock remains pending. Close conservatively records uncertainty for it;
it does not drain that caller while holding a lock the caller needs. Callers
that release their lifetime read lock before publishing cancellation remain
covered by the same pending/publication ordering.

Release itself does not admit an effect and is not counted as pending.
Its failure to enter is coordinated with retirement publication using the
store-local failure mutex: before publication it can latch a failure; after
publication it returns ErrInactive joined with the original entry error, keeping
context cancellation/deadline diagnostics and the existing latch. Once Release
owns the gate, its transaction or response failure publishes before releasing
the gate and lifetime read lock, so a subsequent Release/Close sees it.
The short failure mutex never waits for the gate, lifetime lock or SQL.

Close verifies each view's stored binding and generation before retiring it.
An exact inactive generation already proves committed retirement, so Close
preserves its durable uncertainty, receipts and events. Active generations
retain the ordinary pending-before-fenced retirement path.

A lost retirement response is not evidence of a new external effect. In the
narrow case where preparation and a definitive Denied receipt were committed,
and retirement committed without pending calls or prior uncertainty, dirty
reopen may retain definitive Denied even if Release's response was lost.
An in-process response failure can still project uncertainty until exact
durable retirement is reconciled. Both a successful retry and Close preserve
the proved inactive generation's durable state. A successful retry removes
the old view and its volatile response-failure overlay. A proved settled Denied
remains settled, with replay prohibited by the original prepared record. This exception does not apply to
unknown Prepare, Save or Revoke,
or to the concurrent admitted failed-Save handoff: those must retain the dirty
guard or durable uncertainty. An exact retry preserves any uncertainty already
committed by retirement; subsequent Activate cannot clear it.

Close waits for bounded database operations, invalidates all views, and attempts
the same atomic retirement before SQL closure and finally inode unlock. A failed
retirement preserves the guard for recovery. A successful database call or
PrepareNew result is not proof that external handoff has settled. Close and
exclusive inode ownership supply no executor-quiescence proof. This journal
has no completion API or production completed state, and new evidence never
silently resolves stored uncertainty. Operator resolution remains separately
scoped.

## Receipts and inspection

Save validates the entire immutable binding before admission and verifies the
active row's binding and generation in its transaction. Receipt IDs are
scoped to the attempt. Identical ID/content is idempotent, while same-ID changed
content is retained as another evidence row and makes the projection uncertain.
Different IDs with contradictory outcome states do the same. Prepared evidence
is distinct from outcome evidence and cannot downgrade an existing outcome.
Empty evidence is a valid BLOB. When writing scanned outcome bytes back, the
journal binds a nonnil empty slice for a nil driver scan result, preserving the
empty BLOB rather than SQL NULL. Binary IDs and evidence remain byte-exact;
there is no string normalization or schema relaxation.
Original evidence is never overwritten. Foreign bindings cannot poison the
legitimate attempt. A failed late contradictory Save remains guarded even if
the original Started receipt was already durable. Receipt evidence is exact
and is refused with an error if over its bound; it is never truncated.

The [eager snapshot implementation](../../internal/journal/snapshot_linux.go)
reads records, all evidence, consumed grant bindings and events in one
transaction. Each event has the stable identity `(StoreID, IncarnationID,
Sequence)`. State mutations and their events commit together. The watermark
is the highest included committed sequence. The in-process failure latch may
conservatively add uncertainty to a record's projection; it is not another
committed event or a claim that the watermark includes an unpersisted failure.
After scanning the bounded durable rows, Snapshot samples every included
activation's failure latch under the same store-local mutex used to publish
failures. This produces one coherent volatile overlay, rather than a sequence
of per-row samples that could describe a set of failures that never coexisted.
The gate keeps durable rows and activation membership unchanged throughout
the transaction and overlay capture. The mutex covers only the bounded
in-memory pass, never SQL or waiting for operations that need the gate.
Active guards and the original evidence remain visible.

Snapshots leave no reader open. Query, scan, row-iteration, closure, transaction
or size-limit errors return no partial snapshot and no complete watermark.
Inspect uses these same eager bounds. Returned slices can be changed by the
caller without changing later reads. These are trusted control-process reads,
not public cursors, enrolled-client snapshots or authentication-generation
claims. A future application service must obtain current disclosure authority.

## Ownership and path requirements

The storage platform is Linux on a trusted local filesystem that supports
native open file description (OFD) locking. The caller is responsible for
selecting a supported local filesystem and maintaining trusted private paths.
The database directory must already exist, belong to the effective user, allow
owner access and have no group/other permissions. Create requests mode 0600
for the new main file. Existing main files and SQLite sidecars must be regular,
owned 0600 files. Symlink path components and sidecars are refused. The
implementation walks directory descriptors with no-follow opens and validates
the main inode and parent directory again before accepting connections.
It never chmods an existing directory or file.

A nonblocking exclusive `flock` on the main database inode is held until SQL
closes. Competing same-process and subprocess handles receive `ErrOwned`.
This lifetime ownership lock is separate from SQLite's record locks; holding
`flock` alone does not preserve SQLite transaction locking.
Hardlink aliases cannot bypass a live owner's inode lock. Multiply-linked
databases are also refused when idle, because SQLite sidecars are associated
with a pathname and an alias could miss another name's WAL. Sidecars must
have exactly one link. Path replacement and ownership/permission changes
detected while opening physical connections fail closed.

The directory, path ancestry and database remain under trusted local control
throughout the handle lifetime. Advisory locks coordinate cooperating owners;
they do not isolate malicious same-user code or fence an external executor.
Closing the database proves neither provider completion nor executor quiescence.

## Process startup and SQLite lock domains

The selected driver defaults to process-owned POSIX record locks. On Linux,
closing **any** descriptor for an inode releases that process's POSIX record
locks on it. Even a rejected competing Open can close a main-file descriptor,
so the inode `flock` is insufficient to protect a live SQLite transaction.

Create/Open and internal ownership acquisition call the native
`sqlite.OFDLocking(true)` API before opening any independent main-file
descriptor. Every physical connector also requires that mode before opening
SQLite. OFD main-file locks survive an unrelated main-file descriptor close.
If the process already attempted SQLite locking in POSIX mode, construction
refuses the required mode **before** opening or closing a main-file descriptor,
preserving that earlier caller's locks.

Composition that uses SQLite elsewhere must select `sqlite.OFDLocking(true)`
at process startup, before any SQLite connection or lock attempt, and keep
that mode unchanged. Repeating `true` is supported after use when already
enabled. This configures the selected SQLite library within the Ataniq process;
it changes no host environment or system setting and exposes no unsafe bypass.

The API request alone is not proof of OFD support: on the first unsupported
OFD `fcntl` returning `EINVAL`, the library silently falls back to POSIX. Each
connection must therefore consume a real main-schema read through completion
and then verify `sqlite.OFDLockingEnabled()` before mutable settings or
initialization. Once an OFD lock has succeeded, this driver reports a later
`EINVAL` as an error rather than switching modes. Unavailable, too-late or
fallen-back mode, read failures and profile mismatches fail closed; no safe
Store handle is returned. A failed Create can still leave its exclusive new
file as described above.

OFD applies to the **main database file**. WAL coordination through `-shm`
still uses SQLite-managed POSIX locks. Lifecycle code only inspects sidecar
metadata; it does not independently open or close `-shm`. Other code in the
process must also avoid raw `-shm` descriptor lifecycles that would strip
those locks. OFD does not solve WAL pathname aliasing, so hardlink rejection
and all private-path and inode checks remain required.

## Read-only preflight and connection profile

After requesting OFD mode and acquiring the inode lock, Open uses `mode=ro`
and `query_only=1`
to check application/schema identity, all exact journal table definitions,
single identity row and authority. Only after this succeeds does it open a
writer connection and apply persistent writer settings. Wrong-schema or
wrong-authority refusal preserves existing main database bytes and journal
mode. SQLite read-only access to a WAL database can create an empty WAL and
shared-memory index; it does not write journal records. Existing valid WAL
contents are read as part of the database, rather than ignored by an
`immutable` connection.

One private zero-value `sqlite.Driver` serves each Store. A connector verifies
every physical connection, including replacements, using raw driver queries
with the caller's bounded context. It never queries its own `sql.DB` from
inside connection setup, adds global registrations or accumulates global
hooks. The temporary preflight pool closes before the writer pool opens;
each has at most one open and one idle connection. Read-only transaction
options alone are not used as query-only enforcement.

The connector's DSN contains no writer PRAGMAs. Setup first applies the finite
busy timeout, fully consumes `SELECT count(*) FROM main.sqlite_schema`, and
asserts OFD remains enabled. This boundary applies to read-only preflight and
every replacement connection as well as initial writer setup. Only then does
configuration proceed to the other settings, including writer `journal_mode`
and `max_page_count`. The read uses the same bounded caller context as the
rest of connection verification.

The selected [dependency pins and notices](../../third_party/modernc-sqlite/README.md)
are `modernc.org/sqlite v1.62.0` and `modernc.org/libc v1.80.1`. Connection
verification requires SQLite `3.54.1` and source ID
`2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76`.

| Setting | Required value |
| --- | --- |
| Main-file record locking | Native OFD, verified after a consumed schema read |
| WAL shared-memory locking | SQLite-managed POSIX; no independent `-shm` open/close |
| Writer journal mode | WAL |
| Synchronous | FULL (2) |
| Foreign keys | 1 |
| Trusted schema | 0 |
| Query only | 0 for writer, 1 for preflight |
| Locking mode | normal |
| Busy timeout | Configured finite milliseconds, default 250 |
| Defensive / DQS | true / false |
| Page size | 4096 bytes |
| Writer page ceiling | Configured MaxBytes divided by page size |

The connector reads back settings and checks defensive/DQS behavior. The pinned
driver applies `_dqs=false` before statements and propagates configuration
failures. The behavioral `SELECT "journal_dqs_probe"` check additionally requires
the pinned engine's exact missing-column diagnostic and SQLite error code 1.
BUSY, I/O, unrelated SQL errors and successful string-literal readback are
refused. A mismatch closes the physical connection before it can enter the pool.
Writer transactions use immediate BEGIN. The default local SQLite VFS is
used; no alternate driver or external locking service is involved.

## Bounds and cancellation

`Options.Authority` is required: 1–256 bytes with no Unicode whitespace.
Zero-valued bounds select these defaults:

| Option | Default | Accepted explicit values |
| --- | --- | --- |
| `MaxBytes` | 64 MiB | 64 KiB–1 GiB, multiples of 4096 bytes |
| `OperationTimeout` | 5 seconds | 1 millisecond–1 minute |
| `BusyTimeout` | 250 milliseconds | Whole milliseconds, 1 millisecond–5 seconds, no greater than OperationTimeout |

Earlier caller deadlines take precedence. Pre-cancelled Create/Open calls
touch no storage, pool waits respect the operation context, and SQLite busy
waits are finite. Store admission and Info do not wait for the lifetime lock
when Close is pending or active: an already cancelled/expired context returns
its context error; otherwise admission returns os.ErrClosed. Calls through an
already retiring Attempt retain their ErrInactive classification, and foreign
binding rejection still precedes admission. Close is the only lifetime writer;
calls that acquired the read lock retain it until they finish, so Close still
waits for admitted local operations. No detached cleanup is started.
Context deadlines do not provide a hard real-time bound
on kernel/filesystem I/O.

MaxBytes bounds main database pages through `max_page_count`, including on
replacement connections. An already larger database is refused. This is not
a total filesystem quota: WAL, shared memory, filesystem overhead and temporary
files are additional. Internal engineering bounds also limit each receipt to
64 KiB of exact evidence and a 1–256-byte receipt ID, and each intent payload
to the frozen dispatch limit of 1 MiB. Each snapshot has a combined 4096-row
budget across registrations, receipts, consumed grants and events, and a
conservative 16 MiB copy budget, including duplicated intent payloads and row
overhead. Exceeding either refuses the whole read. There is no public pagination
or unbounded fallback. These choices are engineering limits, not a capacity or
throughput qualification. Per-view serialization waits and SQL calls share the
operation deadline. Finite bounds are checked before copying accepted inputs;
storage constraints and bounded accounting also limit scanned values.

Reaching the page ceiling can also refuse Release, Close retirement or Open
recovery with SQLITE_FULL: each needs room for its causal event and state
update. Failed retirement/recovery preserves the original evidence and dirty
guards, and exposes no partially recovered Store. Close still closes SQL and
releases ownership even when it reports retirement failure. Reopening at the
same fixed bound can continue to fail closed.

Trusted composition may explicitly configure a larger accepted MaxBytes for
Open of that same database. A bounded fixture fills a 64 KiB database, observes
failed Close retirement and same-bound Open, then opens at 128 KiB with the same
store/incarnation identity and original evidence. Recovery records uncertainty
before returning a Store; a guarded duplicate invokes no Boundary and produces
no effect. This uses the existing configuration path, not writable restore or
migration. The journal never automatically increases the configured bound,
reserves retirement capacity or guarantees recovery at any particular larger
bound. MaxBytes remains a main-page limit, not a total WAL or disk cap.

## Reopen, restore and evidence limits

Open is a normal same-store reopen, not a writable restore or adoption API.
It does not generate a new incarnation on reopen and cannot distinguish a
faithful file copy from the original solely from the copied metadata.
Backup restore, copied writable replicas and identity-changing migration are
unsupported. A copied database must not be treated as newly authorized work.

The package tests use fresh private temporary SQLite databases and bounded,
waited helper subprocesses. They cover stable reopen identities, existing-only
and overwrite refusal, exact authority bytes, schema/authority refusal without
main-file mutation, unsafe paths and permissions, live owners and hardlinks,
cancellation and pool waits, replacement verification, setting mismatch,
page-limit refusal, and process death before/after metadata commit.

Guarded regressions invoke the frozen Coordinator with independently counted
fake Boundaries. They cover exact and concurrent duplicates, every binding
field, binary and nil/empty payloads, grants, expiry/revocation, fresh Boundary
policy checks, cancellation before lookup and after actual fake start, response
loss, failed saves and late contradictions, immutable receipts across reopen,
stale views/releases, atomic retirement and eager snapshot consistency/bounds.
Bounded fixtures exercise real SQLite busy and FULL errors, including explicit
larger-bound recovery, without filling the host disk. Actual-driver tests save
Prepared observations after Started and Denied with empty or binary evidence,
checking exact receipts and projections before/after reopen without replay.
Conflict-before/after tests retain the original grant binding and committed
event while keeping failed commits unknown. Injected raw-connection tests
distinguish the qualified DQS error from unrelated failures and verify subsequent
readback errors propagate. Per-store unexported test seams inject journal I/O response
failures without global hooks or production environment controls.

Deterministic scheduling seams cover an already-admitted Save completing
failure publication between retirement samples, cancellation publication
after leaving the lifetime lock while Close retires, new-call rejection during
Close, stale activations and competing cancelled releases, foreign cancelled
or blocked Save/Prepare isolation, and a coherent multi-record failure overlay.
Held-Close tests require prompt cancelled admission and Info results alongside
concurrent idempotent Close calls; an admitted Save must finish before SQL
closure. Same-store Release retry tests cover concurrent/stale retries, exact
generation proof, fresh guarded late contradictions, and preservation of unknown
Prepare/Save/Revoke outcomes across reopen.
The historical draft Release-before test exposed a draft defect. With the
inherited retirement gate, that same new-call interleaving correctly returns
ErrInactive; an obsolete expected-timeout assertion is not a reproduction on
final source. The admitted-call sampling regression exercises the separate
remaining ordering defect, with per-store synchronization seams.

Waited helper children receive SIGKILL at owned seams before and after
activation, preparation commit, receipt persistence, late contradictory saves,
release and recovery, and at fake handoff. An independent owned file counts
actual fake effects across fresh reopen; assertions concern the original
attempt, not merely its row status. A dirty registration with no prepared row
cannot replay. Additional before/after Release and Close SIGKILL cases retain
a settled Denied receipt after committed retirement and require uncertainty
when the dirty guard survives; an independent counting Boundary cannot start
the original attempt on reopen. Graceful Close tests alone establish no SIGKILL
recovery claim. No live provider, network, service, namespace, runtime sandbox,
real-state migration or host pressure is involved.

Fresh subprocess regressions hold a real Store read transaction and use a
nonwriting external POSIX `fcntl` probe to confirm contention before and after
rejected same-process Open and unrelated main-file descriptor closure. The
probe acquires after SQL closes while the lifetime `flock` remains held,
distinguishing the two lock domains. Separate legacy-POSIX processes verify
refusal before main-file acquisition preserves existing SQL locks. Real
connection and replacement tests observe complete schema-read consumption;
negative profile seams check refusal and closure before mutable settings.
These seams do not claim unsupported-kernel or filesystem qualification.

WAL and FULL synchronization plus process-kill tests do not establish physical
power-loss durability, storage-stack behavior or throughput guarantees.
Injected journal-seam I/O failure is not a physical fsync fault qualification.
Driver-only qualification evidence is separate context and does not qualify
any production provider boundary. Source peer review, independent crash probes,
lead per-commit reproduction and the completed-feature whole-suite checkpoint
remain separate gates. Bootstrap, dependency freeze graphs, public admission,
FIFO, authentication, operator APIs and writable restore remain outside this
implementation.
