// Package leadunit is the interface through which the daemon manages one
// transient systemd user unit per lead role, and a scriptable fake of it.
//
// # What it is
//
// A [Manager] starts, inspects, signals and proves empty one unit. A [Spec]
// says what to start: the unit name, the epoch and intent id that the unit's
// description carries (see [Spec.Description] and [ParseDescription]), the
// command, the working directory, an explicit environment and a closed set of
// extra hardening properties. [FixedProperties] lists the properties every
// unit gets, in one place for the real manager and for the documentation. A
// [State] is what an inspection or a start returns, with the invocation id and
// the control group identity that tie a later signal or proof to one run of
// the unit.
//
// The package holds no systemd call, no process, no file and no socket. The
// real manager lives elsewhere and implements [Manager]; [Fake] is an
// in-memory implementation for tests, together with [Clock] and [FakeClock],
// which a lifecycle that waits on the manager needs in order to be tested
// without real time.
//
// # What it does not prove
//
// The package proves nothing about systemd. [Fake] enforces the rules the
// contract names (one unit per name, collection of inactive or failed units,
// refusal of a signal for a stale invocation), but it is a model written from
// the contract, not from systemd: it does not stop the other processes of a
// control group when the main process exits, it has no unit that is only
// activating, and it knows no signal but SIGTERM and SIGKILL. A passing test
// against the fake says that the caller follows the contract, not that the
// real manager keeps it.
//
// # Status
//
// The package is provisional and unqualified: its names and behaviour may
// change until a qualification run on the real manager has covered it.
package leadunit
