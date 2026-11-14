//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
)

// These constants select one opt-in qualification experiment, not host policy.
const (
	R1Profile       = "r1-memory-fixed-v1"
	R1ControlTarget = "/r1-control"
	R1ContextName   = "context.json"
	R1ReleaseName   = "release.json"
	R1FinishName    = "finish.json"
	R1MaxFrame      = 4096
	R1MemoryBytes   = 64 << 20
	R1MainLifetime  = 8 * time.Second
	R1ChildLifetime = 10 * time.Second
	R1ReadyWait     = 2 * time.Second
	R1ReleaseWait   = 2 * time.Second
	R1TouchWait     = time.Second
	R1FinishWait    = 2 * time.Second
	R1ReapWait      = 500 * time.Millisecond
)

// R1Request deliberately has no plan hash: the plan already hashes stdin.
type R1Request struct {
	Version   int    `json:"version"`
	RunID     string `json:"run_id"`
	RequestID string `json:"request_id"`
	AttemptID string `json:"attempt_id"`
	Profile   string `json:"profile"`
	Challenge string `json:"challenge"`
}

// R1ObjectIdentity uses namespace-visible global inode identity, not host UID.
type R1ObjectIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type R1Context struct {
	R1Request
	RequestSHA256    string           `json:"request_sha256"`
	PlanSHA256       string           `json:"plan_sha256"`
	ControlDirectory R1ObjectIdentity `json:"control_directory"`
}

// R1Binding is common to every post-context record. Hashes bind exact bytes;
// echoing them is evidence, not authentication by the workload.
type R1Binding struct {
	R1Request
	RequestSHA256 string `json:"request_sha256"`
	PlanSHA256    string `json:"plan_sha256"`
	ContextSHA256 string `json:"context_sha256"`
}

type R1Release struct {
	R1Binding
	Action        string `json:"action"`
	ExpectedStage string `json:"expected_stage"`
	Sequence      int    `json:"sequence"`
}

type R1Finish struct {
	R1Binding
	ReleaseSHA256 string `json:"release_sha256"`
	Action        string `json:"action"`
	ExpectedStage string `json:"expected_stage"`
	Sequence      int    `json:"sequence"`
}

type R1ActorIdentity struct {
	PID          int              `json:"pid"`
	Start        uint64           `json:"start"`
	Executable   R1ObjectIdentity `json:"executable"`
	PIDNamespace R1ObjectIdentity `json:"pid_namespace"`
}

type R1Ready struct {
	ControlDirectory R1ObjectIdentity `json:"control_directory"`
	Main             R1ActorIdentity  `json:"main"`
	Child            R1ActorIdentity  `json:"child"`
}

type R1Pressure struct {
	ReleaseSHA256 string `json:"release_sha256"`
}

type R1Touched struct {
	ReleaseSHA256  string `json:"release_sha256"`
	RequestedBytes int    `json:"requested_bytes"`
	TouchedBytes   int    `json:"touched_bytes"`
	PageSize       int    `json:"page_size"`
	Retained       bool   `json:"retained"`
}

// Reaped means Wait4 has already returned this exact owned child's status.
// Unknown identity/status is explicit on failed startup or cleanup.
type R1ChildResult struct {
	Identity    *R1ActorIdentity `json:"identity,omitempty"`
	Termination string           `json:"termination"`
	Reap        string           `json:"reap"`
	ExitCode    *int             `json:"exit_code,omitempty"`
	Signal      *int             `json:"signal,omitempty"`
	Token       string           `json:"token"`
}

type R1Completion struct {
	ReleaseSHA256       string        `json:"release_sha256"`
	FinishSHA256        string        `json:"finish_sha256"`
	TouchedBytes        int           `json:"touched_bytes"`
	RetainedUntilFinish bool          `json:"retained_until_finish"`
	Child               R1ChildResult `json:"child"`
	Outcome             string        `json:"outcome"`
}

type R1Failure struct {
	Stage         string        `json:"stage"`
	Error         string        `json:"error"`
	ReleaseSHA256 string        `json:"release_sha256,omitempty"`
	FinishSHA256  string        `json:"finish_sha256,omitempty"`
	TouchedBytes  int           `json:"touched_bytes"`
	PageSize      int           `json:"page_size"`
	Cleanup       string        `json:"cleanup"`
	Child         R1ChildResult `json:"child"`
}

// Startup failures have no authenticated context and never echo rejected IDs.
type R1StartupFailure struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Stage   string `json:"stage"`
	Error   string `json:"error"`
	Cleanup string `json:"cleanup"`
}

func DecodeR1StartupFailure(data []byte) (R1StartupFailure, error) {
	var f R1StartupFailure
	if err := r1Decode(data, &f); err != nil {
		return f, err
	}
	if f.Version != 1 || f.Kind != "r1-startup-failure" || (f.Stage != "context" && f.Stage != "request") || (f.Error != "input" && f.Error != "deadline" && f.Error != "control" && f.Error != "binding") || (f.Cleanup != "not-opened" && f.Cleanup != "closed" && f.Cleanup != "failed") {
		return f, errors.New("invalid R1 startup failure")
	}
	return f, nil
}

// Each append-only frame contains exactly one stage payload. Sequence numbers
// are fixed: ready=0, pressure-start=1, retained=2, completion=3. Failure uses
// the sequence of the interrupted stage; it never replaces earlier frames.
type R1Evidence struct {
	R1Binding
	Stage      string        `json:"stage"`
	Sequence   int           `json:"sequence"`
	Ready      *R1Ready      `json:"ready,omitempty"`
	Pressure   *R1Pressure   `json:"pressure,omitempty"`
	Touched    *R1Touched    `json:"touched,omitempty"`
	Completion *R1Completion `json:"completion,omitempty"`
	Failure    *R1Failure    `json:"failure,omitempty"`
}

func (r R1Request) valid() bool {
	return r.Version == 1 && validHex(r.RunID) && validHex(r.RequestID) && validHex(r.AttemptID) && r.Profile == R1Profile && validHex(r.Challenge)
}
func (i R1ObjectIdentity) valid() bool { return i.Device != 0 && i.Inode != 0 }
func (i R1ActorIdentity) valid() bool {
	return i.PID > 0 && i.Start > 0 && i.Executable.valid() && i.PIDNamespace.valid()
}
func (b R1Binding) valid() bool {
	return b.R1Request.valid() && validDigest(b.RequestSHA256) && validDigest(b.PlanSHA256) && validDigest(b.ContextSHA256)
}
func (c R1ChildResult) valid() bool {
	if c.Token != "none" && c.Token != "finish" && c.Token != "abort" {
		return false
	}
	if c.Identity != nil && !c.Identity.valid() {
		return false
	}
	if c.Termination == "unknown" {
		return c.Reap == "unknown" && c.ExitCode == nil && c.Signal == nil
	}
	if c.Reap != "reaped" {
		return false
	}
	if c.Termination == "signaled" {
		return c.ExitCode == nil && c.Signal != nil && *c.Signal > 0 && *c.Signal <= 64
	}
	return c.Termination == "exited" && c.ExitCode != nil && *c.ExitCode >= 0 && *c.ExitCode <= 255 && c.Signal == nil
}

// In addition to the shared strict decoder, require every non-optional field,
// including false/zero fields. Embedded types are flattened exactly as JSON is.
func r1Required(t reflect.Type, raw []byte) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for n := 0; n < t.NumField(); n++ {
		f := t.Field(n)
		if f.Anonymous {
			if err := r1Required(f.Type, raw); err != nil {
				return err
			}
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")
		b, ok := fields[tag[0]]
		optional := len(tag) > 1 && tag[1] == "omitempty"
		if !ok {
			if optional {
				continue
			}
			return errors.New("missing R1 field")
		}
		if err := r1Required(f.Type, b); err != nil {
			return err
		}
	}
	return nil
}

func r1Decode(data []byte, v any) error {
	if err := decode(bytes.NewReader(data), R1MaxFrame, v); err != nil {
		return err
	}
	return r1Required(reflect.TypeOf(v), data)
}

func DecodeR1Request(data []byte) (R1Request, error) {
	var r R1Request
	if err := r1Decode(data, &r); err != nil {
		return r, err
	}
	if !r.valid() {
		return r, errors.New("invalid R1 request")
	}
	return r, nil
}
func DecodeR1Context(data []byte) (R1Context, error) {
	var c R1Context
	if err := r1Decode(data, &c); err != nil {
		return c, err
	}
	if !c.R1Request.valid() || !validDigest(c.RequestSHA256) || !validDigest(c.PlanSHA256) || !c.ControlDirectory.valid() {
		return c, errors.New("invalid R1 context")
	}
	return c, nil
}
func DecodeR1Release(data []byte) (R1Release, error) {
	var r R1Release
	if err := r1Decode(data, &r); err != nil {
		return r, err
	}
	if !r.R1Binding.valid() || r.Action != "release" || r.ExpectedStage != "ready" || r.Sequence != 1 {
		return r, errors.New("invalid R1 release")
	}
	return r, nil
}
func DecodeR1Finish(data []byte) (R1Finish, error) {
	var f R1Finish
	if err := r1Decode(data, &f); err != nil {
		return f, err
	}
	if !f.R1Binding.valid() || !validDigest(f.ReleaseSHA256) || f.Action != "finish" || f.ExpectedStage != "retained" || f.Sequence != 2 {
		return f, errors.New("invalid R1 finish")
	}
	return f, nil
}

func (e R1Evidence) valid() bool {
	if !e.R1Binding.valid() {
		return false
	}
	n := 0
	for _, present := range []bool{e.Ready != nil, e.Pressure != nil, e.Touched != nil, e.Completion != nil, e.Failure != nil} {
		if present {
			n++
		}
	}
	if n != 1 {
		return false
	}
	switch {
	case e.Ready != nil:
		r := e.Ready
		return e.Stage == "ready" && e.Sequence == 0 && r.ControlDirectory.valid() && r.Main.valid() && r.Child.valid() && r.Main.PID != r.Child.PID && r.Main.Executable == r.Child.Executable && r.Main.PIDNamespace == r.Child.PIDNamespace
	case e.Pressure != nil:
		return e.Stage == "pressure-start" && e.Sequence == 1 && validDigest(e.Pressure.ReleaseSHA256)
	case e.Touched != nil:
		t := e.Touched
		return e.Stage == "retained" && e.Sequence == 2 && validDigest(t.ReleaseSHA256) && t.RequestedBytes == R1MemoryBytes && t.TouchedBytes == R1MemoryBytes && r1Page(t.PageSize) && t.Retained
	case e.Completion != nil:
		c := e.Completion
		return e.Stage == "completion" && e.Sequence == 3 && validDigest(c.ReleaseSHA256) && validDigest(c.FinishSHA256) && c.TouchedBytes == R1MemoryBytes && c.RetainedUntilFinish && c.Child.valid() && c.Child.Identity != nil && c.Child.Token == "finish" && c.Child.Termination == "exited" && c.Child.ExitCode != nil && *c.Child.ExitCode == 0 && c.Outcome == "completed"
	case e.Failure != nil:
		f := e.Failure
		return e.Stage == "failure" && e.Sequence >= 0 && e.Sequence <= 3 && r1Stage(f.Stage) && r1Error(f.Error) && (f.ReleaseSHA256 == "" || validDigest(f.ReleaseSHA256)) && (f.FinishSHA256 == "" || validDigest(f.FinishSHA256)) && f.TouchedBytes >= 0 && f.TouchedBytes <= R1MemoryBytes && (f.PageSize == 0 || r1Page(f.PageSize)) && (f.Cleanup == "released" || f.Cleanup == "not-allocated" || f.Cleanup == "failed") && f.Child.valid()
	}
	return false
}
func r1Page(n int) bool { return n > 0 && n <= R1MemoryBytes && n&(n-1) == 0 && R1MemoryBytes%n == 0 }
func r1Stage(s string) bool {
	switch s {
	case "context", "child-ready", "ready", "release", "pressure-start", "allocate", "touch", "retained", "finish", "child-reap", "cleanup", "completion":
		return true
	}
	return false
}
func r1Error(s string) bool {
	switch s {
	case "input", "deadline", "control", "binding", "early-finish", "child", "metadata-unsupported", "io", "allocation", "touch", "cleanup":
		return true
	}
	return false
}
func DecodeR1Evidence(data []byte) (R1Evidence, error) {
	var e R1Evidence
	if err := r1Decode(data, &e); err != nil {
		return e, err
	}
	if !e.valid() {
		return e, errors.New("invalid R1 evidence")
	}
	return e, nil
}
func EncodeR1Evidence(w io.Writer, e R1Evidence) error {
	if !e.valid() {
		return errors.New("invalid R1 evidence")
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > R1MaxFrame {
		return errors.New("R1 evidence too large")
	}
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		return io.ErrShortWrite
	}
	return err
}
