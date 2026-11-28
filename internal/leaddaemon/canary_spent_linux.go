//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

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
// result, refusal or unsent line never frees an epoch (see [scratchSpentEpochs]
// for the one of scratch mode).
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

// evidenceState is what the daemon takes from the evidence when it starts.
type evidenceState struct {
	// spent is the epochs whose message is spent, by the rule of the mode.
	spent map[uint64]bool
	// effects is the largest counter, the number after the last "-", of an
	// effect id on a line of the role; 0 when no line has one (the lines of an
	// older daemon have none). The daemon's command ids continue from it.
	effects uint64
}

// scratchSpentEpochs is [spentEpochs] for a lead of scratch mode, whose message
// stays unspent when the holder answered that nothing was written (see
// [leadcanary.ReleasesMessage]) or refused the request itself as disabled,
// wrong-epoch or bad-request (see [holderFrees]), or when the daemon wrote that
// the frame was never written (the code [evidenceUnsent]); a holder's busy closes
// the message but does not free it. The file is read as strictly, and the
// lines of role that count are its inject-sent and inject-result lines and the
// inject-refused lines whose code is holder-<code> of one of the holder's four
// refusals or unsent; the other members they need are "message_id" (a
// string) and, of a result line, "outcome", "written" (strings) and, when
// present, "reason" (a string). A line may also carry an "effect_id" (the
// command id of the frame the line is about, written on the inject-sent line
// and the unsent line); the reader does not look at it, so a file of an older
// daemon, whose lines have none, reads the same.
//
// The lines of one epoch are read in the order of the file as a series of
// messages, each an inject-sent line that its own inject-result line, or its
// own holder refusal or unsent line, follows (the daemon sends one message at
// a time and writes the sent line before the frame, so before any of them). An epoch is free when its series is
// complete and every close of it releases the message. It is spent otherwise,
// and every doubt spends it, so that nothing makes a message that may have
// been delivered look unspent:
//
//   - an inject-sent line that no result line, holder refusal or unsent line
//     follows (a daemon killed after the line, with or without a frame sent);
//   - an inject-sent line that follows another one that has no result (a
//     second line of one message, which a failed sync of the first may leave,
//     is one of these);
//   - a result line or a holder refusal that follows no inject-sent line of
//     its epoch or of its message id, and so also the second of two lines
//     that close one message;
//   - a result that does not release the message, or whose words are not a
//     combination the wire carries;
//   - a holder refusal of busy, which says that a delivery is running that
//     the daemon has no record of.
//
// An inject-refused line with any other code (a message refused before it was
// recorded: inject-off, second-message, evidence-unwritable and the like, or the
// holder-unreachable of an older daemon) is not part of a series and changes
// nothing: it does not close the message that is open, which stays open, so the
// epoch stays spent. So an older reader, which knows no unsent code, reads an
// unsent line as such a line and keeps the epoch spent.
//
// A message id that is used again in the epoch by a message that follows a
// released one is two messages, each with its own pair of lines.
func scratchSpentEpochs(path, role string) (map[uint64]bool, error) {
	return readSpent(path, role, true)
}

// readSpent is the reader of both: scratch says which rule is applied to the
// lines of role.
func readSpent(path, role string, scratch bool) (map[uint64]bool, error) {
	st, err := readEvidence(path, role, scratch)
	return st.spent, err
}

// readEvidence reads the evidence file as [readSpent] does and returns the
// state the daemon starts from: the spent epochs and the largest effect
// counter. On an error the spent set is nil.
func readEvidence(path, role string, scratch bool) (evidenceState, error) {
	out := evidenceState{spent: make(map[uint64]bool)}
	spent := out.spent
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return out, nil
	}
	if err != nil {
		return evidenceState{}, fmt.Errorf("cannot open the evidence file to read it back: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return evidenceState{}, fmt.Errorf("cannot examine the evidence file: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return evidenceState{}, errors.New("the evidence file is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEvidenceRead+1))
	if err != nil {
		return evidenceState{}, fmt.Errorf("cannot read the evidence file back: %w", err)
	}
	if len(data) > maxEvidenceRead {
		return evidenceState{}, fmt.Errorf("the evidence file is larger than %d bytes", maxEvidenceRead)
	}
	// open holds the message id of each epoch's sent line that has no result
	// line yet; doubt holds the epochs that a line has spent for good.
	open := make(map[uint64]string)
	doubt := make(map[uint64]bool)
	for n := 1; len(data) > 0; n++ {
		line, rest, ended := bytes.Cut(data, []byte{'\n'})
		if !ended {
			return evidenceState{}, fmt.Errorf("line %d of the evidence file does not end with a newline", n)
		}
		data = rest
		l, err := readLine(line, role, scratch)
		if err != nil {
			return evidenceState{}, fmt.Errorf("line %d of the evidence file is damaged: %w", n, err)
		}
		out.effects = max(out.effects, l.effect)
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
		case l.refused:
			// The holder's own refusal, which closes the message that is
			// open; the message id must be that message's. A busy does not
			// free the epoch.
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
	return out, nil
}

// evidenceLine is what the reader takes from a line of the evidence: for a
// line of the role, whether it is an inject-sent or an inject-result line, and
// its epoch; in scratch mode also the message id of either and whether the
// result releases the message.
type evidenceLine struct {
	sent, result bool
	// refused is an inject-refused line with the code holder-<code> of one of
	// the holder's refusals; read in scratch mode only, and its releases says
	// whether the refusal frees the message (every code but busy does). Its
	// messageID is "" if the line has none.
	refused   bool
	epoch     uint64
	messageID string
	releases  bool
	// effect is the counter of the line's effect id, 0 for a line of the role
	// that has none (and for a line of another role).
	effect uint64
}

// readLine reads one line of the evidence. In scratch mode an inject-result
// line of role is read as well as an inject-sent one.
func readLine(line []byte, role string, scratch bool) (l evidenceLine, err error) {
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
	// Whatever the line is, its effect id counts, so that the daemon's counter
	// passes it. The id is not otherwise looked at, and a member that is not a
	// string is no id.
	defer func() {
		if err == nil {
			l.effect = effectCounter(members)
		}
	}()
	switch {
	case event == string(leadcanary.EventInjectSent):
		l.sent = true
	case scratch && event == string(leadcanary.EventInjectResult):
		l.result = true
	case scratch && event == string(leadcanary.EventInjectRefused):
		releases, ok := closingRefusal(members)
		if !ok {
			return evidenceLine{epoch: l.epoch}, nil
		}
		l.refused, l.releases = true, releases
	default:
		return evidenceLine{epoch: l.epoch}, nil
	}
	if l.epoch == 0 {
		return evidenceLine{}, fmt.Errorf("an %s line without an epoch", event)
	}
	if !scratch {
		return l, nil
	}
	if l.refused {
		// A refusal of the holder always carries the id of the message it
		// refused; one without a string id matches no sent line.
		stringMember(members, "message_id", &l.messageID)
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

// effectCounter returns the counter of the effect id in the members of a line:
// the number after the last "-" of the string "effect_id". It is 0 for a line
// with no such member, one that is not a string and an id that does not end in
// a number that fits.
func effectCounter(members map[string]json.RawMessage) uint64 {
	var id string
	if raw, has := members["effect_id"]; !has || len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &id) != nil {
		return 0
	}
	n, err := strconv.ParseUint(id[strings.LastIndexByte(id, '-')+1:], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// closingRefusal reports whether the members of an inject-refused line carry a
// code that closes the message that is open, and whether the close releases
// it. Two codes close one: holder-<code> of one of the holder's own refusals,
// the ones that say nothing was written to the lead for the message (see
// [holderRefusal]), which release it except busy (see [holderFrees]); and
// [evidenceUnsent], which says the daemon wrote the inject-sent line and then
// could not dial the holder or set the write deadline, so that no frame was
// written, and which always releases it. Any other code is not part of a
// series.
func closingRefusal(members map[string]json.RawMessage) (releases, ok bool) {
	var code string
	if raw, has := members["code"]; !has || len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &code) != nil {
		return false, false
	}
	if code == evidenceUnsent {
		return true, true
	}
	rest, held := strings.CutPrefix(code, "holder-")
	if !held || !holderRefusal(rest) {
		return false, false
	}
	return holderFrees(rest), true
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
