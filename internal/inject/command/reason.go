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
	// ReasonOriginalEvidenceArrived: a fence result or delivery evidence
	// arrived for a command that an operator had already resolved as "not
	// delivered, send again", while its replacement was still queued. The
	// replacement becomes uncertain, so that nothing is sent a second time
	// before an operator has looked at the evidence.
	ReasonOriginalEvidenceArrived Reason = "original-evidence-arrived"
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
)

// Reasons that refuse a call without changing any command. They are never
// journaled.
const (
	ReasonQueueFrozen        Reason = "queue-frozen"
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
