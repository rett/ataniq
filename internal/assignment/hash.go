package assignment

import (
	"bytes"
	"encoding/json"
	"reflect"
	"unicode/utf8"
)

const recordLimit = 64 << 10
const (
	requestDomain = "ataniq.assignment.request.v1"
	publicDomain  = "ataniq.assignment.public.v1"
	ledgerDomain  = "ataniq.assignment.ledger.v1"
)

func domainDigest(domain string, b []byte) string {
	return sha256Hex(append([]byte(domain+"\x00"), b...))
}

func requestDigest(c Command) (string, error) {
	if c.Evidence == nil {
		c.Evidence = []Evidence{}
	}
	b, err := canonicalJSON(c)
	if err != nil {
		return "", err
	}
	return domainDigest(requestDomain, b), nil
}

// The event ID commits to the private request and resulting row before the
// public envelope (which contains the ID) is produced.
func publicID(e Event) (string, error) {
	result := e.Result
	result.EventID = ""
	b, err := canonicalJSON(struct {
		PreviousHash  string `json:"previous_hash"`
		RequestDigest string `json:"request_digest"`
		Result        Result `json:"result"`
		Schema        uint64 `json:"schema"`
		Sequence      uint64 `json:"sequence,string"`
	}{e.PreviousHash, e.RequestDigest, result, e.Schema, e.Sequence})
	if err != nil {
		return "", err
	}
	return domainDigest(publicDomain, b), nil
}

func ledgerDigest(e Event) (string, error) {
	b, err := e.recordFrame()
	if err != nil {
		return "", err
	}
	return domainDigest(ledgerDomain, b), nil
}

// Validate strings before encoding/json can silently replace malformed UTF-8.
// The raw string lengths bound encoder allocation before escape expansion.
func checkStrings(v reflect.Value, remaining *int) error {
	switch v.Kind() {
	case reflect.String:
		s := v.String()
		if len(s) > *remaining {
			return ErrLimit
		}
		if !utf8.ValidString(s) {
			return ErrInvalid
		}
		*remaining -= len(s)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := checkStrings(v.Field(i), remaining); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if v.Len() > *remaining {
			return ErrLimit
		}
		*remaining -= v.Len()
		for i := 0; i < v.Len(); i++ {
			if err := checkStrings(v.Index(i), remaining); err != nil {
				return err
			}
		}
	}
	return nil
}

func frame(v any) ([]byte, error) {
	b, err := canonicalJSON(v)
	if err != nil {
		return nil, err
	}
	if len(b) >= recordLimit {
		return nil, ErrLimit
	}
	return append(b, '\n'), nil
}

type headerRecord struct {
	Format string `json:"format"`
	Header Header `json:"header"`
}

// Bytes returns the immutable schema-1 header, including its terminating LF.
func (h Header) Bytes() ([]byte, error) {
	if h.Schema != 1 || h.Authority == "" || h.Store == "" || h.Incarnation == "" || h.KeyID == "" || h.LedgerID == "" {
		return nil, ErrInvalid
	}
	if !referencesFit(h.Authority, h.Store, h.Incarnation, h.KeyID, h.LedgerID) {
		return nil, ErrLimit
	}
	return frame(headerRecord{"ataniq.assignment", h})
}

// NewLedgerFromHeader validates and pins the exact bytes presented at recovery.
// Byte equality also rejects duplicate, unknown, missing and noncanonical fields.
func NewLedgerFromHeader(raw []byte) (Ledger, error) {
	if len(raw) > recordLimit {
		return Ledger{}, ErrLimit
	}
	var rec headerRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Ledger{}, ErrInvalid
	}
	canonical, err := rec.Header.Bytes()
	if err != nil {
		return Ledger{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return Ledger{}, ErrInvalid
	}
	return initialLedger(rec.Header, raw), nil
}

type journalRecord struct {
	Command       Command        `json:"command"`
	PreviousHash  string         `json:"previous_hash"`
	Public        PublicEnvelope `json:"public"`
	RequestDigest string         `json:"request_digest"`
	Result        Result         `json:"result"`
	Schema        uint64         `json:"schema"`
	Sequence      uint64         `json:"sequence,string"`
}

func (e Event) recordFrame() ([]byte, error) {
	p := e.Public
	p.Signature = e.Signature
	c := e.Command
	if c.Evidence == nil {
		c.Evidence = []Evidence{}
	}
	return frame(journalRecord{c, e.PreviousHash, p, e.RequestDigest, e.Result, e.Schema, e.Sequence})
}

// RecordBytes returns one complete signed journal record with its LF.
func (e Event) RecordBytes() ([]byte, error) {
	if e.Signature == "" || e.Public.Signature != "" {
		return nil, ErrChain
	}
	return e.recordFrame()
}

// DecodeRecord checks the frame and its canonical representation. Replay
// authenticates the public envelope and checks the chain and transition.
func DecodeRecord(raw []byte) (Event, error) {
	if len(raw) > recordLimit {
		return Event{}, ErrLimit
	}
	var r journalRecord
	if err := json.Unmarshal(raw, &r); err != nil || r.Schema != 1 {
		return Event{}, ErrChain
	}
	e := Event{Schema: r.Schema, Sequence: r.Sequence, PreviousHash: r.PreviousHash, RequestDigest: r.RequestDigest, Command: r.Command, Result: r.Result, Public: r.Public, Signature: r.Public.Signature}
	e.Public.Signature = ""
	canonical, err := e.RecordBytes()
	if err != nil {
		return Event{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return Event{}, ErrChain
	}
	return e, nil
}
