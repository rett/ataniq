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
//     reason). [NewSpec] builds one from the profile inputs.
//   - The inside checks, assertions 1 to 8, in [RunInside], over a small
//     [System] interface. [HostSystem] is the real one (Linux); tests use a
//     fake. The checks never create a file: writability is asked of the
//     kernel with access(W_OK) and read from the mount table's read-only flag,
//     and both must say read-only. They never issue TIOCSTI, never send a byte
//     on a connection, and start no process: the package imports no os/exec.
//   - The outside checks (assertions 9 and 10, and the outside halves of 1 and
//     5): [CheckRoleLockAndLingering], [CheckQualification],
//     [CheckHolderBinds] and [CheckTIOCSTIOutside], and [RunOutside] to run
//     them, behind the interfaces [RoleLock], [Lingering] and
//     [QualificationRecords], which the holder and the daemon implement.
//     [QualificationRecord] and [Current] say when a record counts. No store
//     is built here.
//   - The [Report]: [Result]s with a status ([StatusOK], [StatusFailed] or
//     [StatusNotChecked]) and a bounded, escaped detail. [EncodeLine] and
//     [ParseLine] write and read it as one line after [ReportPrefix].
//     [Failed] and [Problems] judge a report against its spec. The report has
//     no field that says the sandbox as a whole passed or is supported.
//   - The [Canary] (Linux): a loopback TCP listener and an abstract socket
//     owned by the caller, which count the connections they accept, for
//     assertion 3.
//   - The stop rule of the design as constants ([StopTermWait] and the
//     others). The holder implements it.
//
// # Not-checked is not ok
//
// A check that cannot be made (a file that cannot be read, a mount table that
// does not parse, a dial that failed for a reason that does not show the
// address unreachable) is reported as not-checked, with the reason. [Failed]
// treats it as a failure, unless the spec lists the assertion as optional. A
// failed result is a failure whatever the spec says. A result that is missing
// is a failure.
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
//     root. A path bound at another place from a file system with another root
//     is not found by either.
//   - Assertion 7 shows that the three paths are on a read-only mount and that
//     a write is refused by access(W_OK). That the tree is the lead's own
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
