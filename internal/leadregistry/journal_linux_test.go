package leadregistry

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadrole"
)

// buildJournal runs a fixed sequence of operations and returns the journal
// bytes, the file size after each record (index = sequence number) and the
// list of roles after each record.
func buildJournal(t *testing.T) ([]byte, []int64, [][]Entry) {
	t.Helper()
	ok := okf(t)
	dir := regDir(t)
	r := openReg(t, dir)
	sizes := []int64{journalSize(t, dir)}
	states := [][]Entry{snapshot(t, r)}
	step := func() {
		sizes = append(sizes, journalSize(t, dir))
		states = append(states, snapshot(t, r))
	}
	a, b := lead(t, "alpha"), lead(t, "beta")
	ok(r.Define(a, ManagerAtaniq, goodProfile()))
	step()
	ok(r.Define(b, ManagerTmux, goodProfile()))
	step()
	ok(r.SetDesired(a, DesiredRunning))
	step()
	in, err := r.BeginLaunch(a)
	if err != nil {
		t.Fatal(err)
	}
	step()
	ok(r.RecordOutcome(a, in.ID, 1, OutcomeLaunched, UnitEvidence{InvocationID: "inv", ControlGroupPath: "/x.slice/y", ControlGroupID: "9", BootID: "b"}))
	step()
	ok(r.SetObserved(a, 1, Observed{Model: Seen("m"), Version: Seen(`v"1<&>`)}))
	step()
	ok(r.SetState(a, setState(StateReady, 1)))
	step()
	ok(r.SetState(a, StateChange{Epoch: 1, State: StateStopped, Evidence: EvidenceOwner, Reason: Reason{Code: "stop", Detail: "café"}, Proof: "gone"}))
	step()
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	return data, sizes, states
}

// TestCrashTruncationAtEveryOffset cuts the journal at every byte offset and
// checks the recovery rule: Open succeeds, after the cut each record is
// wholly present or wholly absent, a record whose bytes are intact is never
// lost, and the registry is writable afterwards.
func TestCrashTruncationAtEveryOffset(t *testing.T) {
	data, sizes, states := buildJournal(t)
	if int64(len(data)) != sizes[len(sizes)-1] {
		t.Fatal("sizes")
	}
	for cut := 0; cut <= len(data); cut++ {
		dir := writeJournal(t, data[:cut])
		r, err := Open(dir, testOpts(1))
		if err != nil {
			t.Fatalf("cut %d: Open: %v", cut, err)
		}
		// The last record fully contained in the cut.
		last := -1
		for i, s := range sizes {
			if int64(cut) >= s {
				last = i
			}
		}
		keep := int64(0)
		if last >= 0 {
			keep = sizes[last]
		}
		rec := r.Recovery()
		if int64(cut) == keep {
			if rec != (Recovery{}) {
				t.Fatalf("cut %d on a boundary reported recovery %+v", cut, rec)
			}
		} else if rec != (Recovery{Lines: 1, Bytes: int64(cut) - keep, Offset: keep}) {
			t.Fatalf("cut %d: recovery %+v, want %d bytes cut at %d", cut, rec, int64(cut)-keep, keep)
		}
		var want []Entry
		if last >= 0 {
			want = states[last]
		}
		if got := snapshot(t, r); !sameEntries(got, want) {
			t.Fatalf("cut %d: state is not that of record %d", cut, last)
		}
		// The unfinished tail is gone from the file.
		if st := journalSize(t, dir); last >= 0 && st != keep {
			t.Fatalf("cut %d: file is %d bytes after recovery, want %d", cut, st, keep)
		}
		if cut%7 == 0 || cut > len(data)-3 {
			// The registry accepts a write and a second Open sees a clean file.
			if _, err := r.Define(lead(t, "gamma"), ManagerAtaniq, goodProfile()); err != nil {
				t.Fatalf("cut %d: write after recovery: %v", cut, err)
			}
			r.Close()
			r2, err := Open(dir, testOpts(1))
			if err != nil {
				t.Fatalf("cut %d: reopen: %v", cut, err)
			}
			if _, err := r2.Get(lead(t, "gamma")); err != nil {
				t.Fatalf("cut %d: %v", cut, err)
			}
			if r2.Recovery() != (Recovery{}) {
				t.Fatalf("cut %d: second open recovered again", cut)
			}
			r2.Close()
		} else {
			r.Close()
		}
	}
}

// journalLines splits a journal into its lines, newlines kept.
func journalLines(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n') + 1
		out = append(out, data[:i])
		data = data[i:]
	}
	return out
}

func openErr(t *testing.T, data []byte) error {
	t.Helper()
	dir := writeJournal(t, data)
	r, err := Open(dir, testOpts(1))
	if err == nil {
		r.Close()
	}
	return err
}

// craft builds a line with a valid chain that would follow lines[:n] as
// record number seq.
func craft(t *testing.T, lines [][]byte, n int, seq uint64, kind string, role leadrole.Role, body any) []byte {
	t.Helper()
	prev := genesis
	for i := 0; i < n; i++ {
		_, sum, err := decodeLine(prev, uint64(i), bytes.TrimSuffix(lines[i], []byte("\n")))
		if err != nil {
			t.Fatal(err)
		}
		prev = sum
	}
	line, _, err := encodeLine(prev, seq, (&fakeClock{}).Now(), kind, role, body)
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func TestCorruptionFailsOpenAndNamesTheLine(t *testing.T) {
	data, _, _ := buildJournal(t)
	lines := journalLines(data)
	n := len(lines)
	alpha := lead(t, "alpha")
	// assemble returns the first keep lines followed by extra.
	assemble := func(keep int, extra ...[]byte) []byte {
		var parts [][]byte
		parts = append(parts, lines[:keep]...)
		parts = append(parts, extra...)
		return bytes.Join(parts, nil)
	}
	replace := func(i int, repl []byte) []byte {
		parts := append([][]byte(nil), lines...)
		parts[i] = repl
		return bytes.Join(parts, nil)
	}
	edit := func(i int, from, to string) []byte {
		if !bytes.Contains(lines[i], []byte(from)) {
			t.Fatalf("line %d lacks %q", i+1, from)
		}
		return replace(i, bytes.Replace(lines[i], []byte(from), []byte(to), 1))
	}
	cases := []struct {
		name string
		data []byte
		line int // 1-based line the error must name
	}{
		{"changed payload value in the middle", edit(4, `"epoch":1`, `"epoch":2`), 5},
		{"changed hash digit", edit(2, `"hash":"`, `"hash":"0`), 3},
		{"deleted middle line", append(assemble(3), bytes.Join(lines[4:], nil)...), 4},
		{"deleted first record", append(assemble(1), bytes.Join(lines[2:], nil)...), 2},
		{"duplicated line", append(assemble(4, lines[3]), bytes.Join(lines[4:], nil)...), 5},
		{"replaced line", replace(2, lines[3]), 3},
		{"garbage line", replace(4, []byte("not json\n")), 5},
		{"empty line", replace(4, []byte("\n")), 5},
		{"cut JSON with a newline", replace(4, append(append([]byte(nil), lines[4][:20]...), '\n')), 5},
		{"changed last complete line", edit(n-1, `"proof":"gone"`, `"proof":"gonf"`), n},
		{"unknown record kind", assemble(3, craft(t, lines, 3, 3, "bogus", alpha, &desiredPayload{Desired: DesiredRunning})), 4},
		{"sequence gap", assemble(3, craft(t, lines, 3, 5, kindDesired, alpha, &desiredPayload{Desired: DesiredRunning})), 4},
		{"sequence repeated", assemble(3, craft(t, lines, 3, 2, kindDesired, alpha, &desiredPayload{Desired: DesiredRunning})), 4},
		{"second header", assemble(3, craft(t, lines, 3, 3, kindHeader, leadrole.Role{}, &headerPayload{Format: formatName, Version: 1})), 4},
		{"invalid record", assemble(3, craft(t, lines, 3, 3, kindDesired, alpha, &desiredPayload{Desired: "paused"})), 4},
		{"stale epoch in a record", assemble(3, craft(t, lines, 3, 3, kindState, alpha, &statePayload{Epoch: 9, State: StateReady, Evidence: EvidenceOwner, Reason: Reason{Code: "x"}})), 4},
		{"desired from a live state", assemble(5, craft(t, lines, 5, 5, kindState, alpha, &statePayload{Epoch: 1, State: StateDesired, Evidence: EvidenceOwner, Reason: Reason{Code: "x"}})), 6},
		{"stopped while the intent is pending", assemble(5, craft(t, lines, 5, 5, kindState, alpha, &statePayload{Epoch: 1, State: StateStopped, Evidence: EvidenceObservation, Reason: Reason{Code: "x"}, Proof: "p"})), 6},
		{"owner decision outside unknown", assemble(3, craft(t, lines, 3, 3, kindOwnerDecision, alpha, &decisionPayload{Epoch: 0, State: StateStopped, Reason: Reason{Code: "x"}, Decision: "d"})), 4},
		{"owner decision without text", assemble(3, craft(t, lines, 3, 3, kindOwnerDecision, alpha, &decisionPayload{Epoch: 0, State: StateStopped, Reason: Reason{Code: "x"}})), 4},
		{"owner decision to a live state", assemble(3, craft(t, lines, 3, 3, kindOwnerDecision, alpha, &decisionPayload{Epoch: 0, State: StateReady, Reason: Reason{Code: "x"}, Decision: "d"})), 4},
		{"record for an undefined role", assemble(3, craft(t, lines, 3, 3, kindDesired, lead(t, "ghost"), &desiredPayload{Desired: DesiredRunning})), 4},
		{"record without a role", assemble(3, craft(t, lines, 3, 3, kindDesired, leadrole.Role{}, &desiredPayload{Desired: DesiredRunning})), 4},
		{"line over the cap", assemble(3, append(bytes.Repeat([]byte("x"), MaxLineBytes+10), '\n')), 4},
	}
	for _, c := range cases {
		err := openErr(t, c.data)
		var ce *CorruptError
		if !errors.Is(err, ErrCorrupt) || !errors.As(err, &ce) {
			t.Errorf("%s: Open = %v, want ErrCorrupt", c.name, err)
			continue
		}
		if ce.Line != c.line {
			t.Errorf("%s: error names line %d, want %d (%v)", c.name, ce.Line, c.line, err)
		}
		if !strings.Contains(err.Error(), "line ") {
			t.Errorf("%s: message does not name the line: %v", c.name, err)
		}
	}
}

func TestHeaderProblems(t *testing.T) {
	data, _, _ := buildJournal(t)
	lines := journalLines(data)
	if err := openErr(t, bytes.Join(lines[1:], nil)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("no header: %v", err)
	}
	mk := func(p *headerPayload) []byte {
		line, _, err := encodeLine(genesis, 0, (&fakeClock{}).Now(), kindHeader, leadrole.Role{}, p)
		if err != nil {
			t.Fatal(err)
		}
		return line
	}
	for name, p := range map[string]*headerPayload{
		"future version": {Format: formatName, Version: 2},
		"version zero":   {Format: formatName, Version: 0},
		"other format":   {Format: "something-else", Version: 1},
	} {
		err := openErr(t, mk(p))
		var ce *CorruptError
		if !errors.Is(err, ErrCorrupt) || !errors.As(err, &ce) || ce.Line != 1 {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := openErr(t, mk(&headerPayload{Format: formatName, Version: formatVersion})); err != nil {
		t.Errorf("header only: %v", err)
	}
}

// TestWholeLineTruncationIsNotDetected documents the limit of the chain: a
// journal cut at a line boundary is a valid, shorter journal.
func TestWholeLineTruncationIsNotDetected(t *testing.T) {
	data, sizes, states := buildJournal(t)
	for i := range sizes {
		dir := writeJournal(t, data[:sizes[i]])
		r, err := Open(dir, testOpts(1))
		if err != nil {
			t.Fatalf("prefix of %d records: %v", i, err)
		}
		if !sameEntries(snapshot(t, r), states[i]) {
			t.Fatalf("prefix of %d records: state differs", i)
		}
		if rev, _ := r.Revision(); rev != uint64(i) {
			t.Fatalf("revision %d, want %d", rev, i)
		}
		r.Close()
	}
}

func TestUnknownPayloadFieldIsCorrupt(t *testing.T) {
	data, _, _ := buildJournal(t)
	lines := journalLines(data)
	type extra struct {
		Desired Desired `json:"desired"`
		Extra   int     `json:"extra"`
	}
	line := craft(t, lines, 2, 2, kindDesired, lead(t, "alpha"), &extra{DesiredRunning, 1})
	var ce *CorruptError
	if err := openErr(t, append(bytes.Join(lines[:2], nil), line...)); !errors.As(err, &ce) || ce.Line != 3 {
		t.Fatalf("unknown payload field: %v", err)
	}
}

func TestPermissionAndSymlinkRefusals(t *testing.T) {
	t.Run("file with group or other bits", func(t *testing.T) {
		dir := regDir(t)
		openReg(t, dir).Close()
		for _, mode := range []os.FileMode{0o640, 0o604, 0o660, 0o666, 0o700, 0o601} {
			if err := os.Chmod(filepath.Join(dir, FileName), mode); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir, testOpts(1)); !errors.Is(err, ErrInsecure) {
				t.Errorf("mode %o: %v", mode, err)
			}
		}
		if err := os.Chmod(filepath.Join(dir, FileName), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := Open(dir, testOpts(1))
		if err != nil {
			t.Fatalf("mode 0600: %v", err)
		}
		r.Close()
	})
	t.Run("file narrower than 0600 is not refused for its mode", func(t *testing.T) {
		dir := regDir(t)
		openReg(t, dir).Close()
		if err := os.Chmod(filepath.Join(dir, FileName), 0o400); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, testOpts(1)); errors.Is(err, ErrInsecure) {
			t.Fatalf("mode 0400 refused as insecure: %v", err)
		}
	})
	t.Run("folder with group or other bits", func(t *testing.T) {
		dir := regDir(t)
		openReg(t, dir).Close()
		for _, mode := range []os.FileMode{0o750, 0o705, 0o755, 0o770, 0o701} {
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir, testOpts(1)); !errors.Is(err, ErrInsecure) {
				t.Errorf("folder mode %o: %v", mode, err)
			}
		}
		os.Chmod(dir, 0o700)
	})
	t.Run("file is a symlink", func(t *testing.T) {
		dir := regDir(t)
		openReg(t, dir).Close()
		target := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.Rename(filepath.Join(dir, FileName), target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, FileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, testOpts(1)); !errors.Is(err, ErrInsecure) {
			t.Fatalf("symlinked file: %v", err)
		}
	})
	t.Run("file is a dangling symlink", func(t *testing.T) {
		dir := regDir(t)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "missing")
		if err := os.Symlink(missing, filepath.Join(dir, FileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, testOpts(1)); !errors.Is(err, ErrInsecure) {
			t.Fatalf("dangling symlink: %v", err)
		}
		if _, err := os.Lstat(missing); err == nil {
			t.Fatal("Open created the symlink target")
		}
	})
	t.Run("folder is a symlink", func(t *testing.T) {
		real := regDir(t)
		openReg(t, real).Close()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(link, testOpts(1)); !errors.Is(err, ErrInsecure) {
			t.Fatalf("symlinked folder: %v", err)
		}
	})
	t.Run("folder is a file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "plain")
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(p, testOpts(1)); !errors.Is(err, ErrInsecure) {
			t.Fatalf("file as folder: %v", err)
		}
	})
	t.Run("journal is not a regular file", func(t *testing.T) {
		dir := regDir(t)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(dir, FileName), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, testOpts(1)); !errors.Is(err, ErrInsecure) {
			t.Fatalf("fifo: %v", err)
		}
	})
	t.Run("journal is a folder", func(t *testing.T) {
		dir := regDir(t)
		if err := os.MkdirAll(filepath.Join(dir, FileName), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, testOpts(1)); err == nil {
			t.Fatal("opened a folder as the journal")
		}
	})
}

func TestEmptyAndExistingFolder(t *testing.T) {
	dir := regDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir, testOpts(1))
	if err != nil {
		t.Fatalf("existing empty folder: %v", err)
	}
	r.Close()
	// An existing zero-length journal (a crash after the create) starts fresh.
	r2, err := Open(writeJournal(t, nil), testOpts(1))
	if err != nil {
		t.Fatal(err)
	}
	if rev, _ := r2.Revision(); rev != 0 {
		t.Fatal(rev)
	}
	r2.Close()
}
