//go:build linux && amd64

package qualification

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func typedDescriptorChild(t *testing.T, dir, role, scenario string) {
	t.Helper()
	if strings.HasPrefix(scenario, "terminal-") {
		typedTerminalChild(t, dir, strings.TrimPrefix(scenario, "terminal-"))
		return
	}
	a := childAttempt(t, dir, role)
	for _, fd := range injectionFDs() {
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
	}
	a.ID = strings.Repeat("a", 32)
	a.Config.Campaign = dir
	a.Config.DescriptorProfile = FD1TypedProfile
	a.FixtureNonce = strings.Repeat("e", 32)
	if role == "candidate" {
		a.Config.DirectControl = strings.Repeat("b", 32)
	}
	path := filepath.Join(dir, a.ID)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "fixtures"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := prepareFD1Static(a, path); err != nil {
		t.Fatal(err)
	}
	a.Request = hostRequest(a)
	a.TargetCommand = []string{os.Args[0], "-test.run=^TestTypedDescriptorTarget$", "--", "typed-target", dir, role}
	a.PlanSHA256 = planDigest(a)
	if scenario == "collision" {
		if err := syscall.Dup3(0, FD1PipeFD, 0); err != nil {
			t.Fatal(err)
		}
	}
	r, err := installFD1(a)
	if scenario == "collision" {
		if err == nil {
			t.Fatal("collision accepted")
		}
		if _, err := os.Stat(filepath.Join(path, "fixtures", FD1BindingName)); !os.IsNotExist(err) {
			t.Fatal("published on collision", err)
		}
		os.Exit(42)
	}
	if err != nil {
		// Only actual kernel socket metadata denial is a coverage gap. Other
		// setup errors must fail instead of being promoted to a skip.
		var failure *hostFailure
		if errors.As(err, &failure) && (failure.Stage == "typed-socket-setup" || failure.Stage == "typed-socket-metadata") && (errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOPROTOOPT)) {
			fmt.Fprintln(os.Stderr, "typed socket admission unavailable; integration unqualified")
			os.Exit(77)
		}
		t.Fatal(err)
	}
	a.FD1Runtime = r
	for _, mutate := range []string{scenario} {
		switch mutate {
		case "flags":
			if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, FD1PipeFD, syscall.F_SETFL, 0); e != 0 {
				t.Fatal(e)
			}
		case "identity":
			if err := syscall.Dup3(0, FD1PipeFD, 0); err != nil {
				t.Fatal(err)
			}
		case "regular-hardlink":
			if err := os.Link(a.Fixture.Path, filepath.Join(dir, "hardlink")); err != nil {
				t.Fatal(err)
			}
		case "bpf-offset":
			syscall.Seek(BPFFD, 1, 0)
		case "artifact-missing":
			if err := os.Remove(r.Artifact.Path); err != nil {
				t.Fatal(err)
			}
		case "artifact-replaced":
			b, e := os.ReadFile(r.Artifact.Path)
			if e != nil {
				t.Fatal(e)
			}
			if err := os.Rename(r.Artifact.Path, r.Artifact.Path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := exclusiveFile(r.Artifact.Path, b, 0400); err != nil {
				t.Fatal(err)
			}
		}
	}
	// These calls must not consume either channel. The exec target proves it
	// receives both exact initial frames followed by EOF after two preflights.
	p, err := prepareDescriptorExec(a)
	if err != nil {
		os.Exit(42)
	}
	if _, err = prepareDescriptorExec(a); err != nil {
		t.Fatal(err)
	}
	if scenario == "post-flags" {
		if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, FD1PipeFD, syscall.F_SETFL, 0); e != 0 {
			t.Fatal(e)
		}
	}
	if scenario == "post-links" {
		if err := os.Link(a.Fixture.Path, filepath.Join(dir, "hardlink")); err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "post-directory" {
		if err := os.Mkdir(filepath.Join(a.FD1Static.Directory.Path, "extra"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Unknown aliases must close at exec; they are test-only and are never
	// created by the production injector. The preflight never repairs flags.
	if err := syscall.Dup3(FD1PipeFD, 1001, 0); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(a)
	if err := os.WriteFile(filepath.Join(dir, "typed-attempt"), data, 0400); err != nil {
		t.Fatal(err)
	}
	p.exec()
}

// This independently exercises the raw terminal loop with actual directory
// link counts and a prefilled pipe even when socket admission is unavailable.
// It does not substitute for the skipped complete injector/profile tests.
func typedTerminalChild(t *testing.T, dir, scenario string) {
	a := childAttempt(t, dir, "direct")
	p, err := prepareDescriptorExec(a)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "directory")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Dup3(fd, FD1DirectoryFD, 0); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Close(fd); err != nil {
		t.Fatal(err)
	}
	pipe := make([]int, 2)
	if err := syscall.Pipe2(pipe, syscall.O_NONBLOCK|syscall.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	frame := fd1Frame(CaseFakePipeFD, strings.Repeat("e", 32))
	if n, err := syscall.Write(pipe[1], frame); err != nil || n != len(frame) {
		t.Fatal("prefill", n, err)
	}
	if err := syscall.Dup3(pipe[0], FD1PipeFD, 0); err != nil {
		t.Fatal(err)
	}
	for _, fd := range pipe {
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
	}
	for _, fd := range []int{FD1DirectoryFD, FD1PipeFD} {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			t.Fatal(err)
		}
		flags, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
		if e != 0 {
			t.Fatal(e)
		}
		if int(flags) != fd1ExpectedFlags(fd) {
			t.Fatal("typed admission flags", flags)
		}
		if fd == FD1DirectoryFD && (st.Mode&syscall.S_IFMT != syscall.S_IFDIR || st.Nlink == 1) {
			t.Fatal("directory link-count fixture unavailable")
		}
		p.fds = append(p.fds, terminalFD{fd, st, int(flags), true})
	}
	if err := syscall.Dup3(FD1PipeFD, 1001, 0); err != nil {
		t.Fatal(err)
	}
	switch scenario {
	case "links":
		if err := os.Link(a.Fixture.Path, filepath.Join(dir, "hardlink")); err != nil {
			t.Fatal(err)
		}
	case "directory":
		if err := os.Mkdir(filepath.Join(path, "extra"), 0700); err != nil {
			t.Fatal(err)
		}
	case "flags":
		if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, FD1PipeFD, syscall.F_SETFL, 0); e != 0 {
			t.Fatal(e)
		}
	}
	p.command = []string{os.Args[0], "-test.run=^TestTypedTerminalTarget$", "--", "typed-terminal-target", dir}
	p.exec()
}

func TestTypedTerminalTarget(t *testing.T) {
	args := descriptorChildArgs("typed-terminal-target")
	if args == nil {
		return
	}
	if len(os.Environ()) != 0 {
		t.Fatal("environment survived")
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(FD1DirectoryFD, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFDIR || st.Nlink == 1 {
		t.Fatal("directory not retained", err)
	}
	for _, fd := range []int{BPFFD, 64, 768, 1000, 1001} {
		if _, err := fdFlags(fd); err != syscall.EBADF {
			t.Fatal("alias survived", fd, err)
		}
	}
	frame := fd1Frame(CaseFakePipeFD, strings.Repeat("e", 32))
	buf := make([]byte, len(frame)+1)
	if n, err := syscall.Read(FD1PipeFD, buf); err != nil || n != len(frame) || string(buf[:n]) != string(frame) {
		t.Fatal("frame not intact", n, err)
	}
	if n, err := syscall.Read(FD1PipeFD, buf); err != nil || n != 0 {
		t.Fatal("producer not closed", n, err)
	}
	if _, err := syscall.Write(1, []byte("typed-terminal-ok\n")); err != nil {
		t.Fatal(err)
	}
}

func TestFD1DirectoryPipeTerminalWithoutSocketAdmission(t *testing.T) {
	for _, scenario := range []string{"ok", "links", "directory", "flags"} {
		t.Run(scenario, func(t *testing.T) {
			code, out, diag := runDescriptorChild(t, "direct", "typed-terminal-"+scenario)
			if code == 77 || code == 125 && strings.Contains(diag, "stage=01") {
				t.Skipf("terminal fixture unavailable: %s", diag)
			}
			want := 0
			if scenario != "ok" {
				want = 125
			}
			if code != want || want == 0 && !strings.Contains(out, "typed-terminal-ok") || want != 0 && out != "" {
				t.Fatalf("code=%d out=%q diag=%q", code, out, diag)
			}
			if (scenario == "links" || scenario == "directory") && !strings.Contains(diag, "stage=02") {
				t.Fatal(diag)
			}
			if scenario == "flags" && !strings.Contains(diag, "stage=03") {
				t.Fatal(diag)
			}
		})
	}
}

func TestTypedDescriptorTarget(t *testing.T) {
	args := descriptorChildArgs("typed-target")
	if args == nil {
		return
	}
	if len(os.Environ()) != 0 {
		t.Fatal("environment survived")
	}
	dir, role := args[0], args[1]
	data, err := os.ReadFile(filepath.Join(dir, "typed-attempt"))
	if err != nil {
		t.Fatal(err)
	}
	var a Attempt
	if err := json.Unmarshal(data, &a); err != nil {
		t.Fatal(err)
	}
	for _, fd := range append(injectionSlots(&a), 1001) {
		flags, err := fdFlags(fd)
		if permittedForAttempt(&a, fd) {
			if err != nil || flags != 0 {
				t.Fatalf("missing allowed slot %d %v", fd, err)
			}
		} else if err != syscall.EBADF {
			t.Fatalf("denied slot survived %d %v", fd, err)
		}
	}
	// The test target takes bwrap's BPF consumption position without launching
	// a backend. This checks terminal inheritance, not actual BPF consumption.
	if role == "weakened-synthetic" {
		if err := syscall.Close(BPFFD); err != nil {
			t.Fatal(err)
		}
	}
	fds, err := (kernelMetadata{}).fds()
	if err != nil {
		t.Fatal(err)
	}
	o := descriptorInventory(fds)
	o.Status = "observed"
	o.RunID = a.Config.RunID
	o.RequestID = a.Config.RequestID
	o.Case = CaseInventory
	if err := validateDescriptorObservations(&a, o); err != nil {
		t.Fatal(err)
	}
	for _, caseName := range []string{"fake-dir-fd", "fake-pipe-fd", "fake-socket-fd"} {
		a.Config.Case = caseName
		a.Request = hostRequest(&a)
		o = Observation{Version: 1, RunID: a.Config.RunID, RequestID: a.Config.RequestID, Case: a.Request.Case, Status: "observed", Facts: []Fact{}}
		fakeTypedFD(&o, a.Request, Config{FixtureRoot: filepath.Join(dir, a.ID, "fixtures")})
		if err := validateDescriptorObservations(&a, o); err != nil {
			t.Fatalf("%s recovery/closure: %v %+v", caseName, err, o)
		}
	}
	if _, err := syscall.Write(1, []byte("typed-target-ok\n")); err != nil {
		t.Fatal(err)
	}
}

func TestFD1OwnedInjectorAndTerminal(t *testing.T) {
	for _, role := range []string{"direct", "candidate", "weakened-synthetic"} {
		for _, scenario := range []string{"ok", "collision", "flags", "identity", "regular-hardlink", "bpf-offset", "artifact-missing", "artifact-replaced", "post-flags", "post-links", "post-directory"} {
			t.Run(role+"/"+scenario, func(t *testing.T) {
				code, out, diag := runDescriptorChild(t, role, "typed-"+scenario)
				if code == 77 {
					t.Skipf("socket admission gap: %s", diag)
				}
				if code == 125 && strings.Contains(diag, "stage=01") {
					t.Skipf("close_range gap: %s", diag)
				}
				want := 42
				if scenario == "ok" {
					want = 0
				}
				if strings.HasPrefix(scenario, "post-") {
					want = 125
				}
				if code != want || want == 0 && !strings.Contains(out, "typed-target-ok") || want != 0 && out != "" {
					t.Fatalf("code=%d want=%d out=%q diag=%q", code, want, out, diag)
				}
				if scenario == "post-flags" && !strings.Contains(diag, "stage=03") {
					t.Fatal(diag)
				}
				if (scenario == "post-links" || scenario == "post-directory") && !strings.Contains(diag, "stage=02") {
					t.Fatal(diag)
				}
			})
		}
	}
}
