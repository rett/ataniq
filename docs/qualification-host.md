# Static execution-boundary observations

This is qualification infrastructure for a Linux/amd64 static workload subject.
It does not qualify a sandbox, development image, provider or authentication
path. The workload emits facts, status and errno. The explicit trusted host
driver binds synthetic controls and lifecycle evidence; independent host
verification and review remain necessary for any support decision. There is
no pass or support field in workload or driver output.

The overall design has two entrypoints: a trusted driver and a static workload
probe sharing an internal package. `qualification/host/cmd/qualify` implements
one concrete bubblewrap profile with a private service-side reexec injector.
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
does not execute a live inventory or launch a sandbox. Tests use owned fake
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
metadata are retained, and RPATH/RUNPATH or path-valued dependencies are
rejected so the frozen copy cannot change relative loader searches. No loader
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
Cases are `inventory`, `fake-file`, `fake-fd` and `hold`; roles are `direct` and
`candidate`. `reviewed_bpf:true` is the operator's explicit statement that the
selected bounded classic BPF artifact was separately reviewed for this profile;
length checks and hashing do not review its policy. The filter must enforce
the reviewed syscall/nested-userns restrictions. The driver retains its digest.
No default filter is invented or installed.

The commands are:

```text
qualification-qualify run --allow-host-operations --config /owned/qualification/config.json
qualification-qualify inspect --campaign /owned/qualification/campaign
qualification-qualify inspect --campaign /owned/qualification/campaign --attempt <32-hex-attempt>
qualification-qualify reconcile --allow-host-operations --campaign /owned/qualification/campaign --attempt <32-hex-attempt>
```

`inspect` locks and reads the exact campaign without contacting the manager.
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
elapsed deadlines never clear launch uncertainty.

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

The finite private `private-inject` reexec is run only inside this service. It
validates exact intent/root/subjects and opens only the owned synthetic regular
fixture and reviewed BPF subject for intentional inheritance. RLIMIT and unused
sparse FD checks precede modification of its own child table: synthetic FD 512,
BPF FD 513, and stdin/stdout/stderr are separately tracked, with metadata,
digest and CLOEXEC checks. No orchestrator FD mutation or final exec.Command
FD remapping occurs. BPF is consumed by bubblewrap and closed for direct runs.

Before exec the injector publishes bounded owned evidence with its actual
INVOCATION_ID, PID/starttime, namespaces/cgroup, subject and descriptor identity.
It waits at most ten seconds for its exact owned release marker. The controller
independently binds systemd show, injector identity, pidfd, effective guards and
parent populated=1, persists that binding and release intent, then publishes
the marker. It is excluded from candidate mounts and closed before exec.
Injection and executable identities are rechecked immediately before syscall.Exec
of the direct helper or complete bubblewrap chain, with an empty environment.
Failure means no backend exec; acknowledgement loss means launch-unknown,
without a retry. Paths remain subject to the trusted frozen-tree premise.

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
old at selection, with matching profile/driver/probe/bwrap/BPF digests and valid
injection/cleanup/direct digest recovery. The candidate records the control's
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
readiness, malformed records and sync/rename crash windows. They establish
implementation behavior on transcripts, not actual host behavior. The separate
coordinator retained-slice preflight is narrow host evidence, not a driver test
or a full profile pass. Live injector/FD/hold runs, host property formats and
launcher dependency closure remain coordinator verification gates.

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
include weak synthetic controls, complete dynamic dependency closure, isolated
fixture networking with workload/host topology, locked-thread anonymous session
keyring fixtures with returned-serial-only cleanup, independently attributed
local/outer pressure events with surviving outside heartbeat, descendant
cancellation and the full matrix. Unknown cleanup blocks subsequent launches.
Development-image admission, real credentials, provider authority, egress
grants/DNS, arbitrary imported trees and concurrent snapshots retain separate
gates. Default checks perform no systemd/Podman setup, global configuration,
key operation, pressure workload, external address or real network service.

## Source proposal and executed verification

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
