package release

import (
	"errors"
	"syscall"

	"ataniq/internal/authorityhost/noaicheck"
)

// The codes of a refusal or a finding, each with its fixed reason. A line is
// "FAIL: <code> <reason>" with, after a colon, the one name the finding is
// about when that name passed the name rules (see shown).
const (
	// Arguments of build.
	CodeCounter    = "counter"
	CodeCommit     = "commit"
	CodeGoVersion  = "go-version"
	CodeBuiltOn    = "built-on"
	CodeSnapshot   = "package-snapshot"
	CodeComponents = "components"
	CodeStaticStep = "static-package-step"
	CodeBinName    = "bin-name"
	CodeBinTwice   = "bin-twice"
	CodeBinMissing = "bin-missing"
	CodeOut        = "out"
	CodeSrc        = "src"

	// Arguments and refusals of baseline.
	CodeBaselineArgs  = "baseline-args"
	CodeStamp         = "stamp"
	CodeInputNotFile  = "input-not-regular"
	CodeInputTooLarge = "input-too-large"
	CodeInputInvalid  = "input-invalid"
	CodeInputEmpty    = "input-empty"
	CodeOutExists     = "out-exists"
	CodeNoDefaultDeny = "no-default-deny"

	// Refusals of build.
	CodeExists   = "exists"
	CodeTooLarge = "too-large"
	CodeIO       = "io-error"

	// Findings about a folder, of build and check.
	CodeNotRegular = "not-regular"
	CodeBadName    = "bad-name"

	// Findings of check about the folder.
	CodeFolder       = "folder-invalid"
	CodeEntryLimit   = "entry-limit"
	CodeHardLink     = "hard-link"
	CodeUnsafeMode   = "unsafe-mode"
	CodeTopLevel     = "top-level"
	CodeNotExec      = "not-executable"
	CodeSumsMissing  = "sums-missing"
	CodeSumsInvalid  = "sums-invalid"
	CodeSumsUnlisted = "sums-unlisted"
	CodeSumsAbsent   = "sums-absent"
	CodeSumsMismatch = "sums-mismatch"
	CodeSumsForm     = "sums-not-canonical"
	CodeManifest     = "manifest-invalid"
	CodeManifestID   = "manifest-mismatch"

	// Findings of check about the lists and the units.
	CodeListMissing    = "list-missing"
	CodeListInvalid    = "list-invalid"
	CodeListEmpty      = "list-empty"
	CodeProgram        = "program-missing"
	CodeProgramExec    = "program-not-executable"
	CodeUnitUnreadable = "unit-unreadable"
	CodeUnitPlacehold  = "unit-placeholder"
	CodeStaticService  = "static-has-service"
	CodeNoService      = "no-decision-service"
	CodeInhibitCond    = "inhibit-start-condition"
	CodeWitness        = "witness-service"
	CodeDecisionExec   = "decision-exec"
	CodeValidatorNone  = "validator-missing"
	CodeValidatorTwice = "validator-twice"
	CodeValidatorOther = "validator-unknown-file"
	CodeReloadNone     = "reload-missing"
	CodeStopNone       = "stop-missing"

	// Findings of check about the no-AI check.
	CodeNoAIStart   = "noaicheck-not-started"
	CodeNoAITimeout = "noaicheck-timeout"
	CodeNoAIExit    = "noaicheck-exit"
	CodeNoAIHit     = "noaicheck-hit"
	CodeNoAIChanged = "noaicheck-changed"
)

var reasons = map[string]string{
	CodeCounter:    "the counter is not a number of 1 or more",
	CodeCommit:     "the commit is not 7 to 64 lowercase hex digits",
	CodeGoVersion:  "the Go version is not 1 to 128 bytes of letters, digits and ._:+-",
	CodeBuiltOn:    "the build host class is not 1 to 128 bytes of letters, digits and ._:+-",
	CodeSnapshot:   "the package snapshot is not 1 to 128 bytes of letters, digits and ._:-",
	CodeComponents: "components is not static",
	CodeStaticStep: "a static release is the generated no-op and ships no ah-pkgstep of its own: build a release that is not static to ship one",
	CodeBinName:    "a binary name is not letters, digits and ._@+- or is not a regular file",
	CodeBinTwice:   "two binaries have one name",
	CodeBinMissing: "a binary every release needs is not given",
	CodeOut:        "the output folder is not usable",
	CodeSrc:        "the source folder lacks etc/ or share/",

	CodeBaselineArgs:  "baseline needs --src, --units, --ports, --packages and --nft",
	CodeStamp:         "the stamp is not 1 to 128 bytes of letters, digits, spaces and ._:+-",
	CodeInputNotFile:  "an input file is not a regular file that can be read",
	CodeInputTooLarge: "an input file is over 2 MiB",
	CodeInputInvalid:  "an input file holds a line that is not valid for its list",
	CodeInputEmpty:    "an input file holds no entry",
	CodeOutExists:     "the output folder exists already",
	CodeNoDefaultDeny: "the nft listing does not deny every hook by default, and --require-default-deny is given",

	CodeExists:   "that release folder exists already",
	CodeTooLarge: "the release is over a size or entry limit",
	CodeIO:       "a file could not be read or written",

	CodeNotRegular: "not a folder or a regular file",
	CodeBadName:    "a name is not letters, digits and ._@+-",

	CodeFolder:       "not a real folder named <counter>-<short commit>",
	CodeEntryLimit:   "too many entries or nested too deep",
	CodeHardLink:     "a file has more than one link",
	CodeUnsafeMode:   "writable by group or others, or set-user-id, set-group-id or sticky",
	CodeTopLevel:     "not a part of a release",
	CodeNotExec:      "has no execute bit",
	CodeSumsMissing:  "SHA256SUMS is missing",
	CodeSumsInvalid:  "SHA256SUMS is not in the format of sha256sum",
	CodeSumsUnlisted: "a file is not listed in SHA256SUMS",
	CodeSumsAbsent:   "a file listed in SHA256SUMS is missing",
	CodeSumsMismatch: "a file does not match SHA256SUMS",
	CodeSumsForm:     "SHA256SUMS is not sorted by path, one line per file",
	CodeManifest:     "MANIFEST is missing or invalid",
	CodeManifestID:   "MANIFEST does not describe the folder",

	CodeListMissing:      "a command list is missing",
	CodeListInvalid:      "a command list does not parse as the switch reads it",
	CodeListEmpty:        "a command list holds no command",
	CodeProgram:          "a program named in a list is not in the release",
	CodeProgramExec:      "a program named in a list has no execute bit",
	CodeUnitUnreadable:   "a unit file could not be read",
	CodeUnitPlacehold:    "a unit file still holds {release}, which ah-release build replaces with the release's folder",
	CodeStaticService:    "a static release carries a service unit",
	CodeNoService:        "no decision service, but the release is not static",
	CodeInhibitCond:      "a unit gates its start on the decision inhibit",
	CodeWitness:          "witness isolation, component or health check is invalid",
	noaicheck.UnitSyntax: "unit contains a continuation, invalid UTF-8, or a forbidden control, whitespace or format character",
	CodeDecisionExec:     "a decision service does not run a binary of the release",
	CodeValidatorNone:    "no validator for a shipped file",
	CodeValidatorTwice:   "more than one validator for a file",
	CodeValidatorOther:   "a validator names a file the release does not ship there",
	CodeReloadNone:       "reload.list does not restart or reload what the release ships",
	CodeStopNone:         "stop.list does not stop a decision service",

	CodeNoAIStart:   "the shipped no-AI check did not start",
	CodeNoAITimeout: "the shipped no-AI check did not finish in 60 seconds",
	CodeNoAIExit:    "the shipped no-AI check exited with a failure",
	CodeNoAIHit:     "the shipped no-AI check reported a hit",
	CodeNoAIChanged: "the shipped no-AI check changed after it was verified, and was not run",
}

// Finding is one thing a build refused or a check found wrong.
type Finding struct {
	Code string
	Name string // the file or unit it is about, shown only when it passes the name rules
	// Detail is the operating system's own text of an error ("permission
	// denied"), for an io-error: fixed words, never a path.
	Detail string
}

// String is the line, "FAIL: <code> <reason>[: <name>]".
func (f Finding) String() string {
	s := "FAIL: " + f.Code + " " + reasons[f.Code]
	if f.Name != "" {
		s += ": " + shown(f.Name)
	}
	if f.Detail != "" {
		s += " (" + f.Detail + ")"
	}
	return s
}

// Refusal is why Build made nothing.
type Refusal struct {
	Finding
	// Usage is true for an argument that is wrong, false for a refusal about
	// what was found (the source, the output).
	Usage bool
}

func (r *Refusal) Error() string { return r.Finding.String() }

func refuse(usage bool, code, name string) *Refusal {
	return &Refusal{Finding: Finding{Code: code, Name: name}, Usage: usage}
}

// ioRefusal is the refusal for an error of the file system or of reading:
// the fixed reason and, when the error is the system's, its fixed text.
func ioRefusal(err error) *Refusal {
	r := refuse(false, CodeIO, "")
	var errno syscall.Errno
	switch {
	case errors.Is(err, errTooMany):
		r.Code = CodeTooLarge
	case errors.As(err, &errno):
		r.Detail = errno.Error()
	}
	return r
}
