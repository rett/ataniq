//go:build linux

package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/ptyhost"
)

// FinalName is the name of the final record in Config.StateDir.
const FinalName = "final.json"

// MaxFinalScreenBytes caps the last screen as text in the final record.
const MaxFinalScreenBytes = 256 << 10

// FinalVersion is the format version of the final record.
const FinalVersion = 1

// Tri is a fact that may not be known: "yes", "no" or "unknown".
type Tri string

const (
	Yes     Tri = "yes"
	No      Tri = "no"
	Unknown Tri = "unknown"
)

func tri(b bool) Tri {
	if b {
		return Yes
	}
	return No
}

// ExitStatus is how the TUI, the direct child, ended.
type ExitStatus struct {
	// How is "exit", "signal" or "unknown".
	How string `json:"how"`
	// Status is the exit code, or 128+n for death by signal n, or -1 when
	// unknown.
	Status int `json:"status"`
	// Signal is the number of the signal that ended it, else 0; SignalName is
	// its name ("SIGTERM"), empty when there is none.
	Signal     int    `json:"signal,omitempty"`
	SignalName string `json:"signal_name,omitempty"`
	CoreDump   bool   `json:"core_dump,omitempty"`
	// Text describes the exit in one line.
	Text string `json:"text"`
}

// Facts are the three separate facts of design 1.4.
type Facts struct {
	// StreamEnded: the terminal stream reached its end (the kernel said so).
	// "no" while it runs and when it did not end in the time allowed after
	// the TUI exited; "unknown" when it ended in an error.
	StreamEnded Tri `json:"stream_ended"`
	// TUIExited: the direct child exited. Status is set from then on.
	TUIExited Tri         `json:"tui_exited"`
	Status    *ExitStatus `json:"status,omitempty"`
	// TreeGone: the agent's process tree is gone. "no" while the TUI runs
	// or the tree is still being waited for, "unknown" when the wait ran out.
	TreeGone Tri `json:"tree_gone"`
	// TreeMethod is ptyhost's method, "cgroup" or "session"; TreeWeak is
	// true for "session", the weaker proof (a descendant that started its
	// own session is not seen). TreeProof is ptyhost's proof string.
	TreeMethod string `json:"tree_method"`
	TreeWeak   bool   `json:"tree_proof_is_weaker,omitempty"`
	TreeProof  string `json:"tree_proof,omitempty"`
}

// FinalScreen is the last screen of the final record.
type FinalScreen struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
	// Text is Pane.Text of the whole screen, cut at a row boundary when it
	// would be over MaxFinalScreenBytes; Truncated says so.
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// FinalRecord is the content of final.json.
type FinalRecord struct {
	Version  int               `json:"version"`
	Role     string            `json:"role"`
	Epoch    string            `json:"epoch"`
	Started  time.Time         `json:"started"`
	Ended    time.Time         `json:"ended"`
	Facts    Facts             `json:"facts"`
	Screen   FinalScreen       `json:"screen"`
	Counters map[string]uint64 `json:"counters"`
}

// Facts returns the three facts now. They change while the session runs and
// are frozen once the lifetime path concluded (Done is closed).
func (s *Session) Facts() Facts {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	if s.concl {
		return s.facts
	}
	f := Facts{StreamEnded: s.streamFact(), TUIExited: No, TreeGone: No, TreeMethod: s.host.TreeMethod()}
	f.TreeWeak = f.TreeMethod == "session"
	if s.facts.Status != nil {
		f.TUIExited, f.Status = Yes, s.facts.Status
		gone, proof := s.host.TreeGone()
		f.TreeGone, f.TreeProof = tri(gone), proof
	}
	return f
}

// streamFact reads the stream state: "no" while it runs.
func (s *Session) streamFact() Tri {
	switch s.stream.Load() {
	case streamEnded:
		return Yes
	case streamUnknown:
		return Unknown
	}
	return No
}

// Final returns the final record once the session is over. The error is
// ErrNotOver before that, and otherwise the error that stopped the record from
// being written to disk (the record is returned anyway).
func (s *Session) Final() (FinalRecord, error) {
	select {
	case <-s.done:
	default:
		return FinalRecord{}, ErrNotOver
	}
	s.fmu.Lock()
	defer s.fmu.Unlock()
	return s.final, s.finalErr
}

func exitStatus(e ptyhost.Exit) *ExitStatus {
	st := &ExitStatus{How: e.How, Status: -1, Text: e.String()}
	switch e.How {
	case "exit":
		st.Status = e.Code
	case "signal":
		st.Status = 128 + int(e.Signal)
		st.Signal = int(e.Signal)
		st.SignalName = unix.SignalName(syscall.Signal(e.Signal))
		if st.SignalName == "" {
			st.SignalName = fmt.Sprintf("signal %d", int(e.Signal))
		}
		st.CoreDump = e.CoreDump
	default:
		st.How = "unknown"
	}
	return st
}

// lifecycle is the lifetime path: it ends the session's facts after the TUI
// exited, writes the final record and closes done.
func (s *Session) lifecycle() {
	defer close(s.done)
	ex := s.host.Wait()
	st := exitStatus(ex)
	s.fmu.Lock()
	s.facts.Status = st
	s.fmu.Unlock()

	// The stream: bounded, then recorded as not ended.
	timer := time.NewTimer(s.cfg.StreamWait)
	select {
	case <-s.readDone:
	case <-timer.C:
	}
	timer.Stop()
	stream := s.streamFact()

	// The tree: bounded, then unknown.
	gone, proof := s.waitTree()
	treeGone := Unknown
	if gone {
		treeGone = Yes
	}

	if stream == Yes {
		// The session's output is over: the pane keeps its last screen.
		s.pane.End(st.Status, st.How != "unknown")
	}
	end := time.Now().UTC()
	method := s.host.TreeMethod()
	facts := Facts{
		StreamEnded: stream, TUIExited: Yes, Status: st, TreeGone: treeGone,
		TreeMethod: method, TreeWeak: method == "session", TreeProof: proof,
	}
	screen := s.finalScreen() // before the counters: a cut is counted
	counters := s.Counters()
	rec := FinalRecord{
		Version: FinalVersion, Role: s.cfg.Role, Epoch: s.cfg.Epoch,
		Started: s.started, Ended: end, Facts: facts,
		Screen: screen, Counters: counters,
	}
	werr := writeFinal(s.cfg.StateDir, &rec)

	s.fmu.Lock()
	s.facts, s.concl = facts, true
	s.final, s.finalErr = rec, werr
	s.fmu.Unlock()

	f := eventlog.Fields{
		Role: s.cfg.Role, Epoch: s.cfg.Epoch, Counters: counters,
		ExitStatus: &st.Status, TUIExited: ptr(true),
	}
	if st.How == "unknown" {
		f.ExitStatus = nil
	}
	switch stream {
	case Yes:
		f.StreamEnded = ptr(true)
	case No:
		f.StreamEnded = ptr(false)
	}
	if treeGone == Yes {
		f.TreeGone = ptr(true)
	}
	if werr != nil {
		f.Reason = "final-record-failed"
	}
	s.event(eventlog.EventExit, f)
	_ = s.log.Sync()
}

func ptr[T any](v T) *T { return &v }

// waitTree polls ptyhost's tree proof until the tree is gone, the wait is over
// or the session is closing (then it looks once more and stops).
func (s *Session) waitTree() (bool, string) {
	deadline := time.NewTimer(s.cfg.TreeWait)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		gone, proof := s.host.TreeGone()
		if gone {
			return true, proof
		}
		select {
		case <-deadline.C:
			gone, proof = s.host.TreeGone()
			return gone, proof
		case <-s.closing:
			gone, proof = s.host.TreeGone()
			return gone, proof
		case <-tick.C:
		}
	}
}

// finalScreen reads the whole screen as text, cut at a row boundary at
// MaxFinalScreenBytes.
func (s *Session) finalScreen() FinalScreen {
	cols, rows := s.pane.Size()
	fs := FinalScreen{Cols: cols, Rows: rows}
	if t, ok := s.pane.Text(uv.Pos(0, 0), uv.Pos(cols-1, rows-1), MaxFinalScreenBytes); ok {
		fs.Text = t
		return fs
	}
	var b strings.Builder
	for y := 0; y < rows; y++ {
		line, ok := s.pane.Text(uv.Pos(0, y), uv.Pos(cols-1, y), MaxFinalScreenBytes-b.Len()-1)
		if !ok {
			break
		}
		if y > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	fs.Text, fs.Truncated = b.String(), true
	s.AddCount("final_screen_truncated", 1)
	return fs
}

// writeFinal writes the record to dir/final.json (mode 0600) through a
// temporary name, syncs it and renames it into place, then syncs the folder.
func writeFinal(dir string, rec *FinalRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("session: encode the final record: %w", err)
	}
	data = append(data, '\n')
	tmp := filepath.Join(dir, FinalName+".tmp")
	fd, err := unix.Open(tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("session: write the final record: %w", err)
	}
	f := os.NewFile(uintptr(fd), tmp)
	_ = f.Chmod(0o600)
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("session: write the final record: %w", werr)
	}
	if err := os.Rename(tmp, filepath.Join(dir, FinalName)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("session: write the final record: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
