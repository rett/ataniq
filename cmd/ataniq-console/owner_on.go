//go:build linux && ataniq_ownerchannel

package main

import (
	"ataniq/internal/consoleclient"
	"ataniq/internal/terminalconsole/approvals"
)

// The daemon's owner client satisfies the console's one view of approvals.
// The console imports neither it nor the wire it speaks: this command hands
// it over as a value in the options, with --daemon, in the tagged build only.
var _ approvals.Backend = (*consoleclient.Owner)(nil)

// ownerBackend returns the console's approvals backend for the daemon whose
// socket is in dir: the owner client, which dials when the console first asks
// and dials again after a lost connection. It decides nothing by itself. A
// daemon with no owner channel is found by the client, and the approvals
// screen then says so. The returned function closes the client.
func ownerBackend(dir string) (approvals.Backend, func(), error) {
	o, err := consoleclient.NewOwner(consoleclient.OwnerConfig{Dir: dir, Client: clientName})
	if err != nil {
		return nil, func() {}, err
	}
	return o, func() { o.Close() }, nil
}
