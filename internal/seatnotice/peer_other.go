//go:build !linux

package seatnotice

import (
	"errors"
	"net"
)

// peerUID cannot read a peer's user id on this system, so the server refuses
// every connection.
func peerUID(net.Conn) (uint32, error) {
	return 0, errors.New("seatnotice: peer credentials are not available on this system")
}
