//go:build linux

package intake

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/exchange/safefs"
	"ataniq/internal/intake/verify"
)

const testProject = "ataniq"

// testGates are the gates of the hand-made results.
var testGates = []verify.Gate{
	{Name: "vet", Argv: []string{"make", "vet"}},
	{Name: "test", Argv: []string{"make", "test"}},
}

// passResult is a hand-made passing result for tip. No sandbox is involved.
func passResult(tip string) verify.Result {
	return verify.Result{
		Tip: tip, TipRef: leadRef, PlanDigest: verify.PlanDigest(testGates),
		Passed: true, Exit: 0, Output: []byte("ataniq-verify: gate vet\nok\n"),
		Started: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), Duration: 1500 * time.Millisecond,
	}
}

func failResult(tip string) verify.Result {
	r := passResult(tip)
	r.Passed, r.Exit = false, 2
	return r
}

// newStoreDir makes an owner-only folder.
func newStoreDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "records")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func openTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := newStoreDir(t)
	return openTestStore(t, dir), dir
}

func idOf(c byte) string { return strings.Repeat(string(c), 40) }

func TestOpenStoreRefusals(t *testing.T) {
	base := t.TempDir()
	good := newStoreDir(t)
	link := filepath.Join(base, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mode := func(m os.FileMode) string {
		d := filepath.Join(base, "m"+m.String())
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, m); err != nil {
			t.Fatal(err)
		}
		return d
	}
	for name, dir := range map[string]string{
		"relative":       "records",
		"empty":          "",
		"not clean":      good + "/../" + filepath.Base(filepath.Dir(good)) + "/records",
		"dot":            good + "/.",
		"missing":        filepath.Join(base, "missing"),
		"symlink":        link,
		"symlink slash":  link + "/",
		"a file":         file,
		"group-writable": mode(0o770),
		"group-readable": mode(0o750),
		"world-readable": mode(0o755),
		"read-only":      mode(0o500),
		"sticky":         mode(0o700 | os.ModeSticky),
	} {
		s, err := OpenStore(dir)
		if err == nil {
			s.Close()
			t.Errorf("%s: the folder was accepted", name)
			continue
		}
		if !errors.Is(err, ErrStore) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	s, err := OpenStore(good)
	if err != nil {
		t.Fatalf("a good folder: %v", err)
	}
	s.Close()
	// The folder is swapped for a symlink after it was opened: the handle
	// still names the folder that was checked.
	s = openTestStore(t, good)
	moved := good + ".moved"
	if err := os.Rename(good, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(base, good); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutVerification(testProject, passResult(idOf('a')), testGates); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(moved); len(entries) == 0 {
		t.Error("the record did not go to the folder that was opened")
	}
	if entries, _ := os.ReadDir(base); slices.ContainsFunc(entries, func(e os.DirEntry) bool { return strings.Contains(e.Name(), "verification") }) {
		t.Error("a record went through the swapped symlink")
	}
}

func TestPutAndReadVerifications(t *testing.T) {
	s, dir := newTestStore(t)
	clock := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock } // the same time every call
	tipA, tipB := idOf('a'), idOf('b')

	var put []VerificationRecord
	for _, res := range []verify.Result{failResult(tipA), passResult(tipB), passResult(tipA), failResult(tipA)} {
		rec, err := s.PutVerification(testProject, res, testGates)
		if err != nil {
			t.Fatal(err)
		}
		put = append(put, rec)
	}
	got, err := s.Verifications(tipA)
	if err != nil {
		t.Fatal(err)
	}
	// Oldest first, although every record has the same clock.
	if len(got) != 3 || got[0].Passed || !got[1].Passed || got[2].Passed {
		t.Fatalf("records for tip a: %+v", got)
	}
	want := put[0]
	if got[0].Version != 1 || got[0].Project != testProject || got[0].TipRef != leadRef || got[0].Tip != tipA ||
		got[0].PlanDigest != verify.PlanDigest(testGates) || got[0].Exit != 2 || got[0].DurationMS != 1500 ||
		!got[0].Started.Equal(want.Started) || !slices.Equal(got[0].Gates[1].Argv, []string{"make", "test"}) {
		t.Errorf("record: %+v", got[0])
	}
	if got[1].Exit != 0 || !got[1].passing() || got[0].passing() {
		t.Errorf("passing: %+v %+v", got[0], got[1])
	}
	if other, err := s.Verifications(tipB); err != nil || len(other) != 1 || !other[0].Passed {
		t.Errorf("tip b: %v %+v", err, other)
	}
	if none, err := s.Verifications(idOf('c')); err != nil || len(none) != 0 {
		t.Errorf("tip c: %v %+v", err, none)
	}

	// The output is stored as its hash and length, and next to the record.
	sum := sha256.Sum256(passResult(tipA).Output)
	if got[1].OutputSHA256 != hex.EncodeToString(sum[:]) || got[1].OutputBytes != int64(len(passResult(tipA).Output)) {
		t.Errorf("output hash: %+v", got[1])
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var jsons, outs int
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		info, _ := e.Info()
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v", e.Name(), info.Mode())
		}
		switch {
		case strings.HasSuffix(e.Name(), ".json"):
			jsons++
			if bytes.Contains(data, []byte("ataniq-verify: gate vet")) {
				t.Errorf("%s holds the output", e.Name())
			}
		case strings.HasSuffix(e.Name(), ".out"):
			outs++
			if !bytes.Equal(data, passResult(tipA).Output) {
				t.Errorf("%s: %q", e.Name(), data)
			}
		default:
			t.Errorf("unexpected file %s", e.Name())
		}
		if !recordName.MatchString(e.Name()) && !strings.HasSuffix(e.Name(), ".out") {
			t.Errorf("file name %q", e.Name())
		}
	}
	if jsons != 4 || outs != 4 {
		t.Errorf("%d records, %d outputs", jsons, outs)
	}

	// A run with no output has no output file.
	res := passResult(tipA)
	res.Output = nil
	before := len(entries)
	if _, err := s.PutVerification(testProject, res, testGates); err != nil {
		t.Fatal(err)
	}
	if entries, _ = os.ReadDir(dir); len(entries) != before+1 {
		t.Errorf("%d files after a run with no output, was %d", len(entries), before)
	}
	// A second Store on the folder reads the same records.
	again := openTestStore(t, dir)
	if got2, err := again.Verifications(tipA); err != nil || len(got2) != 4 {
		t.Errorf("reopened: %v, %d records", err, len(got2))
	}
}

func TestPutVerificationRefusals(t *testing.T) {
	s, dir := newTestStore(t)
	good := passResult(idOf('a'))
	for name, tc := range map[string]struct {
		project string
		res     func(*verify.Result)
		gates   []verify.Gate
	}{
		"project":     {project: "Bad Name", gates: testGates},
		"no project":  {project: "", gates: testGates},
		"tip":         {project: testProject, gates: testGates, res: func(r *verify.Result) { r.Tip = "abc" }},
		"tip ref":     {project: testProject, gates: testGates, res: func(r *verify.Result) { r.TipRef = "refs/heads/main" }},
		"plan digest": {project: testProject, gates: testGates, res: func(r *verify.Result) { r.PlanDigest = strings.Repeat("0", 64) }},
		"other gates": {project: testProject, gates: testGates[:1]},
		"no gates":    {project: testProject, gates: nil},
		"stopped":     {project: testProject, gates: testGates, res: func(r *verify.Result) { r.Stopped = "whim" }},
	} {
		res := good
		if tc.res != nil {
			tc.res(&res)
		}
		if _, err := s.PutVerification(tc.project, res, tc.gates); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a refused record left %d files", len(entries))
	}
}

// TestStoreExclusiveCreation: a file is never replaced, also not through a
// symlink at its name, and a failed write leaves no temporary file.
func TestStoreExclusiveCreation(t *testing.T) {
	s, dir := newTestStore(t)
	name := "20261117T100000.000000000Z-verification-0123456789abcdef.json"
	if err := s.put(name, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.put(name, []byte("second\n")); err == nil || !errors.Is(err, unix.EEXIST) {
		t.Errorf("second write: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, name)); string(data) != "first\n" {
		t.Errorf("the file was replaced: %q", data)
	}
	// A symlink at the name is not followed and not replaced.
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("victim\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	planted := "20261117T100000.000000000Z-verification-fedcba9876543210.json"
	if err := os.Symlink(outside, filepath.Join(dir, planted)); err != nil {
		t.Fatal(err)
	}
	if err := s.put(planted, []byte("through the link\n")); err == nil {
		t.Error("a write at the name of a symlink succeeded")
	}
	if data, _ := os.ReadFile(outside); string(data) != "victim\n" {
		t.Errorf("the target of the symlink was written: %q", data)
	}
	if target, err := os.Readlink(filepath.Join(dir, planted)); err != nil || target != outside {
		t.Errorf("the symlink was replaced: %q, %v", target, err)
	}
	// A dangling symlink at the name is not followed either.
	dangling := "20261117T100000.000000000Z-verification-aaaaaaaaaaaaaaaa.json"
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Symlink(gone, filepath.Join(dir, dangling)); err != nil {
		t.Fatal(err)
	}
	if err := s.put(dangling, []byte("x\n")); err == nil {
		t.Error("a write at the name of a dangling symlink succeeded")
	}
	if _, err := os.Lstat(gone); err == nil {
		t.Error("the target of a dangling symlink was created")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}
}

func TestStoreNeverReplacesAcrossManyRecords(t *testing.T) {
	s, dir := newTestStore(t)
	s.now = func() time.Time { return time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC) }
	for i := 0; i < 60; i++ {
		if _, err := s.PutVerification(testProject, passResult(idOf('a')), testGates); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 120 {
		t.Errorf("%d files for 60 records with output", len(entries))
	}
	recs, err := s.Verifications(idOf('a'))
	if err != nil || len(recs) != 60 {
		t.Errorf("%v, %d records", err, len(recs))
	}
}

// writeRecord writes a record file by hand under a well-formed name.
func writeRecord(t *testing.T, dir, stamp, kind, content string) string {
	t.Helper()
	name := stamp + "-" + kind + "-0123456789abcdef.json"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestStoreReadsOnlyRegularFilesOfRecordNames(t *testing.T) {
	s, dir := newTestStore(t)
	tip := idOf('a')
	rec, err := s.PutVerification(testProject, passResult(tip), testGates)
	if err != nil {
		t.Fatal(err)
	}
	goodText, _ := os.ReadFile(only(t, dir, ".json"))

	// Files with other names are ignored, whatever they are.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".tmp-0123"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{
		"20261117T100000.000000000Z-verification-0123456789abcdef.json.bak",
		"20261117T100000.000000000Z-other-0123456789abcdef.json",
		"20261117T100000.000000000Z-verification-0123456789abcde.json",
		"x20261117T100000.000000000Z-verification-0123456789abcdef.json",
		"20261117T100000.000000000Z-verification-0123456789ABCDEF.json",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{garbage"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	within(t, "a folder with odd files", func() {
		got, err := s.Verifications(tip)
		if err != nil || len(got) != 1 || got[0].Tip != rec.Tip {
			t.Errorf("%v, %+v", err, got)
		}
	})

	// A symlink with a record's name is not followed and not read, and it is
	// an error: a record that cannot be read could be a failure.
	valid := filepath.Join(t.TempDir(), "valid.json")
	if err := os.WriteFile(valid, goodText, 0o600); err != nil {
		t.Fatal(err)
	}
	linkName := "20300101T000000.000000000Z-verification-aaaaaaaaaaaaaaaa.json"
	if err := os.Symlink(valid, filepath.Join(dir, linkName)); err != nil {
		t.Fatal(err)
	}
	within(t, "a symlink", func() {
		got, err := s.Verifications(tip)
		if err == nil || !strings.Contains(err.Error(), linkName) || !errors.Is(err, safefs.ErrEscape) {
			t.Errorf("symlink: %v", err)
		}
		if got != nil {
			t.Errorf("the symlink's target was read: %+v", got)
		}
		// A verification file is not an intake record: Intakes does not
		// read it.
		if recs, err := s.Intakes(); err != nil || len(recs) != 0 {
			t.Errorf("Intakes: %v, %+v", err, recs)
		}
	})
	if err := os.Remove(filepath.Join(dir, linkName)); err != nil {
		t.Fatal(err)
	}

	// A FIFO with a record's name does not block, and is an error.
	fifoName := "20300101T000000.000000000Z-verification-bbbbbbbbbbbbbbbb.json"
	if err := unix.Mkfifo(filepath.Join(dir, fifoName), 0o600); err != nil {
		t.Fatal(err)
	}
	within(t, "a FIFO", func() {
		if _, err := s.Verifications(tip); err == nil || !strings.Contains(err.Error(), fifoName) || !errors.Is(err, safefs.ErrNotRegular) {
			t.Errorf("fifo: %v", err)
		}
	})
	os.Remove(filepath.Join(dir, fifoName))

	// A folder with a record's name is an error too.
	dirName := "20300101T000000.000000000Z-intake-cccccccccccccccc.json"
	if err := os.Mkdir(filepath.Join(dir, dirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Intakes(); err == nil || !strings.Contains(err.Error(), dirName) {
		t.Errorf("folder: %v", err)
	}
}

// within runs f and fails the test when it does not return in time, which is
// what a read that blocks on a FIFO would do.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: blocked", what)
	}
}

// only returns the one file in dir with the suffix.
func only(t *testing.T, dir, suffix string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d files with suffix %s", len(found), suffix)
	}
	return found[0]
}

func TestStoreRecordSizeAndForm(t *testing.T) {
	tip := idOf('a')
	s, dir := newTestStore(t)
	if _, err := s.PutVerification(testProject, passResult(tip), testGates); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(only(t, dir, ".json"))
	if err != nil {
		t.Fatal(err)
	}
	// Whitespace after the value is fine; the size cap is 1 MiB for the file.
	pad := func(n int) string { return string(text) + strings.Repeat(" ", n-len(text)) }
	for _, tc := range []struct {
		name    string
		content string
		ok      bool
	}{
		{"exactly the cap", pad(maxRecordBytes), true},
		{"one byte over the cap", pad(maxRecordBytes + 1), false},
		{"empty", "", false},
		{"not json", "{garbage", false},
		{"data after the value", string(text) + "{}", false},
		{"a second record", string(text) + string(text), false},
		{"unknown field", strings.Replace(string(text), `"project"`, `"extra": 1, "project"`, 1), false},
		{"unknown nested field", strings.Replace(string(text), `"name"`, `"extra": 1, "name"`, 1), false},
		{"wrong version", strings.Replace(string(text), `"version": 1`, `"version": 2`, 1), false},
		{"no tip", strings.Replace(string(text), tip, "abc", 1), false},
		{"wrong type", strings.Replace(string(text), `"passed": true`, `"passed": "yes"`, 1), false},
		{"an array", "[]", false},
		{"null", "null", false},
		{"intake fields in a verification record", `{"version":1,"base":"x"}`, false},
	} {
		d := newStoreDir(t)
		s2 := openTestStore(t, d)
		name := writeRecord(t, d, "20300101T000000.000000000Z", "verification", tc.content)
		recs, err := s2.Verifications(tip)
		switch {
		case tc.ok && (err != nil || len(recs) != 1):
			t.Errorf("%s: %v, %d records", tc.name, err, len(recs))
		case !tc.ok && (err == nil || !strings.Contains(err.Error(), name) || !errors.Is(err, ErrStore)):
			t.Errorf("%s: error = %v", tc.name, err)
		}
		if !tc.ok && strings.Contains(tc.name, "over the cap") && !errors.Is(err, safefs.ErrLimit) {
			t.Errorf("%s: %v is not a limit", tc.name, err)
		}
		// A malformed record of any tip stops a read for another tip.
		if !tc.ok {
			if _, err := s2.Verifications(idOf('f')); err == nil {
				t.Errorf("%s: a malformed record is ignored for another tip", tc.name)
			}
		}
	}
}

func TestIntakeRecordRoundTrip(t *testing.T) {
	s, dir := newTestStore(t)
	ver, err := s.PutVerification(testProject, passResult(idOf('b')), testGates)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 17, 12, 0, 0, 123456789, time.UTC)
	rec := IntakeRecord{
		Version: 1, Time: t0, Project: testProject,
		TargetRef: upstreamRef, TipRef: "refs/leads/role/heads/caf\xe9",
		Base: idOf('a'), Tip: idOf('b'), ReviewDigest: strings.Repeat("c", 64),
		Commits: 3, Paths: 7,
		SignedPaths:  []string{".envrc", "sub/bad\xff\xfe.json", "tab\there", "quote\"back\\slash", "café", "\x00nul"},
		Verification: ver,
	}
	// Three records with the same time: the order is the order of writing.
	for i := 1; i <= 3; i++ {
		r := rec
		r.Commits = i
		if err := s.putIntake(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Intakes()
	if err != nil || len(got) != 3 {
		t.Fatalf("%v, %d records", err, len(got))
	}
	if got[0].Commits != 1 || got[1].Commits != 2 || got[2].Commits != 3 {
		t.Errorf("order: %d %d %d", got[0].Commits, got[1].Commits, got[2].Commits)
	}
	g := got[2]
	if !g.Time.Equal(t0) || g.TargetRef != rec.TargetRef || g.TipRef != rec.TipRef || g.Base != rec.Base || g.Tip != rec.Tip ||
		g.ReviewDigest != rec.ReviewDigest || g.Commits != 3 || g.Paths != 7 || !slices.Equal(g.SignedPaths, rec.SignedPaths) {
		t.Errorf("round trip:\n got %+v\nwant %+v", g, rec)
	}
	if !g.Verification.passing() || g.Verification.Tip != idOf('b') || g.Verification.OutputSHA256 != ver.OutputSHA256 {
		t.Errorf("verification: %+v", g.Verification)
	}
	// Intake records are not verification records.
	if vs, err := s.Verifications(idOf('b')); err != nil || len(vs) != 1 {
		t.Errorf("%v, %d verification records", err, len(vs))
	}
	// The file holds the raw bytes of a path that is not UTF-8 as base64.
	var found bool
	for _, e := range mustReadDir(t, dir) {
		if strings.Contains(e, "-intake-") {
			data, _ := os.ReadFile(filepath.Join(dir, e))
			if !bytes.Contains(data, []byte(`"b64"`)) || bytes.Contains(data, []byte("\xff")) || bytes.Contains(data, []byte("�")) {
				t.Errorf("%s: %s", e, data)
			}
			found = true
		}
	}
	if !found {
		t.Error("no intake file")
	}

	// A record that is not well formed is refused when written.
	bad := rec
	bad.Tip = "x"
	if err := s.putIntake(bad); err == nil {
		t.Error("a record with a bad tip was written")
	}
	// And one that was edited to say something else is refused when read.
	d := newStoreDir(t)
	s2 := openTestStore(t, d)
	data, _ := json2(rec)
	for name, text := range map[string]string{
		"b64 of valid text": strings.Replace(string(data), `".envrc"`, `{"b64":"LmVudnJj"}`, 1),
		"b64 and more":      strings.Replace(string(data), `".envrc"`, `{"b64":"/w==","x":1}`, 1),
		"a number":          strings.Replace(string(data), `".envrc"`, `7`, 1),
		"bad base64":        strings.Replace(string(data), `".envrc"`, `{"b64":"!!"}`, 1),
		"unknown field":     strings.Replace(string(data), `"commits"`, `"extra":0,"commits"`, 1),
		"verification":      strings.Replace(string(data), `"verification": {`, `"verification": {"extra": 1,`, 1),
	} {
		d2 := newStoreDir(t)
		s3 := openTestStore(t, d2)
		writeRecord(t, d2, "20300101T000000.000000000Z", "intake", text)
		if _, err := s3.Intakes(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	writeRecord(t, d, "20300101T000000.000000000Z", "intake", string(data))
	if recs, err := s2.Intakes(); err != nil || len(recs) != 1 {
		t.Errorf("the unedited record: %v, %d", err, len(recs))
	}
}

func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func json2(rec IntakeRecord) ([]byte, error) { return json.MarshalIndent(rec, "", "  ") }
