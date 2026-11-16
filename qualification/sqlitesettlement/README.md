# Native SQLite settlement qualification

This disposable Linux/amd64 harness investigates an unknown database-only
transaction result. It does not implement schema2, a production traffic fence,
or a process-handoff API. Independent source/mechanism review and the lead's
targeted rerun remain prerequisites to adopting a settlement algorithm.

## Run and interpret

Use the repository's selected offline toolchain and an already populated module
cache. Do not bootstrap or download anything for this experiment. Set `GOCACHE`
to task-local writable storage; leave the supplied `GOMODCACHE` read-only.

```sh
make doctor
sh scripts/go-tool.sh test -count=1 ./qualification/sqlitesettlement/...
ATANIQ_SQLITE_SETTLEMENT_GATE=1 sh scripts/go-tool.sh test -count=1 -timeout=20m -v ./qualification/sqlitesettlement/...
```

The last command is the **qualification gate**. It fails if the architecture,
kernel, parent sandbox, or sentinel controls cannot support the required sync
denial. Ordinary tests retain their other runnable checks and explicitly log
`INCOMPLETE` if that capability is unavailable; their exit status alone is not
qualification. On unsupported platforms the strict gate also fails. No fallback
to a single filtered Go thread, different driver, or replacement VFS is allowed.
The `SUPPORTED` summary requires every native control to complete and every
schedule to pass, including when a later child loses the required capability.
Directory lifecycle additionally requires bounded procfs descriptor observation,
unique main/WAL matches and descriptor-selected sync-denial controls. Missing or
ambiguous required observations report `INCOMPLETE` and fail the strict gate.
If a worker cannot install the filter, the lead's smallest host-context check is
the same strict command on its already authorized Linux/amd64 qualification host;
do not change host policy to make a worker pass.

Verbose output contains bounded JSON receipts: runtime Go/architecture, verified
engine/source ID/profile, filesystem magic, ordered operation/error stages,
sentinel controls, full before/after relational state, and physical-close/parent
wait facts. `BeforeRead` and `AfterRead` distinguish actual observations from
unpopulated fields at a pause or error. `Waited` is set by the parent only after
successful process termination. SIGKILL has a separate waited receipt. These are
protocol receipts, **not a syscall trace**. No arbitrary database content is read.
`Engine`, `SourceID` and `Profile` are expected constants emitted only after
actual SQL/API/behavioral checks succeed; they are not raw readback echoes.
The native default VFS is selected by construction (no `vfs=` override), not
independently read back.

## Fixture and lifecycle

The synthetic STRICT schema contains a command key/full binding/result, a policy
row, a causal event, and a checked integer barrier epoch. A capacity-control table
is used only to provoke `SQLITE_FULL`. The fixed key is `command-1`, the binding is
`policy=enabled;revision=1`, and the immutable result/event is `policy-enabled:1`.
Policy, result and event are committed together. Post-settlement readback only
reads these facts; it does not resubmit a command or test deduplication/admission.
Fixed-fixture equality includes the event key and zero capacity rows, along with
the complete command binding/result, policy value/revision and event cause/count.
This is not a universal database integrity check or corruption-repair framework.

The parent creates private `.settlement-*` directories on the checkout filesystem
(not a possibly memory-only `TMPDIR`). It retains the original main-file
flock descriptor and a pinned directory descriptor, never a SQLite connection,
across child execution and cleanup.
Each child receives a duplicate descriptor, checks inode identity, enables native
OFD mode before its SQLite open, and verifies OFD survived the first schema read.
The earlier settlement cases check named ownership before SQLite opens by path.
The directory lifecycle cases additionally bind SQLite's main/WAL descriptors
using bounded metadata observation as described below.
No independent SHM descriptor is opened. The directory must be writable and on
the filesystem being investigated; filesystem type is reported, not qualified
universally. Cleanup runs after every child has been killed/waited if necessary,
while the parent still owns the original inode.

Every physical connection repeats the profile from
`internal/journal/connection_linux.go`: native default VFS, WAL, synchronous FULL
(2), NORMAL locking, foreign keys on, trusted schema off, query-only off,
defensive mode on and DQS off (behavioral checks). Fixture-specific bounds are
4096-byte pages, 64 maximum database pages (256 KiB) and a 100 ms busy timeout.
The native default `fsync` selection and default WAL autocheckpoint of 1000 pages
are checked without changing them. The finite fixture stays below that checkpoint
threshold. Each child also has a 2 MiB per-file `RLIMIT_FSIZE`; file size/mode checks
are independent of the SQLite page bound. No file-pressure experiment is used.

Children have a 15-second process deadline, a five-second operation context and a
one-second pipe-drain bound. Recovery starts only after the old child is waited.
Ordinary children have a combined 64 KiB stdout/stderr cap; paused children have
separate 64 KiB stdout and 64 KiB stderr bounds. The strict command allows 20
minutes for the finite sequence of children and cleanup instead of the default
ten-minute test timeout. Pauses use a pipe receipt/acknowledgement;
no sleep establishes ordering. All SQLite calls use one physical raw connection
from the pinned driver, including its `BeginTx`/`Commit`/`Rollback` implementation.
This deliberately excludes `database/sql` pooling and a service scheduler.

## Fault mechanism and controls

Immediately before a targeted COMMIT, the helper verifies successful unfiltered
`fsync` and `fdatasync` on a disposable sentinel. With its goroutine pinned to an
OS thread across both operations, it requests `PR_SET_NO_NEW_PRIVS`, then
`SECCOMP_SET_MODE_FILTER` with `SECCOMP_FILTER_FLAG_TSYNC`. Both errno and the raw
return are checked: a positive failure TID is not success. The BPF program checks
`AUDIT_ARCH_X86_64` before syscall numbers, rejects x32 and other ABIs, and returns
EIO for either sync syscall without executing it. Both filtered sentinel controls
must return EIO. All other calls retain native behavior.

The permanent filter is installed only in that disposable child; it starts no
further executables. It denies sync process-wide, including a later close-time
checkpoint. The parent remains unfiltered. This is pre-call denial, not a device
that partly flushes before returning an error. An after-success lost-response hook
is separately labeled `application-observation` and never called native injection.

Independent unfiltered helpers run the same dirty transaction sequence. Both a
freshly reopened WAL and an already populated, still-open WAL are covered. The
latter warms the WAL with a real commit before installing the filter so failure
is not limited to creating/syncing a WAL header. Fresh-WAL failure can occur at
the WAL-header sync; this harness does not trace which sync failed. A filtered
empty transaction successfully commits with an unchanged epoch, showing why
COMMIT success without a dirty page is insufficient.

## Covered schedules and observed results

On the dispatched source and Linux/amd64 checkout filesystem (Btrfs magic
`0x9123683e`), the strict run obtained these results. The run's output bundle holds
exact command, runtime, source hashes and JSON receipts; reruns must obtain their
own evidence.

| Schedule | Observed evidence and interpretation |
| --- | --- |
| Successful mutation / post-settlement readback | One result/event/revision; successful dirty barrier and complete fixed-fixture equality on subsequent readback. No business retry is attempted. |
| Lost observation after successful driver Commit | Synthetic caller error; reopened barrier/readback recovers the original result. Successful sync is source-backed inference, not a traced event. |
| Sync denial at mutation COMMIT | Driver `SQLITE_IOERR_FSYNC` (1034); same-connection readback absent in the recorded run, not a universal assertion. Immediately after waiting for that filtered writer, recovery reopens, commits the dirty FULL barrier and reads back without an intervening unfiltered helper/Close. Reopening may select applied or absent history; confirmation requires stable, exact fixed-fixture equality. |
| Paused old writer | Another snapshot sees absent while the writer is live, but is not authoritative. Pipe-triggered cancellation occurs before calling Commit, which must return exactly `context canceled` with SQLite code 0; parent waits before barrier/readback. This does not test mid-Commit cancellation. No new mutation is admitted during this harness schedule. |
| Dirty barrier, reopened / already-open WAL | Unfiltered commits succeed; filtered commits return 1034. This establishes native sync dependence with source-backed inference, not a successful-syscall trace. |
| Empty transaction under sync denial | Commit succeeds; epoch unchanged; classifier remains unknown. Later Close returns nil, which does not establish checkpoint durability. |
| Barrier cancellation | Context is canceled before Commit, which returns exactly `context canceled` (SQLite code 0); classifier stays unknown. Later unfiltered settlement recovers the original mutation. A deadline expiry cannot satisfy the cancellation proof. |
| Capacity/write failure | An explicit oversized allocation in the barrier transaction returns `SQLITE_FULL` (13). Subsequent Rollback reports code 1, no active transaction, after SQLite's automatic rollback. Neither result clears uncertainty; later bounded settlement succeeds. |
| Death before/after mutation and barrier Commit | Pipe-defined SIGKILL, actual child wait checked before reopen; immutable state reconciles. Recovery observes barrier epoch 1 before the killed Commit or 2 after it, then increments it. These are process-crash schedules with the kernel cache retained. |
| Reported rollback/close errors | Classifier controls refuse confirmation. Synthetic protocol errors are labeled as such; native arbitrary rollback/close I/O failure injection is not established. |

The capacity control adds an allocation to the epoch transaction. It does not
claim that a fixed-width epoch update always allocates a page or that a full
database always prevents such an update. No accepted-bound increase, reserve,
deletion policy, NORMAL-mode qualification, or hardware power-loss model is used.

Native refusal assertions normalize a copy to the accepted `barrier` mode, so
failed or missing evidence must cause refusal; original receipts retain their
mode labels. Visibility-only controls use a separate fixture or a snapshot while
the paused writer is still live. Classifier-only synthetic controls cover each
required evidence field, epoch bounds, exact fixed fields, reported errors and
absent/applied interleavings. They do not inject native faults or qualify races.

## Inspected pinned implementation

Original settlement source review base: `a9198c2368098ef4e4c31ba256c3e58b9cffb444`.
Directory lifecycle extension base: `e1d2a0eba3855c7038b1e377b55f1859db91ab52`.
Driver: `modernc.org/sqlite v1.62.0`, module checksum
`h1:gMaCjSM1qpl+iOjiWhR1LYP3zqrYDI+5GYLGt6veP2g=`.
Engine: `3.54.1`; source ID:
`2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76`.
The fixture compares actual SQL readbacks with these values on every connection.

Locations below are relative to that exact cached driver module. Generated files'
build constraints were checked for Linux/amd64; no external source was exported.

| Source location | Relevant behavior |
| --- | --- |
| `tx.go:45`, `:71`, `:82` | Commit watches BeginTx's context, returns the original error, checks autocommit and attempts background rollback on error; that rollback error is ignored. Rollback itself does not watch the canceled context. Successful transaction execution is not overwritten merely because context cancellation is subsequently observed. |
| `sqlite.go:83`, `stmt.go:95` | Cancellation watcher/progress/busy handling; a general statement may report cancellation after execution. Cancellation is not proof that work never took effect. |
| `conn.go:73`, `:107`, `:1074`, `:1107` | Raw connection construction registers no finalizer or automatic cleanup; a module search found no `SetFinalizer` or `runtime.AddCleanup` registration. Physical Close explicitly calls `sqlite3_close_v2`, under its interrupt mutex. No raw statement handle is left live by the harness. |
| `lib/sqlite_g_0000000000000900.go:24747`, `lib/sqlite_g_000000000003fd6b.go:12592` | OP_JournalMode changes WAL mode using a rollback-journal transaction; BtreeSetVersion writes the main header's read/write version bytes. This precedes the fixture's schema/state WAL commit. |
| `lib/sqlite_g_000000000003fd6b.go:16024`, `:9163` | FULL enables the WAL commit sync flags; pager sends dirty pages to WAL. |
| `lib/sqlite_g_000000000003ffff.go:9393` | Commit phase one returns early when no pages changed; dirty WAL transactions proceed to frame writing. |
| `lib/sqlite_g_000000000003bd68.go:4356` | WAL header sync and FULL commit sync; sync failure is propagated before publishing the successful WAL-index header. |
| `lib/sqlite.go:34365` | `sqlite3OsSync` invokes native `xSync` when flags are nonzero. |
| `lib/sqlite_g_0000000000007d00.go:19617` | `_unixSync` checks the file `_full_fsync` return and maps failure to `SQLITE_IOERR_FSYNC`. It ignores the directory `_full_fsync` return, converts directory-open failure to OK, and clears `DIRSYNC`. File-sync success therefore does not establish directory-entry persistence for main-file creation or WAL creation/removal. |
| `lib/sqlite_g_0000000000000900.go:26897`, `:7148` | `_unixOpen` selects DIRSYNC from CREATE and journal/WAL flags, not proof of exclusive creation. `_openDirectory` derives a pathname and opens read-only without O_DIRECTORY; it does not use our pinned descriptor. |
| `lib/sqlite_g_000000000003fd6b.go:19588` | `_sqlite3WalOpen` requests READWRITE\|CREATE\|WAL even for an existing WAL. Existing WAL is not an expected-no-directory-sync control. |
| `lib/sqlite_g_000000000003fffc.go:274`, `fdatasync.go:16` | Native sync calls libc fsync by default, with a separate opt-in fdatasync switch. Neither switch nor VFS is changed here. |
| `lib/sqlite_g_000000000003fd6b.go:22989` | When backfill is needed, checkpoint syncs WAL, writes pages, truncates main to the selected database size, then syncs main before advancing completed backfill. If nBackfill equals mxFrame it skips backfill. |
| `lib/sqlite_g_000000000007fd68.go:2316` | FULL obtains checkpoint/writer locks; contention can cause partial passive work while ultimately returning BUSY. Counts and status must be consumed. |
| `lib/sqlite_g_000000000003ffff.go:11335`, `:9273` | Last-close checkpoint can sync and remove WAL on success. PagerClose ignores WalClose's return and itself returns OK. A nil driver Close is not proof that those syncs succeeded. |
| `lib/sqlite_g_0000000000007f80.go:7831` | `_unixDelete` syncs the directory only if requested; directory-open failure becomes OK. Default last-close WAL deletion requests dirSync=0 and ignores the deletion return. |
| `lib/sqlite_g_000000000003ffff.go:4332` | `_pagerOpenWalIfPresent` can delete WAL beside a zero-page main. The existing-main protocol is restricted to known initialized WAL fixtures. |
| `lib/sqlite_g_000000000003fd6b.go:21304`, `sqlite.go:1212` | Unix syscall overrides exist, but generated callback pointers have ABI/lifetime assumptions. Native `_full_fsync` directly calls libc here; no safe delegating callback seam was assumed or installed. |

`modernc.org/libc v1.80.1/ccgo_linux_amd64.go:149723` and `:149731` implement
fdatasync/fsync using their Linux syscalls. The cached sources, not another SQLite
build, back the inference. Repository `internal/journal/ownership_linux.go` and
`connection_linux.go` provide the original-inode/OFD/profile prerequisites.

Primary contracts: [seccomp filter](https://www.kernel.org/doc/html/latest/userspace-api/seccomp_filter.html),
[seccomp(2)](https://man7.org/linux/man-pages/man2/seccomp.2.html),
[SQLite sync failures](https://www.sqlite.org/howtocorrupt.html#failure_to_sync),
and [synchronous](https://www.sqlite.org/pragma.html#pragma_synchronous).

## Bounded directory lifecycle

This extension qualifies a test-only public Go/driver sequence. It adds no
production API, schema2, VFS, persistent-WAL profile, dependency or service.
The existing fixture entrypoint still initializes automatically; a shared owned,
uninitialized fixture entrypoint makes fresh-main initialization observable.
One raw physical writer and one original-inode owner remain the normal model.

For an **existing initialized main**, the ordered receipts are:

1. Verify that the database is already in WAL mode, verify the exact connection
   profile, and consume a read. Observe and bind the actual named WAL and the
   live SQLite main/WAL descriptor identities. Open success alone is insufficient.
2. Check pinned/named identities, sync the retained directory alias, and check
   identities again (`pre-dirty`). Only then consume Before and execute a genuine
   dirty epoch transaction under FULL. Consume After and require exact fixed
   state equality except for the incremented epoch.
3. Check main/directory/WAL identities, sync the same alias, and recheck them
   (`live`). This completed prefix is the **live boundary**. The connection is
   still open; the post-commit directory sync is intentionally retained.
4. Check identities before checkpoint. Fully consume
   `PRAGMA main.wal_checkpoint(FULL)`: exactly one row of three integers, EOF,
   successful Rows.Close, busy=0 and positive log=checkpointed. Positive counts
   are required because this fixture has a new dirty transaction, stays below
   autocheckpoint threshold, and has no intervening checkpoint. These are zero
   outstanding frames, not a zero-length WAL. No TRUNCATE is substituted.
5. With no further SQL reads, writes, PRAGMA changes, retries or new connections,
   check identities, physically Close, inspect identities and sidecars, sync the
   directory alias, and recheck (`final`). The parent actually waits before
   accepting the **closed boundary**, recovering or restoring test paths.

For **fresh main creation**, require exclusive zero-length main and absent
sidecars. Configuration first performs a mode-conversion transaction using a
rollback journal, followed by one WAL commit initializing the fixed schema and
exact applied state at epoch 0. Consume that state, bind descriptors and perform
checked `init-complete` directory sync. The conversion and ignored native
directory-sync error paths are pinned-source evidence, not traced directory
syscalls. An interrupted conversion/initialization can leave a half-initialized,
nonempty, schema-less main; existing read/preflight checks refuse that state.
There is no automatic completion or repair, and no conversion-crash qualification.
This initialization stage is recorded separately; it is not a dirty-epoch live
boundary. FULL checkpoint, physical Close and the final directory boundary are
still required for the stronger closed result. A fresh observer must read the
exact state. Directory sync alone is not proof of main-file content persistence.

Each reopened incarnation repeats its prerequisite. The positive existing-main
case observes absent WAL, binds the newly opened WAL, completes both boundaries,
then repeats once. Main and directory identities remain constant. A filesystem
may recycle an unlinked WAL inode number; observed absence before the next open
and subsequent binding establish recreation without assuming unequal numbers.
Unexpected live WAL identity change refuses the current attempt. Ordinary WAL
salt/content reuse on the same inode is not inode replacement.

The newly created owned fixture directory is canonicalized once with
`EvalSymlinks`, allowing normal symlinked checkout ancestors. It is then acquired
by bounded no-follow descriptor walking, fstat'd and kept pinned; subsequent
acquisition and identity checks retain the strict no-follow behavior.
The helper retains inherited fd 4 and a CLOEXEC duplicate until
process exit; explicit Sync uses the duplicate, not a pathname reopen. Before
and after boundaries, descriptor-relative no-follow metadata and a fresh
no-follow configured-directory walk must match original ownership. Main and
sidecars must be regular, owned, mode 0600, single-link and within the file cap.
No independent SHM description is opened. The procfs selector enumerates at most
128 descriptor names, closes that enumeration, and uses only fstat on numbers.
It excludes ownership/alias descriptors, requires exactly one main and WAL match,
and never opens `/proc/self/fd/N`, duplicates SQLite descriptors or closes them.
Directory descriptors are rooted through receipt encoding so finalizers cannot
release/reuse their numbers. The main-sync selector repeats uniqueness selection
and checks target/control identity immediately before filter installation.

| Added schedule | Required evidence and attribution |
| --- | --- |
| Fresh main and separate init EIO | Checked initialization plus explicit closed sequence; denied init-complete Sync keeps the failed receipt unknown. No discard/recreate policy. |
| Existing main, two WAL incarnations | Checked pre-dirty and live boundaries, positive complete FULL counts, physical Close, checked final boundary and observed default WAL absence. WAL remaining is recorded and is never manually unlinked to manufacture deletion success. |
| Pre-dirty, live and final directory EIO | Child TSYNC filter denies fsync/fdatasync only on the pinned alias. Target and independent pinned descriptor fsync succeed before installation; target calls return EIO afterward while the other descriptor remains allowed. The actual File.Sync boundary returns EIO. |
| WAL-sync checkpoint denial | Existing all-sync filter installed after dirty commit/live sync; explicit checkpoint returns native 1034 before backfill, according to pinned source ordering. Clean physical Close leaves an observed regular nonempty WAL. |
| Main-sync checkpoint denial | Unique live main fd selector denies only its syncs. Direct EIO and unaffected WAL-sync controls precede native checkpoint error 1034. Its position after page writes/truncation is source-backed, not a trace of media writes. Clean physical Close leaves an observed regular nonempty WAL. |
| Reported Close error | Successful explicit checkpoint and real Close, then a labeled synthetic Close error. Closed classification refuses; immutable live completion survives. |
| Hidden close-time checkpoint | Separate all-sync denial before Close, without explicit checkpoint, requires a clean physical-close step and an observed regular nonempty WAL afterward. The internal attempted-checkpoint explanation remains pinned-source inference, not a traced checkpoint syscall. No Close sync error is demanded after a completed checkpoint. |
| Reader-blocked FULL | A bounded snapshot helper pins the old state before dirty commit. FULL returns busy=1 with incomplete counts; both helpers are released/waited before recovery. This concurrent-reader negative control is not the allowed writer architecture. |
| Two process deaths | Pipe-defined SIGKILL after commit/before live directory sync, and after Close/before final sync. Actual wait precedes reopen. Missing receipts stay unknown; kernel cache remains. |
| Main, directory and WAL replacement | One retained sibling at a deterministic seam. Sync on the original directory succeeds but identity mismatch refuses before dirty work. Disposable child exits without SQL Close/checkpoint before restoration; replacements are never initialized or synced. |
| Classifier and acquisition controls | Individually absent/error-bearing sync/Close/checkpoint facts, malformed counts/EOF/Rows.Close, changed identities/descriptors, missing live observations and wrong order refuse from the same accepted mode. Fresh controls require WAL absence before open, complete initialization boundary facts/order and the exact applied state at epoch 0. Missing, symlink and non-directory acquisition cases are wrapper refusals, not native directory-open injection; the normal fixture entrypoint also checks symlinked checkout compatibility. |

Residual WAL in these three denial schedules is a selected observed layout,
not a universal prediction after arbitrary EIO or proof that an internal
checkpoint syscall ran. The current failed closed attempt is still refused.
The closed classifier supports consistent selected residual layouts; the positive
default-removal test separately requires WAL absence. Releasing the last reader
may checkpoint on its Close before the next observer, so the reader-blocked
schedule's subsequent recovery is observational.

`Lifecycle` receipts retain ordered identity observations, device/inode/type/
owner/mode/link metadata, directory attempts/results, checkpoint counts/EOF/
Rows.Close, actual Close and synthetic reported errors, selector identities and
controls, exact Before/After state and parent Waited. `Before` selector controls
mean target fsync then other-fd fsync; `Denied` means target fsync then fdatasync;
`Unaffected` records two post-installation other-fd fsync calls. Control syncs
themselves can persist data/names, so these tests establish refusal/error
observability, not that the denied boundary left a name nondurable.

Prerequisite/identity failures deliberately exit the disposable helper with
`FailStop`, `disposable-exit-without-sql-close` and absent Close evidence. There is
no deferred SQL Close or hidden cleanup checkpoint on that path. The pinned
raw driver has no connection finalizer or automatic cleanup (source locations
above), so losing the local Go reference does not invoke SQL Close. The parent
owns the original main/directory through receipt encoding and child exit,
waits, then may start a separate unfiltered
incarnation. Original failed and recovery receipts are logged and retained
separately by construction; recovery does not upgrade the earlier failed attempt.
Checkpoint or Close failure invalidates only the current closed attempt; an
already completed live prefix and immutable selected history remain separate.

These filters intercept the application's checked directory alias, **not**
SQLite's separately opened directory descriptor. Native ignored directory-error
branches remain exact source evidence. The native open lacks O_DIRECTORY; an
O_DIRECTORY filter would not establish native interception. There is no syscall
trace, unknown-fd trap, unlink filter, private pointer or generated callback.
Procfs is a qualification observation dependency, not a production protocol
requirement. A concrete capability gap remains INCOMPLETE.

The fixture directory already exists. Persisting creation/rename of that directory
would require its parent's synchronization and is a separate provisioning
lifecycle. Identity observations detect these deterministic replacements; they
do not defeat an adversarial same-UID actor racing and restoring SQLite's path
opens. The trusted-host/sandbox separation assumption remains. File.Sync has no
context parameter; the 15-second helper deadline is not proof that uninterruptible
kernel I/O can be promptly canceled. No host pressure or broad stale-file sweep
is used. See [fsync(2)](https://man7.org/linux/man-pages/man2/fsync.2.html),
[FULL checkpoint](https://www.sqlite.org/c3ref/wal_checkpoint_v2.html) and the
[checkpoint PRAGMA](https://www.sqlite.org/pragma.html#pragma_wal_checkpoint).

## Supported conclusion and limits

The tested sequence supports relational settlement in these fixtures under the
documented SQLite/OS sync contract: stop new traffic; settle and discard the old
physical writer while retaining original ownership; reopen and verify the profile; commit an actual
epoch change under FULL; only after success read the exact key, complete binding,
result and relational state. Any missing capability, unobserved state, failed
operation or inconsistent evidence remains unknown. The harness requires parent
wait and paired sync-dependence evidence before classifying applied or absent.
Before confirmation, the entire Before state with only its epoch incremented
must equal After; fixed applied/absent templates must also match.

**Directory-entry persistence for main-file creation and WAL creation/removal remains unqualified for production adoption.**
The bounded candidate now exposes checked directory failures and explicit FULL
checkpoint completion under stable ownership and the SQLite/OS sync contract.
It does not amend production Create/Close, pooling, replacement or continuous
traffic semantics. Independent source review and lead targeted rerun remain
required before adoption. Stale-WAL resurrection motivates the pre-dirty order
from source/crash reasoning; it is not a demonstrated power-loss result here.

This establishes pre-sync error propagation and sync dependence of the tested
dirty commits. It cannot prove hardware honored successful sync, emulate a
partially completed device flush, or erase the kernel cache with SIGKILL. It does
not trace a native sync success followed by a native error. Checkpoint and internal
rollback errors can be hidden by the pinned implementation; explicit checkpoint
receipts avoid reliance on ignored close-time checkpoint errors but do not expose
every internal error. Synthetic rollback/Close controls remain labeled as such.
A later successful fsync does not undo a prior real storage failure, and these
fixtures do not repair EIO-induced data loss. The production design, pooling, live admission
fence, filesystem/storage qualification and broader failure interleavings remain
outside this research and require independent qualification before adoption.
