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
(not a possibly memory-only `TMPDIR`). It retains only the original main-file
flock descriptor, never a SQLite connection, across child execution and cleanup.
Each child receives a duplicate descriptor, checks inode identity, enables native
OFD mode before its SQLite open, and verifies OFD survived the first schema read.
The identity check uses `Lstat` before SQLite opens by path in the private fixture
directory; it is not inspection of SQLite's own main-file descriptor.
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

Repository base: `a9198c2368098ef4e4c31ba256c3e58b9cffb444`.
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
| `conn.go:1074`, `:1107` | Physical Close calls `sqlite3_close_v2`, under its interrupt mutex. No raw statement handle is left live by the harness. |
| `lib/sqlite_g_000000000003fd6b.go:16024`, `:9163` | FULL enables the WAL commit sync flags; pager sends dirty pages to WAL. |
| `lib/sqlite_g_000000000003ffff.go:9393` | Commit phase one returns early when no pages changed; dirty WAL transactions proceed to frame writing. |
| `lib/sqlite_g_000000000003bd68.go:4356` | WAL header sync and FULL commit sync; sync failure is propagated before publishing the successful WAL-index header. |
| `lib/sqlite.go:34365` | `sqlite3OsSync` invokes native `xSync` when flags are nonzero. |
| `lib/sqlite_g_0000000000007d00.go:19617` | `_unixSync` checks the file `_full_fsync` return and maps failure to `SQLITE_IOERR_FSYNC`. It ignores the directory `_full_fsync` return, converts directory-open failure to OK, and clears `DIRSYNC`. File-sync success therefore does not establish directory-entry persistence for main-file creation or WAL creation/removal. |
| `lib/sqlite_g_000000000003fffc.go:274`, `fdatasync.go:16` | Native sync calls libc fsync by default, with a separate opt-in fdatasync switch. Neither switch nor VFS is changed here. |
| `lib/sqlite_g_000000000003fd6b.go:22989` | Checkpoint syncs WAL before backfill, then syncs the main database before advancing completed backfill. |
| `lib/sqlite_g_000000000003ffff.go:11335`, `:9273` | Last-close checkpoint can sync and remove WAL on success. PagerClose ignores WalClose's return and itself returns OK. A nil driver Close is not proof that those syncs succeeded. |
| `lib/sqlite_g_000000000003fd6b.go:21304`, `sqlite.go:1212` | Unix syscall overrides exist, but generated callback pointers have ABI/lifetime assumptions. Native `_full_fsync` directly calls libc here; no safe delegating callback seam was assumed or installed. |

`modernc.org/libc v1.80.1/ccgo_linux_amd64.go:149723` and `:149731` implement
fdatasync/fsync using their Linux syscalls. The cached sources, not another SQLite
build, back the inference. Repository `internal/journal/ownership_linux.go` and
`connection_linux.go` provide the original-inode/OFD/profile prerequisites.

Primary contracts: [seccomp filter](https://www.kernel.org/doc/html/latest/userspace-api/seccomp_filter.html),
[seccomp(2)](https://man7.org/linux/man-pages/man2/seccomp.2.html),
[SQLite sync failures](https://www.sqlite.org/howtocorrupt.html#failure_to_sync),
and [synchronous](https://www.sqlite.org/pragma.html#pragma_synchronous).

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
The pinned native implementation ignores directory synchronization failures as
described above. This first milestone establishes sync dependence and tested
relational settlement only. Checked owned-directory synchronization with lifecycle
ordering, or an equivalent protocol, needs separate qualification before adopting
a production barrier. No such second stage or replacement VFS is implemented here.

This establishes pre-sync error propagation and sync dependence of the tested
dirty commits. It cannot prove hardware honored successful sync, emulate a
partially completed device flush, or erase the kernel cache with SIGKILL. It does
not trace a native sync success followed by a native error. Checkpoint and internal
rollback errors can be hidden by the pinned implementation; this work does not
invent observability for them. The extra rollback/close refusal tests validate
only the report classifier. The entire production design, pooling, live admission
fence, filesystem/storage qualification and broader failure interleavings remain
outside this research and require independent qualification before adoption.
