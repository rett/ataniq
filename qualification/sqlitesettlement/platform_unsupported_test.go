//go:build !linux || !amd64

package sqlitesettlement

import (
	"os"
	"runtime"
	"testing"
)

func TestUnsupportedQualificationPlatform(t *testing.T) {
	message := "INCOMPLETE native qualification: supported ABI is Linux/amd64, observed " + runtime.GOOS + "/" + runtime.GOARCH
	if os.Getenv("ATANIQ_SQLITE_SETTLEMENT_GATE") == "1" {
		t.Fatal(message)
	}
	t.Log(message)
}
