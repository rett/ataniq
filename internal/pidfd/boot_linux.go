package pidfd

import (
	"errors"
	"strings"
)

// BootID reads the kernel's boot identity, a lower-case UUID that changes on
// every boot. It says only which boot the caller is running in. It does not
// prove a time or order across boots, and a host that fakes the file is
// outside what it can detect.
func BootID() (string, error) {
	b, err := readKernel("/proc/sys/kernel/random/boot_id", 64)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if !ValidBoot(s) {
		return "", errors.New("missing boot identity")
	}
	return s, nil
}

// ValidBoot reports whether s has the shape of a boot identity: 36 characters,
// lower-case hexadecimal digits with dashes at positions 8, 13, 18 and 23. It
// checks the shape only and not that the value is the current boot.
func ValidBoot(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
