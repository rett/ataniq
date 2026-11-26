// Package noaicheck is the authority host's no-AI check: it looks, read-only,
// through a file tree for the signs of an AI agent tool or its credentials
// and reports each one as a hit. The host must never carry such a tool; the
// check is detection, not prevention (the prevention is that nobody logs in,
// the egress filter and the package allowlist).
//
// Every path is read beneath one root folder ("/" on the host, a temporary
// folder in tests) through an [os.Root], so a link in the tree never leads
// outside it; links are resolved component by component within the root, at
// most 40 of them per path. A walk opens each folder relative to its
// parent's descriptor without following a link and lists it in bounded
// batches, so a link loop cannot hang it and a wide folder cannot exhaust
// memory. A file is opened without blocking and without following a final
// link, and the open descriptor must be the regular file that was checked
// before a byte is read. The running processes and listening sockets are read
// from the root’s proc/ folder. On the live systemd host, a bounded systemctl
// show query also reads the witness unit’s effective address grants.
//
// A hit names a kind and a path or name, never a file's content or an
// environment variable's value. A place the check must look into but cannot
// read is itself a hit (kind "unreadable"), and so is a check that could
// not cover all it must (kind "incomplete": a walk stopped by its depth
// limit, or the run's budget of entries, bytes, time, memory and nesting
// spent), so
// the check fails closed.
//
// A run has a phase (see [Checker.RunPhase]): "pre", before a switch, also
// reads the candidate release's own units and configuration and leaves out
// the listening ports and the firewall digest; "post", after it, runs every
// check; "full", the default, checks the host as it is.
//
// The lists come from the release (its share/ folder, see [LoadLists]).
// Stated limits: a renamed binary is not caught by name (the package
// allowlist and the egress filter cover that), and the lists need upkeep.
package noaicheck
