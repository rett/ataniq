//go:build linux && amd64

package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/plan"
)

// EvidenceFormat is the version of the evidence files.
const EvidenceFormat = 1

// The evidence files of an attempt folder.
const (
	FilePlan     = "plan.json"
	FileSpec     = "spec.json"
	FileReport   = "report.json"
	FileOutside  = "outside.json"
	FileTerminal = "terminal.log"
	FileSummary  = "summary.json"
	// FileReportFIFO is the report channel; it is removed when the run ends.
	FileReportFIFO = "report.fifo"
	// DirChannel holds the report FIFO and nothing else; it is removed with
	// the FIFO when the run ends.
	DirChannel = "channel"
	// DirBound holds what the plan binds from the harness: the resolv.conf
	// and the folder of the probe spec.
	DirBound = "bound"
)

// The evidence is facts, status and errno. No key of it says that the profile
// passed, is qualified or is supported, and a test checks that no key does.

// PlanEvidence is plan.json: the plan as generated and verified.
type PlanEvidence struct {
	Format int      `json:"format"`
	Argv   []string `json:"argv"`
	// EnvNames are the names of the allowlist; the values that the profile
	// defines are also in the --setenv operands of the argument vector.
	EnvNames []string        `json:"env_names"`
	Mounts   []MountEvidence `json:"mounts"`
	Files    []FileEvidence  `json:"files"`
	Notes    []string        `json:"notes"`
	WorkDir  string          `json:"work_dir"`
	// Digest is the digest of the profile (profile.Inputs.Digest).
	Digest string `json:"digest"`
}

// MountEvidence is one emitted row of the bind table.
type MountEvidence struct {
	Kind         string `json:"kind"`
	Inside       string `json:"inside"`
	Source       string `json:"source,omitempty"`
	LeadWritable bool   `json:"lead_writable,omitempty"`
	Optional     bool   `json:"optional,omitempty"`
}

// FileEvidence is a descriptor the plan's pass-through step opens.
type FileEvidence struct {
	FD      int    `json:"fd"`
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Purpose string `json:"purpose"`
	SHA256  string `json:"sha256,omitempty"`
}

func planEvidence(p *plan.Plan) PlanEvidence {
	e := PlanEvidence{
		Format: EvidenceFormat, Argv: append([]string{}, p.Argv...), Notes: append([]string{}, p.Notes...),
		WorkDir: p.WorkDir, Digest: p.Digest, EnvNames: []string{}, Mounts: []MountEvidence{}, Files: []FileEvidence{},
	}
	for _, v := range p.Env {
		e.EnvNames = append(e.EnvNames, v.Name)
	}
	sort.Strings(e.EnvNames)
	for _, m := range p.Mounts {
		e.Mounts = append(e.Mounts, MountEvidence{Kind: string(m.Kind), Inside: m.Inside, Source: m.Source, LeadWritable: m.LeadWritable, Optional: m.Optional})
	}
	for _, f := range p.Files {
		e.Files = append(e.Files, FileEvidence{FD: f.FD, Path: f.Path, Mode: string(f.Mode), Purpose: f.Purpose, SHA256: f.SHA256})
	}
	return e
}

// ReportOutcome says what came through the report channel.
type ReportOutcome string

// The outcomes of the wait for the report.
const (
	// ReportLineRead: a line was read and the strict parser accepted it. The
	// results are the probe's own statuses.
	ReportLineRead ReportOutcome = "line-read"
	// ReportRefused: a line or bytes came and the parser refused them.
	ReportRefused ReportOutcome = "refused-line"
	// ReportTimeLimit: nothing complete came within the time limit.
	ReportTimeLimit ReportOutcome = "time-limit"
	// ReportExitedFirst: the process exited and the channel closed with no
	// report.
	ReportExitedFirst ReportOutcome = "exited-before-report"
	// ReportInterrupted: the run was interrupted.
	ReportInterrupted ReportOutcome = "interrupted"
	// ReportNotLaunched: nothing was launched.
	ReportNotLaunched ReportOutcome = "not-launched"
)

// ReportEvidence is report.json: what the probe reported, or what came
// instead.
type ReportEvidence struct {
	Format  int           `json:"format"`
	Outcome ReportOutcome `json:"outcome"`
	// Detail is the parser's reason for a refused line, or a note. Bounded
	// and escaped.
	Detail string `json:"detail,omitempty"`
	// LineBytes is the length of what the channel delivered.
	LineBytes int `json:"line_bytes"`
	// LineExcerpt is the start of a refused line, escaped.
	LineExcerpt string `json:"line_excerpt,omitempty"`
	// Digest is the report's profile digest and DigestMatches says whether it
	// equals the spec's.
	Digest        string          `json:"digest,omitempty"`
	DigestMatches *bool           `json:"digest_matches,omitempty"`
	Results       []assert.Result `json:"results"`
	// TrailingBytes is the fact asked after the stop: true when the report
	// channel held any byte after the accepted line (a failure of the
	// start), false when it held none. It is absent when no line was
	// accepted, because nothing was asked.
	TrailingBytes *bool `json:"report_trailing_bytes,omitempty"`
}

// CanaryEvidence is what the canary counted.
type CanaryEvidence struct {
	TCPHits      int64 `json:"tcp_hits"`
	TCP2Hits     int64 `json:"tcp2_hits"`
	AbstractHits int64 `json:"abstract_hits"`
	AcceptErrors int64 `json:"accept_errors"`
	Stopped      bool  `json:"stopped"`
}

// OutsideEvidence is outside.json: the results of the checks the harness
// makes on the host after the probe has reported.
type OutsideEvidence struct {
	Format  int             `json:"format"`
	Results []assert.Result `json:"results"`
	Canary  CanaryEvidence  `json:"canary"`
}

// ExitEvidence is how the first process ended.
type ExitEvidence struct {
	Launched bool `json:"launched"`
	// Exited is false when the process was still there at the time limit (or
	// the run was interrupted first); Code is then -1 and Signal and Error are
	// empty, and AfterStop may say how the stop ended it.
	Exited bool   `json:"exited"`
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	Error  string `json:"error,omitempty"`
	// AfterStop is how the first process ended when it was still there at the
	// time limit and the stop rule ended it. It is absent when the process
	// had ended before, and when it was still there after the stop.
	AfterStop *EndEvidence `json:"after_stop,omitempty"`
}

// EndEvidence is how a process ended: its exit code, or the signal that ended
// it (the code is then -1).
type EndEvidence struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	Error  string `json:"error,omitempty"`
}

// TerminalEvidence counts the terminal output.
type TerminalEvidence struct {
	CapturedBytes int   `json:"captured_bytes"`
	TotalBytes    int64 `json:"total_bytes"`
	DroppedBytes  int64 `json:"dropped_bytes"`
	// MarkerSeen is set only when the default scratch command ran: whether
	// its marker is in the captured output.
	MarkerSeen *bool `json:"marker_seen,omitempty"`
}

// ResolvEvidence is what the resolv.conf derivation found.
type ResolvEvidence struct {
	Source          string   `json:"source,omitempty"`
	Nameservers     int      `json:"nameservers"`
	DroppedLoopback int      `json:"dropped_loopback"`
	Notes           []string `json:"notes,omitempty"`
}

// CaseResult is one result a case speaks to.
type CaseResult struct {
	ID     string        `json:"id"`
	Status assert.Status `json:"status"`
}

// CaseEvidence is the facts one requested case speaks to.
type CaseEvidence struct {
	Case    string       `json:"case"`
	Results []CaseResult `json:"results"`
	// Notes say what else in the evidence bears on the case.
	Notes []string `json:"notes,omitempty"`
}

// Summary is summary.json.
type Summary struct {
	Format  int    `json:"format"`
	DryRun  bool   `json:"dry_run"`
	Attempt string `json:"attempt"`
	Start   string `json:"start"`
	End     string `json:"end"`

	Runtime        string `json:"runtime"`
	RuntimeVersion string `json:"runtime_version"`
	// ProfileDigest is the digest of the profile (profile.Inputs.Digest).
	ProfileDigest string `json:"profile_digest,omitempty"`

	Versions    ToolVersions `json:"versions"`
	Executables []ExeRecord  `json:"executables"`
	Steps       []StepRecord `json:"steps"`

	Exit     ExitEvidence     `json:"exit"`
	Stop     StopEvidence     `json:"stop"`
	Report   ReportOutcome    `json:"report_outcome"`
	Canary   CanaryEvidence   `json:"canary"`
	Terminal TerminalEvidence `json:"terminal"`
	Resolv   ResolvEvidence   `json:"resolv_conf"`
	// Notes are facts the steps found that no other field holds (a source
	// whose real path differs from its name, for example).
	Notes []string `json:"notes,omitempty"`
	// Cases are the requested cases, with the facts each speaks to.
	Cases []CaseEvidence `json:"cases"`
	// Files lists the evidence files written next to the summary.
	Files []string `json:"files"`
}

// writeFile creates dir/name exclusively with mode 0600 (never replacing, never
// following a link), writes data, syncs the file and then the folder.
func writeFile(dir, name string, data []byte) error {
	return writeFileMode(dir, name, data, 0o600)
}

func writeFileMode(dir, name string, data []byte, mode os.FileMode) error {
	p := dir + "/" + name
	fd, err := unix.Open(p, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_NOCTTY|unix.O_CLOEXEC, uint32(mode))
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	f := os.NewFile(uintptr(fd), p)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open folder %s: %w", dir, err)
	}
	defer unix.Close(fd)
	if err := unix.Fsync(fd); err != nil {
		return fmt.Errorf("sync folder %s: %w", dir, err)
	}
	return nil
}

func marshal(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
