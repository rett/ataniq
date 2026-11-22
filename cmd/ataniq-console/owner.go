package main

import (
	"ataniq/internal/consoleclient"
	"ataniq/internal/terminalconsole/approvals"
)

// The daemon's owner client satisfies the console's one view of approvals.
// The console imports neither it nor the wire it speaks: this command hands
// it over as a value in the options, with --daemon.
var _ approvals.Backend = (*consoleclient.Owner)(nil)

// ownerBackend returns the owner client for the daemon whose socket is in dir:
// the console's approvals backend, which dials when the console first asks and
// dials again after a lost connection. It decides nothing by itself. A daemon
// with no owner channel is found by the client, and the approvals screen then
// says so. The returned function closes the client.
//
// It returns the client itself and not the interface, because the pair-a-phone
// view's adapter must wrap this same value: the daemon counts one owner
// connection as the decider, and a second client would be a second console and
// be refused. The caller hands the one value to both backends.
func ownerBackend(dir string) (*consoleclient.Owner, func(), error) {
	o, err := consoleclient.NewOwner(consoleclient.OwnerConfig{Dir: dir, Client: clientName})
	if err != nil {
		return nil, func() {}, err
	}
	return o, func() { o.Close() }, nil
}
