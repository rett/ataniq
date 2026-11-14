# Journal storage lifecycle

Status: implemented internal Linux storage foundation. The persistent journal
feature remains incomplete: this package does not implement
`dispatch.Journal`, guarded attempts, Prepare/Save, grants, receipts or events.
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

Paths must be clean absolute filesystem paths supplied by trusted composition.
The package constructs an escaped `file:` URI; it accepts neither user DSNs
nor SQL. The SQL pool and connection handles are private.

Creation uses one immediate transaction for the exact metadata schema,
application ID `0x41544a4c`, schema version 1, configured authority and two
independent cryptographically random 128-bit IDs. Store and incarnation IDs
are stored as BLOBs and exposed as hexadecimal strings. Authority is also a
BLOB, preserving its exact bytes. This local DDL initialization has no
provider conversation bootstrap meaning. No unused workflow tables exist.
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

## Ownership and path requirements

The initial execution platform is Linux with local SQLite file locking. The
database directory must already exist, belong to the effective user, allow
owner access and have no group/other permissions. Create requests mode 0600
for the new main file. Existing main files and SQLite sidecars must be regular,
owned 0600 files. Symlink path components and sidecars are refused. The
implementation walks directory descriptors with no-follow opens and validates
the main inode and parent directory again before accepting connections.
It never chmods an existing directory or file.

A nonblocking exclusive `flock` on the main database inode is held until SQL
closes. Competing same-process and subprocess handles receive `ErrOwned`.
Hardlink aliases cannot bypass a live owner's inode lock. Multiply-linked
databases are also refused when idle, because SQLite sidecars are associated
with a pathname and an alias could miss another name's WAL. Sidecars must
have exactly one link. Path replacement and ownership/permission changes
detected while opening physical connections fail closed.

The directory, path ancestry and database remain under trusted local control
throughout the handle lifetime. Advisory locks coordinate cooperating owners;
they do not isolate malicious same-user code or fence an external executor.
Closing the database proves neither provider completion nor executor quiescence.

## Read-only preflight and connection profile

Open first acquires the inode lock, then uses `mode=ro` and `query_only=1`
to check application/schema identity, the exact metadata table definition,
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

The selected [dependency pins and notices](../../third_party/modernc-sqlite/README.md)
are `modernc.org/sqlite v1.62.0` and `modernc.org/libc v1.80.1`. Connection
verification requires SQLite `3.54.1` and source ID
`2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76`.

| Setting | Required value |
| --- | --- |
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

The connector reads back settings and checks defensive/DQS behavior. A
mismatch closes the physical connection before it can enter the pool.
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
waits are finite. Close waits for the bounded local calls rather than
detaching cleanup. Context deadlines do not provide a hard real-time bound
on kernel/filesystem I/O.

MaxBytes bounds main database pages through `max_page_count`, including on
replacement connections. An already larger database is refused. This is not
a total filesystem quota: WAL, shared memory, filesystem overhead and temporary
files are additional. There is no evidence or eager snapshot API at this
stage, so no speculative bounds for those absent operations are exposed.

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
page-limit refusal, and process death before/after metadata commit. These are
storage lifecycle tests, not tests of guarded effect dispatch or recovery of
uncertain provider work.

WAL and FULL synchronization plus process-kill tests do not establish physical
power-loss durability, storage-stack behavior or throughput guarantees.
Driver-only qualification evidence is separate context and does not qualify
the journal's absent workflow or any production provider boundary.
