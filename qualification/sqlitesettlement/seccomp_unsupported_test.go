//go:build !linux || !amd64

package sqlitesettlement

import "errors"

func installSyncDenial() (string, error) {
	return "architecture", errors.New("sync denial is only qualified for Linux/amd64 native ABI")
}

func installSelectedSyncDenial(int) (string, error) { return installSyncDenial() }
