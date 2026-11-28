package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// HistoryVersion is the version of the History frame's own layout, the first
// byte of its payload. It is the layout's, not the protocol's: the frame is
// not part of the attach exchange until the holder sends it, and a client
// that finds another version refuses the frame with [ErrHistoryVersion]
// without reading the rest.
const HistoryVersion = 1

// Caps of a History frame.
const (
	// MaxHistoryPayload is the largest History payload. A ring of rows
	// that does not fit is cut from its oldest end ([History.Fit]); the
	// frame says so by the number of its oldest row.
	MaxHistoryPayload = 4 << 20
	// MaxHistoryRowBytes is the longest encoded row (the session pane's
	// sessionpane.MaxRecentRowBytes, which a test of the pane checks against
	// this constant).
	MaxHistoryRowBytes = 48 << 10
	// MaxHistoryRows is the most rows a frame stands for, an omitted
	// interval counting as its rows (the session pane's MaxRecentRows).
	MaxHistoryRows = 16384
	// MaxHistoryEntries is the most entries a frame has.
	MaxHistoryEntries = MaxHistoryRows
)

// historyHeaderLen is the fixed part of a History payload: version, flags,
// the output position, the number of the oldest row and the count of entries.
const historyHeaderLen = 1 + 1 + 8 + 8 + 4

// Kinds of a History entry.
const (
	historyRow     = 1
	historyOmitted = 2
)

// Flags of a History frame.
const historyApproximate = 1

// ErrHistoryVersion is a History payload of another layout version. It is
// wrapped with [ErrInvalid].
var ErrHistoryVersion = errors.New("wire: history frame of another version")

// HistoryEntry is one row of recent output, or an interval of rows whose
// content was never recorded. Exactly one of Row and Omitted is set.
type HistoryEntry struct {
	// Row is an encoded row, opaque to this package (the session pane's
	// EncodeRecentRow): 1 to [MaxHistoryRowBytes] bytes.
	Row []byte
	// Omitted is a count of rows, 1 to [MaxHistoryRows]; the rows have no
	// content, and two intervals never follow each other.
	Omitted uint32
}

func (e HistoryEntry) rows() uint64 {
	if e.Row != nil {
		return 1
	}
	return uint64(e.Omitted)
}

func (e HistoryEntry) size() int {
	if e.Row != nil {
		return 1 + 2 + len(e.Row)
	}
	return 1 + 4
}

// History is the payload of a History frame: the rows that scrolled off the
// holder's main screen, kept for a console that attaches later. It is
// read-only: it carries no command, and a client installs the rows as
// history without writing anything to the session.
type History struct {
	// Position is the output position (as in [EncodeSnapshot]) at which the
	// ring was read. A client that is also given a Snapshot accepts the
	// history only when the two positions are equal.
	Position uint64
	// Oldest is the number of the first entry's first row, counting the rows
	// that scrolled off since the holder's epoch began from zero. A history
	// that starts at zero is complete; one that starts above it says that
	// earlier rows were evicted or cut, and a replacement holder starts a new
	// count at zero.
	Oldest uint64
	// Approximate says that Oldest, and so the numbers after it, are lower
	// bounds (the pane saw a burst of rows it could only count as over its
	// window).
	Approximate bool
	// Entries are oldest first. None means an empty ring, which is not an
	// omitted interval.
	Entries []HistoryEntry
}

// Rows returns the number of rows the history stands for.
func (h History) Rows() uint64 {
	var n uint64
	for _, e := range h.Entries {
		n += e.rows()
	}
	return n
}

// check applies the rules both directions share, and returns the size of the
// payload.
func (h History) check() (int, error) {
	if len(h.Entries) > MaxHistoryEntries {
		return 0, fmt.Errorf("%w: history has %d entries, cap %d", ErrTooLarge, len(h.Entries), MaxHistoryEntries)
	}
	size := historyHeaderLen
	var rows uint64
	for i, e := range h.Entries {
		switch {
		case e.Row != nil && e.Omitted != 0:
			return 0, invalidf("history entry %d is a row and an omitted interval", i)
		case e.Row != nil:
			if len(e.Row) == 0 || len(e.Row) > MaxHistoryRowBytes {
				return 0, invalidf("history row %d has %d bytes, want 1 to %d", i, len(e.Row), MaxHistoryRowBytes)
			}
		case e.Omitted == 0:
			return 0, invalidf("history entry %d is empty", i)
		case e.Omitted > MaxHistoryRows:
			return 0, invalidf("history entry %d omits %d rows, cap %d", i, e.Omitted, MaxHistoryRows)
		case i > 0 && h.Entries[i-1].Row == nil:
			return 0, invalidf("history entries %d and %d are omitted intervals that follow each other", i-1, i)
		}
		rows += e.rows()
		size += e.size()
		if rows > MaxHistoryRows {
			return 0, fmt.Errorf("%w: history stands for more than %d rows", ErrTooLarge, MaxHistoryRows)
		}
		if size > MaxHistoryPayload {
			return 0, fmt.Errorf("%w: history is over %d bytes", ErrTooLarge, MaxHistoryPayload)
		}
	}
	if h.Oldest > ^uint64(0)-rows {
		return 0, invalidf("history row numbers pass 64 bits")
	}
	return size, nil
}

// EncodeHistory returns the payload of a History frame. It refuses, and does
// not cut, a history that is over a cap ([ErrTooLarge]) or breaks a rule of
// the frame ([ErrInvalid]); [History.Fit] makes one that fits.
//
//	version     byte ([HistoryVersion])
//	flags       byte: bit 0 approximate; the others are zero
//	position    8 bytes big endian
//	oldest      8 bytes big endian
//	count       4 bytes big endian, the entries
//	entries     each: kind byte (1 row, 2 omitted), then for a row its length
//	            in 2 bytes big endian and the bytes, for an interval its rows
//	            in 4 bytes big endian
func EncodeHistory(h History) ([]byte, error) {
	size, err := h.check()
	if err != nil {
		return nil, err
	}
	out := make([]byte, historyHeaderLen, size)
	out[0] = HistoryVersion
	if h.Approximate {
		out[1] = historyApproximate
	}
	binary.BigEndian.PutUint64(out[2:], h.Position)
	binary.BigEndian.PutUint64(out[10:], h.Oldest)
	binary.BigEndian.PutUint32(out[18:], uint32(len(h.Entries)))
	for _, e := range h.Entries {
		if e.Row != nil {
			out = append(out, historyRow, byte(len(e.Row)>>8), byte(len(e.Row)))
			out = append(out, e.Row...)
		} else {
			out = binary.BigEndian.AppendUint32(append(out, historyOmitted), e.Omitted)
		}
	}
	return out, nil
}

// DecodeHistory reads the payload of a History frame, strictly: another
// version ([ErrHistoryVersion]), a flag that is not defined, a count or a
// length over its cap, an entry of an unknown kind, an empty row, an interval
// of no rows or one that follows another, a payload cut short and bytes left
// over are all refused ([ErrInvalid]; [ErrTooLarge] and [ErrTooSmall] for the
// size). Nothing is allocated from a declared count before the bytes that
// back it are known to be there. The rows are slices of payload.
func DecodeHistory(payload []byte) (History, error) {
	if len(payload) > MaxHistoryPayload {
		return History{}, fmt.Errorf("%w: history payload is %d bytes, cap %d", ErrTooLarge, len(payload), MaxHistoryPayload)
	}
	if len(payload) < historyHeaderLen {
		return History{}, fmt.Errorf("%w: history payload is %d bytes, want at least %d", ErrTooSmall, len(payload), historyHeaderLen)
	}
	if payload[0] != HistoryVersion {
		return History{}, fmt.Errorf("%w: %w: %d, want %d", ErrInvalid, ErrHistoryVersion, payload[0], HistoryVersion)
	}
	if payload[1]&^historyApproximate != 0 {
		return History{}, invalidf("history flags 0x%02x", payload[1])
	}
	h := History{
		Position:    binary.BigEndian.Uint64(payload[2:]),
		Oldest:      binary.BigEndian.Uint64(payload[10:]),
		Approximate: payload[1]&historyApproximate != 0,
	}
	count := binary.BigEndian.Uint32(payload[18:])
	if count > MaxHistoryEntries {
		return History{}, fmt.Errorf("%w: history declares %d entries, cap %d", ErrTooLarge, count, MaxHistoryEntries)
	}
	// Every entry is at least five bytes, so the declared count is bounded
	// by what the payload can hold, and nothing larger is allocated.
	if int(count) > (len(payload)-historyHeaderLen)/5 {
		return History{}, invalidf("history declares %d entries in %d bytes", count, len(payload)-historyHeaderLen)
	}
	h.Entries = make([]HistoryEntry, 0, count)
	b := payload[historyHeaderLen:]
	for i := uint32(0); i < count; i++ {
		if len(b) == 0 {
			return History{}, invalidf("history ends in entry %d", i)
		}
		kind := b[0]
		b = b[1:]
		switch kind {
		case historyRow:
			if len(b) < 2 {
				return History{}, invalidf("history ends in entry %d", i)
			}
			n := int(b[0])<<8 | int(b[1])
			b = b[2:]
			if n == 0 || n > MaxHistoryRowBytes || n > len(b) {
				return History{}, invalidf("history row %d declares %d bytes, %d left", i, n, len(b))
			}
			h.Entries = append(h.Entries, HistoryEntry{Row: b[:n:n]})
			b = b[n:]
		case historyOmitted:
			if len(b) < 4 {
				return History{}, invalidf("history ends in entry %d", i)
			}
			h.Entries = append(h.Entries, HistoryEntry{Omitted: binary.BigEndian.Uint32(b)})
			b = b[4:]
		default:
			return History{}, invalidf("history entry %d has kind %d", i, kind)
		}
	}
	if len(b) != 0 {
		return History{}, invalidf("history has %d bytes left over", len(b))
	}
	if _, err := h.check(); err != nil {
		return History{}, err
	}
	return h, nil
}

// Fit returns the history cut to the caps of the frame: when it has too many
// entries or rows, or would be over [MaxHistoryPayload], the oldest entries
// are dropped (an omitted interval is shortened from its old end) and Oldest
// rises by the rows dropped, so that the frame says how much earlier history
// is missing. The rows kept are the newest. The receiver is not changed and
// the result shares its rows. A history that already fits is returned as it
// is.
func (h History) Fit() History {
	rows := h.Rows()
	size := historyHeaderLen
	for _, e := range h.Entries {
		size += e.size()
	}
	// Row bytes over the cap are the caller's fault, not a thing to cut.
	if rows <= MaxHistoryRows && len(h.Entries) <= MaxHistoryEntries && size <= MaxHistoryPayload {
		return h
	}
	out := History{Position: h.Position, Oldest: h.Oldest, Approximate: h.Approximate}
	entries := append([]HistoryEntry(nil), h.Entries...)
	for len(entries) > 0 && (rows > MaxHistoryRows || len(entries) > MaxHistoryEntries || size > MaxHistoryPayload) {
		e := entries[0]
		if e.Row == nil && e.Omitted > 1 && len(entries) <= MaxHistoryEntries && size <= MaxHistoryPayload {
			// Only the rows are over: shorten the interval from its old end.
			drop := min(uint64(e.Omitted-1), rows-MaxHistoryRows)
			entries[0].Omitted -= uint32(drop)
			rows -= drop
			out.Oldest += drop
			continue
		}
		rows -= e.rows()
		size -= e.size()
		out.Oldest += e.rows()
		entries = entries[1:]
	}
	out.Entries = entries
	return out
}
