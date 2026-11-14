# Static execution-boundary observations

This is qualification infrastructure for a Linux/amd64 static workload subject.
It does not qualify a sandbox, development image, provider or authentication
path. The workload emits facts, status and errno. The explicit trusted host
driver binds synthetic controls and lifecycle evidence; independent host
verification and review remain necessary for any support decision. There is
no pass or support field in workload or driver output.

## Selected host evidence

The lead retained fifteen synthetic FD1 role and case observations from the
historical applied source `32291c48f19c5685202dd36f57e05238f19e98ea`, tree
`56c9fac6ae7fe7b73577bc2dd64e4dff8ad1fd27`, integrated as `6ee7ef5`.
The matrix covers five cases across direct, candidate and weakened-synthetic
roles. Its SHA-256 is
`1bbd1046c36ce4e953a7e615ec85639729d04b3e39a0cfca69a9f609c5dfa17f`.
Historical exits were observed by the controller. The descriptor receipt is
pre-exec and post-exec observations remain necessary to establish closure.

A later successful no-pressure `r1-entry` child-entry observation used source
`c98da9cc1087ba181536efae008ec6b253097733`, tree
`a9723c75a2e1d0b0f80c1dce257d468cf1c6e28d`. Attempt
`6f51ac5f0aefe516ac715ea27e8dd0d2` used kernel 7.2.15 and systemd 262.1
and received independent raw review. The run summary SHA-256 is
`fc1b4c901a74ec414396f9c952a8af847f3f3e978a3198bf30583fa1df0d2b26`.
The lead gate receipt `r1-scoped-admission-lead-gates.json` has SHA-256
`921236234e2cf57aba088a36c7850657ffebb62ebbaacde8c3ab41ae98f445c7`.
Raw artifacts are retained externally. These observations do not establish full
sandbox qualification. Dynamic dependency closure remains incomplete and
pressure R1 remains unsupported.

The overall design has two entrypoints: a trusted driver and a static workload
probe sharing an internal package. `qualification/host/cmd/qualify` implements
one concrete bubblewrap profile with a private service-side reexec injector
and a dedicated final descriptor reexec in the same static driver.
There is no runtime/provider implementation, OCI backend, daemon, policy
generator or additional module dependency. Platform-specific code has explicit
`linux && amd64` build constraints; other platforms are outside this tranche.

## Compile and check

Follow [the checkout instructions](../AGENTS.md) and review the bootstrap
preview/helper before any bootstrap. The developer Go pin lives only in
[`.go-version`](../.go-version). The targeted check is:

```sh
make doctor
sh scripts/go-tool.sh test ./qualification/host/... ./tests/...
```

Go test compiles the CLI and tests its framing through injected streams. It
does not launch a sandbox. Descriptor regressions execute owned child processes,
inspect only their own FD metadata, and use fresh synthetic files, directories,
anonymous pipes and anonymous Unix socketpairs. Test-only
seccomp errno rules exercise the production terminal path without production
fault hooks. Kernel-restricted fixtures explicitly skip and remain unqualified.
Other tests use owned fake
files and frozen trees, synthetic metadata and optional private Unix fixtures.
Unavailable Unix fixtures report explicit skips. Device type rejection uses
synthetic stat metadata; tests do not create or open host devices. No backend,
keyring, resource-pressure or live cancellation experiment runs in default
tests. Driver tests use deterministic supervisor/process/cgroup transcripts;
they cannot dispatch systemd or bubblewrap.

The wrapper's `test|build` actions share the selected offline Go logic. Build
both static Linux/amd64 entrypoints without tool or dependency acquisition:

```sh
make qualification-host-build
```

If the default cache or temporary directory is unwritable, export `GOCACHE`
and `TMPDIR` pointing to owned writable directories before these commands.
Outputs are `build/qualification-probe` and `build/qualification-qualify`.
Static compilation alone does not establish applied runtime isolation.

## Explicit host driver

Host execution is coordinator-only after review. Existing user systemd,
unified cgroup v2, pidfd-capable Linux, bubblewrap with the selected flags and
an independently reviewed classic BPF binary are qualification subjects. They
are **not** default developer prerequisites and bootstrap installs none of
them. The selected driver and probe must be static ELF64/amd64. Bubblewrap
may be dynamic ELF64/amd64: its original absolute PT_INTERP and DT_NEEDED
metadata are retained. The bounded reader uses the loader's PT_DYNAMIC and
file-backed PT_LOAD view for DT_STRTAB/DT_STRSZ, even when section headers are
absent or describe different tables. It rejects ambiguous/unmapped extents,
unterminated tables, invalid dependency string offsets or termination, and
path-valued dependencies. Metadata extents and PT_LOAD memory ranges are expanded
to the actual host page size with checked addition/rounding: any other load
touching a dynamic/string metadata page is unsupported, even with disjoint
logical bytes. Linux's [elf_map](https://github.com/torvalds/linux/blob/v6.12/fs/binfmt_elf.c)
maps whole pages, so a later load can replace those bytes from another file page.
Fixtures check the Linux/amd64 4096-byte page premise; they do not execute the
loader. Any DT_RPATH/DT_RUNPATH tag is rejected by presence,
including empty or invalid strings, so the frozen copy cannot change relative
loader searches. The reader accepts at most 128 program headers, 65536 bytes
per dynamic/string table and 32 dependencies. Static driver/probe checks and
selected/frozen/pre-exec bubblewrap checks share this runtime metadata reader.
No loader
resolver, `ldd` or dynamic-library mounts are used. Dynamic launcher dependency
closure remains incomplete and full runtime/profile support unqualified.

Create a stable private mode-0700 campaign root on storage supporting flock,
exclusive creation, atomic rename and file/directory fsync. Do not put secrets,
credential paths or unrelated state there. The driver takes an exclusive
controller flock and strictly scans at most 128 campaign entries. Fresh random
attempt directories live beneath this root; malformed, orphaned, unresolved or
durability-failed records block all new starts in that campaign, including
after restart. Never bypass the fence by selecting a new campaign for the same
unresolved work. The root and all subject paths must remain frozen against
external mutation. Kernel, host administrator and controller are trusted.

Supply a single owned mode-0600 JSON configuration with these exact keys:

```json
{
  "version": 1,
  "campaign": "/owned/qualification/campaign",
  "driver": "/owned/qualification/qualification-qualify",
  "probe": "/owned/qualification/qualification-probe",
  "bwrap": "/usr/bin/bwrap",
  "bpf": "/owned/qualification/reviewed.bpf",
  "systemctl": "/usr/bin/systemctl",
  "systemd_run": "/usr/bin/systemd-run",
  "role": "direct",
  "role_id": "0011223344556677",
  "run_id": "0123456789abcdef",
  "request_id": "fedcba9876543210",
  "case": "inventory",
  "reviewed_bpf": true
}
```

These are illustrative paths, not discovery defaults. Every path is absolute,
canonical and distinct; subjects are outside the campaign. Symlink components,
nonregular files, hardlinks and group/other-writable subjects are rejected.
The driver path must name the running executable. IDs use the probe vocabulary.
Legacy cases are `inventory`, `fake-file`, `fake-fd` and `hold`; roles are `direct`,
`candidate` and the restricted `weakened-synthetic` control described below.
The fixed `r1-entry` candidate case has its own selection and bounds below.
`reviewed_bpf:true` is the operator's explicit statement that the
selected bounded classic BPF artifact was separately reviewed for this profile;
length checks and hashing do not review its policy. The filter must enforce
the reviewed syscall/nested-userns restrictions. The driver retains its digest.
No default filter is invented or installed.

### Fixed FD1 host selection and binding

The optional configuration key `"descriptor_profile":"fd1-typed"` selects
one fixed descriptor challenge. It accepts `inventory`, `fake-fd`,
`fake-dir-fd`, `fake-pipe-fd` and `fake-socket-fd`, under `direct`, `candidate`
or `weakened-synthetic`. Typed `hold` and `fake-file`, unknown profiles and
typed operation cases without this selector are rejected. Every typed
candidate requires `direct_control` naming a completed matching direct
attempt. Omitting the selector retains the historical launch arguments,
descriptor policy, serialization and plan hash meaning. Neither `HostProfile`
nor `FinalDescriptorPolicy` changes version.

Preparation freezes the separate owned `capability/leaf` tree, the regular
fixture, its static leaf challenge and the fixed metadata filename before
creating `request.json` and its plan hash. The metadata lives at
`fixtures/inherited-bindings.json`; typed bubblewrap arguments add only its
read-only file bind at `/fixtures/inherited-bindings.json`. They never mount
the capability directory or leaf. Direct controls use the same owned fixture
root. The artifact contains fixed identities and challenges, never source
paths or caller-selected descriptors.

Runtime channel identities are separate durable evidence outside the plan
hash. The controller authenticates their artifact's owned regular single-link
subject, identity and hash, parsed content, fixed endpoint owner/type/flags,
closed-source provenance and exact run/request/attempt/profile/nonce/static
challenge/plan relationships. The existing exact executable, process, service
invocation and boundary authenticate the injector's provenance. The artifact
alone conveys no authority. A partial binding or failed durable save cannot
publish release. Restart validates durable binding structure separately from
current artifact authentication; it never reconstructs endpoints or launches
another injector. Unexpected typed runtime fields on historical records are
rejected.

After a lost service-start acknowledgement, cleanup-only reconciliation first
validates the exact original service/injector receipt and authenticates its
runtime artifact against the immutable plan. It adopts the runtime binding
together with the injector, workload and target before saving cancellation.
Cleanup saves reject missing, partial or contradictory typed bindings too.
An interruption before or after the original service stop can resume cleanup
from those durable identities; reconciliation never publishes release or starts
a replacement.

Normal launch also retains the authenticated injector, runtime, workload and
target together before checking effective budgets or acquiring a process handle.
A failure in those checks preserves the original stop authority. If artifact
integrity makes a bound or released save refuse the binding, the controller saves
that authority and the integrity failure as `cleanup-unknown`; it publishes no
release. Cleanup after reopening uses the retained identities even when the
artifact or original receipt file can no longer authorize adoption.

If an artifact disappears, changes or is replaced after a valid durable binding,
the structurally validated original record remains available to `inspect` and
exact-identity cleanup. Its optional `fd1_integrity_failure` field reports
`typed runtime artifact integrity unproved`; `reconcile` reports reason code
`typed-runtime-integrity-failed`. Cleanup saves retain this failure alongside
the original runtime identities. It keeps new starts fenced after complete
cleanup and restart, even if the original artifact is later restored. The
failure is separate from filesystem durability poisoning: it does not erase
independently validated stop authority. A completed record still permits no
qualification claim or direct-control reuse after this failure. Release, final
wrapper preparation, controls and successful collection continue to require
current artifact authentication and no retained integrity failure. There is no
artifact replacement, endpoint recovery or automatic fence reset.

Inspection durably latches a detected incident in the validated attempt
directory without waiting for reconciliation or an intent save. The marker
does not grant cleanup authority; the original validated record supplies it.
If marker publication or syncing fails, the campaign is durability-fenced and
requires operator reconciliation. When the filesystem cannot persist any
marker, a new crash-persistent fence cannot be guaranteed.

Post-adoption authentication failures at release and collection entry latch the
integrity incident and start fence immediately, before any fallback save or
finalizer can read the artifact again. Restoring the exact original file between
the failed check and that later read cannot erase the observed failure. Evidence
that never passed adoption does not become trusted cleanup authority.

Every collection return checks artifact integrity and retains detected incidents,
including failed observation validation and authentication failure on collection
entry after completion was saved. An artifact change during either evidence-save
boundary retains available raw observations, the original observation error and
proved cleanup with the integrity failure, but returns an inconclusive result.
Restoring the original artifact before reopening cannot erase that recorded
incident. An `observed` raw probe status in such a record cannot authorize a
successful host report or direct-control reuse. Collection without complete
boundary proof stays unresolved. A failed durable save remains an error and
retains the existing filesystem failure fence; it cannot establish successful
cleanup or become an ordinary readable record after restart.
If saving an unknown transition also fails, the returned error retains both the
original cause and the save failure; neither error establishes cleanup.

The fixed role table for this selector is:

| Role | Descriptors preserved at target exec |
| --- | --- |
| `candidate` | 0–2 and reviewed BPF513 |
| `direct` | 0–2, directory128, pipe256, Unix stream384 and regular512 |
| `weakened-synthetic` | 0–2, 128/256/384/512 and reviewed BPF513 |

All three roles enter the identical service-local injector and final wrapper.
All fixed slots must be vacant and below actual RLIMIT_NOFILE; no fallback or
limit raising occurs. The injector opens the directory read-only/no-follow,
creates CLOEXEC nonblocking pipe and AF_UNIX/SOCK_STREAM temporaries, and writes
one complete bounded type-specific nonce frame to each channel. Receiving
endpoints are installed before their temporary sources close. Every producer,
peer and temporary alias, including aliases sharing receiving status flags,
must close successfully and be verified closed before artifact/receipt
publication. No living peer, ancillary transport or configurable FD list is
introduced. A short write, collision, unexpected flags or unavailable socket
metadata aborts before release. Neither wrapper nor probe repairs flags.

The final wrapper requires the exact released record, matching injection and
the same authenticated artifact. It rereads that exact artifact and verifies
actual endpoints, host ownership, fixed dev/inode/type, exact F_GETFL access and
NONBLOCK, AF_UNIX/SOCK_STREAM metadata and frozen leaf content before terminal
preparation. It never reads, seeks or Preads a channel. Typed directory link
counts and metadata are captured after per-kind admission and checked again
in the raw terminal loop. Regular files and stdio retain strict single-link
checks. The close_range barrier, immediate exec, empty environment, exact
stdio and BPF offset-zero rules remain in force.

Typed candidate controls match case/profile, current chain hashes, campaign,
run, boot, complete cleanup and successful operation or inventory proof.
Their original creation time is retained in `control.direct_created` and
checked against the 300-second freshness window again before release and
wrapper execution. The pair reuses nonce/challenge bytes; each attempt binds
its own directory, leaf, regular and channel identities. It never copies a
previous attempt's inode identities as expectations for new objects.

Candidate operations require observed fstat EBADF at the selected fixed slot.
Directory controls require identity, open, read and digest success. Channel
controls require identity, read, exact frame and EOF, without an inconclusive
tail. Missing metadata, a reused slot, EAGAIN, incomplete reads or an unrelated
denied syscall cannot establish closure. Typed inventory uses the same exact
table parser and mandatory owned stdio anchors. It additionally matches each
fixed object's identity and mode/type: candidates require all four intended
slots absent and no alias anywhere; direct/weakened inventories require each
capability exclusively at its intended slot. BPF must be absent everywhere
after final workload exec. No unknown inventory descriptor is read. Failed
observations remain durable after proved cleanup.

Per-attempt reports remain observations. Full FD1 coverage requires independent
review of direct/candidate/weakened operation records and all three inventory
records; neither one successful run nor the pre-exec receipt supplies a matrix.
At this integration's source stage no systemd/bubblewrap launch or live matrix
was run by the worker.
The worker's socket metadata admission was denied: complete typed injector/
terminal cases explicitly skipped, leaving those controls unqualified. Separate
owned directory/pipe terminal tests exercised real directory link counts,
regular hardlink rejection, flag drift, EOF and unknown-alias closure. Fake
controller transcripts and captured accepted-base encodings supply offline
checks only. The lead must repeat owned-child tests outside that restriction,
review source and execute the bounded live matrix separately. Dynamic backend
dependency closure remains unresolved. The feature-wide `make test` gate
remains the lead's checkpoint; this worker runs only targeted uncached host
tests and static Linux/amd64 builds, without bootstrap or downloads.

The commands are:

```text
qualification-qualify run --allow-host-operations --config /owned/qualification/config.json
qualification-qualify inspect --campaign /owned/qualification/campaign
qualification-qualify inspect --campaign /owned/qualification/campaign --attempt <32-hex-attempt>
qualification-qualify reconcile --allow-host-operations --campaign /owned/qualification/campaign --attempt <32-hex-attempt>
```

`inspect` locks and reads the exact campaign without contacting the manager;
it can also durably record a detected integrity incident.
`reconcile` is cleanup-only for the exact persisted attempt: it never releases
an injector or launches a replacement. There is no wildcard cleanup, prune,
manager reset, service restart or automatic name reuse. Invalid CLI/configuration
returns 2, output failure 1, unsupported host outcome 3 and inconclusive outcome
4. `run` returns 0 only for a retained bounded observation, which is never a
qualification pass. Reconcile always returns 4 because it cannot create a case
verdict. Host JSON retains `outcome`, `phase`, fixed `stage`/`reason_code`, bounded
sanitized `reason` and `new_starts_blocked`. A malformed campaign can expose only
the fence through inspect, without a usable attempt record.

Each intent is created exclusively and synced before any supervisor query or
start. Atomic updates sync the new record and parent directory. The record
retains root dev/inode/owner, boot ID, unique nonhyphen slice/service names,
scope of cleanup authority, finite deadlines, profile, request/role IDs, exact
arguments, selected executable/BPF digests and owned frozen copies. Filesystem
sync failures poison the controller and leave an unresolved intent/marker;
elapsed deadlines never clear launch uncertainty. Phase/reason changes become
visible in shared records only after replacement and directory sync succeed.
Early encoding/size, entropy and ownership failures also set a sticky fence;
they preserve the prior durable intent and cannot be cleared by a later save.
Invalid phases/identities are rejected before mutation or marker writes. Failure
markers are written only relative to a pinned, validated owned attempt directory.
If an earlier failure had no safe marker destination, a later update first
requires a durable marker at its validated destination. Thus even a subsequent
successful completion keeps new starts blocked in that controller and on restart.
Initial creation failures likewise remain fenced; cleanup evidence does not
authorize ignoring a durability fence or creating a replacement attempt.

The driver first checks that the service is not found, and the generated slice
is inactive with empty ControlGroup/FragmentPath and no pending job. A generated
slice may already report `LoadState=loaded`. It starts that exact parent and
requires actual active identity, InvocationID, cgroup dev/inode and
`StopWhenUnneeded=no`, with the controller outside. It requests only properties
supported by each unit kind, including empty properties using `show --all`.
The manually retained parent is the complete-boundary oracle: `populated=1`
while the injector/workload lives, then `populated=0` on the **same live parent
identity** after completion/cancellation, before exact slice stop. Missing
service, missing cgroup, ENODEV, a held cgroup FD, elapsed time or exit 0 never
substitutes for that observation.

The service uses `Type=exec`, `Restart=no`, `RemainAfterExit=no`,
`RuntimeMaxSec=20s`, `TimeoutStopSec=3s`, `KillMode=control-group`,
`NoNewPrivileges=yes`, `MemoryMax=67108864`, `TasksMax=32`, `CPUQuota=100%`,
`LimitNOFILE=1024` and `LimitFSIZE=131072`. Actual manager properties and
memory.max/pids.max/cpu.max must match. These are aggregate safety budgets,
not resource-enforcement qualification. Literal duration/quota output and
effective-limit shapes require selected-host verification; unsupported shapes
remain inconclusive. There is no `--collect`, runtime drop-in or host setting
change. Trusted controller commands alone receive the host manager connection.

With no descriptor profile selected, the finite private `private-inject`
reexec is run only inside this service. It
validates exact intent/root/subjects and opens only the owned synthetic regular
fixture and reviewed BPF subject for intentional inheritance. RLIMIT and unused
sparse FD checks precede modification of its own child table: synthetic FDs
64, 512, 768 and 1000, BPF FD 513, and stdin/stdout/stderr are separately
tracked, with metadata, digest and CLOEXEC checks. The extra synthetic slots
are deliberately sparse aliases of the owned fake file, not additional fixture
types. They must fit the actual RLIMIT_NOFILE and be vacant; no slot fallback
or limit raising occurs. No orchestrator FD mutation or final exec.Command
FD remapping occurs. The injector preserves every injected descriptor through
its self-reexec; it does not close fake files and attribute that to bubblewrap.

Before exec the injector publishes bounded owned evidence with its actual
INVOCATION_ID, PID/starttime, namespaces/cgroup, subject and descriptor identity.
It waits at most ten seconds for its exact owned release marker. The controller
independently binds systemd show, injector identity, pidfd, effective guards and
parent populated=1, persists that binding and release intent, then publishes
the marker. It is excluded from candidate mounts and closed before exec.
Injection and executable identities are rechecked before syscall.Exec of the
frozen driver as `private-final-descriptors <exact-attempt-path>`, with an empty
environment. Both direct and candidate roles use this identical injection and
wrapper path, still before the complete backend chain.
Failure means no backend exec; acknowledgement loss means launch-unknown,
without a retry. Paths remain subject to the trusted frozen-tree premise.

### Final descriptor policy

New Prepare always chooses `descriptor_policy:"final-fds-v1"`. The exact plan
digest includes that version, the wrapper `command`, separate `target_command`,
launch arguments, profile, selected/frozen subject hashes, streams and original
control bindings. There is no caller-configurable descriptor list or bypass
flag. With no descriptor profile selected, the verified durable role selects
this compiled policy (the opt-in typed table is above):

| Role | Preserved descriptors at target exec | Target |
| --- | --- | --- |
| `candidate` | Owned 0–2 and reviewed read-only BPF513 | Frozen bubblewrap, then probe |
| `direct` | Owned 0–2 and synthetic FD512; aliases denied | Frozen static probe |
| `weakened-synthetic` | Owned 0–2, BPF513 and all four synthetic descriptors | Same bubblewrap namespace/guard profile, then probe |

Without the typed selector, `weakened-synthetic` accepts only the owned
`fake-fd` and `inventory` fixtures,
rejects a direct-control reference, and cannot qualify a candidate as its direct
control. It deliberately exposes the fake capability to check the observation
oracle in the same binary/profile family. It is not an ordinary execution mode.

The intermediate mode requires the exact released durable intent and release
marker, matching injection evidence, campaign/attempt roots, boot, PID/start,
executable and namespace/cgroup identities, including the retained parent
boundary. It revalidates frozen static self, probe, bubblewrap and BPF hashes
and the target command. It receives no `INVOCATION_ID` environment variable:
that binding comes from the durable injector/release record and unchanged
process identity. Manager, bus and credential environment is never forwarded.

Before the terminal section it verifies the selected synthetic descriptor
kinds (regular aliases without the typed selector), and actual owned stdio
identities and flags. Stdin
must be the request file opened read-only; stdout/stderr must be their distinct
owned regular files opened write-only with append. All are non-CLOEXEC.
BPF513 must match the frozen singly linked regular subject, be read-only and
non-CLOEXEC, match its digest through bounded Pread and have offset zero.
Pread does not consume the shared offset. This ordinary frozen owned BPF file
does not require memfd seals. The trusted host must exclude concurrent writable
aliases and changes to its content, offset or status flags: table unsharing
does not isolate open-file-description state.

Arguments, the empty final environment, validation buffers and bounded evidence
are prepared before unsharing. The mode pins its goroutine with LockOSThread,
then issues raw Linux/amd64 syscall 436:
`close_range(3, UINT_MAX, CLOSE_RANGE_UNSHARE|CLOSE_RANGE_CLOEXEC)`, flags `2|4`.
On the private table it raw-revalidates identities, access modes and descriptor
flags, including stdio and every deliberate alias, plus BPF offset zero. It
clears CLOEXEC only for the compiled role exceptions, confirms the allowed
flags and every deliberate denied alias's CLOEXEC, then immediately calls
syscall.Exec once on the frozen target with the prepared empty environment.

The reviewed Go/Linux syscall.Exec internally allocates argv/environment
pointers, serializes exec against runtime thread creation and restores the
original RLIMIT_NOFILE if Go raised it at startup. Those operations open no
descriptors in the locked thread's private table. LockOSThread starts the Go
template thread before unsharing; creation from a locked M uses that template.
It does not stop existing threads. The frozen static driver and selected
toolchain are part of this concrete premise, not a general execution framework.

After the terminal block starts, any syscall, identity, flag or exec error
exits the entire wrapper with raw exit_group, status 125. There is no unlock,
ordinary return, proc-FD fallback, retry, callback, new file open or alternate
launch. A fixed raw stderr diagnostic has `final-fds stage=NN errno=NNNN`;
stages 01–08 mean close_range, identity, access, incoming flags, BPF offset,
exception clearing, final flags and exec respectively. Unsupported/denied
close_range blocks execution and remains unqualified.

`descriptor-policy.json`, later retained as `descriptor_receipt`, records
verified preflight and the planned role/command at `prepared-before-unshare`.
It cannot attest that close_range or exec succeeded. Only post-exec workload
observations establish closure facts. Hardened candidate `fake-fd` requires the
exact FD512 fstat EBADF observation. Direct/weakened `fake-fd` require recovery
of the expected synthetic digest. Run paired `inventory` cases as well: they
retain every matching fake/BPF dev/inode and distinguish absent numbers from
reused numbers in `descriptor_observation`. Candidate inventory must contain no
fake identity anywhere; direct inventory must retain only FD512; weakened
inventory must retain all four synthetic descriptors. Independently, BPF513
must be absent and its identity must appear nowhere in the workload inventory,
demonstrating bubblewrap consumption/closure rather than assuming it. Reused
512/513 remain explicit observations and cannot satisfy these closure checks.
Complete inventory requires the exact declared `fds.count`/row index set,
without duplicate, extra or unavailable FD-table facts, and descriptors 0–2
matching the durable owned request/output/stderr dev/inode identities and
regular-file types. Empty tables and missing or mismatched stdio cannot
establish exclusion. These checks validate consumed evidence; the production
probe already rejects enumeration read/stat/cap failures.
Incomplete inventory remains inconclusive. Failed exclusion observations are
retained durably after independently proved cleanup, never erased or relabeled.

An absent optional policy uses the original recorded command, injection
validator and omitted-field plan-digest encoding. Such records remain readable
for inspect and exact cleanup, including their original failed observations;
the new driver never launches or releases a legacy policy. New direct controls
must bind the current policy/driver/probe/BPF/profile and matching preflight
receipt. A legacy control cannot qualify a hardened candidate.

This barrier covers inherited descriptor capabilities at the target exec only.
Stdio remains deliberate authority. Cwd/root references, ancestor reopening,
descriptor transfer and new descriptors opened by the trusted launcher retain
separate gates, as does dynamic launcher dependency closure. A pre-exec flag
receipt or a single FD512 observation is not whole-capability qualification.

The concrete candidate arguments are retained verbatim: `--unshare-all`
plus explicit `--unshare-user`, UID/GID 0 in the private mapping,
`--die-with-parent --new-session --clearenv --cap-drop ALL`,
`--disable-userns --assert-userns-disabled --seccomp 513`, an empty tmpfs root,
declared `/fixtures`, `/dev`, `/tmp` directories, read-only probe and synthetic
file mounts, private proc, read-only root and `/` working directory. There is
no host root/home bind, host bus, credentials, listener or dynamic probe library
mount. Actual hold-helper namespace, privilege, mapping and mount state is
retained; requested flags alone do not prove application.

For candidate `fake-fd`, include `direct_control` naming a completed direct
`fake-fd` attempt in this campaign and run, on the same boot, at most 300 seconds
old at selection, with matching current descriptor policy and
profile/driver/probe/bwrap/BPF digests and valid injection/receipt/cleanup/direct
digest recovery. The candidate records the control's
plan digest, freshness/scope and a copy of its typed fresh synthetic nonce in a
new owned file; its own FD dev/inode/digest must still match its own injection.
Candidate EBADF is only a raw observation after these controls, never proof of
whole-descriptor alias exclusion. A changed identity remains inconclusive.

`--private-ready-hold` is a private probe mode for an inventory identity request,
isolated from the existing one-request/one-observation API. It emits a bounded
ready frame before EOF, waits for TERM/INT or a 1500 ms self-lifetime, then emits
one final frame (`cancelled` or `self-lifetime`). Both frames bind IDs and
executable/PID/starttime/PID-namespace identity. The ready frame proves no
liveness. The controller enumerates only the bounded exact owned service
cgroup, finds a unique static helper, matches its namespace PID and executable,
retains a pidfd and verifies it alive/contained immediately before cancellation.
MainPID is recorded separately: bubblewrap's monitor can differ from the helper.
Cancellation requires pidfd exit (including reaped-target HUP), same-parent
post-workload empty evidence, exact slice cleanup and matching ready/final
frames before reporting an observation. A finite self-exit cannot qualify cancel.

Restart preserves the original injector/target bindings. A previously bound
attempt with prior live-parent evidence can complete cleanup only on the same
boot, same retained parent invocation/dev/inode, no pending service job and
post-workload populated=0. Original PID/starttime exit, absence or reuse is
checked independently; an executable change alone is exec, not PID reuse.
Permission failures, missing/replaced parent, missing original binding or boot
change remain unknown. This path never fabricates workload/cancellation output.
After exact cleanup, final frozen output is read through the existing flat
collector. Collection requires the independent process-exit and complete
boundary-empty proof; a successful manager result alone cannot reach it.

Deterministic checks cover acknowledgement loss, foreign invocation/cgroup/
process identities, launchpartial, cleanupfailure, missing/collected services,
removed cgroups, pending jobs, restart fencing/recovery, FD collisions, stale
readiness, malformed records and sync/rename crash windows. Owned-child checks
also exercise allowed descriptor roles, sparse aliases, private-table sibling
mutation, preflight identity/access/offset failures and whole-process terminal
failures. They establish implementation behavior, not actual backend isolation.

The lead's pre-change selected bubblewrap chain actually preserved injected
regular FD512 and the workload recovered its expected synthetic digest after
a valid direct control. Cleanup still worked. This remains a **failed isolation
observation** for that exact chain. Separately, direct/candidate hold, inventory,
fixture visibility and two controller-crash reconciliation paths actually ran
successfully; none reverses the descriptor-exclusion failure. Those are retained
lead-run facts, not executions by a source reviewer or this implementation's
worker. The new binary still requires independent source review and lead-run
direct/candidate/weakened, BPF consumption, hold and crash-recovery checks against
the actual campaign before any new selected-host conclusion. Host property
formats and launcher dependency closure retain their separate gates.

## Explicit invocation

The operator supplies exactly one JSON request on stdin and closes the stream.
One newline-terminated JSON observation is written to stdout. Example using
only a freshly created owned synthetic fixture directory:

```sh
fixture_root=$(mktemp -d)
printf 'synthetic fixture\n' > "$fixture_root/fake-file"
printf '%s\n' '{"version":1,"run_id":"0123456789abcdef","request_id":"fedcba9876543210","case":"fake-file","payload":{"file":{"name":"fake-file"}},"max_duration_ms":1000,"max_bytes":4096}' \
    | build/qualification-probe --fixture-root "$fixture_root"
rm "$fixture_root/fake-file"
rmdir "$fixture_root"
```

`--fixture-root` is an explicit absolute owned frozen directory, required for
`fake-file` and `fake-unix`. No implicit fixture path is chosen. It is trusted
launcher configuration, never a JSON path. The launcher must exclude real
credentials and real services from this root. Fixture paths, identities and
descriptor lifetimes must remain stable throughout the operation. A static
root-confinement API alone cannot establish those premises.

For `fake-unix`, the trusted caller must independently freeze the **complete
fixture pathname**, all pathname ancestors, the mount mapping and every writable
alias for the entire call. Metadata uses the pinned `os.Root`, but `net.Dial`
resolves the original absolute pathname again. Root alone does not confine Dial.
The joined Linux pathname must contain no NUL and fit in 107 bytes (UTF-8
bytes, not characters); longer paths are rejected before any fixture access.
Nonce validation follows connection and cannot reverse exposure to a substituted
endpoint. Mutable arbitrary host paths remain unsupported; this tranche adds no
path-freezing mechanism or backend enforcement of this trusted caller premise.

`inventory` and `fake-fd` do not need a fixture root. Run inventory only as an
explicit opt-in operation: it reads the limited own-process kernel metadata
listed below. No CLI invocation is an isolation assertion. Flags accept no
positional arguments; invalid flags, including help requests, produce the
same bounded error envelope. The CLI does not echo argument or request data.

Exit 0 means a valid observation was emitted, including inconclusive and
unsupported observations. Exit 2 means invalid input/configuration. Exit 1
means output failure. A failed writer can leave a partial frame; it is never
reported as successful emission, and a second frame is never attempted.
The host driver supplies a finite regular request file and bounded output
files under a finite service guard and requires cleanup before final collection.
Other callers must bound/drain I/O, supply EOF and apply an independent outer
watchdog; stdin byte limits do not time-bound a stalled input stream.

## Version 1 wire contract

Request fields `version`, `run_id`, `request_id`, `case`, `max_duration_ms`
and `max_bytes` are required. Version is 1; IDs and Unix nonces are 8–32
lowercase hexadecimal bytes. IDs and case names are echoed unchanged only
after validation. Duration is 1–2000 milliseconds; bytes is 1–4096. Duration
bounds Unix connect/exchange, while bytes bounds fake-file size, fake-FD reads
and Unix frame size. These limits do not replace a supervisor lifetime bound.

The payload must match exactly:

| Case | Payload | Operation |
| --- | --- | --- |
| `inventory` | absent | Passive capabilities, environment presence, namespace and network/FD metadata |
| `fake-file` | `{"file":{"name":"fake-file"}}` | Read-only open/stat/close; no content read |
| `fake-unix` | `{"unix":{"name":"fake-socket","device":1,"inode":2,"uid":1000,"nonce":"01234567"}}` | Owned pathname stream fixture with expected metadata and typed nonce echo |
| `fake-fd` | `{"fd":{"name":"fake-file","fd":512,"device":1,"inode":2,"regular":true,"sha256":"<64 lowercase hex bytes>"}}` | Identity/type check before bounded offset-zero Pread and digest comparison |
| `fake-dir-fd` | `typed_fd` challenge described below, with `fd:128` and `leaf` | Validate directory and leaf identities, open one flat leaf and compare its synthetic digest |
| `fake-pipe-fd` | `typed_fd` challenge, with `fd:256` | Validate pipe identity/flags, read the fixed nonce frame and require EOF |
| `fake-socket-fd` | `typed_fd` challenge, with `fd:384` | Validate Unix stream identity/flags/type, read the fixed nonce frame and require EOF |
| Future matrix ID | absent | Explicit unsupported observation with reason |

The numeric identities in this table are illustrative. Supply identities of
the actual owned synthetic fixtures. Device, inode and UID fields may be zero
but cannot be missing. FD is required, between 3 and 1048575 inclusive, and
`regular` must explicitly be true. Descriptor zero, stdout and stderr are
never read. Identity mismatches, nonregular FDs and excessive sizes prevent
Pread. The FD operation does not change the shared file offset or close the
caller-owned FD. It emits only identity/digest comparisons, never bytes or
an observed digest. The host driver's separate injection/direct-control path
does not change this raw observation API.

`UnixPayload.UID` is the expected inode owner **and** listener credential in the
probe's user-namespace projection. Any trusted Unix-fixture driver must derive it from
verified UID mappings. A host UID or zero is never a fallback for missing mapping
evidence. Explicit zero is valid only when verified in that projection. This
single field covers only the deliberate same-owner inode/listener fixture with
no idmapped ownership shift. Differently owned or idmapped endpoints remain
unsupported. UID presence/range and synthetic comparison tests do not qualify
actual namespace mapping. The current host command does not launch Unix cases.

Names are 1–64 ASCII letters, digits, dots, underscores or hyphens, cannot
start with a dot or contain `..`, and are flat: no path separators, absolute
paths, traversal or proc aliases. Fake-file rejects links, nonregular files
and files with multiple hardlinks, reports device/inode/size after opening,
and leaves contents private.

Fake-Unix checks the visible endpoint's socket type, device, inode and owner
before connecting, then peer UID and unchanged endpoint metadata before
exchange. It sends `ataniq-fixture-v1 <nonce>\n` and compares one fixed-length
reply with that exact frame, followed by EOF, under the same bounded deadline.
The listener must call `CloseWrite` or close after its reply; it need not keep
the connection open. A listener may keep its read half open after `CloseWrite`.
Trailing bytes record `nonce_match:"false"` at `nonce-frame`. A matching prefix
without EOF remains inconclusive at `nonce-eof`, with no known nonce result.
This is a fresh owned fake
stream fixture only: no arbitrary address, abstract name, datagram, real bus,
command or ancillary-FD protocol. A missing path retains numeric errno and
the exact `lstat` stage; it says nothing about a connection attempt. A working
outside positive control and listener receipts
remain responsibilities of a future Unix-fixture driver. No connection result alone
qualifies isolation or authenticates a product service.

Input and output are independently capped at 65536 bytes including the output
newline. Decoders read at most cap+1 and reject an oversized valid prefix,
unknown or duplicate keys, trailing values/junk, nulls, invalid UTF-8 and
nesting beyond eight levels. Structural keys must be canonical lowercase
ASCII letters/digits/underscore, so ASCII case aliases and Unicode SimpleFold
aliases (including escaped long-s) cannot reach Go struct matching. There
must be exactly one JSON object. Errors use fixed bounded descriptions,
never excerpts from rejected data. A rejected request receives version 1,
empty run/request/case identifiers, `status:"error"` and an empty fact array;
this reserved envelope is explicitly uncorrelated.

An observation has version/IDs/case, `status`, required `facts` array,
optional bounded `reason` and optional positive Linux `errno` (1–4095).
Statuses have these meanings:

| Status | Meaning |
| --- | --- |
| `observed` | Completed stage with known observations, including syscall errno; comparisons can be false |
| `inconclusive` | Missing, unavailable, excessive, unstable or incomplete metadata/fixture/operation |
| `unsupported` | Recognized future case; no operation implemented |
| `error` | Invalid request/configuration; no usable case result |

Core-operation failures include a known `stage` fact naming the exact failed
stage. A completed syscall returning a known errno is `observed`, including
denial at `lstat`, `open`, `connect`, initial FD `fstat` or `pread`. For example,
`stage:"lstat"` with ENOENT records only the failed metadata lookup; it never
implies `open` or `connect` was attempted. A visible owned socket whose connect
returns ECONNREFUSED records `stage:"connect"`. A connect errno is promoted
only from a wrapped `os.SyscallError` naming `connect`, after the joined Linux
pathname passed validation; Go can otherwise label a pre-syscall sockaddr
encoding EINVAL as `connect`. Neither observation establishes protection.
An independently reviewed qualification decision combines valid fixtures,
applied state and paired working controls. Missing fixtures cannot qualify
isolation even when their failed lookup is a complete raw observation.

An unavailable `fixture-root` or failed directory pin at `open-dir` remains
inconclusive, retaining any known errno. In the latter case the target `openat`
was never attempted. Unix socket creation failures record inconclusive `socket`;
known setup failures record `setsockopt`, `bind`, `getsockopt`, `getsockname` or
`fcntl`. Unattributed dial failures use inconclusive `dial-setup` and do not
claim a connect attempt. Raw EAGAIN/EWOULDBLOCK remains a syscall errno, not a
deadline timeout; actual deadline expiry remains inconclusive.

A rejected `validate-file`, `file-identity`,
`fd-identity`, `fd-size`, `postread-fstat`/`postread-identity`, or
`socket-identity` check remains inconclusive. Post-open `fstat`/`close` failures
retain uncertainty about completion. Unix failures after connection identify
`deadline`, `syscall-conn`, `peer-control`, `peer-credentials`, `peer-identity`,
`endpoint-lstat`, `endpoint-identity`, `nonce-write`, `nonce-read`,
`nonce-eof`, `nonce-frame`, `nonce-compare` or `close` as applicable.
Incomplete EOF, short-write, timeout or
mismatched nonce exchanges remain inconclusive and retain any known errno.
An insufficient nonce-frame byte budget reports `error` at `nonce-size` before
any fixture access. An invalid or overlong joined pathname reports `error` at
`socket-path`, also before any fixture access, with no fabricated errno.

`opened:"true"` is recorded immediately after successful file open, and
`connected:"true"` immediately after successful Unix dial. All later failures
preserve these effects, including failed validation, close or nonce exchange.
A full mismatched reply records `nonce_match:"false"` with the inconclusive
`nonce-compare` stage. An incomplete reply does not imply a known nonce result.
Absent effect facts never mean false. The generic unknown `operation` fact is
replaced by exact failure stages; no workload qualification flag is added.

There are at most 256 uniquely named facts. Fact names are case-sensitive
ASCII names up to 64 bytes. Each fact has `state:"known"` and an explicit
`value` string, or `state:"unknown"`/`"missing"` with no value. Empty, false
and zero are explicit known strings; absent data never becomes one of them.
Unknown/missing facts require an inconclusive status. Values allow bounded
UTF-8 text without controls, up to 1024 bytes; reasons allow 256 bytes and
are mandatory for non-observed statuses. Encode validates and marshals the
whole frame before any write and detects nil-error short writes.

## Fixed typed inherited observations

These three raw cases supply fixed typed operation observations. The host
command prepares and dispatches them only with the explicit `fd1-typed`
configuration selector described above. Historical absent-profile records and
the regular `fake-fd` request API retain their encoding and hash meanings.
`FD1` itself still returns `unsupported`; no command or flag is added.

Each new request has exactly one `payload.typed_fd` member, mutually exclusive
with `file`, `unix` and `fd`. A pipe request, for example, is:

```json
{"version":1,"run_id":"01234567","request_id":"89abcdef","case":"fake-pipe-fd","payload":{"typed_fd":{"name":"inherited-bindings.json","fd":256,"profile":"fd1-typed","attempt_id":"aabbccdd","nonce":"01234567"}},"max_duration_ms":100,"max_bytes":4096}
```

The typed payload requires exactly `name`, `fd`, `profile`, `attempt_id` and
`nonce`, plus `leaf` for `fake-dir-fd` only. `name` must be
`inherited-bindings.json`; `profile` must be `fd1-typed`; `fd` must match the
selected case's fixed slot. Attempt IDs and nonces use the same 8–32 lowercase
hexadecimal character bound as run/request IDs. Channel frames are derived
from the immutable case and nonce:

```text
ataniq-fd1-pipe-v1 <nonce>\n
ataniq-fd1-unix-v1 <nonce>\n
```

Here `\n` represents one newline byte. Frames are type-distinct, shorter than
128 bytes, and must fit `max_bytes` before fixture access. There is no arbitrary
frame, descriptor list, kind selector, transport, path, peer or executable.
The directory request's `leaf` has exactly `name`, `device`, `inode`, `size`
and `sha256`: one flat synthetic filename and its known expected regular-file
identity, length and content hash. Its size is 0–`max_bytes`; the hash is exactly
64 lowercase hexadecimal characters. The trusted launcher must keep descriptor
ownership/lifetime and the complete synthetic fixture tree stable for the call.

### Frozen runtime binding artifact

The artifact is trusted fixture metadata, read only through the explicit
absolute `--fixture-root`. The typed host launcher mounts it read-only
only for this profile, with trusted-side authentication before release. It is
neither a broker nor a configuration allowlist. It cannot establish its own
provenance. The directory capability and its leaf remain unmounted in hardened
candidates; direct and weakened controls expose that tree through descriptor
128 only. Existing frozen-tree and trusted-launcher premises still apply.

`FD1Bindings` and `DecodeFD1Bindings` define the strict version-1 artifact.
It is at most 8192 bytes and contains exactly these required top-level fields:
`version`, `run_id`, `request_id`, `attempt_id`, `profile`, `plan_sha256`,
`nonce`, `directory`, `pipe`, `socket`, `regular`. Version is 1; the profile is
`fd1-typed`; IDs/nonce have the bounds above; the immutable plan hash has exactly
64 lowercase hexadecimal characters. The four named records are mandatory.

Every record has exactly these common fields: `fd`, `device`, `inode`, `type`,
`access`, `nonblock`, plus the additional fields in this table:

| Record | FD | Type | Access | Nonblock | Additional required fields |
| --- | --- | --- | --- | --- | --- |
| `directory` | 128 | `directory` | `read-only` | false | `leaf` object, with the five fields defined above |
| `pipe` | 256 | `pipe` | `read-only` | true | `length`, `sha256`, `peer_closed:true` |
| `socket` | 384 | `unix-stream` | `read-write` | true | `length`, `sha256`, `peer_closed:true` |
| `regular` | 512 | `regular` | `read-only` | false | `size`, `sha256` |

Descriptor 513 is reserved for BPF by the fixed profile and has no artifact
record. Artifact regular/leaf sizes are 0–4096; sizes and boolean fields must be
explicit even when zero or false. All device/inode identities, including the
leaf's, are required and nonzero; zero is never an unknown-identity sentinel.
Channel lengths must be positive, below 128, and exactly equal to their fixed
nonce frame lengths; their hashes must equal those frames' SHA-256. Regular
size/hash describe the synthetic regular fixture bound by the trusted side.
The artifact uses the wire decoder's canonical ASCII keys, strict field types,
depth bound and rejection of duplicate, unknown or null keys, extra values,
invalid UTF-8 and excessive input. Missing fixed records and conflicting fixed
slots/types/access/nonblock or impossible lengths are rejected.

The probe shares the collector's bounded frozen-file reader: lstat,
regular/single-link/size checks,
pinned flat no-follow/nonblocking open, exact opened identity, cap+1 read,
short-read and stable post-read checks. The launcher must establish that this
trusted metadata file is frozen outside workload write reach. The running probe
does not assert workload-stop proof; the separate `CollectFlat` API still
requires that proof. Run/request/attempt IDs, profile and nonce must
match the immutable request before any capability fstat/open/read. For the
directory case every expected leaf field must also match. Artifact ownership
is authenticated on the trusted side; the namespace probe matches global
device/inode/type without comparing host UID to projected UID0. Existing
idmapped-owner admission remains unsupported.

The service-local injector creates exact channel identities after
request/plan freezing, publishes the artifact and its hash/identity in its
receipt, and waits while the controller durably binds them before release.
The trusted side must verify `plan_sha256` against the launched plan. That hash
is deliberately absent from stdin: the existing plan hashes `request.json`,
so including the plan hash in that request would be circular. Neither stdin
nor an already launched plan may be rewritten or rehashed. The probe never
learns an expected identity from its own fstat. Runtime artifact generation,
receipt authentication, read-only mounting, wrapper revalidation and release
are host integration mechanisms; they do not assert successful host execution.

### Operations and interpretation

After binding validation each case first fstats its fixed slot and compares
exact expected device/inode/type. A closed slot records `stage:"fstat"` with
exact EBADF. A reused or wrong slot is inconclusive at `fd-identity` and stops
before openat or receive. An unavailable/malformed/context-mismatched artifact
is inconclusive at `binding-read`, `binding-validate` or `binding-context`,
before any capability access. Missing artifact evidence cannot establish closure.

The directory case also checks read-only flags, then performs no-follow
relative metadata lookup on the one flat leaf. A rejected leaf is never
opened. It uses only
`openat(128, leaf, O_RDONLY|O_CLOEXEC|O_NOFOLLOW|O_NONBLOCK)`, records
`opened:"true"` immediately, and requires the opened file to retain the expected
identity, regular type, single link and bounded exact size before offset-zero
Pread. It compares the synthetic hash, checks stable post-read metadata and
closes the new descriptor. It neither enumerates entries nor mutates the source.
Post-open failures preserve the open fact. `leaf-lstat`, `leaf-identity`,
`fstat-leaf`, `pread`, `postread-fstat`, `postread-identity`, `leaf-compare`
and `close` distinguish validation/read/close failures from `openat` errno.

Channel cases validate actual F_GETFL access and O_NONBLOCK, rejecting O_PATH
or missing flags without repair. Socket metadata must identify AF_UNIX and
SOCK_STREAM after identity validation. The injector prewrites exactly
one frame and close every writer/peer before publication and release. The probe
only calls raw nonblocking read; it never uses Pread/Seek, a blocking wrapper,
writes, recvmsg or ancillary parsing. It requires exact length/hash followed by
EOF, using at most expected length+1 storage, at most 128 read syscalls including
EINTR, and `max_duration_ms` checked before and after each read. A successful
byte read records `read_success:"true"` immediately. A matching frame plus EOF
records `eof:"true"` and `frame_match:"true"`; later fstat or flag failure
retains those known facts. Leaf reads likewise retain successful byte-read
evidence through later failure. Payload bytes and observed digests are never
emitted; absent effects never mean false.

EAGAIN (including a kept-open producer), short/empty/wrong/trailing frames,
missing nonblock, unsupported reads, metadata drift, deadlines and retry budget
exhaustion are inconclusive, never closure. Stages distinguish `fd-flags`,
`socket-type`, `channel-read`, `channel-frame`, `channel-deadline`,
`channel-budget`, `postread-fstat`, `postread-identity` and `postread-flags`.
The workload retains the existing observation envelope with no support/pass
field. Socket metadata unavailable under a host syscall policy is inconclusive;
owned-child tests explicitly skip full admission there and separately exercise
raw socket reception and deterministic domain/type validation.

These observations concern fixed regular/directory/pipe/Unix-stream inherited
capability coverage. Live RPC, bidirectional write protocols, Unix connect
authorization and SCM_RIGHTS remain separate FD2/US work. No direct/candidate/
weakened/inventory FD1 evidence bundle, launcher qualification, live sandbox
execution is claimed by these tests. Runtime-binding generation is implemented;
full socket-admitted child coverage and the live matrix remain separate gates.

## Inventory and evidence limits

Inventory reads only `/proc/self/status`, `/proc/self/net/dev`,
`/proc/self/net/route`, `/proc/self/net/ipv6_route`, own `pid`, `mnt` and `net`
namespace identities under `/proc/self/ns`, and bounded directory entries
plus fstat metadata under `/proc/self/fd`. There is no recursive proc/sys
traversal or descriptor-content read in inventory.

All five capability sets (`CapInh`, `CapPrm`, `CapEff`, `CapBnd`, `CapAmb`)
are explicit. Missing fields are missing/inconclusive; read failures or
malformed/duplicate fields are unknown/inconclusive. Namespace facts contain
device/inode identities. Environment facts contain presence booleans only
(an empty value is still present): `HOME`, `XDG_RUNTIME_DIR`,
`DBUS_SESSION_BUS_ADDRESS`, `SSH_AUTH_SOCK`, `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` and
`ATANIQ_FIXTURE_MARKER`. Values are never emitted.

Interface names and IPv4/IPv6 route fields have explicit counts. A valid
header-only IPv4 table is known empty; missing/malformed/empty unframed
metadata is conservatively unknown. Each metadata file is capped at 32768
bytes, lines at 1024 bytes and each network table at 24 entries. FD inventory
is capped at 128 directory entries, not at a maximum FD number: sparse high
numbers are retained. Concurrent FD disappearance makes that inventory
unknown. Bounds never silently truncate a table. An observation exceeding
the whole-frame limit is replaced with an inconclusive bounded inventory
fact. This replacement applies only to inventory and cannot erase successful
open/connect facts from other cases. FD inventory includes descriptors created
by the probe/runtime; its count is not an inherited-descriptor count. Capabilities
and namespaces under `/proc/self` describe the thread-group leader, not a future
locked-thread case. The trusted driver must compare namespace identities, interfaces and
both route families against its declared profile; no networking decision is
made by the workload.

`CollectFlat(root, name, limit, externallyStopped)` is a separate trusted-side
reader, not a probe command. Before collection the caller must independently
prove the exact workload empty and the fresh owned tree frozen outside its
reach with no writers. Passing `true` asserts this prerequisite; it cannot
prove termination, and passing `false` is rejected. Unit tests establish
owned frozen fixtures, not actual workload stop proof.

The reader accepts only flat names, lstat-checks regular type/single link/size
before opening, pins the directory through `os.Root.OpenFile`, then uses
Linux `openat` with `O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC` for the final component.
This corrects an experimentally observed proposal assumption: the selected
Go `Root.OpenFile` follows an in-root symlink despite `O_NOFOLLOW`; an
escaping final symlink is rejected by Root. The direct flat open rejects
both. Selected-toolchain tests check those behaviors and FIFO nonblocking
open, plus optional owned sockets. Regular positives, exact/over cap,
directories, links and absent stop assertion are covered deterministically.

Opened fstat identity/type/link count is checked against lstat before reading.
An independent cap+1 read and postread identity/size/time/link checks reject
changes; no partial evidence is returned. Limit is 1–65536 bytes. Collection
remains fail-closed if the directory pin fails before the target open. These
checks do not make concurrent mutable sources safe, admit arbitrary workspaces,
establish copied-content provenance or prevent effects of a device swapped
by a concurrent writer. The frozen owned-tree premise is mandatory.

## Opt-in fixed R1 actor protocol

The probe has a separate `--r1-memory` mode for the fixed
`r1-memory-fixed-v1` experiment. It emits raw evidence and has no qualification
result. Ordinary probe invocation, legacy request/hold/FD1 encodings and host
cases retain their existing behavior. `HostConfig` continues to reject `R1`,
`r1-memory` and the actor profile name as host cases. The separate fixed
`r1-entry` host case below admits only no-pressure child entry. Neither mode
establishes runtime support.
The mode cannot be combined with `--fixture-root`, `--private-ready-hold` or
`--private-r1-child`. There are no command, control-path, size or lifetime flags.
The private child mode is exclusively for fixed self-exec with two private
pipes; it is not a host inherited-FD profile.

The main actor expects sandbox entry already complete and one control directory
mounted read-only at `/r1-control`. It reads only `context.json`, `release.json`
and `finish.json`. It never writes or repairs these files, changes a mount,
sets a resource limit, touches cgroups or launches a service. The final wrapper's
existing entry release does not authorize memory allocation. The later exact
`release.json` record is required before any pressure allocation.

### Strict fixed records and byte hashes

Every R1 input and evidence frame is at most 4096 bytes. Separate R1 types use
the shared strict JSON rules: canonical ASCII keys, no unknown/duplicate/null
fields, invalid UTF-8, trailing value or excessive nesting. Every required
field must be present, including zero/false fields. IDs and challenge contain
8–32 lowercase hexadecimal characters; hashes contain exactly 64. Version is
always 1. There is no caller-selected bound or arbitrary dispatcher.

Immutable stdin has exactly these fields, illustrated here without prescribing
its serialization or whitespace:

```json
{"version":1,"run_id":"01234567","request_id":"89abcdef","attempt_id":"aabbccdd","profile":"r1-memory-fixed-v1","challenge":"1234abcd"}
```

The actor hashes the exact validated stdin bytes, including any whitespace.
It never reserializes stdin for hashing or adds `plan_sha256` to it.
`context.json` contains those six request fields plus `request_sha256`,
`plan_sha256` and `control_directory:{"device":...,"inode":...}`. Device and
inode must be nonzero. Context has no `context_sha256` field. Its byte hash is
computed only after validation. The future host must freeze directory identity,
mount target/schema and challenge in its plan, then publish context after plan
hashing and authenticate its exact subject/hash in a late runtime receipt. This
avoids a request/plan/context hash cycle; workload echoes cannot authenticate it.

Every post-context record shares the fixed `R1Binding`: all six request fields,
`request_sha256`, `plan_sha256` and `context_sha256`. Release adds
`action:"release"`, `expected_stage:"ready"`, `sequence:1`. Finish adds
`release_sha256`, `action:"finish"`, `expected_stage:"retained"`, `sequence:2`.
All common fields and consumed byte hashes must match exactly. Mutable sequence
numbers cannot select a command or a different experiment.

Only the main writes stdout, as append-only newline-delimited frames. Each
`R1Evidence` contains the common binding, `stage`, `sequence`, and exactly one
payload from this table:

| Stage / sequence | Payload | Required observations |
| --- | --- | --- |
| `ready` / 0 | `ready` | `control_directory`, `main`, `child` |
| `pressure-start` / 1 | `pressure` | `release_sha256`, before allocation or touch |
| `retained` / 2 | `touched` | `release_sha256`, `requested_bytes:67108864`, actual `touched_bytes`, `page_size`, `retained:true` |
| `completion` / 3 | `completion` | `release_sha256`, `finish_sha256`, `touched_bytes`, `retained_until_finish:true`, `child`, `outcome:"completed"` |
| `failure` / interrupted sequence | `failure` | Interrupted `stage`, fixed `error`, actual `touched_bytes`/`page_size`, `cleanup`, `child`; release/finish hashes when consumed |

Failure sequences describe phases, not output frame counts: 0 covers `context`,
`child-ready`, `ready`; 1 covers `release`, `pressure-start`, `allocate`, `touch`;
2 covers `retained`, `finish`, `cleanup`; 3 covers `child-reap`, `completion`.
Both encoding and decoding reject contradictions. Release failures may lack a
release hash while waiting or rejecting a record, but `pressure-start` and later
require the consumed release hash. No earlier phase can contain it. Finish
failures may lack a finish hash while waiting or rejecting a record; `cleanup`
and later require it. A finish hash requires a release hash and cannot precede
the finish phase. Positive touched bytes require valid page size/alignment and
cleanup other than `not-allocated`. Touch failures can report partial accounting
at sequence 1; `retained` and later require full touch. Zero-touch allocation
errors and failed cleanup remain valid evidence; the codec never infers cleanup
success from accounting or phase.

Actor identities contain namespace-local `pid`, `start`, and `executable` and
`pid_namespace` device/inode objects. Main independently validates its owned
child's handshake against proc metadata and requires the same executable/PID
namespace and a distinct PID. These local identities do not authorize host
termination or establish actual cgroup membership. If independent metadata
lookup fails after a valid handshake, readiness returns no public child identity.
The exact spawned process handle/PID stays owned for bounded cleanup; a later
independent observation may corroborate identity, but the handshake alone cannot.
An already ended child can still have an observed reap outcome with unknown
identity. Neither case permits readiness or completion.

Child results preserve identity when available, `token` (`none`, `finish` or
`abort`), `termination` (`unknown`, `exited` or `signaled`) and `reap` (`unknown`
or `reaped`). An observed normal exit has `exit_code`; a signal has `signal`.
Unknown status never invents an exit code. `reaped` describes an already returned
wait status for the exact owned child. Completion requires its validated identity,
an observed exit code 0 following finish, and completed cleanup. It never
promises a future reap. Private child exit codes distinguish finish (0), abort
(20), lifetime (21), parent-pipe EOF (22), protocol/I/O failure (23) and unsupported
identity metadata (24). Unsupported proc metadata is explicit failure evidence.

Before context validation, failures instead use the uncorrelated
`R1StartupFailure` frame: version 1, `kind:"r1-startup-failure"`, request/context
`stage`, fixed `error` and directory `cleanup` (`not-opened`, `closed`, `failed`);
rejected IDs are never echoed. Later failure labels
distinguish deadlines, control/binding errors, early finish, child or unsupported
metadata, I/O, allocation, touch and cleanup. `cleanup` is `not-allocated`,
`released` or `failed`. Output failure can leave a partial last frame; earlier
observations remain evidence, and an incomplete stream cannot establish success.

### Pinned reads, phase order and bounds

The actor pins the nofollow directory once, checks its visible identity against
the pin, and retains the initial context subject and exact bytes. Fixed flat
reads require nofollow/nonblocking open, a regular singly linked bounded file,
before/open/post-read/visible metadata agreement, an independent cap and exact
length. Context and consumed release/finish subjects/bytes are rechecked before
later transitions. Directory replacement, subject replacement, content drift,
wrong IDs/challenge/hash or any malformed record fails. Only an absent initial
lookup of release/finish in its expected stage is a bounded wait. Lost directories
or files that disappear after lookup are errors. Temporary names are ignored.
An early release is stale; finish visible before retained evidence is rejected.

The fixed child starts before readiness, with empty environment, no shell,
null standard streams, no parent-death kill and two private CLOEXEC pipes
created by the actor after sandbox entry. Only its two assigned endpoints pass
through self-exec. It reads no host-control files and allocates no pressure
memory. Both actors must be ready before release; there is no later child spawn.
Only exact private finish/abort tokens cause normal protocol termination.
EOF means parent lost and retains the child's independent lifetime; main death
cannot immediately look like kernel-group termination. Failure cleanup may send
abort, which is retained as intervention evidence.

Go `exec` implements the child's nil standard streams by opening `/dev/null`.
The legacy bubblewrap layout creates an empty `/dev`. The entry-only host case
adds exactly `--dev-bind /dev/null /dev/null`, with trusted character-device
metadata (`S_IFCHR`, device number 1:3), selected endpoint and mount readback.
The source bind and its actual observability still require independent host
qualification. Ordinary profiles retain their empty `/dev` layout.

After exact release, main emits pressure-start and maps exactly 64 MiB with
anonymous private mmap. It writes a nonzero byte at the actual OS page stride,
counting bytes covered by successfully touched pages. There is no mlock,
hugepage request, filesystem payload or large Go heap buffer. Partial touch and
deadline failures retain actual accounting. The mapping stays live until exact
finish or an explicit failure cleanup path. Successful touch/retention/completion
is valid raw protocol evidence even under a low limit; a later host oracle must
classify such survival as inconclusive, not reject its wire encoding.

| Fixed bound | Duration |
| --- | --- |
| Main overall monotonic lifetime | 8 seconds |
| Child independent lifetime | 10 seconds |
| Readiness, including stdin/context/child handshake | At most 2 seconds |
| Release wait | At most 2 seconds |
| Allocation/touch/retained emission | At most 1 second |
| Finish wait | At most 2 seconds |
| Child termination/reap, including failure retry | At most 500 milliseconds |

Every main stage additionally uses the one overall monotonic deadline. Retry
loops have bounded waits and call/byte budgets. Raw pipe
descriptors are cached before nonblocking setup: repeated Go `File.Fd()` calls
can restore blocking mode. A stalled-handshake regression checks that private
pipe reads time out and abort/reap remains observable. Deadline checks cannot forcibly
eliminate scheduler delays or blocking regular-file/kernel syscalls. Future
host unit guards remain necessary. A cleanup deadline or syscall failure leaves
failed/unknown outcomes; it cannot create completion or a support decision.

Tests substitute allocator, clock, control reader, process and I/O seams. They
exercise accounting/deadline composition without mapping the 64 MiB path or
invoking the actual main actor CLI. A bounded owned child test exercises only
the real private handshake and reap, and reports unsupported metadata explicitly.
Actual write-denial attacks, visibility of later inert control publication,
kernel pressure counters and independent retention/heartbeat/oracle evidence
remain separate qualification gates. The entry controller below supplies source
integration and fake tests for host identities, selected endpoints, publication,
safety readbacks and cleanup. These tests and static builds execute no host
campaign and establish no runtime enforcement claim.

## Fixed no-pressure R1 child entry

This first host deliverable is based on feature source `244fc1f`. Its fixed
profile is `r1-entry-no-pressure-v1`; it uses the existing actor request
`r1-memory-fixed-v1`. In an otherwise normal host configuration set
`"case":"r1-entry"`, `"role":"candidate"` and add an exact
`"busctl":"/usr/bin/busctl"` subject. The path is illustrative. This selected
public manager helper is a coordinator qualification input, not a developer
prerequisite. `descriptor_profile` and `direct_control` must be absent.
There are no command, memory, mount, lifetime or pressure-enable selectors.
Use the normal `qualify run --allow-host-operations --config ...`, `inspect`
and cleanup-only `reconcile --allow-host-operations` entrypoints.

The controller creates a fresh retained aggregate at the user-manager root,
outside `app.slice`, through one pinned public `StartTransientUnit` call. It
requires `StopWhenUnneeded=no`, 256 MiB memory, no swap, 64 tasks and one CPU.
Its only Tool service has 96 MiB memory, no swap, 32 tasks, one CPU,
`Restart=no`, `Delegate=no`, the existing finite FD/output limits, a 30-second
runtime guard and a 3-second stop guard. Direct kernel readbacks and conservative
ancestor/global headroom checks are mandatory. Entry requires **no applicable
configured oomd kill policy observed** on the actual controller and target
ancestry. A stable active oomd with only a sibling `app.slice` configured monitor
can qualify. The controller changes no host policy. These are safety guards,
not evidence of resource enforcement or successful weak pressure retention.

The normal qualifier itself must already be the main process of a fresh user
transient service, a direct child of the real user-manager root. A controller in
`app.slice` refuses this profile. The fixed controller contract is `Type=exec`,
`Slice=-.slice`, `Restart=no`, `Delegate=no`, `OOMPolicy=continue`, 512 MiB memory,
unlimited MemoryHigh, zero swap, 128 tasks, one CPU, a 90-second runtime and a
5-second stop timeout. It also requires `KillMode=control-group`, `SendSIGKILL=yes`
and `FinalKillSignal=9` (SIGKILL), explicitly launched and read back so the stop
bound covers remaining helper children. Actual kernel caps and
`memory.oom.group=0` are checked.
The controller cap must still leave at least 384 MiB of observed headroom after
normal use; it does not authorize pressure allocation. The controller is separate
from, and outside, the fresh 256 MiB workload aggregate.

After source acceptance, an operator can launch the normal CLI as follows. These
paths are examples; use the selected pinned executable and an existing owned
configuration. The unit name must be fresh. The command has this exact argument
order; command arguments containing whitespace or systemctl escaping are
unsupported by this narrow observer.

```sh
controller_id=$(cat /proc/sys/kernel/random/uuid)
runtime=/run/user/$(id -u)
env XDG_RUNTIME_DIR="$runtime" DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime/bus" \
  /usr/bin/systemd-run --user --unit="aq-entry-controller-$controller_id.service" \
  --slice=-.slice --service-type=exec \
  --property=RuntimeMaxSec=90s --property=TimeoutStopSec=5s \
  --property=KillMode=control-group --property=SendSIGKILL=yes --property=FinalKillSignal=9 \
  --property=Restart=no --property=Delegate=no --property=OOMPolicy=continue \
  --property=MemoryMax=536870912 --property=MemoryHigh=infinity \
  --property=MemorySwapMax=0 --property=TasksMax=128 --property=CPUQuota=100% \
  /path/to/qualification-qualify run --allow-host-operations --config /path/to/entry.json
```

Entry-specific manager operations explicitly use `/run/user/UID/bus`, including
cleanup; an inherited private session bus is never selected. Pinned busctl reads
the public bus ID, systemd name owner and its Unix PID/UID credentials. Those
credentials are compared with bounded readable kernel PID/start/boot/UID/cgroup
metadata for both real managers. PID 1's executable and namespace links are not
required. User-manager `init.scope`, its system `user@UID.service`, and the
user-manager `-.slice` representation must agree. The current qualifier's exact
PID/start/boot, executable, containing service generation, MainPID and declared
normal CLI command are bound too. These checks rely on trusted managers, kernel
and administrators; they are not an authentication mechanism against a hostile
administrator or compromised user bus.

Two bounded configured-policy snapshots precede wrapper entry: preflight before
parent-start intent, and a final sample after original parent/Tool/injector
bindings have been saved. The created parent is also checked before Tool-dispatch
intent. Walks cover every component of actual controller ancestry and proposed,
then actual, target ancestry through both user-root representations to system
`-.slice` at `/`. At most 32 path components are accepted. Every component must
map exactly to its manager's unit; unexplained paths, changed identities,
conflicting mappings, missing/duplicate properties, pending jobs and exhausted
operation/wrapper deadlines refuse entry. Each applicable unit must report
`ManagedOOMSwap=auto`, `ManagedOOMMemoryPressure=auto`, and an empty `OOMRules`.
An auto child cannot cancel an ancestor policy. Missing OOMRules support is
unknown. Every nonempty rules list is rejected without interpreting rules.

Shared and controller ancestors require at least 384 MiB headroom whenever their
memory limit is finite; every non-root ancestor records memory max/current and
the kernel group flag. The shared user-manager service requires
`OOMPolicy=continue` and group flag zero. The cgroup root has no memory.max.
The intentionally capped parent/Tool use their fixed 256/96 MiB budget validators
instead of the shared headroom rule. Both samples require at least 512 MiB
MemAvailable. oomd LoadState/ActiveState/Job, invocation, cgroup and monotonic
state-change timestamp must be well formed and stable. Failed, transitioning or
unreadable daemon state refuses entry. Omit/avoid, daemon dry-run and global
policy settings are not used as protection or changed.

This is a configured-policy observation under stable trusted host administration,
not proof that all daemon-held monitor maps are empty. Manager property reads
are not a synchronization barrier for asynchronous daemon state. An unobserved
administrative transition and stale daemon policy remain outside the guarantee.
The reviewed systemd v262.1 `manager_get_dump_string` omits the rules monitor map;
no oomctl helper or complete daemon-state absence oracle is added. The public
[resource-control manual](https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html),
[oomd manual](https://www.freedesktop.org/software/systemd/man/latest/systemd-oomd.service.html)
and reviewed [v262.1 daemon source](https://github.com/systemd/systemd/blob/v262.1/src/oom/oomd-manager.c)
define the applicability boundary. Installed manuals were checked during this
implementation; daemon source inspection is the supplied Astra review, not a
new source fetch or live qualification. Future pressure qualification remains
unsupported and needs its own observation contract.

The selected static driver/probe and reviewed bwrap/BPF chain remain frozen.
Systemctl, systemd-run and busctl hashes and ELF inventories, kernel release,
running user-manager/client versions, ancestor identities and actual headroom
are retained. Original-path manager search paths are inventoried; the frozen
bwrap search-path rejection remains unchanged. Dynamic dependency closure
remains incomplete. An owned control
directory and exact typed stdin bytes are bound before the immutable plan hash;
context is exclusively published afterwards with its exact captured subject/hash.
The workload sees that directory read-only. Only this profile mounts the exact
nofollow metadata-bound root-owned mode-0666 null character device. The source
device is revalidated at dispatch and through the workload mount; filesystem
device identity and `st_rdev` are distinct. Namespace ownership is projected from
the actual UID map, including the selected kernel's overflow UID.

`<attempt>/release.json` permits the injector to enter the reviewed wrapper
chain. It does **not** authorize allocation. The controller has no operation
that publishes `<attempt>/r1-control/release.json` or `finish.json`; either
file, a misplaced root finish, an early wrapper gate or pressure evidence is
an integrity failure. A marker retains detected integrity loss through restored
bytes and reopening. Ambiguous publication or failed durable updates fence
further progress. Structurally valid original cleanup authority is retained.

Manager admission checks, subject work and service limits are checked while the wrapper
is gated. The fixed controller operation deadline is 45 seconds, within the
60-second attempt horizon with cleanup reserve. An incremental complete-frame
reader obtains provisional readiness without waiting for output EOF. One
monotonic observation budget measures a bounded interval from receipt of
readiness and reserves 250 ms within that receipt-relative budget. Required
actors and members must remain live through the final pidfd checks. The actor's
actual remaining timeout is not inferred from receipt time. Direct proc/cgroup
samples bind both distinct actors,
strict actual `NSpid` projections and every observed bwrap/monitor member in the
exact Tool branch, with live pidfds and identity revalidation around the batch.
Unknown members/branches, lost actors, inaccessible metadata or exhausted margin
are inconclusive. Two listings plus the frozen spawn chain do not continuously
observe every transient setup process. Injector-to-bwrap exec is recorded as
one retained PID/start identity, not PID reuse.

Selected endpoint evidence requires child fd0 read-only and fd1/2 write-only
on the exact mounted null character device, device usability/nosuid mount
properties and a read-only containing directory. Child fd4 must be a read pipe
with the exact main actor's unique write-pipe peer. Main stdout remains the
owned evidence stream. Observers read metadata only. The closed hello fd3 may
be reused by Go; runtime epoll/eventfd/pidfd descriptors and the main's pinned
control-directory descriptor are permitted. This is not a complete runtime FD
allowlist or proof of actor write denial or later publication consumption.

Positive evidence is exactly ready sequence 0 followed by failure sequence 1:
`stage=release`, `error=deadline`, zero touched bytes/page size,
`cleanup=not-allocated`, empty release/finish hashes, exact child identity,
`token=abort`, `termination=exited`, `exit_code=20`, `reap=reaped`. The public
probe's natural exit 1 is required and distinguished from watchdog/kill/other
failure. Missing best-effort failure output, partial/extra frames or unknown
reap remain inconclusive even when cleanup succeeds. Final collection requires
all retained original handles terminal, no pending service job, the same parent
empty, saved empty evidence, a stopped parent and an unchanged owned output
identity. Main/child 8/10-second lifetimes are unchanged; cleanup never uses
the legacy four-second fast completion loop.

Reconcile only adopts exact original stop authority and performs bounded owned
cleanup. It never starts a replacement, republishes context or either release,
or reconstructs a positive live observation. Changed boot/generation, missing
required original identity or unproved cleanup retains the start fence. An
empty original parent without a launched Tool can be stopped with explicit
prelaunch cleanup evidence, without fabricating actor observations.
Parent-start and Tool-dispatch intent facts survive failed reconciliation and
reopening independently of progress labels. Verified service-generation stop
authority is saved before cleanup effects even when injection evidence is
missing. Such a service stop remains partial cleanup until original process
termination can be proved; it does not imply `TargetExited` or completion.
The first entry failure is retained as `entry_runtime.failure_reason` alongside
the latest nonempty cleanup reason, including after successful cleanup.

The optional version-1 `entry_runtime.admission` receipt binds manager/controller
identities once and keeps two bounded policy snapshots, capture times, headroom
and the configured-policy result. It adds no immutable plan/request/context
fields and changes none of their bytes or the three-helper inventory. New wrapper
publication and successful entry classification require complete admission
evidence. Old records without it still decode and retain their original cleanup
authority; they cannot acquire a new positive result or replay authority.
Missing, incomplete or unsupported-version admission evidence does not become a
gate for structurally valid original cleanup records.
Historical 14-setting controller receipts still decode and authorize original
cleanup; new publication and success require the three termination settings too.
After wrapper publication controller liveness uses direct metadata only, without
new manager commands in the actor observation window. Natural timeout, nullFD,
process/member, reap and original-generation cleanup oracles remain mandatory.

Before wrapper entry, the selected parent and Tool explicitly set and read back
`MemoryHigh=infinity` and effective `memory.high=max`. Tool also requires
`OOMPolicy=kill` and effective `memory.oom.group=1`; no service OOM policy is
applied to the slice. These are fixed safety guards without an OOM claim.

Deterministic gates cover the positive controller transcript, prohibited
transitions, the old empty-device startup ENOENT at the trusted spawn seam,
selected metadata failures, split/partial output, publication/save faults,
restart and sticky integrity restoration. Fake transcripts use no real pressure
mapping or host services. The later lead observation is recorded above. Heartbeat,
paired 32/96 MiB pressure runs, OOM/group attribution and full sandbox support
remain outside this entry profile.

### Changelog [Unreleased]

- Require explicit controller `KillMode=control-group`, `SendSIGKILL=yes` and
  numeric `FinalKillSignal=9` in R1 entry readback and admission receipts, and
  document them in the normal CLI launch. Preserve historical receipt cleanup.
- Replace the R1 entry profile's global active-oomd rejection with bounded
  configured-policy observations of the real direct-root controller and target
  ancestry, explicit real user-bus access and compatible runtime admission
  receipts. Document the fixed normal CLI controller service contract and the
  unobserved/stale administrative-transition limit. No pressure support or
  executed host qualification is added.
- Add the fixed `r1-entry` candidate host case and explicit busctl subject through
  normal run/inspect/reconcile, with separate entry, expected timeout and cleanup
  evidence. Preserve historical plan hashes, FD1/hold entrypoints and pressure
  case rejection. This source change carries no executed host qualification.
- Preserve entry cleanup authority across failed admission/reconciliation and
  missing injection evidence, retain inconclusive reasons, bind the fixed
  high/OOM safety settings and clarify receipt-relative observation timing.

## Separate implementation and support gates

All full matrix cases remain unsupported: FS1–FS4, US1–US3, FD1–FD2,
PR1–PR3, NS1, KR1, N1–N3, R1–R4 and L1–L2. Those exact IDs are accepted
with no payload and return `unsupported`; unknown spellings are errors.
The core observations do not constitute completed FS/US/FD matrix cases.
Fake terminal coverage is also absent.

The bounded driver supplies hashed selected subjects/launch plans, separate
direct/candidate roles, regular fake-FD reexec injection, process identities,
durable ownership, exact reconciliation and a retained parent-empty oracle.
These remain subject to selected-host verification. Missing future controls
include broader weak controls, complete dynamic dependency closure, isolated
fixture networking with workload/host topology, locked-thread anonymous session
keyring fixtures with returned-serial-only cleanup, independently attributed
local/outer pressure events with surviving outside heartbeat, descendant
cancellation and the full matrix. Unknown cleanup blocks subsequent launches.
Development-image admission, real credentials, provider authority, egress
grants/DNS, arbitrary imported trees and concurrent snapshots retain separate
gates. Default checks perform no systemd/Podman setup, global configuration,
key operation, pressure workload, external address or real network service.

## Source proposal and executed verification

The separate descriptor-policy design reviews requested Fable
`claude-fable-5-2` and Sonnet `sonnet`, both at medium effort. Their recorded
observed models were `claude-fable-5-2` and `claude-sonnet-5-5` respectively.
Both accepted the mechanism with evidence refinements; local primary-source
review corroborated Linux close_range and the selected Go exec/thread path.
These were source/design assessments, not implementation acceptance or host
execution. An earlier oversized Sonnet request timed out without a completed
review or observed model attribution. Implementation tests and lead-run host
evidence remain separate from all of these review records.

The retry source proposal was requested as Sonnet and its recorded observed
model was `claude-sonnet-5-5`. An earlier 300-second attempt timed out without
a proposal; the completed retry is separate. Its original two `.go.txt`
files, intake, provenance and raw response remain unchanged outside this
checkout. They are source-only author input: Claude did not execute, compile
or validate this code.

This reconstruction adopts the Request/Payload/Fact/Observation shape,
version/case/ID vocabulary, bounded decoder/encoder structure, and flat
collector's lstat/fstat/capped-read sequence. It replaces lowercase-based
duplicate detection with canonical ASCII keys, adds an FD upper bound and
regressions, detects Encode short writes, requires fact-value presence,
rejects duplicate fact names and expands bounded inventories. It replaces
the proposal's implicit fixture path with an explicit root and corrects
the selected-Go final-symlink behavior using a pinned flat open. The core
probe, CLI and deterministic tests are reconstruction work. Final correctness
belongs to the implementer; tests executed for this reconstruction are
recorded separately in the delivery log, including unavailable fixtures.

Coordinator review corrections preserve syscall stages and successful effects,
keep incomplete exchanges uncertain, and specify projected Unix UID and frozen
pathname premises. Their regression tests use owned files, synthetic failures
and optional owned listeners. Where the worker cannot create a Unix listener,
the EOF/timeout/mismatch and refused-connect fixtures explicitly skip and remain
ready for coordinator execution. No successful live exchange or actual namespace
qualification is inferred from those skips or from the source proposal.

A later source-only Fable review of candidate `131ffab` identified directory-pin,
socket-setup and pathname-encoding errors misattributed to target syscalls. The
appended correction separates those stages, validates nonce size before effects,
requires exact reply framing with EOF, confines the inventory fallback and
distinguishes raw EAGAIN from deadline expiry. Synthetic regressions cover these
paths and observation-decoder rejection independently of optional Unix listeners.
Fable supplied review input, not executed validation of this reconstruction.
