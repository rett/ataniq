package leadwire

// The desk's chat switch: two operations of their own, kept apart from the
// six of the lead control plane. A daemon in scratch mode lists them in its
// hello after the six; a client that looks for lead control counts only
// [Operations], so these two never make a daemon read as another version of
// it. Only the daemon's own user reaches them, on the Unix socket: the seat
// gate does not serve them, and nothing of the seat can call them.
//
//	chat.list  no params        -> [ChatListResult]
//	chat.set   [ChatSetParams]  -> [ChatLead], the lead's row after the set
//
// chat.set is idempotent: the same switch set twice changes nothing the
// second time, so it takes no command id and keeps no receipt. A lead's
// switch is off or read; send is not built, and is refused
// ([CodeChatSendNotBuilt]).
const (
	OpChatList = "chat.list"
	OpChatSet  = "chat.set"
)

// ChatOperations returns the two chat operations in the order the daemon
// lists them: chat.list, chat.set. The slice is a new one on every call.
func ChatOperations() []string {
	return []string{OpChatList, OpChatSet}
}

// The values of a lead's chat switch. ChatSend is named so that a desk can
// say it is refused; no daemon of this build accepts it.
const (
	ChatOff  = "off"
	ChatRead = "read"
	ChatSend = "send"
)

// The error codes of chat.set beside the four of [ErrorCodes] (it answers
// invalid-request, refused for a key that is not a lead of the daemon, and
// unavailable for a lead whose chat cannot be used).
const (
	// CodeChatSendNotBuilt: the switch asked for is send, which is not built.
	CodeChatSendNotBuilt = "chat-send-not-built"
	// CodeChatKeyUnrepresentable: the lead's key is longer than a seat's
	// lead identifier holds (32 bytes), so the seat could never show it and
	// its chat is never turned on.
	CodeChatKeyUnrepresentable = "chat-key-unrepresentable"
)

// ChatErrorCodes returns the two chat error codes above, in a new slice.
func ChatErrorCodes() []string {
	return []string{CodeChatSendNotBuilt, CodeChatKeyUnrepresentable}
}

type (
	// ChatSetParams are the params of chat.set: the lead's key and the
	// switch, off or read. Both keys are required.
	ChatSetParams struct {
		Key  string `json:"key"`
		Chat string `json:"chat"`
	}

	// ChatListResult is the result of chat.list: one row per configured
	// lead, in the order of the config.
	ChatListResult struct {
		Leads []ChatLead `json:"leads"`
	}

	// ChatLead is one lead's chat as the desk sees it. It holds counts and
	// words only: never a reply's text, a file name or a path.
	ChatLead struct {
		// Key is the name in the daemon's config.
		Key string `json:"key"`
		// Chat is the switch: off or read. Every lead is off when the
		// daemon starts.
		Chat string `json:"chat"`
		// Seat is false for a lead whose key is longer than a seat's lead
		// identifier holds: the seat never lists it, and its chat stays off.
		Seat bool `json:"seat"`
		// Suspended is true when the daemon stopped the lead's chat (its
		// reply folder was replaced, opened to others, or held too much):
		// the seat reads nothing of it, and only a fresh chat.set read
		// takes replies again.
		Suspended bool `json:"suspended"`
		// Unavailable is true when the lead's chat store cannot be used
		// (it could not be opened, or a write to it failed): the lead has
		// no chat until the daemon is restarted, and the other leads are
		// untouched.
		Unavailable bool `json:"unavailable"`
		// Head is the highest sequence number the lead's chat has given, 0
		// when none was. It never goes down.
		Head int64 `json:"head"`
		// Entries is how many entries the chat holds now.
		Entries int `json:"entries"`
		// Dropped counts, by reason, the files the daemon removed or left
		// since it started; a reason with none is left out.
		Dropped map[string]int `json:"dropped"`
	}
)
