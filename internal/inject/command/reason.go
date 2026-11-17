package command

// Reason is a stable code for why a command was refused, requeued, made
// uncertain or resolved. The codes are written to the journal; do not change
// their spelling.
type Reason string

// Reasons for refusing a message at admission. None of them queues anything.
const (
	// ReasonBadSender: the sender or the target is the zero value.
	ReasonBadSender Reason = "bad-sender"
	// ReasonBadProject: a project name does not match the name grammar.
	ReasonBadProject Reason = "bad-project"
	// ReasonEdge: the sender may not send to the target.
	ReasonEdge Reason = "edge-refused"
	// ReasonBadID: the command id does not match the name grammar.
	ReasonBadID Reason = "bad-id"
	// ReasonBadKind: the kind does not match the name grammar.
	ReasonBadKind Reason = "bad-kind"
	// ReasonDuplicateID: a command with this id exists.
	ReasonDuplicateID Reason = "duplicate-id"
	// ReasonEmptyBody: the body has no character but spaces and newlines.
	ReasonEmptyBody Reason = "empty-body"
	// ReasonInvalidUTF8: the body is not valid UTF-8.
	ReasonInvalidUTF8 Reason = "invalid-utf8"
	// ReasonBadCharacter: the body has a character that is neither a newline
	// nor printable.
	ReasonBadCharacter Reason = "bad-character"
	// ReasonForgedHeader: a body line starts with the header marker.
	ReasonForgedHeader Reason = "forged-header"
	// ReasonTooLong: the whole text is over the size cap.
	ReasonTooLong Reason = "text-too-long"
	// ReasonQueueFull: the target's queue already holds the most commands.
	ReasonQueueFull Reason = "queue-full"
	// ReasonQueueBytes: the target's queue would hold more than the byte cap.
	ReasonQueueBytes Reason = "queue-bytes"
	// ReasonRate: the sender sent more than the rate cap in the window.
	ReasonRate Reason = "rate-limit"
)

// Reasons for a command refused after it was admitted.
const (
	// ReasonNotIdle: the guard never found the lead idle within the queue
	// time limit.
	ReasonNotIdle Reason = "not-idle"
	// ReasonUntestedVersion: the runtime version is not in the tested table.
	ReasonUntestedVersion Reason = "untested-version"
	// ReasonTextRefused: the fence refused the text; nothing was written.
	ReasonTextRefused Reason = "text-refused"
)

// Reasons that appear on other transitions.
const (
	// ReasonBlocked: the guard found the lead blocked; the command stays
	// queued.
	ReasonBlocked Reason = "blocked"
	// ReasonEnterWritten: the paste and Enter were written; waiting for
	// confirmation.
	ReasonEnterWritten Reason = "enter-written"
	// ReasonConfirmed: delivery evidence arrived.
	ReasonConfirmed Reason = "confirmed"
	// ReasonNotSubmitted: the paste was written and Enter was not.
	ReasonNotSubmitted Reason = "not-submitted"
	// ReasonSubmittedNotConfirmed: Enter was written and no evidence came in
	// time.
	ReasonSubmittedNotConfirmed Reason = "submitted-not-confirmed"
	// ReasonHolderDied: the fence reported that the holder or the agent died,
	// or what it wrote is unknown, or the daemon found the command dispatching
	// at restore.
	ReasonHolderDied Reason = "holder-or-agent-died"
	// ReasonNoFenceResult: no fence result was recorded in FenceTimeout after
	// the dispatching record. Nobody saw the holder or the agent die: the
	// fence may still be running, or its result may be lost. What was written
	// is unknown; a result or evidence that comes later is kept as an
	// annotation (ReasonLateFenceResult, ReasonLateEvidence).
	ReasonNoFenceResult Reason = "no-fence-result"
	// ReasonLateFenceResult: a fence result arrived for a command that was
	// already uncertain. It annotates the command and changes nothing else.
	ReasonLateFenceResult Reason = "late-fence-result"
	// ReasonLateEvidence: delivery evidence arrived for a command that was
	// already uncertain. It annotates the command and changes nothing else:
	// only an operator ends uncertain.
	ReasonLateEvidence Reason = "late-evidence"
	// ReasonInconsistentResult: the fence result contradicted itself.
	ReasonInconsistentResult Reason = "inconsistent-result"
	// ReasonOperatorDelivered: an operator decided the command was delivered.
	ReasonOperatorDelivered Reason = "operator-delivered"
	// ReasonOperatorDrop: an operator decided it was not delivered and to
	// drop it.
	ReasonOperatorDrop Reason = "operator-drop"
	// ReasonOperatorResend: an operator decided it was not delivered and to
	// send it again as a new command.
	ReasonOperatorResend Reason = "operator-resend"
	// ReasonOperatorWithdrawn: an operator withdrew a queued command from a held
	// queue; it was never sent and is not sent now.
	ReasonOperatorWithdrawn Reason = "operator-withdrawn"
)

// Reasons of a queue hold (RecordHold). Each says what the late information
// arrived about, and each is set only for a command in the state it names.
const (
	// ReasonHoldAfterResend: a late fence result or late delivery evidence
	// arrived for a command an operator resolved as "not delivered, send again".
	ReasonHoldAfterResend Reason = "late-after-resend"
	// ReasonHoldAfterDrop: the same, for a command an operator resolved as "not
	// delivered, drop".
	ReasonHoldAfterDrop Reason = "late-after-drop"
	// ReasonHoldDuringResend: the same, for an uncertain command whose resend is
	// half written: its replacement is queued and the resolution that completes
	// the resend is not yet journaled.
	ReasonHoldDuringResend Reason = "late-during-resend"
	// ReasonHoldReleased: the reason of a release record. An operator ended the
	// hold.
	ReasonHoldReleased Reason = "hold-released"
)

// Reasons that refuse a call without changing any command. They are never
// journaled.
const (
	ReasonQueueFrozen Reason = "queue-frozen"
	// ReasonQueueHeld: BeginDispatch for a target whose queue is held.
	ReasonQueueHeld Reason = "queue-held"
	// ReasonNotHeld: ReleaseHold or Withdraw for a queue that is not held.
	ReasonNotHeld            Reason = "queue-not-held"
	ReasonQueueEmpty         Reason = "queue-empty"
	ReasonAlreadyDispatching Reason = "already-dispatching"
	ReasonHeadNotQueued      Reason = "head-not-queued"
	ReasonNoCommand          Reason = "no-such-command"
	ReasonWrongState         Reason = "wrong-state"
	// ReasonLateNotesFull: the command already has the most late notes it
	// keeps, and this one is not a repeat of one of them.
	ReasonLateNotesFull Reason = "late-notes-full"
	ReasonNoEnter       Reason = "enter-not-written"
	ReasonBadEvidence   Reason = "bad-evidence"
	ReasonBadOperator   Reason = "bad-operator"
	ReasonBadDecision   Reason = "bad-decision"
)
