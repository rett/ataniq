//go:build !(linux && ataniq_ownerchannel)

package main

import "ataniq/internal/terminalconsole/approvals"

// ownerBackend returns the console's approvals backend for the daemon whose
// socket is in dir. A release build has none: the console is given no
// backend, and its approvals screen says there is no owner channel in this
// build. The returned function ends what the backend holds, and does nothing
// here.
func ownerBackend(dir string) (approvals.Backend, func(), error) {
	return nil, func() {}, nil
}
