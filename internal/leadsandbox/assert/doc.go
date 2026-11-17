// Package assert holds the start-up assertions of the lead sandbox: the ten of
// the lead-hosting design (section 3.8) as a table, the spec the holder writes
// for the probe, the checks the probe makes inside the sandbox, the checks the
// holder makes outside, and the report the probe writes. It is a proposal that
// awaits qualification and an owner decision. Nothing here says the sandbox is
// qualified, safe or supported: a report states facts about ten assertions,
// and a reader decides what to do with them.
//
// # What it holds
//
//   - The table ([Assertions]): the number, a stable ID, the text, where each
//     assertion is checked ([Inside] by the probe, [Outside] by the holder, or
//     [Both]) and the qualification cases of the design (section 7) it maps to.
//   - The [Spec]: JSON, versioned ([SpecVersion]), at most [MaxSpecBytes]
//     (256 KiB), decoded strictly by [DecodeSpec] (an unknown field, trailing
//     data, an over-long list or a malformed entry is refused with the
//     reason). [NewSpec] builds one from the profile inputs, the expected
//     environment the caller passes (the plan's inside environment, which holds
//     PWD) and the facts of the host.
//   - The inside checks, assertions 1 to 8, in [RunInside], over a small
//     [System] interface. [HostSystem] is the real one (Linux); tests use a
//     fake. The checks never create a file: writability is asked of the
//     kernel with access(W_OK) and read from the read-only flag of the mount
//     that statx names, and both must say read-only. They never issue
//     TIOCSTI, never send a byte on a connection, and start no process: the
//     package imports no os/exec.
//   - The outside checks (assertions 9 and 10, and the outside halves of 1, 3
//     and 5): [CheckRoleLockAndLingering], [CheckQualification],
//     [CheckHolderBinds], [CheckCanary] and [CheckTIOCSTIOutside], and
//     [RunOutside] to run them, behind the interfaces [RoleLock], [Lingering] and
//     [QualificationRecords], which the holder and the daemon implement.
//     [QualificationRecord] and [Current] say when a record counts. No store
//     is built here.
//   - The [Report]: [Result]s with a status ([StatusOK], [StatusFailed] or
//     [StatusNotChecked]) and a bounded, escaped detail. [EncodeLine] and
//     [ParseLine] write and read it as one line after [ReportPrefix];
//     [ParseProbeLine] reads the line of a probe, which holds the inside
//     results 1 to 8, each once, and no other. [Failed] and [Problems] judge
//     a report against its spec. The report has no field that says the
//     sandbox as a whole passed or is supported.
//   - The [Canary] (Linux): a loopback TCP listener, a second on ::1 when the
//     host has IPv6 loopback (no failure when it has not: [Canary.TCP2] is
//     then empty, and the spec's second address, [Spec.LoopbackTCP2], is
//     left out), and an abstract socket owned by the caller, which count the connections they accept. Assertion
//     3 is checked on both sides: the probe dials the addresses and
//     classifies the failure, and after the probe's report [CheckCanary] says
//     the canary accepted nothing. That count is the evidence independent of
//     the probe's own judgement, and a timeout, which is weaker evidence than
//     a refusal, rests on it. The accept loops survive a transient error of
//     accept (counted, with a bounded back-off); a count taken after such an
//     error, or after the canary stopped, is not-checked.
//   - The stop rule of the design as constants ([StopTermWait] and the
//     others). The holder implements it.
//
// # The probe and the holder
//
// The contract of the report line, which the probe command and the holder are
// written against:
//
//   - The probe ALWAYS writes its report line to its report descriptor, on
//     success too, before it execs the agent. A probe whose report is
//     [Failed] exits without exec (status 97). The descriptor is
//     close-on-exec, so the exec of the agent closes it.
//   - The holder owns the other end of that descriptor. It judges a start
//     only by what it reads there and by the probe's exit status: a line that
//     [ParseProbeLine] accepts, followed by end-of-file while the process is
//     still alive (the exec happened), and no [Problems] for the line with the
//     holder's own results added; or the probe's exit status, which is a
//     start that did not happen. A missing line, end-of-file without a line,
//     or a line that is refused, is not a start.
//   - A line that is seen anywhere else, the terminal in particular, is
//     display only and never input to [Failed] or [FailedAll]. The lead
//     writes to the same terminal after the exec and can print a line with
//     [ReportPrefix]; the report carries the digest of the spec, which the
//     lead can read, so the prefix and the digest authenticate nothing.
//   - The probe makes the results 1 to 8, the inside half of each assertion
//     that has one, once each. The holder makes the others (the outside half
//     of 1, 3 and 5, then 9 and 10) with [RunOutside], which takes the
//     canary, adds them with [Report.With] and judges the whole with
//     [FailedAll], after the probe's report and before it accepts the start. [ParseLine] reads a full report;
//     the probe's line is read with [ParseProbeLine].
//
// # Not-checked is not ok
//
// A check that cannot be made (a file that cannot be read, a mount table that
// does not parse, a dial that failed for a reason that does not show the
// address unreachable) is reported as not-checked, with the reason. [Failed]
// treats it as a failure, unless the spec lists the assertion as optional. Only
// assertion 4 (the provider host resolves, [OptionalAssertion]) may be listed,
// and only its own result counts: [Spec.Validate] refuses any other number,
// and the outside half of an assertion is never excused. A failed result is a
// failure whatever the spec says. A result that is missing is a failure.
//
// # Untrusted text
//
// A detail may quote text that a lead or the host controls: a path, an
// environment name, a resolver error. [EscapeDetail] turns invalid UTF-8 and
// every non-printable character into a visible escape and cuts the text to
// [MaxDetailBytes]; [ParseLine] refuses a detail that is not already so.
//
// # Limits stated
//
//   - Assertion 6 looks for host paths at the same place inside. The probe
//     cannot know where a leak would be bound, so assertion 1 also looks in
//     the mount table for the same paths, which a bind mount keeps as its
//     root, and fails a mount whose root, source or mount point is a listed
//     owner or holder path, lies below it, or is one of its ancestors
//     (compared by component). A mount root is relative to its file system:
//     a separate file system (a /home of its own) mounted above an owner
//     path has the root "/" and is not found by either. The other runtime's
//     config folder is ok only when it is absent, or an empty folder that is
//     not a mount point and has nothing mounted below it.
//   - Assertion 7 shows that each of the three paths, and the
//     managed-settings file of assertion 8, is on a read-only mount (found by
//     the mount ID that statx gives, not by the longest mount point above the
//     path, so a later bind that hides an earlier one is seen), that no
//     read-write mount lies at or below it, and that a write is refused by
//     access(W_OK). Where the kernel gives no mount ID (before Linux 5.8) the
//     result is not-checked. That the tree is the lead's own
//     clone, and not below another repository, is the check of the
//     mountpoints package; the probe checks that .git is a folder.
//   - Assertion 8 shows that the managed-settings file is a regular file on a
//     read-only mount and contains the marker text. It does not parse the
//     file: a file that holds the text where it does not make the hook
//     active passes. That the hook runs is for the qualification case that
//     exercises it.
//   - Assertion 5 accepts the sysctl dev.tty.legacy_tiocsti reading 0 and
//     nothing else when the profile names no seccomp filter: a filter that
//     something else loaded is not evidence that TIOCSTI is blocked. When the
//     profile names a filter, the probe requires Seccomp: 2 and reports
//     Seccomp_filters; it does not verify from inside which filter it is.
//   - The checks run once, before the agent starts. They say nothing about
//     what the agent does after it.
package assert
