package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// Role is what a client may do on the attach socket.
type Role string

const (
	// RoleWriter may type, paste and resize. There is at most one.
	RoleWriter Role = "writer"
	// RoleViewer only watches. There is at most one.
	RoleViewer Role = "viewer"
)

func (r Role) check(field string) error {
	if r != RoleWriter && r != RoleViewer {
		return invalidf("%s is %q, want %q or %q", field, string(r), string(RoleWriter), string(RoleViewer))
	}
	return nil
}

// Fact is one of the three lifetime facts of a holder.
type Fact string

const (
	FactYes     Fact = "yes"
	FactNo      Fact = "no"
	FactUnknown Fact = "unknown"
)

func (f Fact) check(field string) error {
	switch f {
	case FactYes, FactNo, FactUnknown:
		return nil
	}
	return invalidf("%s is %q, want yes, no or unknown", field, string(f))
}

// IdleUnknown is the only idle classification in this feature.
const IdleUnknown = "unknown"

// Facts are the three separate lifetime facts that the holder reports: the
// terminal stream ended, the TUI exited, and the agent's process tree is gone.
type Facts struct {
	StreamEnded Fact `json:"stream_ended"`
	TUIExited   Fact `json:"tui_exited"`
	TreeGone    Fact `json:"tree_gone"`
}

func (f Facts) validate() error {
	if err := f.StreamEnded.check("stream_ended"); err != nil {
		return err
	}
	if err := f.TUIExited.check("tui_exited"); err != nil {
		return err
	}
	return f.TreeGone.check("tree_gone")
}

func checkStatus(s *int) error {
	if s != nil && (*s < 0 || *s > 255) {
		return invalidf("exit_status is %d, want 0 to 255 (a death by signal n is 128+n)", *s)
	}
	return nil
}

func checkCounters(m map[string]uint64) error {
	if len(m) > MaxCounters {
		return invalidf("%d counters, want at most %d", len(m), MaxCounters)
	}
	for k := range m {
		if err := checkToken("counter name", k, MaxToken); err != nil {
			return err
		}
	}
	return nil
}

// Colors are the outer terminal's colours as #rrggbb, each optional.
type Colors struct {
	Foreground string `json:"foreground,omitempty"`
	Background string `json:"background,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
}

func checkColor(field, s string) error {
	if s == "" {
		return nil
	}
	if len(s) != 7 || s[0] != '#' {
		return invalidf("%s is not #rrggbb", field)
	}
	for i := 1; i < 7; i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return invalidf("%s is not #rrggbb", field)
		}
	}
	return nil
}

func (c Colors) validate() error {
	if err := checkColor("foreground", c.Foreground); err != nil {
		return err
	}
	if err := checkColor("background", c.Background); err != nil {
		return err
	}
	return checkColor("cursor", c.Cursor)
}

// Hello is the first frame of a client. The holder accepts it only with a
// ticket that it was given for this role and epoch and a Version equal to
// [ProtocolVersion].
//
// A Version other than ours decodes when the rest of the payload has the
// fields of this package's Hello, but a later version may well add a field,
// and the strict decoder refuses a payload with a field it does not know.
// The holder therefore never judges the version by [DecodeHello] alone: when
// that fails, or returns another version, it asks [HelloVersion] and, for a
// version that is not [ProtocolVersion], refuses with [ReasonRefusedVersion].
type Hello struct {
	Version int    `json:"version" wire:"required"`
	Ticket  string `json:"ticket" wire:"required"`
	Name    string `json:"name"`
	Columns int    `json:"columns"`
	Rows    int    `json:"rows"`
	Colors  Colors `json:"colors"`
}

func (h *Hello) validate() error {
	if h.Version < 1 || h.Version > MaxDimension {
		return invalidf("version is %d", h.Version)
	}
	if err := CheckTicket(h.Ticket); err != nil {
		return err
	}
	if err := checkText("name", h.Name, 1, MaxClientName, false); err != nil {
		return err
	}
	if err := checkDimension("columns", h.Columns); err != nil {
		return err
	}
	if err := checkDimension("rows", h.Rows); err != nil {
		return err
	}
	return h.Colors.validate()
}

// EncodeHello returns the payload of a Hello frame.
func EncodeHello(h Hello) ([]byte, error) { return encodeMessage(TypeHello, &h) }

// DecodeHello decodes and validates the payload of a Hello frame.
func DecodeHello(payload []byte) (Hello, error) {
	var h Hello
	err := decodeMessage(&h, payload)
	return pick(h, err)
}

// pick returns the zero value with an error, so that a caller never sees a
// half-decoded message.
func pick[T any](v T, err error) (T, error) {
	if err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}

// Caps are the holder's limits as it tells a client in Welcome.
type Caps struct {
	MinColumns      int `json:"min_columns"`
	MinRows         int `json:"min_rows"`
	MaxColumns      int `json:"max_columns"`
	MaxRows         int `json:"max_rows"`
	ClientPayload   int `json:"client_payload"`
	HolderPayload   int `json:"holder_payload"`
	SnapshotPayload int `json:"snapshot_payload"`
}

// DefaultCaps are the limits of the lead-hosting design: a screen of 20 by 5
// to 500 by 200 and this package's payload caps.
func DefaultCaps() Caps {
	return Caps{
		MinColumns: 20, MinRows: 5, MaxColumns: 500, MaxRows: 200,
		ClientPayload: MaxClientPayload, HolderPayload: MaxHolderPayload, SnapshotPayload: MaxSnapshotPayload,
	}
}

func (c Caps) validate() error {
	for _, d := range []struct {
		name string
		n    int
	}{{"min_columns", c.MinColumns}, {"min_rows", c.MinRows}, {"max_columns", c.MaxColumns}, {"max_rows", c.MaxRows}} {
		if err := checkDimension(d.name, d.n); err != nil {
			return err
		}
	}
	if c.MinColumns > c.MaxColumns || c.MinRows > c.MaxRows {
		return invalidf("a minimum screen size is above its maximum")
	}
	for _, p := range []struct {
		name     string
		n, limit int
	}{
		{"client_payload", c.ClientPayload, MaxClientPayload},
		{"holder_payload", c.HolderPayload, MaxHolderPayload},
		{"snapshot_payload", c.SnapshotPayload, MaxSnapshotPayload},
	} {
		if p.n < 1 || p.n > p.limit {
			return invalidf("%s is %d, want 1 to %d", p.name, p.n, p.limit)
		}
	}
	return nil
}

// checkEpoch refuses epoch 0. Epochs are numbered from 1, so that a request
// or a frame that lacks one, and decodes as zero, can never match a holder's
// epoch.
func checkEpoch(n uint64) error {
	if n == 0 {
		return invalidf("epoch is 0, want 1 or more")
	}
	return nil
}

// Welcome answers an accepted Hello. A client that cannot decode it asks
// [WelcomeVersion] whether the holder is newer.
type Welcome struct {
	Version        int    `json:"version" wire:"required"`
	Role           Role   `json:"role"`
	Lead           string `json:"lead"`
	Epoch          uint64 `json:"epoch" wire:"required"`
	Runtime        string `json:"runtime"`
	RuntimeVersion string `json:"runtime_version"`
	Caps           Caps   `json:"caps"`
}

func (w *Welcome) validate() error {
	if w.Version < 1 || w.Version > MaxDimension {
		return invalidf("version is %d", w.Version)
	}
	if err := checkEpoch(w.Epoch); err != nil {
		return err
	}
	if err := w.Role.check("role"); err != nil {
		return err
	}
	if err := checkText("lead", w.Lead, 1, MaxLead, false); err != nil {
		return err
	}
	if err := checkText("runtime", w.Runtime, 1, MaxRuntime, false); err != nil {
		return err
	}
	if err := checkText("runtime_version", w.RuntimeVersion, 0, MaxRuntime, false); err != nil {
		return err
	}
	return w.Caps.validate()
}

// EncodeWelcome returns the payload of a Welcome frame.
func EncodeWelcome(w Welcome) ([]byte, error) { return encodeMessage(TypeWelcome, &w) }

// DecodeWelcome decodes and validates the payload of a Welcome frame.
func DecodeWelcome(payload []byte) (Welcome, error) {
	var w Welcome
	err := decodeMessage(&w, payload)
	return pick(w, err)
}

// Size is a screen size in columns and rows. It is the payload of Resize (a
// client asks) and of Size (the holder says what it has). The holder clamps a
// Resize to its limits; the codec only refuses what no size can be.
type Size struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

func (s *Size) validate() error {
	if err := checkDimension("columns", s.Columns); err != nil {
		return err
	}
	return checkDimension("rows", s.Rows)
}

// EncodeResize returns the payload of a Resize frame.
func EncodeResize(s Size) ([]byte, error) { return encodeMessage(TypeResize, &s) }

// DecodeResize decodes and validates the payload of a Resize frame.
func DecodeResize(payload []byte) (Size, error) {
	var s Size
	err := decodeMessage(&s, payload)
	return pick(s, err)
}

// EncodeSize returns the payload of a Size frame.
func EncodeSize(s Size) ([]byte, error) { return encodeMessage(TypeSize, &s) }

// DecodeSize decodes and validates the payload of a Size frame.
func DecodeSize(payload []byte) (Size, error) {
	var s Size
	err := decodeMessage(&s, payload)
	return pick(s, err)
}

// Modes is the full set of terminal modes that are on: DEC private mode
// numbers and ANSI mode numbers, each sorted ascending without repeats.
type Modes struct {
	DEC  []int `json:"dec"`
	ANSI []int `json:"ansi"`
}

func checkModeList(field string, l []int) error {
	if len(l) > MaxModes {
		return invalidf("%s has %d modes, want at most %d", field, len(l), MaxModes)
	}
	for i, n := range l {
		if n < 1 || n > MaxDimension {
			return invalidf("%s has mode %d", field, n)
		}
		if i > 0 && n <= l[i-1] {
			return invalidf("%s is not sorted ascending without repeats", field)
		}
	}
	return nil
}

func (m *Modes) validate() error {
	if err := checkModeList("dec", m.DEC); err != nil {
		return err
	}
	return checkModeList("ansi", m.ANSI)
}

// SortModes sorts a list of mode numbers ascending and removes repeats, in
// place, and returns it.
func SortModes(l []int) []int {
	sort.Ints(l)
	out := l[:0]
	for i, n := range l {
		if i == 0 || n != l[i-1] {
			out = append(out, n)
		}
	}
	return out
}

// EncodeModes returns the payload of a Modes frame. An empty list is encoded
// as [], never null.
func EncodeModes(m Modes) ([]byte, error) {
	if m.DEC == nil {
		m.DEC = []int{}
	}
	if m.ANSI == nil {
		m.ANSI = []int{}
	}
	return encodeMessage(TypeModes, &m)
}

// DecodeModes decodes and validates the payload of a Modes frame. Lists that
// are absent or null decode as empty.
func DecodeModes(payload []byte) (Modes, error) {
	var m Modes
	err := decodeMessage(&m, payload)
	return pick(m, err)
}

// State is the holder's account of the lead: the three lifetime facts, the
// exit status when known, the idle classification with the signals that gave
// it, and the title.
type State struct {
	Facts       Facts    `json:"facts"`
	ExitStatus  *int     `json:"exit_status,omitempty"`
	Idle        string   `json:"idle"`
	IdleSignals []string `json:"idle_signals"`
	Title       string   `json:"title"`
}

func (s *State) validate() error {
	if err := s.Facts.validate(); err != nil {
		return err
	}
	if err := checkStatus(s.ExitStatus); err != nil {
		return err
	}
	if err := checkToken("idle", s.Idle, MaxToken); err != nil {
		return err
	}
	if len(s.IdleSignals) > MaxSignals {
		return invalidf("%d idle signals, want at most %d", len(s.IdleSignals), MaxSignals)
	}
	for _, sig := range s.IdleSignals {
		if err := checkToken("idle signal", sig, MaxToken); err != nil {
			return err
		}
	}
	return checkText("title", s.Title, 0, MaxTitle, false)
}

// EncodeState returns the payload of a State frame. A nil signal list is
// encoded as [].
func EncodeState(s State) ([]byte, error) {
	if s.IdleSignals == nil {
		s.IdleSignals = []string{}
	}
	return encodeMessage(TypeState, &s)
}

// DecodeState decodes and validates the payload of a State frame.
func DecodeState(payload []byte) (State, error) {
	var s State
	err := decodeMessage(&s, payload)
	return pick(s, err)
}

// RoleInfo is the payload of a Role frame: who the writer is now (a client
// name and the pid of its process; both empty or zero when there is none) and
// the role of the client that receives the frame.
type RoleInfo struct {
	WriterName string `json:"writer_name" wire:"required"`
	WriterPID  int    `json:"writer_pid" wire:"required"`
	Role       Role   `json:"role"`
}

func (r *RoleInfo) validate() error {
	if err := checkText("writer_name", r.WriterName, 0, MaxClientName, false); err != nil {
		return err
	}
	if r.WriterPID < 0 || (r.WriterName == "" && r.WriterPID != 0) {
		return invalidf("writer_pid is %d with writer name %q", r.WriterPID, r.WriterName)
	}
	return r.Role.check("role")
}

// EncodeRole returns the payload of a Role frame.
func EncodeRole(r RoleInfo) ([]byte, error) { return encodeMessage(TypeRole, &r) }

// DecodeRole decodes and validates the payload of a Role frame.
func DecodeRole(payload []byte) (RoleInfo, error) {
	var r RoleInfo
	err := decodeMessage(&r, payload)
	return pick(r, err)
}

// DigestLen is the length of a Check digest in hexadecimal characters.
const DigestLen = 2 * sha256.Size

// Check is the payload of a Check frame: the output position it stands for and
// the SHA-256 of what the holder's emulator holds there (cells, cursor, modes
// and title; the pane defines the bytes), as 64 lower-case hexadecimal
// characters.
type Check struct {
	Position uint64 `json:"position" wire:"required"`
	Digest   string `json:"digest" wire:"required"`
}

func (c *Check) validate() error { return checkHex("digest", c.Digest, DigestLen) }

// DigestHex formats a SHA-256 digest as a Check carries it.
func DigestHex(sum [sha256.Size]byte) string { return hex.EncodeToString(sum[:]) }

// EncodeCheck returns the payload of a Check frame.
func EncodeCheck(c Check) ([]byte, error) { return encodeMessage(TypeCheck, &c) }

// DecodeCheck decodes and validates the payload of a Check frame.
func DecodeCheck(payload []byte) (Check, error) {
	var c Check
	err := decodeMessage(&c, payload)
	return pick(c, err)
}

// Reason is the code of a Notice. A decoder accepts any well-formed code, so
// that a newer holder's code does not break an older client; [Reason.Known]
// says whether this package has it.
type Reason string

// Reason codes of a Notice.
const (
	ReasonViewerInputRefused Reason = "viewer-input-refused"
	ReasonKeyHeldSuspend     Reason = "key-held-suspend"
	ReasonKeyHeldSignal      Reason = "key-held-signal"
	ReasonWriterChanged      Reason = "writer-changed"
	ReasonWriterReleased     Reason = "writer-released"
	ReasonResync             Reason = "resync"
	ReasonTooSlow            Reason = "too-slow"
	ReasonStalled            Reason = "stalled"
	ReasonRefusedTicket      Reason = "refused-ticket"
	ReasonRefusedBusy        Reason = "refused-busy"
	ReasonRefusedVersion     Reason = "refused-version"
	ReasonSnapshotWaiting    Reason = "snapshot-waiting"
)

var knownReasons = map[Reason]bool{
	ReasonViewerInputRefused: true, ReasonKeyHeldSuspend: true, ReasonKeyHeldSignal: true,
	ReasonWriterChanged: true, ReasonWriterReleased: true, ReasonResync: true, ReasonTooSlow: true,
	ReasonStalled: true, ReasonRefusedTicket: true, ReasonRefusedBusy: true, ReasonRefusedVersion: true,
	ReasonSnapshotWaiting: true,
}

// Known reports whether the reason is one of the constants of this package.
func (r Reason) Known() bool { return knownReasons[r] }

// Notice is the payload of a Notice frame: a reason code and a short text for
// a person.
type Notice struct {
	Reason Reason `json:"reason"`
	Text   string `json:"text"`
}

func (n *Notice) validate() error {
	if err := checkToken("reason", string(n.Reason), MaxToken); err != nil {
		return err
	}
	return checkText("text", n.Text, 0, MaxNoticeText, false)
}

// EncodeNotice returns the payload of a Notice frame.
func EncodeNotice(n Notice) ([]byte, error) { return encodeMessage(TypeNotice, &n) }

// DecodeNotice decodes and validates the payload of a Notice frame.
func DecodeNotice(payload []byte) (Notice, error) {
	var n Notice
	err := decodeMessage(&n, payload)
	return pick(n, err)
}

// Exit is the payload of an Exit frame, the holder's final record: the three
// facts, the exit status when known, the counters (name to number) and the
// last screen as text.
type Exit struct {
	Facts      Facts             `json:"facts"`
	ExitStatus *int              `json:"exit_status,omitempty"`
	Counters   map[string]uint64 `json:"counters"`
	LastScreen string            `json:"last_screen"`
}

func (e *Exit) validate() error {
	if err := e.Facts.validate(); err != nil {
		return err
	}
	if err := checkStatus(e.ExitStatus); err != nil {
		return err
	}
	if err := checkCounters(e.Counters); err != nil {
		return err
	}
	return checkText("last_screen", e.LastScreen, 0, MaxLastScreen, true)
}

// EncodeExit returns the payload of an Exit frame. A nil counter map is
// encoded as {}.
func EncodeExit(e Exit) ([]byte, error) {
	if e.Counters == nil {
		e.Counters = map[string]uint64{}
	}
	return encodeMessage(TypeExit, &e)
}

// DecodeExit decodes and validates the payload of an Exit frame.
func DecodeExit(payload []byte) (Exit, error) {
	var e Exit
	err := decodeMessage(&e, payload)
	return pick(e, err)
}
