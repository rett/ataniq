//go:build !linux

package intake

import (
	"errors"

	"ataniq/internal/exchange/safefs"
)

var errStoreUnsupported = errors.New("the records store works on Linux only")

// folder is not available on this system.
type folder struct{}

func openFolder(string, *safefs.Root) (folder, error) { return folder{}, errStoreUnsupported }
func (folder) close() error                           { return errStoreUnsupported }
func (folder) create(string, []byte) error            { return errStoreUnsupported }
func (folder) names(int) ([]string, error)            { return nil, errStoreUnsupported }
