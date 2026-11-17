package leadregistry

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadrole"
)

const helperEnv = "LEADREGISTRY_KILL_HELPER_DIR"

// TestKillHelper is not a test: when the environment names a folder, the
// re-executed test binary becomes a writer that appends records in a tight
// loop and prints "ack <revision>" after each call that returned, until the
// parent kills it.
func TestKillHelper(t *testing.T) {
	dir := os.Getenv(helperEnv)
	if dir == "" {
		t.Skip("helper process only")
	}
	r, err := Open(dir, Options{})
	if err != nil {
		fmt.Println("open-error", err)
		os.Exit(3)
	}
	role, err := leadrole.Lead("killed")
	if err != nil {
		os.Exit(3)
	}
	cur, err := r.Get(role)
	if err != nil {
		if _, err := r.Define(role, ManagerAtaniq, goodProfile()); err != nil {
			fmt.Println("define-error", err)
			os.Exit(3)
		}
	} else {
		// Resume after an earlier writer was killed: resolve what it left.
		if cur.Intent != nil && cur.Intent.Outcome == OutcomePending {
			if _, err := r.RecordOutcome(role, cur.Intent.ID, cur.Epoch, OutcomeAbandoned, UnitEvidence{}); err != nil {
				fmt.Println("resume-error", err)
				os.Exit(3)
			}
		}
		if cur.Shown.State != StateStopped && cur.Shown.State != StateEnded {
			c := StateChange{Epoch: cur.Epoch, State: StateStopped, Evidence: EvidenceObservation, Reason: Reason{Code: "resume"}, Proof: "test resume"}
			if _, err := r.SetState(role, c); err != nil {
				fmt.Println("resume-error", err)
				os.Exit(3)
			}
		}
	}
	out := bufio.NewWriter(os.Stdout)
	ack := func() {
		rev, _ := r.Revision()
		fmt.Fprintln(out, "ack", rev)
		out.Flush()
	}
	ack()
	proof := strings.Repeat("proof ", 40)
	for {
		in, err := r.BeginLaunch(role)
		if err != nil {
			fmt.Println("begin-error", err)
			os.Exit(3)
		}
		ack()
		steps := []func() error{
			func() error {
				_, err := r.RecordOutcome(role, in.ID, in.Epoch, OutcomeLaunched, UnitEvidence{InvocationID: "inv", ControlGroupPath: strings.Repeat("/cg", 100)})
				return err
			},
			func() error {
				_, err := r.SetObserved(role, in.Epoch, Observed{Model: Seen(strings.Repeat("m", 100))})
				return err
			},
			func() error {
				_, err := r.SetState(role, StateChange{Epoch: in.Epoch, State: StateStopped, Evidence: EvidenceObservation, Reason: Reason{Code: "unit-gone"}, Proof: proof})
				return err
			},
		}
		for _, s := range steps {
			if err := s(); err != nil {
				fmt.Println("step-error", err)
				os.Exit(3)
			}
			ack()
		}
	}
}

// TestKilledWriterLeavesAReadableJournal kills a writer process with SIGKILL
// in the middle of its append loop, several times against the same folder,
// and checks that the journal reopens, that every acknowledged revision is
// still there, and that the lock dies with the process.
func TestKilledWriterLeavesAReadableJournal(t *testing.T) {
	if os.Getenv(helperEnv) != "" {
		t.Skip("inside the helper")
	}
	dir := regDir(t)
	role := lead(t, "killed")
	var lastAcked uint64
	for round := 0; round < 8; round++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestKillHelper$", "-test.v")
		cmd.Env = append(os.Environ(), helperEnv+"="+dir)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		killAfter := 3 + round*17
		sc := bufio.NewScanner(stdout)
		acks := 0
		var rev uint64
		lockChecked := false
		done := make(chan struct{})
		go func() {
			time.Sleep(20 * time.Second)
			select {
			case <-done:
			default:
				cmd.Process.Kill()
			}
		}()
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) == 2 && f[0] == "ack" {
				n, err := strconv.ParseUint(f[1], 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				rev = n
				acks++
				if !lockChecked {
					lockChecked = true
					if _, err := Open(dir, Options{}); !errors.Is(err, ErrLocked) {
						t.Errorf("round %d: Open while the writer lives: %v", round, err)
					}
				}
				if acks >= killAfter {
					break
				}
				continue
			}
			if len(f) > 0 && (strings.HasSuffix(f[0], "-error") || f[0] == "open-error") {
				t.Fatalf("round %d: helper failed: %s", round, sc.Text())
			}
		}
		if acks < killAfter {
			t.Fatalf("round %d: helper stopped after %d acks", round, acks)
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		cmd.Wait()
		close(done)
		if rev < lastAcked {
			t.Fatalf("round %d: revision went backwards in the helper", round)
		}

		r, err := Open(dir, Options{})
		if err != nil {
			t.Fatalf("round %d: reopen after kill: %v", round, err)
		}
		got, _ := r.Revision()
		if got < rev {
			t.Fatalf("round %d: acknowledged revision %d lost, journal ends at %d", round, rev, got)
		}
		lastAcked = got
		e, err := r.Get(role)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if e.Intent != nil && e.Intent.Epoch != e.Epoch {
			t.Fatalf("round %d: intent epoch %d, epoch %d", round, e.Intent.Epoch, e.Epoch)
		}
		if rec := r.Recovery(); rec.Lines > 0 {
			t.Logf("round %d: cut %d unfinished bytes", round, rec.Bytes)
		}
		r.Close()
	}
}
