package switcher

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	rel1 = "0001-aaaaaaa"
	rel2 = "0002-bbbbbbb"
	rel3 = "0003-ccccccc"
)

// active switches to rel1 and then rel2, both healthy.
func active(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.release(rel1, "2026-11-01")
	e.release(rel2, "2026-11-01")
	if code := e.switchTo(rel1); code != ExitOK {
		t.Fatalf("switch to %s: exit %d", rel1, code)
	}
	if code := e.switchTo(rel2); code != ExitOK {
		t.Fatalf("switch to %s: exit %d", rel2, code)
	}
	e.runner.reset()
	return e
}

// unchanged checks that a failed switch left current at want and that the
// failure was recorded at step.
func (e *env) unchanged(want, step string) {
	e.t.Helper()
	if got := e.link("current"); got != want {
		e.t.Errorf("current -> %q, want %q", got, want)
	}
	rec := e.record()
	if rec.Result != ResultFailed || rec.FailedStep != step {
		e.t.Errorf("record: result %q at %q, want failed at %q (%s)", rec.Result, rec.FailedStep, step, rec.Error)
	}
}

func TestCleanSwitch(t *testing.T) {
	e := newEnv(t)
	e.release(rel1, "2026-11-01")
	e.release(rel2, "2026-11-01")
	if code := e.switchTo(rel1); code != ExitOK {
		t.Fatalf("first switch: exit %d", code)
	}
	if got := e.link("current"); got != "releases/"+rel1 {
		t.Fatalf("current -> %q", got)
	}
	if got := e.link("previous"); got != "" {
		t.Fatalf("previous after the first switch -> %q, want none", got)
	}
	e.runner.reset()
	if code := e.switchTo(rel2); code != ExitOK {
		t.Fatalf("second switch: exit %d", code)
	}
	if got := e.link("current"); got != "releases/"+rel2 {
		t.Errorf("current -> %q", got)
	}
	if got := e.link("previous"); got != "releases/"+rel1 {
		t.Errorf("previous -> %q", got)
	}
	if got := e.highest(); got != "2" {
		t.Errorf("highest counter %q, want 2", got)
	}
	dir := e.relDir(rel2)
	want := []string{
		filepath.Join(dir, NoAICheckProgram) + " --release " + dir,
		"validate-units verify " + dir + "/etc/systemd/app.service",
		"validate-rules -c",
		"reload-units",
		"health-probe http://127.0.0.1/healthz",
	}
	if strings.Join(e.runner.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands run:\n%s\nwant:\n%s", strings.Join(e.runner.calls, "\n"), strings.Join(want, "\n"))
	}
	for _, name := range []string{"current.new", "previous.new"} {
		if _, err := os.Lstat(filepath.Join(e.root, BaseDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", name, err)
		}
	}
}

func TestDeployRecord(t *testing.T) {
	e := active(t)
	rec := e.record()
	want := Record{
		ID:              rel2,
		PreviousID:      rel1,
		CurrentID:       rel2,
		Counter:         2,
		Mode:            ModeManual,
		Time:            "2026-11-21T12:00:00Z",
		Result:          ResultOK,
		Check:           CheckPassed,
		PackageSnapshot: "",
	}
	if rec != want {
		t.Errorf("deploy.json:\n%+v\nwant\n%+v", rec, want)
	}
	data, err := os.ReadFile(filepath.Join(e.root, StateDir, DeployFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "previous_id", "current_id", "counter", "mode", "rollback", "time", "result", "check", "failed_step", "error", "detail", "package_snapshot", "restored_health"} {
		if !strings.Contains(string(data), `"`+key+`":`) {
			t.Errorf("deploy.json has no %s:\n%s", key, data)
		}
	}
	info, err := os.Stat(filepath.Join(e.root, StateDir, DeployFile))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("deploy.json mode %v, %v", info.Mode(), err)
	}

	// A failure records where it failed and keeps current.
	e.release(rel3, "2026-11-01")
	e.runner.fail["ah-noaicheck"] = -1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	rec = e.record()
	if rec.ID != rel3 || rec.PreviousID != rel2 || rec.CurrentID != rel2 || rec.Counter != 3 ||
		rec.Result != ResultFailed || rec.Check != CheckFailed || rec.FailedStep != "noaicheck" ||
		rec.Error != CodeNoAICheckFailed || rec.Detail != "failed (run it by hand to see its hits)" {
		t.Errorf("deploy.json after a failed check: %+v", rec)
	}
}

func TestWrongSum(t *testing.T) {
	e := active(t)
	dir := e.release(rel3, "2026-11-01")
	e.write(filepath.Join(dir, "bin/app"), "changed\n", 0o755)
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "verify")
	if !strings.Contains(e.record().Detail, "bin/app does not match") {
		t.Errorf("error: %s: %s", e.record().Error, e.record().Detail)
	}
	if len(e.runner.calls) != 0 {
		t.Errorf("commands run after a failed verification: %v", e.runner.calls)
	}
}

func TestExtraFile(t *testing.T) {
	e := active(t)
	dir := e.release(rel3, "2026-11-01")
	e.write(filepath.Join(dir, "etc/extra.conf"), "x\n", 0o644)
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "verify")
	if !strings.Contains(e.record().Detail, "not listed in SHA256SUMS: etc/extra.conf") {
		t.Errorf("error: %s: %s", e.record().Error, e.record().Detail)
	}
}

func TestMissingFile(t *testing.T) {
	e := active(t)
	dir := e.release(rel3, "2026-11-01")
	if err := os.Remove(filepath.Join(dir, "bin/app")); err != nil {
		t.Fatal(err)
	}
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "verify")
	if !strings.Contains(e.record().Detail, "missing: bin/app") {
		t.Errorf("error: %s: %s", e.record().Error, e.record().Detail)
	}
}

func TestPathEscapeInSums(t *testing.T) {
	const sum = "0000000000000000000000000000000000000000000000000000000000000000"
	for _, path := range []string{"../0002-bbbbbbb/bin/app", "/etc/passwd", "./bin/app", "bin/../MANIFEST", "bin//app", "bin/app/", "."} {
		t.Run(path, func(t *testing.T) {
			e := active(t)
			dir := e.release(rel3, "2026-11-01")
			f, err := os.OpenFile(filepath.Join(dir, SumsFile), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString(sum + "  " + path + "\n")
			f.Close()
			if code := e.switchTo(rel3); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			e.unchanged("releases/"+rel2, "verify")
			if !strings.Contains(e.record().Detail, "escapes the release folder") {
				t.Errorf("error: %s: %s", e.record().Error, e.record().Detail)
			}
		})
	}
}

func TestSumsFormat(t *testing.T) {
	const sum = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	for _, tc := range []struct {
		name, data, want string
	}{
		{"binary marker", sum + " *a\n", ""},
		{"listed twice", sum + "  a\n" + sum + "  a\n", "listed twice"},
		{"sums listed", sum + "  SHA256SUMS\n", "must not be listed"},
		{"signature listed", sum + "  SHA256SUMS.sig\n", "must not be listed"},
		{"escaped name", `\` + sum + "  a\\nb\n", "escaped"},
		{"upper case", strings.ToUpper(sum) + "  a\n", "not <sha256>"},
		{"one space", sum + " a\n", "not <sha256>"},
		{"empty", "", "lists no file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSums([]byte(tc.data))
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("ParseSums: %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSymlinkInsideReleaseRefused(t *testing.T) {
	e := active(t)
	dir := e.release(rel3, "2026-11-01")
	outside := filepath.Join(t.TempDir(), "secret")
	e.write(outside, "outside\n", 0o644)
	if err := os.Symlink(outside, filepath.Join(dir, "etc/link")); err != nil {
		t.Fatal(err)
	}
	e.sums(dir) // lists nothing for the link; the walk must still refuse it
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "verify")
	if !strings.Contains(e.record().Detail, "etc/link is a symbolic link") {
		t.Errorf("error: %s: %s", e.record().Error, e.record().Detail)
	}
}

func TestSymlinkReleaseFolderRefused(t *testing.T) {
	e := active(t)
	real := filepath.Join(t.TempDir(), rel3)
	other := newEnv(t)
	src := other.release(rel3, "2026-11-01")
	if err := os.Rename(src, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, e.relDir(rel3)); err != nil {
		t.Fatal(err)
	}
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "verify")
	if !strings.Contains(e.record().Detail, "is a symbolic link, not a release folder") {
		t.Errorf("error: %s: %s", e.record().Error, e.record().Detail)
	}
}

func TestMissingReleaseFolder(t *testing.T) {
	e := active(t)
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "verify")
}

func TestManifestMismatch(t *testing.T) {
	for _, tc := range []struct{ name, manifest, want string }{
		{"other id", "id: 0004-ddddddd\ncounter: 3\ncommit: ccccccc0\nbuilt_on: dev\ngo_version: go\npackage_snapshot: s\n", "not the folder's name"},
		{"other counter", "id: 0003-ccccccc\ncounter: 4\ncommit: ccccccc0\nbuilt_on: dev\ngo_version: go\npackage_snapshot: s\n", "counter 4"},
		{"other commit", "id: 0003-ccccccc\ncounter: 3\ncommit: ddddddd0\nbuilt_on: dev\ngo_version: go\npackage_snapshot: s\n", "does not start with"},
		{"key missing", "id: 0003-ccccccc\ncounter: 3\ncommit: ccccccc0\nbuilt_on: dev\ngo_version: go\n", "package_snapshot missing"},
		{"key twice", "id: 0003-ccccccc\nid: 0003-ccccccc\n", "given twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			dir := e.release(rel3, "s")
			e.write(filepath.Join(dir, ManifestFile), tc.manifest, 0o644)
			e.sums(dir)
			if code := e.switchTo(rel3); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			e.unchanged("releases/"+rel2, "verify")
			if !strings.Contains(e.record().Detail, tc.want) {
				t.Errorf("error: %s: %s", e.record().Error, e.record().Detail)
			}
		})
	}
}

func TestLowerCounterRefused(t *testing.T) {
	for _, id := range []string{rel1, rel2} {
		t.Run(id, func(t *testing.T) {
			e := active(t)
			if code := e.switchTo(id); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			e.unchanged("releases/"+rel2, "counter")
			if !strings.Contains(e.record().Detail, "not above the highest ever activated, 2") {
				t.Errorf("error: %s: %s", e.record().Error, e.record().Detail)
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
		})
	}
}

func TestBadHighestCounterRefused(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	e.write(filepath.Join(e.root, StateDir, HighestFile), "seven\n", 0o644)
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "counter")
}

func TestRollbackToPrevious(t *testing.T) {
	e := active(t)
	if code := e.rollBack("previous"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if got := e.link("current"); got != "releases/"+rel1 {
		t.Errorf("current -> %q", got)
	}
	if got := e.link("previous"); got != "releases/"+rel2 {
		t.Errorf("previous -> %q", got)
	}
	if got := e.highest(); got != "2" {
		t.Errorf("highest counter %q after a rollback, want 2", got)
	}
	rec := e.record()
	if rec.Result != ResultOK || !rec.Rollback || rec.ID != rel1 || rec.PreviousID != rel2 || rec.CurrentID != rel1 {
		t.Errorf("record: %+v", rec)
	}
	if e.runner.called("ah-noaicheck") != 1 || e.runner.called("health-probe") != 1 {
		t.Errorf("a rollback runs the same checks: %v", e.runner.calls)
	}
	// The id previous points at is accepted too, and the counter still does
	// not move.
	if code := e.rollBack(rel2); code != ExitOK {
		t.Fatalf("rollback by id: exit %d", code)
	}
	if got := e.link("current"); got != "releases/"+rel2 {
		t.Errorf("current -> %q", got)
	}
	if got := e.highest(); got != "2" {
		t.Errorf("highest counter %q, want 2", got)
	}
}

func TestRollbackRefusedForOtherID(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	for _, id := range []string{rel3, rel2, "0009-fffffff"} {
		if code := e.rollBack(id); code != ExitFailed {
			t.Fatalf("rollback to %s: exit %d", id, code)
		}
		e.unchanged("releases/"+rel2, "counter")
		if !strings.Contains(e.record().Detail, "accepts only previous") {
			t.Errorf("error: %s: %s", e.record().Error, e.record().Detail)
		}
	}
	if code := e.rollBack("latest"); code != ExitUsage {
		t.Errorf("rollback to a word other than previous: exit %d, want %d", code, ExitUsage)
	}
	if len(e.runner.calls) != 0 {
		t.Errorf("commands run: %v", e.runner.calls)
	}
}

func TestRollbackWithoutPrevious(t *testing.T) {
	e := newEnv(t)
	e.release(rel1, "s")
	if code := e.switchTo(rel1); code != ExitOK {
		t.Fatal(code)
	}
	if code := e.rollBack("previous"); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel1, "counter")
}

func TestNoAICheckFailingBlocksSwitch(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	e.runner.fail["ah-noaicheck"] = -1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "noaicheck")
	if got := e.link("previous"); got != "releases/"+rel1 {
		t.Errorf("previous -> %q", got)
	}
	if e.runner.called("validate-units") != 0 || e.runner.called("reload-units") != 0 {
		t.Errorf("ran past the check: %v", e.runner.calls)
	}
	if got := e.highest(); got != "2" {
		t.Errorf("highest %q", got)
	}
}

func TestNoAICheckOverride(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	check := filepath.Join(t.TempDir(), "other-check")
	e.write(check, "#!/bin/sh\nexit 0\n", 0o755)
	if code := e.switchTo(rel3, func(c *Config) { c.NoAICheck = check }); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if want := check + " --release " + e.relDir(rel3); e.runner.calls[0] != want {
		t.Errorf("first command %q, want %q", e.runner.calls[0], want)
	}
}

func TestNoAICheckMissing(t *testing.T) {
	e := active(t)
	dir := e.release(rel3, "2026-11-01")
	os.Remove(filepath.Join(dir, NoAICheckProgram))
	e.sums(dir)
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "noaicheck")
}

func TestValidatorFailing(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	e.runner.fail["validate-rules"] = -1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "validate")
	rec := e.record()
	if rec.Check != CheckPassed || rec.Error != CodeValidatorFailed || rec.Detail != "validator 2 of 2, validate-rules: failed" {
		t.Errorf("record: %+v", rec)
	}
	if e.runner.called("reload-units") != 0 {
		t.Errorf("reloaded after a failed validator: %v", e.runner.calls)
	}
}

func TestMissingValidatorsListAllowed(t *testing.T) {
	e := active(t)
	dir := e.release(rel3, "2026-11-01")
	os.Remove(filepath.Join(dir, ValidatorsList))
	os.Remove(filepath.Join(dir, ReloadList))
	e.sums(dir)
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
}

func TestMissingHealthCmdRefused(t *testing.T) {
	e := active(t)
	dir := e.release(rel3, "2026-11-01")
	os.Remove(filepath.Join(dir, HealthCmd))
	e.sums(dir)
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "verify")
}

func TestPackageStep(t *testing.T) {
	e := newEnv(t)
	dir := e.release(rel1, "2026-11-01")
	e.write(filepath.Join(dir, PackageStepProgram), "#!/bin/sh\n", 0o755)
	e.sums(dir)
	if code := e.switchTo(rel1); code != ExitOK {
		t.Fatal(code)
	}
	want := filepath.Join(dir, PackageStepProgram) + " --release " + dir + " --from  --to 2026-11-01"
	if e.runner.called("ah-pkgstep") != 1 || !strings.Contains(strings.Join(e.runner.calls, "\n"), want) {
		t.Errorf("package step not run as %q: %v", want, e.runner.calls)
	}
	if got := e.record().PackageSnapshot; got != "2026-11-01" {
		t.Errorf("recorded snapshot %q", got)
	}

	// The same snapshot: not run.
	e.runner.reset()
	dir = e.release(rel2, "2026-11-01")
	e.write(filepath.Join(dir, PackageStepProgram), "#!/bin/sh\n", 0o755)
	e.sums(dir)
	if code := e.switchTo(rel2); code != ExitOK {
		t.Fatal(code)
	}
	if e.runner.called("ah-pkgstep") != 0 {
		t.Errorf("package step run for an unchanged snapshot")
	}

	// A new snapshot whose step fails: no switch.
	e.runner.reset()
	dir = e.release(rel3, "2026-11-15")
	e.write(filepath.Join(dir, PackageStepProgram), "#!/bin/sh\n", 0o755)
	e.sums(dir)
	e.runner.fail["ah-pkgstep"] = -1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatal(code)
	}
	e.unchanged("releases/"+rel2, "packages")
	if got := e.record().PackageSnapshot; got != "2026-11-01" {
		t.Errorf("recorded snapshot %q after a failed step", got)
	}
}

func TestPackageStepAbsentAllowed(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-15")
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatal(code)
	}
}

func TestHealthFailingRollsBack(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	// The new release never gets healthy (30 attempts over 60 s); the old
	// one is healthy again at once.
	e.runner.fail["health-probe"] = 30
	start := e.clock.now()
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if got := e.link("current"); got != "releases/"+rel2 {
		t.Errorf("current -> %q after a failed health check", got)
	}
	if got := e.link("previous"); got != "releases/"+rel1 {
		t.Errorf("previous -> %q, want it put back", got)
	}
	if got := e.highest(); got != "2" {
		t.Errorf("highest %q", got)
	}
	rec := e.record()
	if rec.Result != ResultRolledBack || rec.FailedStep != "health" || rec.CurrentID != rel2 || rec.ID != rel3 || rec.RestoredHealth != "ok" {
		t.Errorf("record: %+v", rec)
	}
	if n := e.runner.called("reload-units"); n != 2 {
		t.Errorf("reload list run %d times, want 2 (after the switch and after going back)", n)
	}
	if n := e.runner.called("health-probe"); n != 31 {
		t.Errorf("health probe run %d times, want 31", n)
	}
	if d := e.clock.now().Sub(start); d < HealthWindow-HealthInterval || d > HealthWindow {
		t.Errorf("health checked for %s", d)
	}
}

func TestHealthRecovers(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	e.runner.fail["health-probe"] = 3
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if got := e.link("current"); got != "releases/"+rel3 {
		t.Errorf("current -> %q", got)
	}
}

func TestReloadFailingRollsBack(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	e.runner.fail["reload-units"] = 1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if got := e.link("current"); got != "releases/"+rel2 {
		t.Errorf("current -> %q", got)
	}
	rec := e.record()
	if rec.Result != ResultRolledBack || rec.FailedStep != "reload" {
		t.Errorf("record: %+v", rec)
	}
}

func TestFirstSwitchUnhealthyHasNoWayBack(t *testing.T) {
	e := newEnv(t)
	e.release(rel1, "s")
	e.runner.fail["health-probe"] = -1
	if code := e.switchTo(rel1); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if got := e.link("current"); got != "releases/"+rel1 {
		t.Errorf("current -> %q", got)
	}
	rec := e.record()
	if rec.Result != ResultFailed || rec.FailedStep != "health" || rec.Error != CodeHealthFailed || !strings.Contains(rec.Detail, "no earlier release") {
		t.Errorf("record: %+v", rec)
	}
	if got := e.highest(); got != "" {
		t.Errorf("highest written after an unhealthy switch: %q", got)
	}
}

func TestCrashBetweenPreviousAndRename(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	before := e.record()
	s := e.switcher()
	s.afterPrevious = func() error { return errors.New("crash") }
	s.Switch(context.Background(), rel3)
	// current is still the old release, whole and verifiable.
	if got := e.link("current"); got != "releases/"+rel2 {
		t.Fatalf("current -> %q", got)
	}
	if got := e.link("previous"); got != "releases/"+rel2 {
		t.Errorf("previous -> %q, want the old current", got)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(e.root, BaseDir, "current"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseID(rel2)
	rels, err := os.OpenRoot(filepath.Join(e.root, BaseDir, "releases"))
	if err != nil {
		t.Fatal(err)
	}
	defer rels.Close()
	if r, err := openRelease(id, rels, rel2, target, nil); err != nil {
		t.Errorf("current does not verify after the crash: %v", err)
	} else {
		r.Close()
	}
	if rec := e.record(); rec != before {
		t.Errorf("deploy.json changed by a crash: %+v", rec)
	}
	if got := e.highest(); got != "2" {
		t.Errorf("highest %q", got)
	}
	// The lock went with the crashed process; running the switch again
	// completes it.
	e.out.Reset()
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("switch after the crash: exit %d", code)
	}
	if e.link("current") != "releases/"+rel3 || e.link("previous") != "releases/"+rel2 {
		t.Errorf("after recovery: current -> %q, previous -> %q", e.link("current"), e.link("previous"))
	}
}

func TestConcurrentSwitchRefused(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	state, err := os.OpenRoot(filepath.Join(e.root, StateDir))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	held, err := takeLock(state, true, false)
	if err != nil {
		t.Fatal(err)
	}
	before := digest(t, e.root)
	if code := e.switchTo(rel3); code != ExitLocked {
		t.Fatalf("exit %d, want %d", code, ExitLocked)
	}
	if code := e.switchTo(rel3, func(c *Config) { c.Dry = true }); code != ExitLocked {
		t.Fatalf("dry run: exit %d, want %d", code, ExitLocked)
	}
	if after := digest(t, e.root); after != before {
		t.Errorf("a refused switch changed the tree")
	}
	if len(e.runner.calls) != 0 {
		t.Errorf("commands run: %v", e.runner.calls)
	}
	held.release()
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("after the lock was released: exit %d", code)
	}
}

func TestDryChangesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(e *env)
		id    string
		code  int
	}{
		{"first switch", func(e *env) { e.release(rel1, "s") }, rel1, ExitOK},
		{"next switch", func(e *env) {
			e.release(rel1, "s")
			if e.switchTo(rel1) != ExitOK {
				e.t.Fatal("setup")
			}
			dir := e.release(rel2, "t")
			e.write(filepath.Join(dir, PackageStepProgram), "#!/bin/sh\n", 0o755)
			e.sums(dir)
		}, rel2, ExitOK},
		{"failing", func(e *env) {
			dir := e.release(rel1, "s")
			e.write(filepath.Join(dir, "extra"), "x", 0o644)
		}, rel1, ExitFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e)
			e.runner.reset()
			before := digest(t, e.root)
			s := e.switcher(func(c *Config) { c.Dry = true })
			code := s.Switch(context.Background(), tc.id)
			out := e.out.String()
			t.Log(out)
			if code != tc.code {
				t.Fatalf("exit %d, want %d", code, tc.code)
			}
			if after := digest(t, e.root); after != before {
				t.Errorf("--dry changed the tree")
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("--dry ran commands: %v", e.runner.calls)
			}
			if tc.code == ExitOK {
				for _, want := range []string{"step 1/8", "step 2/8", "step 3/8 noaicheck: would run", "step 4/8 validate: would run", "step 5/8", "step 6/8 switch: would point current", "step 7/8 health: would run", "step 8/8 record: would record"} {
					if !strings.Contains(out, want) {
						t.Errorf("--dry output lacks %q", want)
					}
				}
			}
		})
	}
}

func TestSignedMode(t *testing.T) {
	signed := func(v Verifier) func(*Config) {
		return func(c *Config) {
			c.Mode = ModeSigned
			c.AllowedSigners = "/etc/authority-host/allowed_signers"
			c.Verifier = v
		}
	}
	t.Run("good", func(t *testing.T) {
		e := newEnv(t)
		dir := e.release(rel1, "s")
		e.write(filepath.Join(dir, SignatureFile), "sig", 0o644)
		v := &fakeVerifier{}
		if code := e.switchTo(rel1, signed(v)); code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		sums, _ := os.ReadFile(filepath.Join(dir, SumsFile))
		if v.calls != 1 || string(v.data) != string(sums) {
			t.Errorf("verifier called %d times with %q", v.calls, v.data)
		}
		if e.record().Mode != ModeSigned {
			t.Errorf("mode not recorded")
		}
	})
	t.Run("bad", func(t *testing.T) {
		e := newEnv(t)
		dir := e.release(rel1, "s")
		e.write(filepath.Join(dir, SignatureFile), "sig", 0o644)
		if code := e.switchTo(rel1, signed(&fakeVerifier{err: errors.New("bad signature")})); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		e.unchanged("", "verify")
		if len(e.runner.calls) != 0 {
			t.Errorf("commands run: %v", e.runner.calls)
		}
	})
	t.Run("missing", func(t *testing.T) {
		e := newEnv(t)
		e.release(rel1, "s")
		v := &fakeVerifier{}
		if code := e.switchTo(rel1, signed(v)); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		e.unchanged("", "verify")
		if v.calls != 0 {
			t.Errorf("verifier called without a signature")
		}
	})
	t.Run("manual ignores it", func(t *testing.T) {
		e := newEnv(t)
		dir := e.release(rel1, "s")
		e.write(filepath.Join(dir, SignatureFile), "sig", 0o644)
		v := &fakeVerifier{err: errors.New("never asked")}
		if code := e.switchTo(rel1, func(c *Config) { c.Verifier = v }); code != ExitOK || v.calls != 0 {
			t.Fatalf("exit %d, verifier calls %d", code, v.calls)
		}
	})
}

func TestConfigRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(*Config)
		id   string
	}{
		{"bad id", nil, "7-ab12cd3"},
		{"id with path", nil, "../0001-aaaaaaa"},
		{"upper-case commit", nil, "0001-AAAAAAA"},
		{"bad mode", func(c *Config) { c.Mode = "trust" }, rel1},
		{"signed without signers", func(c *Config) { c.Mode = ModeSigned }, rel1},
		{"signers in manual mode", func(c *Config) { c.AllowedSigners = "/x" }, rel1},
		{"relative root", func(c *Config) { c.Root = "relative" }, rel1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.release(rel1, "s")
			before := digest(t, e.root)
			mods := []func(*Config){}
			if tc.mod != nil {
				mods = append(mods, tc.mod)
			}
			if code := e.switchTo(tc.id, mods...); code != ExitUsage {
				t.Fatalf("exit %d, want %d", code, ExitUsage)
			}
			if digest(t, e.root) != before {
				t.Errorf("a refused call changed the tree")
			}
		})
	}
}

func TestCurrentNotALink(t *testing.T) {
	e := newEnv(t)
	e.release(rel1, "s")
	if err := os.Mkdir(filepath.Join(e.root, BaseDir, "current"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := e.switchTo(rel1); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if rec := e.record(); rec.FailedStep != "state" {
		t.Errorf("record: %+v", rec)
	}
}

func TestStatus(t *testing.T) {
	e := newEnv(t)
	if got := e.status(); got != "current: none\nprevious: none\nhighest counter: 0\nlast switch: none recorded\n" {
		t.Errorf("status of an empty root:\n%s", got)
	}
	e.release(rel1, "s")
	e.release(rel2, "s")
	e.switchTo(rel1)
	e.switchTo(rel2)
	got := e.status()
	for _, want := range []string{"current: " + rel2, "previous: " + rel1, "highest counter: 2", "last switch: switch to " + rel2 + ": ok", "no-AI check: passed"} {
		if !strings.Contains(got, want) {
			t.Errorf("status lacks %q:\n%s", want, got)
		}
	}
}

func TestCommandLists(t *testing.T) {
	for _, tc := range []struct {
		name, data, release, want string
		argv                      []string
	}{
		{"plain", "nft -c -f {release}/etc/nftables/rules.nft\n", "/r", "", []string{"nft", "-c", "-f", "/r/etc/nftables/rules.nft"}},
		{"absolute", "  /usr/bin/caddy validate  \n# comment\n\n", "/r", "", []string{"/usr/bin/caddy", "validate"}},
		{"release program", "{release}/bin/check\n", "/r", "", []string{"/r/bin/check"}},
		{"relative program", "bin/check\n", "/r", "relative path", nil},
		{"pipe", "a | b\n", "/r", "no shell", nil},
		{"variable", "a $HOME\n", "/r", "no shell", nil},
		{"quote", "a 'b c'\n", "/r", "no shell", nil},
		{"redirect", "a >/x\n", "/r", "no shell", nil},
		{"glob", "a /etc/*.conf\n", "/r", "no shell", nil},
		{"placeholder outside validators", "a {release}/x\n", "", "only for validators", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmds, err := ParseCommandList("list", []byte(tc.data), tc.release)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("err %v, want %q", err, tc.want)
				}
				return
			}
			if err != nil || len(cmds) != 1 || strings.Join(cmds[0], "\x00") != strings.Join(tc.argv, "\x00") {
				t.Errorf("got %q, %v", cmds, err)
			}
		})
	}
	if _, err := ParseHealthCmd([]byte("a\nb\n")); err == nil {
		t.Errorf("two health commands accepted")
	}
	if _, err := ParseHealthCmd([]byte("# nothing\n")); err == nil {
		t.Errorf("no health command accepted")
	}
}

func TestParseID(t *testing.T) {
	id, err := ParseID("0007-ab12cd3")
	if err != nil || id.Counter != 7 || id.Commit != "ab12cd3" {
		t.Errorf("ParseID: %+v, %v", id, err)
	}
	for _, bad := range []string{"", "7-ab12cd3", "0007", "0007-ab12", "0007-ab12cd3/..", "0007-ab12cd3 ", "x007-ab12cd3", "0007_ab12cd3"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) accepted", bad)
		}
	}
}

// TestChildOutputNeverShown: what a failing program writes reaches neither
// deploy.json nor the steps, since a diagnostic can hold a secret.
func TestChildOutputNeverShown(t *testing.T) {
	for _, prog := range []string{"ah-noaicheck", "validate-rules", "reload-units", "health-probe"} {
		t.Run(prog, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-01")
			e.runner.fail[prog] = -1
			if code := e.switchTo(rel3); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			if strings.Contains(e.last, "said no") || strings.Contains(e.deployJSON(), "said no") {
				t.Errorf("the program's output was passed on:\n%s\n%s", e.last, e.deployJSON())
			}
		})
	}
}

// TestPlantedNameEscaped: a file name holding a newline cannot forge a line
// of the output, and deploy.json holds it escaped.
func TestPlantedNameEscaped(t *testing.T) {
	e := active(t)
	dir := e.release(rel3, "2026-11-01")
	e.write(filepath.Join(dir, "etc", "x\nresult: ok"), "x", 0o644)
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	for _, line := range strings.Split(e.last, "\n") {
		if strings.HasPrefix(line, "result: ok") {
			t.Errorf("a planted name forged a line:\n%s", e.last)
		}
	}
	rec := e.record()
	if rec.Error != CodeFileNotListed || rec.Detail != `not listed in SHA256SUMS: "etc/x\nresult: ok"` {
		t.Errorf("record: %s: %s", rec.Error, rec.Detail)
	}
}

// TestBadCounterNotEchoed: the text of a broken highest-counter is not
// repeated anywhere.
func TestBadCounterNotEchoed(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	e.write(filepath.Join(e.root, StateDir, HighestFile), "token-of-someone\n", 0o644)
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(e.last, "token-of") || strings.Contains(e.deployJSON(), "token-of") {
		t.Errorf("the counter text was echoed:\n%s\n%s", e.last, e.deployJSON())
	}
	if rec := e.record(); rec.Error != CodeCounterInvalid {
		t.Errorf("record: %+v", rec)
	}
}

func TestQuoteField(t *testing.T) {
	long := strings.Repeat("a", maxField+10)
	for _, tc := range []struct{ in, want string }{
		{"bin/app", "bin/app"},
		{"", `""`},
		{"a b", `"a b"`},
		{"x\ny", `"x\ny"`},
		{"\x1b[2J", `"\x1b[2J"`},
		{"\xc3\xa9", `"\` + "u00e9\""},
		{long, strings.Repeat("a", maxField) + "..."},
	} {
		if got := q(tc.in); got != tc.want {
			t.Errorf("q(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestStateFolderMustBeTrusted: the state folder and every folder above it
// up to the root must be real folders, not writable by group or others, and
// owned as the owner check says; otherwise nothing is done or recorded.
func TestStateFolderMustBeTrusted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(e *env)
		mod   func(*Config)
	}{
		{"state folder writable by others", func(e *env) {
			os.Chmod(filepath.Join(e.root, StateDir), 0o777)
		}, nil},
		{"parent writable by its group", func(e *env) {
			os.Chmod(filepath.Join(e.root, "var/lib"), 0o775)
		}, nil},
		{"root writable by others", func(e *env) {
			os.Chmod(e.root, 0o757)
		}, nil},
		{"state folder a link", func(e *env) {
			real := filepath.Join(e.root, "elsewhere")
			os.Rename(filepath.Join(e.root, StateDir), real)
			os.Symlink(real, filepath.Join(e.root, StateDir))
		}, nil},
		{"owner refused", func(e *env) {}, func(c *Config) {
			c.Owner = func(fs.FileInfo) error { return errors.New("owned by uid 1000, not root") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-01")
			tc.setup(e)
			before := digest(t, e.root)
			mods := []func(*Config){}
			if tc.mod != nil {
				mods = append(mods, tc.mod)
			}
			if code := e.switchTo(rel3, mods...); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(e.last, "untrusted") {
				t.Errorf("output does not say why:\n%s", e.last)
			}
			if digest(t, e.root) != before {
				t.Errorf("an untrusted state folder was written to")
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
			if code := e.switcher(mods...).Status(); code != ExitFailed {
				t.Errorf("status: exit %d", code)
			}
			e.out.Reset()
		})
	}
}

// TestReleasesFolderMustBeTrusted: opt/authority-host and releases/ are
// checked as the state folder is.
func TestReleasesFolderMustBeTrusted(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	os.Chmod(filepath.Join(e.root, BaseDir, "releases"), 0o777)
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if rec := e.record(); rec.FailedStep != "state" || rec.Error != CodeUntrusted {
		t.Errorf("record: %+v", rec)
	}
	if got := e.link("current"); got != "releases/"+rel2 {
		t.Errorf("current -> %q", got)
	}
}

// TestStateFilesNotFollowed: a state file that is a link, or too large, is
// not read through.
func TestStateFilesNotFollowed(t *testing.T) {
	t.Run("highest-counter a link", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		other := filepath.Join(t.TempDir(), "counter")
		e.write(other, "1\n", 0o644)
		p := filepath.Join(e.root, StateDir, HighestFile)
		os.Remove(p)
		os.Symlink(other, p)
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if rec := e.record(); rec.FailedStep != "counter" || rec.Error != CodeCounterInvalid {
			t.Errorf("record: %+v", rec)
		}
	})
	t.Run("lock a link", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		other := filepath.Join(t.TempDir(), "lock")
		e.write(other, "", 0o644)
		p := filepath.Join(e.root, StateDir, LockFile)
		os.Remove(p)
		os.Symlink(other, p)
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if got := e.link("current"); got != "releases/"+rel2 {
			t.Errorf("current -> %q", got)
		}
	})
	t.Run("deploy.json too large", func(t *testing.T) {
		e := active(t)
		e.write(filepath.Join(e.root, StateDir, DeployFile), strings.Repeat(" ", maxStateFile+1), 0o644)
		if code := e.switcher().Status(); code != ExitFailed {
			t.Errorf("status: exit %d", code)
		}
		e.out.Reset()
	})
}
