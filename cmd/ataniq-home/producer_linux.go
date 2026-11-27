package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"

	a "ataniq/internal/assignment"
)

// Each command addresses the same synthetic row. After the seed handoff, the
// mirror stays frozen and only its synthetic inventory is sampled again.
func mirrorEvent(l a.Ledger, header a.Header, signer a.Signer) (a.Event, error) {
	key := a.Key{Home: header.Authority, Project: "mirror-1", Role: "mirror-lead"}
	row, _ := l.Row(key)
	if row.Head.Revision != l.Checkpoint().Sequence {
		return a.Event{}, errors.New("observe requires a disposable mirror-only ledger")
	}
	row.Head.Key = key
	tr, _ := l.Transfer(key, row.LastTransferID)
	c := a.Command{Expected: row.Head, Actor: "mirror-owner", Authorization: "mirror-approval", RequestID: "sample-" + strconv.FormatUint(row.Head.Revision, 10), TransferID: row.LastTransferID}
	scope := row.Head
	eventID := ""
	proof := func(kind, subject, outcome string) {
		digest := sha256.Sum256([]byte(c.RequestID + "/" + kind + "/" + subject + "/" + outcome))
		c.Evidence = append(c.Evidence, a.Evidence{Kind: kind, Subject: subject, Outcome: outcome, Issuer: "mirror-source", Revision: c.RequestID, Digest: fmt.Sprintf("%x", digest), Scope: scope, ScopeEventID: eventID, TransferID: c.TransferID})
	}
	candidate := a.Binding{Kind: "legacy", ID: "mirror-holder", Authority: key.Home, Store: "mirror-store", Incarnation: "mirror-instance", Conversation: "mirror-conversation"}
	switch row.Head.Phase {
	case "":
		c.Kind, c.TransferID, c.Candidate = a.Seed, "mirror-seed", candidate
		proof("enrollment", candidate.ID, "verified")
		proof("route", "qargi", "enabled")
	case a.PhaseCommittedClosed:
		if len(tr.Confirmations) == 0 {
			c.Kind = a.ConfirmReceipt
			scope, eventID = tr.Committed, tr.CommittedEventID
			proof("route", "qargi", "installed-stale-refused")
		} else {
			c.Kind = a.Open
			proof("enrollment", row.Holder.ID, "current")
		}
	case a.PhaseConfirmed:
		candidate.ID = "mirror-successor"
		c.Kind, c.TransferID, c.Candidate = a.Freeze, "mirror-inventory", candidate
		proof("barrier", row.Holder.ID, "captured")
		proof("route", "qargi", "enabled")
	case a.PhaseFrozen, a.PhaseInventoried:
		c.Kind = a.Inventory
		proof("inventory", key.Project, "complete")
		proof("handoff", key.Project, "verified")
		proof("route", "qargi", "frozen")
	default:
		return a.Event{}, errors.New("unexpected disposable mirror phase")
	}
	e, _, err := a.Decide(l, c)
	if err != nil {
		return a.Event{}, err
	}
	if e == nil {
		return a.Event{}, errors.New("duplicate mirror command")
	}
	body, err := e.SigningBytes()
	if err != nil {
		return a.Event{}, err
	}
	e.Signature, err = signer.Sign(header, body)
	return *e, err
}
