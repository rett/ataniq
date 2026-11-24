package seatapp

import (
	"strconv"
	"time"
)

// The Leads pages' view of the hint stream (watchstream.go): the directory
// (/leads/) and a conversation (/leads/daemon/{key}) open the stream with
// the chat cursor they were rendered with ({"leads":"<cursor>"}), and the
// stream says when the cursor moves, as one kind of event, streamKindChat,
// whose data is the cursor alone: a decimal integer, never a lead, a head, a
// state, a count or a text. The page then reads again through its own
// authenticated GET, which asks seat.chat, where the desk authorizes every
// list and read live (see watch.js for what the page does with it).
//
// The cursor is the port's chat hint ([ChatWatcher]), which the seat's one
// watcher learns on the one seat.wait it already holds: a Leads stream asks
// the gate nothing, as a card list's does not. The cursor covers the leads
// whose chat the desk switched on and nothing else, so a lead whose chat is
// off never makes an event, and the seat never learns of its activity.
//
// The view is live only while the hint is live and the port offers chat; a
// stream ends when either stops (a lost link, a gate that stops offering
// it, a downgrade) or when the hint is of another connection than the one
// it was admitted on, and is refused "not live" while either is so. A Leads
// stream is no check-in for the notices: it takes no foreground lease and
// counts no card as seen.

// chatLive reports whether a hint can be streamed: live, with a cursor a
// page's script can carry.
func chatLive(h ChatHint) bool {
	return h.Live && h.Cursor >= 1 && h.Cursor <= MaxChatSeq
}

// chatCursor is the cursor of h, 0 for a hint that is not live.
func chatCursor(h ChatHint) int64 {
	if !chatLive(h) {
		return 0
	}
	return h.Cursor
}

// chatToken is the cursor of h as the page carries it: decimal, "0" for a
// hint that is not live.
func chatToken(h ChatHint) string { return strconv.FormatInt(chatCursor(h), 10) }

// validChatToken reports whether s is a cursor as a page carries it: 0 to
// MaxChatSeq in decimal, with no sign, no leading zero and no other
// character, at most 16 digits.
func validChatToken(s string) bool {
	if s == "" || len(s) > 16 || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && chatNumber(n)
}

// chatEvent returns the bytes of a chat-changed event for the cursor tok,
// and false for a token that is not a cursor of a live hint.
func chatEvent(tok string) ([]byte, bool) {
	if !validChatToken(tok) || tok == "0" {
		return nil, false
	}
	return sseEvent(streamKindChat, []byte(tok))
}

// chatWatcher returns the port's chat hint and its chat port, when it has
// both.
func (a *app) chatWatcher() (ChatWatcher, Chat, bool) {
	cw, ok := a.cfg.Daemon.(ChatWatcher)
	if !ok {
		return nil, nil, false
	}
	p, ok := a.cfg.Daemon.(Chat)
	return cw, p, ok
}

// leadsSource is the Leads view's source, made for one stream: the chat
// hint's broadcast, and the cursor, live while the hint is, the port offers
// chat (both are local looks, which send nothing) and the hint is of the
// connection the stream was admitted on. The first look that finds the view
// live, the admission's, binds the stream to the hint's connection: a hint
// of a later connection is not live for it, so the stream ends and the page
// opens another with what it shows, and no cursor of one connection is said
// on a stream admitted on another. The looks are the handler's, on one
// goroutine.
func (a *app) leadsSource() (streamSource, bool) {
	cw, p, ok := a.chatWatcher()
	if !ok {
		return streamSource{}, false
	}
	var conn time.Time
	bound := false
	return streamSource{
		changed: cw.ChatHintChanged,
		current: func() (string, bool) {
			h := cw.ChatHint()
			live := chatLive(h) && p.ChatOffered()
			if live && !bound {
				conn, bound = h.Connection, true
			}
			return chatToken(h), live && h.Connection.Equal(conn)
		},
		event: chatEvent,
	}, true
}

// leadsWatch is what a Leads page carries for its stream (the template part
// "watch-leads"): whether the page may open one, the chat cursor taken
// before the page's own read, which page it is, and, for a conversation that
// shows the newest entries, the cursor its following read is made after.
// Every field is the app's own.
type leadsWatch struct {
	Live bool
	// Chat is the cursor, 0 when there was none.
	Chat int64
	// Conversation marks a conversation's page; the directory's is not.
	Conversation bool
	// Follow marks a conversation page whose read reached the head: it
	// shows the newest entries, so the page may follow new replies. After
	// is then the cursor of the read that follows them (see followAfter).
	Follow bool
	After  int64
	// connection is the connection the cursor was learned on. It stays in
	// the app: a page whose reads came from another connection carries no
	// cursor (see [leadsWatch.boundTo]).
	connection time.Time
}

// boundTo returns lw when its cursor was learned on the connection the
// page's reads came from, and a mark that is not live otherwise: a cursor of
// one connection is never put on a page read through another, where it
// would compare against the wrong chat. The page then shows "Not live" and
// its Refresh link.
func (lw leadsWatch) boundTo(connection time.Time) leadsWatch {
	if lw.Live && lw.connection.Equal(connection) {
		return lw
	}
	return leadsWatch{}
}

// leadsHint takes the chat hint for a Leads page, with the connection it
// was learned on. It must run before the page's own reads, so that a change
// between the two makes the page's cursor older than what it shows, and the
// page's first stream says chat-changed at once, never the other way round;
// the page keeps it only when its reads came from the same connection
// ([leadsWatch.boundTo]).
func (a *app) leadsHint() leadsWatch {
	var lw leadsWatch
	cw, p, ok := a.chatWatcher()
	if !ok {
		return lw
	}
	h := cw.ChatHint()
	if chatLive(h) && p.ChatOffered() {
		lw.Live, lw.Chat, lw.connection = true, chatCursor(h), h.Connection
	}
	return lw
}
