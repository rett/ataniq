package codex

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The child has an explicit short lifetime and leaves a marker on exit. It
// inherits only this test process's output pipes and a minimal environment.
func TestCommandHelper(t *testing.T) {
	switch os.Getenv("ATANIQ_TEST_HELPER") {
	case "parent":
		cmd := exec.Command(os.Args[0], "-test.run=^TestCommandHelper$")
		cmd.Env = []string{"ATANIQ_TEST_HELPER=child", "ATANIQ_TEST_MARKER=" + os.Getenv("ATANIQ_TEST_MARKER")}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "child":
		time.Sleep(2 * time.Second)
		if err := os.WriteFile(os.Getenv("ATANIQ_TEST_MARKER"), []byte("done"), 0600); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
}

func TestQualificationCommandBoundsInheritedOutput(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-exited")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := qualificationCommand(ctx, os.Args[0], "-test.run=^TestCommandHelper$")
	cmd.Env = []string{"ATANIQ_TEST_HELPER=parent", "ATANIQ_TEST_MARKER=" + marker}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	_, err := cmd.Output()
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Errorf("output: %v, want ErrWaitDelay; stderr=%s", err, stderr.String())
	}
	if elapsed := time.Since(start); elapsed >= 1500*time.Millisecond {
		t.Errorf("output drain took %v", elapsed)
	}
	// Wait for the bounded child to exit before removing its temporary directory.
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("helper did not exit before watchdog")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
