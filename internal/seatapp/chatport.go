package seatapp

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Chat is the optional read-only port. Checking the offer uses only local
// state. A call is ErrDaemonUnavailable while the port holds no connection
// to the gate and ErrNotOffered when the connected gate does not offer
// chat; neither sends anything. The pages tell the two apart by that error.
type Chat interface {
	ChatOffered() bool
	Chat(context.Context, ChatCall) (ChatView, error)
}

const (
	ChatLeads        = "leads"
	ChatRead         = "read"
	MaxChatLeads     = 8
	MaxChatEntries   = 16
	MaxChatTextBytes = 2000
	MaxChatKeyBytes  = 32
	MaxChatSeq       = 1<<53 - 1
)

var (
	ErrChatOff         = errors.New("chat is off")
	ErrChatUnknownLead = errors.New("unknown lead")
	chatKey            = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

type ChatCall struct {
	Verb, Lead string
	After      int64
	// SessionDone scopes cached answers to a login. Closed on session end.
	SessionDone <-chan struct{}
}

type LeadRow struct {
	Lead, Chat, State string
	Epoch, Head       *int64
}

type ChatEntry struct {
	Seq  int64
	Who  string
	At   time.Time
	Text string
	Cut  bool
}

type ChatView struct {
	State   string
	StandIn bool
	// Connection identifies the negotiated gate connection, locally.
	Connection  time.Time
	Rows        []LeadRow
	Lead        string
	Epoch, Head int64
	Entries     []ChatEntry
	More, Reset bool
}

func validChatLead(id string) bool {
	key, ok := strings.CutPrefix(id, "daemon:")
	return ok && chatKey.MatchString(key)
}
func chatNumber(n int64) bool { return n >= 0 && n <= MaxChatSeq }

func (r LeadRow) valid() bool {
	if !validChatLead(r.Lead) {
		return false
	}
	if r.Chat == "off" {
		return r.State == "" && r.Head == nil && r.Epoch == nil
	}
	if r.Chat != "read" && r.Chat != "send" {
		return false
	}
	switch r.State {
	case "running", "starting", "stopped", "unknown":
		return r.Epoch != nil && chatNumber(*r.Epoch) && r.Head != nil && chatNumber(*r.Head)
	}
	return false
}

func validChatText(s string) bool {
	if len(s) > MaxChatTextBytes || !utf8.ValidString(s) || strings.Trim(s, " \n") == "" {
		return false
	}
	for _, r := range s {
		if r != '\n' && !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// ValidFor checks the app's own sizes, words, times and cursor semantics.
// Both the wire adapter and the pages check the port boundary.
func (v ChatView) ValidFor(c ChatCall) bool {
	if v.State != c.Verb {
		return false
	}
	switch v.State {
	case ChatLeads:
		if c.Lead != "" || c.After != 0 || v.Lead != "" || v.Head != 0 || v.Epoch != 0 || v.More || v.Reset || v.Entries != nil || v.Rows == nil || len(v.Rows) > MaxChatLeads {
			return false
		}
		seen := map[string]bool{}
		for _, r := range v.Rows {
			if !r.valid() || seen[r.Lead] {
				return false
			}
			seen[r.Lead] = true
		}
		return true
	case ChatRead:
		if v.Rows != nil || !validChatLead(v.Lead) || v.Lead != c.Lead || !chatNumber(c.After) || !chatNumber(v.Head) || !chatNumber(v.Epoch) || v.Entries == nil || len(v.Entries) > MaxChatEntries {
			return false
		}
		last := int64(0)
		if !v.Reset {
			last = c.After
		}
		for _, e := range v.Entries {
			_, offset := e.At.Zone()
			if e.Seq <= last || e.Seq > v.Head || (e.Who != "lead" && e.Who != "system") || !validChatText(e.Text) || offset != 0 || e.At.Nanosecond() != 0 || e.At.Year() < 0 || e.At.Year() > 9999 {
				return false
			}
			last = e.Seq
		}
		return !v.More || (len(v.Entries) > 0 && last < v.Head && last != c.After)
	}
	return false
}
