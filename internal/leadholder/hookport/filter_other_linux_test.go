//go:build linux && !amd64

package hookport

import (
	"os"
	"testing"
)

func TestHP8UnsupportedABI(t *testing.T) {
	if f, err := InstallFilter(); err == nil || f != (Filtered{}) {
		t.Fatal("unsupported ABI allowed the port to serve")
	}
	if os.Getenv("ATANIQ_REQUIRE_SECCOMP") == "1" {
		t.Fatal("HP-8 requires the native Linux/amd64 filter")
	}
}
