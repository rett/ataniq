package leadwire

import (
	"encoding/json"
	"time"
)

// Params of the operations. Every key is required, so none has omitempty. The
// order of the fields is the order of the keys on the wire and of the digest.
type (
	// ListParams are the params of lead.list: none. A client sends no params
	// or an empty object.
	ListParams struct{}

	// DefineParams are the params of lead.define.
	DefineParams struct {
		Client    string `json:"client"`
		CommandID string `json:"command_id"`
		Role      string `json:"role"`
	}

	// StartParams are the params of lead.start.
	StartParams struct {
		Client    string `json:"client"`
		CommandID string `json:"command_id"`
		Role      string `json:"role"`
	}

	// StopParams are the params of lead.stop. An Epoch of 0 means the role's
	// current epoch.
	StopParams struct {
		Client    string `json:"client"`
		CommandID string `json:"command_id"`
		Role      string `json:"role"`
		Epoch     uint64 `json:"epoch"`
	}

	// TicketParams are the params of lead.ticket. AttachRole is
	// [AttachWriter] or [AttachViewer].
	TicketParams struct {
		Client     string `json:"client"`
		CommandID  string `json:"command_id"`
		Role       string `json:"role"`
		Epoch      uint64 `json:"epoch"`
		AttachRole string `json:"attach_role"`
	}

	// InjectParams are the params of lead.inject. Epoch is the epoch the
	// sender saw, so a delayed command cannot land in a newer epoch.
	InjectParams struct {
		Client    string `json:"client"`
		CommandID string `json:"command_id"`
		Role      string `json:"role"`
		Epoch     uint64 `json:"epoch"`
		MessageID string `json:"message_id"`
		Body      string `json:"body"`
	}
)

// Results of the operations, as the JSON in [Receipt.Result] (and, for
// lead.list, as the whole result).
type (
	// ListResult is the result of lead.list: the daemon's cap, how many
	// leads count against it, and one row per configured lead.
	ListResult struct {
		MaxRunning int    `json:"max_running"`
		Running    int    `json:"running"`
		Leads      []Lead `json:"leads"`
	}

	// Lead is one row of lead.list.
	Lead struct {
		// Key is the name in the daemon's config; Role is "scratch-<key>/lead".
		Key  string `json:"key"`
		Role string `json:"role"`
		// Defined: the registry knows the role.
		Defined bool `json:"defined"`
		// Provision is [ProvisionDone], [ProvisionIncomplete] or
		// [ProvisionNone].
		Provision string `json:"provision"`
		// State is the registry's shown state of the role.
		State string `json:"state"`
		// StateReason is the reason code the registry shows with State, such
		// as "holder-ready" with "ready". It is a fixed word (lower-case
		// letters, digits and dashes, the first a letter, at most
		// [MaxStateReason] bytes); the daemon sends "" for a code that is
		// not, and for a lead the registry does not know. The key is always
		// present.
		StateReason string `json:"state_reason"`
		Epoch       uint64 `json:"epoch"`
		Unit        string `json:"unit"`
		// Inject: the lead's config allows lead.inject.
		Inject bool `json:"inject"`
		// MessageSpent: the epoch's one message was already sent.
		MessageSpent bool `json:"message_spent"`
		// MessageResult is what the holder answered about the current
		// epoch's message, in its three words, when the daemon has it: all
		// three are "" when the epoch's message was not sent, when it was
		// sent and the daemon has no answer (it was restarted since, and
		// the lead's last receipt is not that message's), and for a lead the
		// registry does not know. The keys are always present.
		MessageResult MessageResult `json:"message_result"`
		// Last is the role's latest receipt, or null when it has none. In a
		// row it is the summary form: its Result and its Message are left
		// out, because the row's state, state reason, epoch, unit and
		// message result say what they would, and eight rows with full
		// receipts would not fit in 8 KiB (see [MaxRelayBytes]). A replay of
		// the command id gives the full receipt.
		Last *Receipt `json:"last"`
	}

	// MessageResult is the three words of a [Delivery] without its detail, as
	// a row of lead.list carries them. Each is a fixed word of at most
	// [MaxDeliveryWord] bytes, or "" when the daemon has no answer.
	MessageResult struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
		Written string `json:"written"`
	}

	// DefineResult is the result of lead.define.
	DefineResult struct {
		Role          string `json:"role"`
		State         string `json:"state"`
		ProfileDigest string `json:"profile_digest"`
	}

	// StartResult is the result of lead.start.
	StartResult struct {
		Role   string `json:"role"`
		State  string `json:"state"`
		Epoch  uint64 `json:"epoch"`
		Unit   string `json:"unit"`
		Waiver string `json:"waiver"`
	}

	// StopResult is the result of lead.stop.
	StopResult struct {
		Role  string `json:"role"`
		State string `json:"state"`
		Proof string `json:"proof"`
	}

	// TicketResult is the result of lead.ticket. The ticket itself is never
	// in it: it is in the ticket file, which the console reads.
	TicketResult struct {
		Role       string `json:"role"`
		Epoch      uint64 `json:"epoch"`
		TicketFile string `json:"ticket_file"`
		Folder     string `json:"folder"`
	}

	// InjectResult is the result of lead.inject. Result is what the holder
	// answered about the delivery.
	InjectResult struct {
		Role      string   `json:"role"`
		Epoch     uint64   `json:"epoch"`
		MessageID string   `json:"message_id"`
		Result    Delivery `json:"result"`
	}

	// Delivery is the holder's answer to a delivery that was tried: three
	// words from closed sets and a detail for a person reading a log. It
	// has the member names of the holder's inject result, which the daemon
	// copies into it.
	Delivery struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason,omitempty"`
		Written string `json:"written"`
		Detail  string `json:"detail,omitempty"`
	}
)

// Receipt is the durable answer to a mutating command. Acceptance is the
// admission of the command, not its completion: a command is carried out at
// most once, and a replay of the same client, command id and payload returns
// the stored receipt, whatever its status.
type Receipt struct {
	CommandID string `json:"command_id"`
	// Op is one of the five mutating operation names.
	Op   string `json:"op"`
	Role string `json:"role"`
	// Status says where the command stands.
	Status Status `json:"status"`
	// Code is the reason code of a refused or unavailable command.
	Code string `json:"code,omitempty"`
	// Message is a fixed text that echoes nothing a peer sent.
	Message string `json:"message,omitempty"`
	// Result is the JSON of the operation's result struct, when the command
	// is done.
	Result     json.RawMessage `json:"result,omitempty"`
	AdmittedAt time.Time       `json:"admitted_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}
