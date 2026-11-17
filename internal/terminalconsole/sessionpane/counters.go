package sessionpane

// Counters are the times a cap was hit, since the pane was made. A cap that is
// hit is counted and never silent. The values only grow, and a restore does
// not reset them: they count what this pane did with the bytes it was given.
type Counters struct {
	// The first six are the emulator's (the patched x/vt).

	// ClusterBytesDropped is the number of bytes of grapheme clusters
	// dropped because the cluster was already 64 bytes long.
	ClusterBytesDropped uint64
	// ModesIgnored is the number of times a mode that is not in the table of
	// recorded modes was set or reset.
	ModesIgnored uint64
	// LinksOverLength is the number of hyperlinks dropped because the URL was
	// over 2 KiB or the parameters over 256 bytes.
	LinksOverLength uint64
	// LinksOverLimit is the number of cells drawn without their link because
	// the screen already held 256 distinct links.
	LinksOverLimit uint64
	// StringsTruncated is the number of OSC, DCS, SOS, PM and APC strings
	// that were longer than 64 KiB and lost their end.
	StringsTruncated uint64
	// RepeatsCut is the number of REP sequences (CSI n b) whose count was
	// over two lines and two characters, and so was done a line at a time or
	// cut to that count.
	RepeatsCut uint64

	// The last two are the pane's own.

	// ClipboardRequestsBlocked is the number of clipboard requests (OSC 52, a
	// write or a query) the session made. None was passed on.
	ClipboardRequestsBlocked uint64
	// TitlesCut is the number of titles that lost characters to the cap of
	// MaxTitle bytes.
	TitlesCut uint64
}

// CounterValue is the name and the value of one counter, for display.
type CounterValue struct {
	Name  string
	Value uint64
}

// List returns the counters as an ordered list of name and value: the
// emulator's first, then the pane's, in the order of the fields. The names are
// lower case words joined by underscores, and stay as they are.
func (c Counters) List() []CounterValue {
	return []CounterValue{
		{"cluster_bytes_dropped", c.ClusterBytesDropped},
		{"modes_ignored", c.ModesIgnored},
		{"links_over_length", c.LinksOverLength},
		{"links_over_limit", c.LinksOverLimit},
		{"strings_truncated", c.StringsTruncated},
		{"repeats_cut", c.RepeatsCut},
		{"clipboard_requests_blocked", c.ClipboardRequestsBlocked},
		{"titles_cut", c.TitlesCut},
	}
}

// Counters returns the pane's counters. They are readable at any time,
// after End and Close too.
func (p *Pane) Counters() Counters {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.emu.Counters()
	return Counters{
		ClusterBytesDropped:      e.ClusterBytesDropped,
		ModesIgnored:             e.ModesIgnored,
		LinksOverLength:          e.LinksOverLength,
		LinksOverLimit:           e.LinksOverLimit,
		StringsTruncated:         e.StringsTruncated,
		RepeatsCut:               e.RepeatsCut,
		ClipboardRequestsBlocked: uint64(p.clipboard),
		TitlesCut:                p.titlesCut,
	}
}
