//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/wire"
)

// maxEvidenceRead is the largest evidence file the daemon reads back when
// canary mode starts. A line is at most 4096 bytes and a run writes a few
// lines per operation, so a file of a canary state root is far below it; a
// larger one refuses the start.
const maxEvidenceRead = 8 << 20

// spentEpochs reads the evidence file at path and returns the epochs whose one
// message is spent: those that have an inject-sent line for role. It is how the
// rule of one message per epoch survives a restart of the daemon; the evidence
// is the only durable record of a sent message. It is the canary's reader: a
// result never frees an epoch (see [scratchSpentEpochs] for the one of scratch
// mode).
//
// internal/leadcanary writes the file and offers no reader, so this is a small
// strict reader of the three members the rule needs ("event", "role" and
// "epoch"). The file is opened without following a link at its last element,
// must be a regular file and is read to at most [maxEvidenceRead] bytes. Every
// line must end with a newline and be one JSON object with a string "event", a
// string "role" and, when present, a number "epoch" that fits; an inject-sent
// line of role needs an epoch of 1 or more. Anything else is an error, a part
// of a line or an empty line included: such a line may be what is left of an
// inject-sent line, so the caller refuses to start rather than admit a message
// it cannot rule out. A file that is not there has no line.
func spentEpochs(path, role string) (map[uint64]bool, error) {
	return readSpent(path, role, false)
}

// scratchSpentEpochs is [spentEpochs] for a lead of scratch mode, whose message
// stays unspent when the holder answered that nothing was written (see
// [leadcanary.ReleasesMessage]). The file is read as strictly, and the lines
// of role that count are its inject-sent and inject-result lines; the other
// members they need are "message_id" (a string) and, of a result line,
// "outcome", "written" (strings) and, when present, "reason" (a string).
//
// The lines of one epoch are read in the order of the file as a series of
// messages, each an inject-sent line that its own inject-result line follows
// (the daemon sends one message at a time and writes the sent line before the
// result line). An epoch is free when its series is complete and every result
// of it releases the message. It is spent otherwise, and every doubt spends
// it, so that nothing makes a message that may have been delivered look
// unspent:
//
//   - an inject-sent line that no result line follows;
//   - an inject-sent line that follows another one that has no result (a
//     second line of one message, which a failed sync of the first may leave,
//     is one of these);
//   - a result line that follows no inject-sent line of its epoch or of its
//     message id, and so also the second of two result lines of one message;
//   - a result that does not release the message, or whose words are not a
//     combination the wire carries.
//
// A message id that is used again in the epoch by a message that follows a
// released one is two messages, each with its own pair of lines.
func scratchSpentEpochs(path, role string) (map[uint64]bool, error) {
	return readSpent(path, role, true)
}

// readSpent is the reader of both: scratch says which rule is applied to the
// lines of role.
func readSpent(path, role string, scratch bool) (map[uint64]bool, error) {
	spent := make(map[uint64]bool)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return spent, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot open the evidence file to read it back: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("cannot examine the evidence file: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errors.New("the evidence file is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEvidenceRead+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read the evidence file back: %w", err)
	}
	if len(data) > maxEvidenceRead {
		return nil, fmt.Errorf("the evidence file is larger than %d bytes", maxEvidenceRead)
	}
	// open holds the message id of each epoch's sent line that has no result
	// line yet; doubt holds the epochs that a line has spent for good.
	open := make(map[uint64]string)
	doubt := make(map[uint64]bool)
	for n := 1; len(data) > 0; n++ {
		line, rest, ended := bytes.Cut(data, []byte{'\n'})
		if !ended {
			return nil, fmt.Errorf("line %d of the evidence file does not end with a newline", n)
		}
		data = rest
		l, err := readLine(line, role, scratch)
		if err != nil {
			return nil, fmt.Errorf("line %d of the evidence file is damaged: %w", n, err)
		}
		switch {
		case !scratch:
			if l.sent {
				spent[l.epoch] = true
			}
		case l.sent:
			if _, pending := open[l.epoch]; pending {
				doubt[l.epoch] = true
			}
			open[l.epoch] = l.messageID
		case l.result:
			id, pending := open[l.epoch]
			if !pending || id != l.messageID || !l.releases {
				doubt[l.epoch] = true
			}
			delete(open, l.epoch)
		}
	}
	if scratch {
		for epoch := range open {
			spent[epoch] = true
		}
		for epoch := range doubt {
			spent[epoch] = true
		}
	}
	return spent, nil
}

// evidenceLine is what the reader takes from a line of the evidence: for a
// line of the role, whether it is an inject-sent or an inject-result line, and
// its epoch; in scratch mode also the message id of either and whether the
// result releases the message.
type evidenceLine struct {
	sent, result bool
	epoch        uint64
	messageID    string
	releases     bool
}

// readLine reads one line of the evidence. In scratch mode an inject-result
// line of role is read as well as an inject-sent one.
func readLine(line []byte, role string, scratch bool) (evidenceLine, error) {
	var l evidenceLine
	var members map[string]json.RawMessage
	if err := json.Unmarshal(line, &members); err != nil || members == nil {
		return l, errors.New("not a JSON object")
	}
	var event, lineRole string
	if err := stringMember(members, "event", &event); err != nil {
		return l, err
	}
	if err := stringMember(members, "role", &lineRole); err != nil {
		return l, err
	}
	if raw, has := members["epoch"]; has {
		// A number that is not a whole number of 64 bits, and anything that is
		// not a number, does not decode.
		if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' || json.Unmarshal(raw, &l.epoch) != nil {
			return evidenceLine{}, errors.New(`"epoch" is not a whole number`)
		}
	}
	if lineRole != role {
		return evidenceLine{}, nil
	}
	switch {
	case event == string(leadcanary.EventInjectSent):
		l.sent = true
	case scratch && event == string(leadcanary.EventInjectResult):
		l.result = true
	default:
		return evidenceLine{epoch: l.epoch}, nil
	}
	if l.epoch == 0 {
		return evidenceLine{}, fmt.Errorf("an %s line without an epoch", event)
	}
	if !scratch {
		return l, nil
	}
	if err := stringMember(members, "message_id", &l.messageID); err != nil {
		return evidenceLine{}, err
	}
	if l.sent {
		return l, nil
	}
	var outcome, reason, written string
	if err := stringMember(members, "outcome", &outcome); err != nil {
		return evidenceLine{}, err
	}
	if err := stringMember(members, "written", &written); err != nil {
		return evidenceLine{}, err
	}
	if _, has := members["reason"]; has {
		if err := stringMember(members, "reason", &reason); err != nil {
			return evidenceLine{}, err
		}
	}
	l.releases = leadcanary.ReleasesMessage(wire.InjectResult{
		Outcome: wire.InjectOutcome(outcome), Reason: wire.InjectReason(reason), Written: wire.InjectWritten(written),
	})
	return l, nil
}

// stringMember sets v to the member key of members, which must be a string
// that is not empty.
func stringMember(members map[string]json.RawMessage, key string, v *string) error {
	raw, has := members[key]
	if !has || len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, v) != nil || *v == "" {
		return fmt.Errorf("%q is not a string", key)
	}
	return nil
}
