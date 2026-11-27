//go:build linux && !amd64

package hookport

import "errors"

func InstallFilter() (Filtered, error) {
	return Filtered{}, errors.New("hookport: unsupported filter ABI")
}
