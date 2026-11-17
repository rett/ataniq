package wire

import (
	"crypto/rand"
	"encoding/hex"
	"io"
)

// TicketBytes is the number of random bytes in a ticket.
const TicketBytes = 32

// TicketLen is the length of a ticket: its bytes in hexadecimal.
const TicketLen = 2 * TicketBytes

// NewTicket returns a new attach ticket: 32 bytes from crypto/rand as 64
// lower-case hexadecimal characters. The store of issued tickets, their
// binding to a role and epoch, their expiry and their single use are not in
// this package.
func NewTicket() (string, error) { return NewTicketFrom(rand.Reader) }

// NewTicketFrom is [NewTicket] with another source of bytes, for tests. A
// source that is short of bytes makes it fail.
func NewTicketFrom(r io.Reader) (string, error) {
	var b [TicketBytes]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// CheckTicket checks the form of a ticket: exactly [TicketLen] lower-case
// hexadecimal characters. It says nothing about whether the ticket was
// issued.
func CheckTicket(s string) error { return checkHex("ticket", s, TicketLen) }
