//go:build linux && ataniq_canary

package leaddaemon

import "ataniq/internal/leadcanary"

// evidenceAppender is what the extension needs of the evidence file: to append
// a line, which it does under no lock of its own (the evidence has its own).
// It is a seam, so that a test can give an evidence that takes some lines and
// refuses the next. *leadcanary.Evidence is the real one.
type evidenceAppender interface {
	Append(leadcanary.Entry) error
}

// sendStage says how far a recorded send got. The stages are in the order of
// the steps of [runSend], and the first three say that no frame left the
// daemon.
type sendStage int

const (
	// sendNotStarted: the request did not encode. Nothing was recorded and
	// nothing was sent.
	sendNotStarted sendStage = iota
	// sendNotRecorded: the record step failed, so nothing else was tried:
	// nothing was sent, and the evidence may or may not hold the line (an
	// append whose sync failed leaves one).
	sendNotRecorded
	// sendUnsent: the line is recorded, and the holder's socket could not be
	// dialled or its write deadline could not be set. Nothing can have left.
	sendUnsent
	// sendFrameFailed: the line is recorded and writing the frame failed. A
	// part of the frame may have left, so the message counts as sent.
	sendFrameFailed
	// sendWritten: the line is recorded and the frame is written. The answer
	// may still be missing (the error of the call says).
	sendWritten
)

// recorded reports whether the stage is one where the sent line is in the
// evidence, that is whether the message counts as sent.
func (s sendStage) recorded() bool { return s >= sendUnsent }

// sendSteps are the steps of one send of a request to the holder. The seam is
// small on purpose: a test gives fakes that note the order in which they are
// called, and callHolderThen gives the real ones.
type sendSteps struct {
	// record writes the line that spends the message (the inject-sent line).
	// It may be nil, for a request that spends nothing.
	record func() error
	// connect dials the holder and sets the write deadline.
	connect func() error
	// frame writes the request frame.
	frame func() error
	// await reads the answer; its error is the call's, not a stage's.
	await func() error
}

// runSend runs the steps in the order that makes a send recoverable: record,
// then connect, then frame, then await. The record is first and nothing else
// runs when it fails, so that no frame is ever written for a message the
// evidence does not know: a daemon that is killed at any point after the
// record leaves the message spent, and one killed before it has sent nothing.
// It returns the stage reached and the error of the step that failed; at
// sendWritten the error is the await's, if any.
func runSend(s sendSteps) (sendStage, error) {
	if s.record != nil {
		if err := s.record(); err != nil {
			return sendNotRecorded, err
		}
	}
	if err := s.connect(); err != nil {
		return sendUnsent, err
	}
	if err := s.frame(); err != nil {
		return sendFrameFailed, err
	}
	return sendWritten, s.await()
}
