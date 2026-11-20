//go:build linux && ataniq_canary

package leadcanary_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/session"
)

func packageFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files: %v", err)
	}
	return files
}

// TestEveryFileCarriesTheBuildTag holds that the package exists only in canary
// mode: the first line of every file, tests included, is the constraint.
func TestEveryFileCarriesTheBuildTag(t *testing.T) {
	const want = "//go:build linux && ataniq_canary"
	for _, name := range packageFiles(t) {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		first, _, _ := strings.Cut(string(data), "\n")
		if first != want {
			t.Errorf("%s starts with %q", name, first)
		}
	}
}

// TestScreenBoundsAreTheHoldersOwn holds that the package's copies of the
// holder's screen bounds are the session's.
func TestScreenBoundsAreTheHoldersOwn(t *testing.T) {
	if leadcanary.MinCols != session.MinCols || leadcanary.MaxCols != session.MaxCols ||
		leadcanary.MinRows != session.MinRows || leadcanary.MaxRows != session.MaxRows {
		t.Fatalf("screen bounds %d-%d x %d-%d, session %d-%d x %d-%d",
			leadcanary.MinCols, leadcanary.MaxCols, leadcanary.MinRows, leadcanary.MaxRows,
			session.MinCols, session.MaxCols, session.MinRows, session.MaxRows)
	}
}
