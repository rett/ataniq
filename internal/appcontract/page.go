package appcontract

import (
	"errors"
	"math"
	"time"
	"unicode/utf8"

	"ataniq/internal/appmodel"
)

// EntryKind is the provenance label of a conversation entry. The authority
// assigns it; a client never does. KindWithheld is carried only by a page
// entry: it marks an authorized omission and holds no content.
type EntryKind string

const (
	KindReply       EntryKind = "reply"       // text the lead published to its reply outbox
	KindInstruction EntryKind = "instruction" // an inject or typed command, by its command key
	KindReceipt     EntryKind = "receipt"     // admission evidence of a command and its current status
	KindSystem      EntryKind = "system"      // a store or policy observation
	KindWithheld    EntryKind = "withheld"    // a content-free marker of an omission
)

// Disclosure is what one viewer may see of one entry, decided by the
// authority under its current disclosure projection.
type Disclosure uint8

const (
	// Disclosed: the viewer sees the entry.
	Disclosed Disclosure = iota
	// Withheld: the entry's existence is disclosable but its content is not;
	// the page shows a content-free marker in its place.
	Withheld
	// Hidden: even existence is hidden; the page shows nothing and no count.
	// A hidden entry consumes no sequence number of the page's message IDs: the
	// reader numbers the entries the projection does not hide without a gap (see
	// Snapshot.HiddenBelowFloor), so the visible IDs leave no trace of it.
	Hidden
)

// Entry is one record of an entry source, in sequence order within its
// segment. Disclosure is the reading viewer's, set by the source.
type Entry struct {
	Seq        uint64
	Kind       EntryKind
	Text       string
	At         time.Time
	Command    appmodel.CommandKey
	Receipt    *appmodel.AdmissionReceipt
	Disclosure Disclosure
}

// Snapshot is one consistent read of a conversation's readable segment for
// one viewer; the source takes it under a single guard so that entries, head
// and generations agree. The segment's first entry has sequence Start and its
// newest Head (0 while empty); entries below Floor were pruned by retention,
// so Floor > Start is a known loss. Entries holds the retained records,
// ascending, each with sequence from Floor through Head. Statuses holds the
// current status of the commands the entries name, under the viewer's rights.
//
// Entry sequences are the authority's, across every entry of the segment. The
// message ID of a page entry carries instead the entry's number among the
// entries the viewer's projection does not hide: its sequence less the entries
// hidden below it. HiddenBelowFloor is how many of the entries below Floor (the
// pruned ones) were hidden for this viewer, so that retention pruning leaves
// every ID as it was; it is at most Floor-Start. A viewer whose projection hides
// nothing sees the authority's sequence in its IDs.
type Snapshot struct {
	Segment, Projection uint64
	Start, Floor, Head  uint64
	HiddenBelowFloor    uint64
	Entries             []Entry
	Statuses            map[appmodel.CommandKey]appmodel.CurrentStatus
}

// EntrySource supplies snapshots. It must authorize the viewer before it
// answers, with an appmodel.DomainError (permission_denied or
// hidden_or_absent) when the viewer may not read the conversation.
type EntrySource interface {
	Snapshot(viewer string, conversation appmodel.ConversationRef, lead string) (Snapshot, error)
}

// Direction selects the way a read pages.
type Direction string

const (
	// Backward reads entries strictly before a boundary; with no cursor it
	// starts at the newest entries.
	Backward Direction = "backward"
	// Forward reads entries strictly after the newest one already examined.
	Forward Direction = "forward"
)

// PageState says what the reader does at the page's older edge.
type PageState string

const (
	// PageMore: older entries can be read with Page.Older (a forward page
	// has no older edge and is always PageMore, or PageGap).
	PageMore PageState = "more"
	// PageEnd: the authority accounts for the start of the readable segment;
	// show its beginning and stop paging.
	PageEnd PageState = "end"
	// PageGap: the read reached a known loss (retention pruned entries);
	// mark the break before the first entry. It carries no size.
	PageGap PageState = "gap"
	// PageReset: the continuation is expired or invalid, or the segment,
	// instance, viewer, direction or disclosure projection changed. Discard
	// the old page and cursors and read afresh; the wire code is
	// appmodel.ResetRequired. A reset page holds nothing else.
	PageReset PageState = "reset"
)

// PageEntry is one entry as a reader sees it. A withheld entry has only its ID
// and KindWithheld. Receipt is the immutable admission record and Status the
// current, separately revisioned status of the same command key.
type PageEntry struct {
	ID      appmodel.MessageID
	Kind    EntryKind
	Text    string
	At      time.Time
	Command *appmodel.CommandKey
	Receipt *appmodel.AdmissionReceipt
	Status  *appmodel.CurrentStatus
}

// Page is the one page value of a conversation read, shared by desk and seat.
// Entries ascend by sequence. Older continues a backward read and is empty
// at an end, a gap and a reset; Newer continues a forward read, is present on
// the first page and every forward page, and is empty at a reset. Clients
// pass tokens back unchanged.
type Page struct {
	State   PageState
	Entries []PageEntry
	Older   string
	Newer   string
}

// PageRequest is one read. Cursor is empty for the first read, which must be
// Backward. A Limit of 0 means DefaultPageEntries; a larger one than
// MaxPageEntries is cut to it.
type PageRequest struct {
	Viewer       string
	Project      appmodel.ProjectRef
	Conversation appmodel.ConversationRef
	Lead         string
	Direction    Direction
	Cursor       string
	Limit        int
}

// Page bounds. A page holds at most the entry count and the byte cost, where
// an entry costs EntryOverhead plus its text as JSON writes it (a character
// that JSON escapes costs its escape, up to six bytes, so that a page of such
// text cannot encode to several times its bound); it always holds one visible
// entry when one is available. The desk's wire caps an answer at 1 MiB
// (leadwire.MaxConversationResultBytes): the text of a full page is at most
// MaxPageBytes encoded, and 200 entries with every other field at its cap add
// about 3 KiB each beyond EntryOverhead, so the largest answer stays under
// 850 KiB; leadwire's TestWorstCasePageFitsTheResultCap holds that.
const (
	DefaultPageEntries = 50
	MaxPageEntries     = 200
	MaxPageBytes       = 256 << 10
	EntryOverhead      = 128
)

// PageReader pages an entry source. It authenticates nothing: its consumer
// supplies the viewer after authentication, and the source authorizes it.
type PageReader struct {
	instance Instance
	codec    *cursorCodec
	source   EntrySource
}

// NewPageReader binds a reader to one store instance and to the master secret
// of its cursor codec. A cursor minted for another instance, or under another
// master, does not decode here.
func NewPageReader(i Instance, master [32]byte, source EntrySource) (*PageReader, error) {
	if !validID(i.Authority) || source == nil {
		return nil, errInvalid
	}
	codec, err := newCursorCodec(master)
	if err != nil {
		return nil, err
	}
	return &PageReader{instance: i, codec: codec, source: source}, nil
}

func invalidInput() error { return appmodel.DomainError{Code: appmodel.InvalidInput} }

// Read returns one page. The order is fixed: the request's shape is checked,
// the source authorizes the viewer and snapshots, and only then is a cursor
// decoded, so a reader who may not read learns nothing from a cursor. A cursor
// that does not decode for an authorized reader is a reset page with a nil
// error, never a hint of why.
func (r *PageReader) Read(req PageRequest) (Page, error) {
	if r == nil || r.codec == nil || r.source == nil {
		return Page{}, errInvalid
	}
	if !validID(req.Viewer) || !validID(req.Lead) || !localProject(r.instance, req.Project) || !localConversation(r.instance, req.Conversation) ||
		(req.Direction != Backward && req.Direction != Forward) || (req.Cursor == "" && req.Direction != Backward) || req.Limit < 0 {
		return Page{}, invalidInput()
	}
	limit := req.Limit
	if limit == 0 {
		limit = DefaultPageEntries
	}
	limit = min(limit, MaxPageEntries)

	snap, err := r.source.Snapshot(req.Viewer, req.Conversation, req.Lead)
	if err != nil {
		var domain appmodel.DomainError
		if errors.As(err, &domain) {
			return Page{}, domain
		}
		return Page{}, appmodel.DomainError{Code: appmodel.HiddenOrAbsent}
	}
	if err := r.validSnapshot(snap); err != nil {
		return Page{}, err
	}
	ctx := pageCursor{viewer: req.Viewer, project: req.Project.ID, conversation: req.Conversation.ID, lead: req.Lead,
		forward: req.Direction == Forward, projection: snap.Projection}
	vseq := visibleSeqs(snap)
	var position uint64
	if req.Cursor != "" {
		position, err = r.codec.decode(r.instance, ctx, req.Cursor, snap.Segment, snap.Head)
		if err != nil {
			return Page{State: PageReset}, nil
		}
	}
	page := Page{State: PageMore}
	switch {
	case req.Direction == Forward:
		var next uint64
		page.Entries, page.State, next = r.forward(req, snap, vseq, position, limit)
		return r.withNewer(req, snap, page, next)
	case req.Cursor == "":
		var exhausted bool
		page.Entries, exhausted, position = r.backward(req, snap, vseq, snap.Head+1, limit)
		page.State = r.olderState(snap, exhausted)
		if page, err = r.withOlder(req, snap, page, position); err != nil {
			return Page{}, err
		}
		return r.withNewer(req, snap, page, snap.newestNumber())
	default:
		var exhausted bool
		page.Entries, exhausted, position = r.backward(req, snap, vseq, position, limit)
		page.State = r.olderState(snap, exhausted)
		return r.withOlder(req, snap, page, position)
	}
}

// olderState is the state of a backward page: more when the bound stopped the
// read, else the start of the segment (end) or a known loss before the oldest
// retained entry (gap).
func (r *PageReader) olderState(s Snapshot, exhausted bool) PageState {
	switch {
	case !exhausted:
		return PageMore
	case s.prunedVisible() > 0:
		// Only a pruned entry this viewer could see is a loss to it; a pruned
		// hidden one must not show through as a gap.
		return PageGap
	}
	return PageEnd
}

func (r *PageReader) withOlder(req PageRequest, s Snapshot, p Page, position uint64) (Page, error) {
	if p.State != PageMore {
		return p, nil
	}
	ctx := pageCursor{viewer: req.Viewer, project: req.Project.ID, conversation: req.Conversation.ID, lead: req.Lead, projection: s.Projection}
	tok, err := r.codec.mint(r.instance, ctx, position, s.Segment, s.Head)
	if err != nil {
		return Page{}, err
	}
	p.Older = tok
	return p, nil
}

func (r *PageReader) withNewer(req PageRequest, s Snapshot, p Page, position uint64) (Page, error) {
	ctx := pageCursor{viewer: req.Viewer, project: req.Project.ID, conversation: req.Conversation.ID, lead: req.Lead, forward: true, projection: s.Projection}
	tok, err := r.codec.mint(r.instance, ctx, position, s.Segment, s.Head)
	if err != nil {
		return Page{}, err
	}
	p.Newer = tok
	return p, nil
}

func (r *PageReader) validSnapshot(s Snapshot) error {
	if s.Segment == 0 || s.Segment > math.MaxInt64 || s.Projection > math.MaxInt64 ||
		s.Start == 0 || s.Head > math.MaxInt64 || s.Floor < s.Start || s.Floor > s.Head+1 ||
		s.HiddenBelowFloor > s.Floor-s.Start {
		return errInvalid
	}
	prev := s.Floor - 1
	for _, e := range s.Entries {
		if e.Seq <= prev || e.Seq > s.Head || e.Disclosure > Hidden || len(e.Text) > appmodel.MaxTextBytes || !utf8.ValidString(e.Text) {
			return errInvalid
		}
		prev = e.Seq
		switch e.Kind {
		case KindReply, KindSystem:
			if e.Receipt != nil || e.Command != (appmodel.CommandKey{}) {
				return errInvalid
			}
		case KindInstruction, KindReceipt:
			if !r.validCommand(e.Command) || (e.Kind == KindReceipt) != (e.Receipt != nil) || (e.Receipt != nil && e.Receipt.Command != e.Command) {
				return errInvalid
			}
		default:
			return errInvalid
		}
	}
	for k, st := range s.Statuses {
		if !r.validCommand(k) || st.Command != k {
			return errInvalid
		}
	}
	return nil
}

func (r *PageReader) validCommand(k appmodel.CommandKey) bool {
	return k.Authority == r.instance.Authority && validID(k.EnrolledClientID) && validID(string(k.CommandID))
}

// prunedVisible is how many of the entries retention pruned this viewer could
// see. They hold the numbers Start through Start+prunedVisible-1.
func (s Snapshot) prunedVisible() uint64 { return s.Floor - s.Start - s.HiddenBelowFloor }

// newestNumber is the number of the newest entry the viewer's projection does
// not hide (Start-1 while it has none), which is where a forward read of
// everything stops: the segment's head less every entry hidden up to it.
func (s Snapshot) newestNumber() uint64 {
	n := s.Head - s.HiddenBelowFloor
	for _, e := range s.Entries {
		if e.Disclosure == Hidden {
			n--
		}
	}
	return n
}

// visibleSeqs numbers the snapshot's entries for their message IDs, parallel to
// s.Entries: an entry's sequence less the entries hidden below it, counting the
// pruned ones through HiddenBelowFloor. A hidden entry has no number of its own
// and is never shown, so the numbers of the others have no gap where it was.
// validSnapshot has checked that no number falls below Start.
func visibleSeqs(s Snapshot) []uint64 {
	out := make([]uint64, len(s.Entries))
	hidden := s.HiddenBelowFloor
	for i, e := range s.Entries {
		if e.Disclosure == Hidden {
			hidden++
			continue
		}
		out[i] = e.Seq - hidden
	}
	return out
}

func (r *PageReader) entry(req PageRequest, s Snapshot, e Entry, seq uint64) PageEntry {
	id := appmodel.MessageID{Authority: r.instance.Authority, Lead: req.Lead, Conversation: req.Conversation.ID, Generation: s.Segment, Seq: seq}
	if e.Disclosure == Withheld {
		return PageEntry{ID: id, Kind: KindWithheld}
	}
	p := PageEntry{ID: id, Kind: e.Kind, Text: e.Text, At: e.At}
	if e.Kind == KindInstruction || e.Kind == KindReceipt {
		k := e.Command
		p.Command = &k
		if st, ok := s.Statuses[k]; ok {
			p.Status = &st
		}
	}
	if e.Receipt != nil {
		rc := *e.Receipt
		p.Receipt = &rc
	}
	return p
}

func cost(p PageEntry) int { return EntryOverhead + escapedLen(p.Text) }

// escapedLen is the size in bytes of s inside a JSON string as Go's encoder
// writes it, with its HTML escaping on, the largest form: a quote, a backslash
// and a line feed, carriage return or tab take two bytes, any other control
// character, '<', '>', '&' and the separators U+2028 and U+2029 take six, and
// every other character its own length. s must be valid UTF-8.
func escapedLen(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t':
			n += 2
		case r < 0x20 || r == '<' || r == '>' || r == '&' || r == 0x2028 || r == 0x2029:
			n += 6
		default:
			n += utf8.RuneLen(r)
		}
	}
	return n
}

// backward returns the entries with sequence below before, newest first
// until the count or byte bound, ordered ascending. A hidden entry is examined,
// so the boundary passes it, but it is neither shown nor counted. exhausted
// reports that nothing retained lies below the returned boundary, which is the
// lowest sequence examined.
func (r *PageReader) backward(req PageRequest, s Snapshot, vseq []uint64, before uint64, limit int) (entries []PageEntry, exhausted bool, lowest uint64) {
	var rev []PageEntry
	bytes := 0
	lowest = before
	i := len(s.Entries) - 1
	for i >= 0 && s.Entries[i].Seq >= before {
		i--
	}
	for ; i >= 0; i-- {
		e := s.Entries[i]
		if e.Disclosure != Hidden {
			p := r.entry(req, s, e, vseq[i])
			if len(rev) == limit || (len(rev) > 0 && bytes+cost(p) > MaxPageBytes) {
				break
			}
			rev = append(rev, p)
			bytes += cost(p)
		}
		lowest = e.Seq
	}
	entries = make([]PageEntry, len(rev))
	for j, p := range rev {
		entries[len(rev)-1-j] = p
	}
	return entries, i < 0, lowest
}

// forward returns the entries numbered above after, oldest first, under the
// same bounds, and the position the next forward read resumes from: the number
// of the newest entry read. A forward position is a message number, not an
// authority sequence, because the numbers are the viewer's: whether a pruned
// entry was unread and visible is then exact, however many hidden ones are
// mixed with the visible ones below it. Its state is PageGap when retention
// pruned an entry the viewer could see and had not read (a number above after
// among the pruned ones, Start through Start+prunedVisible-1), else PageMore.
func (r *PageReader) forward(req PageRequest, s Snapshot, vseq []uint64, after uint64, limit int) (entries []PageEntry, state PageState, next uint64) {
	state = PageMore
	if v := s.prunedVisible(); v > 0 && after+1 < s.Start+v {
		state = PageGap
	}
	entries = []PageEntry{}
	bytes := 0
	next = s.newestNumber()
	for i, e := range s.Entries {
		if e.Disclosure != Hidden && vseq[i] > after {
			p := r.entry(req, s, e, vseq[i])
			if len(entries) == limit || (len(entries) > 0 && bytes+cost(p) > MaxPageBytes) {
				return entries, state, vseq[i] - 1
			}
			entries = append(entries, p)
			bytes += cost(p)
		}
	}
	return entries, state, next
}
