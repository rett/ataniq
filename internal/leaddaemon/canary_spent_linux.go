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
)

// maxEvidenceRead is the largest evidence file the daemon reads back when
// canary mode starts. A line is at most 4096 bytes and a run writes a few
// lines per operation, so a file of a canary state root is far below it; a
// larger one refuses the start.
const maxEvidenceRead = 8 << 20

// spentEpochs reads the evidence file at path and returns the epochs whose one
// message is spent: those that have an inject-sent line for role. It is how the
// rule of one message per epoch survives a restart of the daemon; the evidence
// is the only durable record of a sent message.
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
	for n := 1; len(data) > 0; n++ {
		line, rest, ended := bytes.Cut(data, []byte{'\n'})
		if !ended {
			return nil, fmt.Errorf("line %d of the evidence file does not end with a newline", n)
		}
		data = rest
		epoch, sent, err := sentLine(line, role)
		if err != nil {
			return nil, fmt.Errorf("line %d of the evidence file is damaged: %w", n, err)
		}
		if sent {
			spent[epoch] = true
		}
	}
	return spent, nil
}

// sentLine reads one line of the evidence and reports whether it is an
// inject-sent line of role, and its epoch.
func sentLine(line []byte, role string) (epoch uint64, sent bool, err error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(line, &members); err != nil || members == nil {
		return 0, false, errors.New("not a JSON object")
	}
	var event, lineRole string
	if err := stringMember(members, "event", &event); err != nil {
		return 0, false, err
	}
	if err := stringMember(members, "role", &lineRole); err != nil {
		return 0, false, err
	}
	if raw, has := members["epoch"]; has {
		// A number that is not a whole number of 64 bits, and anything that is
		// not a number, does not decode.
		if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' || json.Unmarshal(raw, &epoch) != nil {
			return 0, false, errors.New(`"epoch" is not a whole number`)
		}
	}
	if event != string(leadcanary.EventInjectSent) || lineRole != role {
		return 0, false, nil
	}
	if epoch == 0 {
		return 0, false, errors.New("an inject-sent line without an epoch")
	}
	return epoch, true, nil
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
