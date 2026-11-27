package enrollment

import (
	"errors"
	"fmt"
	"strings"

	"ataniq/internal/assignment"
)

// FormatArchiveHead formats the assignment position in the owner's archive.
func FormatArchiveHead(h assignment.Checkpoint) string {
	return fmt.Sprintf("sequence=%d head=%s", h.Sequence, h.Digest)
}

// WriteHeadArchive exclusively publishes a complete, LF-terminated archive.
func WriteHeadArchive(path string, h assignment.Checkpoint) error {
	if len(h.Digest) != 64 || strings.Trim(h.Digest, "0123456789abcdef") != "" {
		return errors.New("invalid head digest")
	}
	return publishDocument(path, []byte(FormatArchiveHead(h)+"\n"))
}
