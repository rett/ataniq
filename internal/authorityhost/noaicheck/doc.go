// Package noaicheck is the authority host's no-AI check: it looks, read-only,
// through a file tree for the signs of an AI agent tool or its credentials
// and reports each one as a hit. The host must never carry such a tool; the
// check is detection, not prevention (the prevention is that nobody logs in,
// the egress filter and the package allowlist).
//
// Every path is read beneath one root folder ("/" on the host, a temporary
// folder in tests) through an [os.Root], so a link in the tree never leads
// outside it; links are resolved component by component within the root, at
// most 40 of them per path, and walks never follow a link, so a link loop
// cannot hang the check. Nothing is written and no process is started: the
// running processes and the listening sockets are read from the root's
// proc/ folder.
//
// A hit names a kind and a path or name, never a file's content or an
// environment variable's value. A place the check must look into but cannot
// read is itself a hit (kind "unreadable"), so the check fails closed.
//
// The lists come from the release (its share/ folder, see [LoadLists]).
// Stated limits: a renamed binary is not caught by name (the package
// allowlist and the egress filter cover that), and the lists need upkeep.
package noaicheck
