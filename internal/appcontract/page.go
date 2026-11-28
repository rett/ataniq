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
type Snapshot struct {
	Segment, Projection uint64
	Start, Floor, Head  uint64
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
// an entry costs EntryOverhead plus its text; it always holds one visible
// entry when one is available, as a single text is far below MaxPageBytes.
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
		page.Entries, page.State, next = r.forward(req, snap, position, limit)
		return r.withNewer(req, snap, page, next)
	case req.Cursor == "":
		var exhausted bool
		page.Entries, exhausted, position = r.backward(req, snap, snap.Head+1, limit)
		page.State = r.olderState(snap, exhausted)
		if page, err = r.withOlder(req, snap, page, position); err != nil {
			return Page{}, err
		}
		return r.withNewer(req, snap, page, snap.Head)
	default:
		var exhausted bool
		page.Entries, exhausted, position = r.backward(req, snap, position, limit)
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
	case s.Floor > s.Start:
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
		s.Start == 0 || s.Head > math.MaxInt64 || s.Floor < s.Start || s.Floor > s.Head+1 {
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

func (r *PageReader) entry(req PageRequest, s Snapshot, e Entry) PageEntry {
	id := appmodel.MessageID{Authority: r.instance.Authority, Lead: req.Lead, Conversation: req.Conversation.ID, Generation: s.Segment, Seq: e.Seq}
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

func cost(p PageEntry) int { return EntryOverhead + len(p.Text) }

// backward returns the entries with sequence below before, newest first
// until the count or byte bound, ordered ascending. A hidden entry is examined,
// so the boundary passes it, but it is neither shown nor counted. exhausted
// reports that nothing retained lies below the returned boundary, which is the
// lowest sequence examined.
func (r *PageReader) backward(req PageRequest, s Snapshot, before uint64, limit int) (entries []PageEntry, exhausted bool, lowest uint64) {
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
			p := r.entry(req, s, e)
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

// forward returns the entries with sequence above after, oldest first, under
// the same bounds, and the position the next forward read resumes from: the
// newest sequence examined. Its state is PageGap when retention pruned entries
// the reader never saw (after+1 is below the oldest retained), else PageMore.
func (r *PageReader) forward(req PageRequest, s Snapshot, after uint64, limit int) (entries []PageEntry, state PageState, next uint64) {
	state = PageMore
	if after+1 < s.Floor {
		state = PageGap
	}
	entries = []PageEntry{}
	bytes := 0
	next = s.Head
	for _, e := range s.Entries {
		if e.Seq <= after {
			continue
		}
		if e.Disclosure != Hidden {
			p := r.entry(req, s, e)
			if len(entries) == limit || (len(entries) > 0 && bytes+cost(p) > MaxPageBytes) {
				return entries, state, e.Seq - 1
			}
			entries = append(entries, p)
			bytes += cost(p)
		}
	}
	return entries, state, next
}
