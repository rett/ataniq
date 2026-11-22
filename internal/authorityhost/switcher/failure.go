package switcher

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Failure codes: the stable words deploy.json, the status and the steps carry
// for a failure. Neither a program's output nor a raw name read from a
// release, the incoming folder or a state file is ever part of them; what
// names a thing is an identifier quoted by q.
const (
	CodeStateInvalid       = "state-invalid"      // current, previous or a state file is not what the switch writes
	CodeUntrusted          = "untrusted"          // a folder or file the switch trusts has the wrong owner, mode or type
	CodePolicyInvalid      = "policy-invalid"     // the switch policy does not parse or names no usable signers file
	CodeReleaseMissing     = "release-missing"    // no release folder of that id
	CodeReleaseNotFolder   = "release-not-folder" // the release name is a link or not a folder
	CodeBadEntry           = "bad-entry"          // a link, device, fifo, socket or hard-linked file in a release
	CodeSumsMissing        = "sums-missing"       // no SHA256SUMS
	CodeSumsMalformed      = "sums-malformed"     // SHA256SUMS does not parse
	CodeSumsMismatch       = "sums-mismatch"      // a file's SHA-256 is not the listed one
	CodeFileNotListed      = "file-not-listed"    // a file of the release is not in SHA256SUMS
	CodeFileMissing        = "file-missing"       // a listed file is not in the release
	CodeManifestInvalid    = "manifest-invalid"   // MANIFEST does not parse
	CodeManifestMismatch   = "manifest-mismatch"  // MANIFEST does not describe the folder
	CodeSignatureMissing   = "signature-missing"  // signed mode and no SHA256SUMS.sig
	CodeSignatureBad       = "signature-bad"      // the signature does not verify
	CodeListMissing        = "list-missing"       // a required command list is missing
	CodeListInvalid        = "list-invalid"       // a command list does not parse
	CodeReadFailed         = "read-failed"        // a file could not be read
	CodeFileChanged        = "file-changed"       // a file or folder changed while it was read
	CodeReleaseExists      = "release-exists"     // --from names a release that is already imported
	CodeTooManyEntries     = "too-many-entries"   // a release holds more entries than the limit
	CodeTooLarge           = "too-large"          // a release holds more bytes than the limit
	CodeFileTooLarge       = "file-too-large"     // a file of a release is larger than the limit
	CodeTimeBudget         = "time-budget"        // the import and the hashing took longer than the limit
	CodeCounterNotAbove    = "counter-not-above"  // the counter is not above the highest
	CodeCounterInvalid     = "counter-invalid"    // highest-counter does not hold a counter
	CodeCounterMissing     = "counter-missing"    // highest-counter is missing on a host that has a current release
	CodeInterrupted        = "interrupted"        // a switch was interrupted and has been put back
	CodeRollbackRefused    = "rollback-refused"   // --rollback-to names a release other than previous
	CodeNoPrevious         = "no-previous"        // nothing to roll back to
	CodeNoAICheckMissing   = "noaicheck-missing"  // the release has no no-AI check
	CodeNoAICheckFailed    = "noaicheck-failed"   // the no-AI check did not exit 0
	CodeNFTListingFailed   = "nft-listing-failed" // nft list ruleset failed, or its listing could not be passed on
	CodeValidatorFailed    = "validator-failed"   // a validator did not exit 0
	CodePackageFailed      = "package-step-failed"
	CodePackageStepMissing = "package-step-missing"     // the snapshot changes and the release has no package step
	CodePackageInterrupted = "package-step-interrupted" // a package step was interrupted; the next switch runs it again
	CodeLinkFailed         = "link-failed"              // a link could not be written
	CodeReloadFailed       = "reload-failed"            // a reload command did not exit 0
	CodeHealthFailed       = "health-failed"            // the health check did not pass in time
	CodeRecordFailed       = "record-failed"            // a state file could not be written
	CodeIOError            = "io-error"                 // any other error of the system
	CodeNoEarlierRelease   = "no-earlier-release"
)

// failure is an error with a stable code and a detail made only of fixed
// words and identifiers quoted by q.
type failure struct {
	code   string
	detail string
}

func (f *failure) Error() string {
	if f.detail == "" {
		return f.code
	}
	return f.code + ": " + f.detail
}

// fail returns a failure. Every argument that comes from outside the program
// (a name, a path, a word of a list) must be passed through q.
func failf(code, format string, args ...any) error {
	return &failure{code: code, detail: fmt.Sprintf(format, args...)}
}

// codeOf returns the code and the detail of err: those of a failure, or for
// any other error CodeIOError and the error's text quoted.
func codeOf(err error) (string, string) {
	var f *failure
	if errors.As(err, &f) {
		return f.code, f.detail
	}
	return CodeIOError, q(err.Error())
}

// maxField bounds an identifier in output and in deploy.json.
const maxField = 160

// q returns s fit for one field of a line: bounded to maxField bytes (with
// "..." appended when cut) and, when it holds anything but printable ASCII
// other than a space, a quote or a backslash, quoted with Go's escapes, so a
// planted name can neither forge a line nor carry a control sequence.
func q(s string) string {
	cut := false
	if len(s) > maxField {
		s, cut = s[:maxField], true
	}
	quote := s == ""
	for i := 0; i < len(s) && !quote; i++ {
		if b := s[i]; b <= ' ' || b >= 0x7f || b == '"' || b == '\\' {
			quote = true
		}
	}
	if quote {
		s = strconv.QuoteToASCII(s)
	}
	if cut {
		s += "..."
	}
	return s
}

// maxArgvText bounds a command as qargv shows it.
const maxArgvText = 1024

// QuoteField is q for the commands of the switch: s fit for one field of a
// line, bounded and quoted with Go's escapes when it holds anything but
// printable ASCII other than a space, a quote or a backslash.
func QuoteField(s string) string { return q(s) }

// maxLine bounds a line EscapeLine returns.
const maxLine = 512

// EscapeLine returns s fit for a line of a diagnostic: every byte that is
// not printable ASCII, and the backslash, is written as \xNN, and s is cut
// at maxLine bytes ("..." follows when it is cut). Spaces and quotes stay,
// so a sentence reads as one.
func EscapeLine(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if b.Len() >= maxLine {
			b.WriteString("...")
			break
		}
		if c := s[i]; c < ' ' || c >= 0x7f || c == '\\' {
			fmt.Fprintf(&b, "\\x%02x", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// qargv quotes each word of a command for output, at most maxArgvText bytes
// of it ("..." follows when it is cut).
func qargv(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		w := q(a)
		if i > 0 {
			w = " " + w
		}
		if b.Len()+len(w) > maxArgvText {
			b.WriteString(" ...")
			break
		}
		b.WriteString(w)
	}
	return b.String()
}

// maxDetail bounds the detail of a failure as it is recorded.
const maxDetail = 4096

// boundDetail cuts a detail to maxDetail bytes, with "..." when cut.
func boundDetail(s string) string {
	if len(s) <= maxDetail {
		return s
	}
	return s[:maxDetail] + "..."
}
