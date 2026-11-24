package handoff

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"ataniq/internal/genesis/plan"
)

// TemplateSoftware is the id of the one plan template a hand-off is made for.
const TemplateSoftware = "software"

// Methodology is the Qargi methodology the command names: the only one Qargi
// ships.
const Methodology = "gitflow"

// TagLen is how many digits of a thread's id its tag is.
const TagLen = 6

// DigestShortLen is how many digits of a plan's digest the header shows.
const DigestShortLen = 12

// TextNotAvailable is the fixed text of the refusal of a plan whose kind has
// no hand-off yet.
const TextNotAvailable = "A hand-off for this kind of project is not available yet."

// CodeNotAvailable is the code of that refusal.
const CodeNotAvailable = "not-available"

// Refusal is the error of a plan a hand-off is not made for. Its text is
// fixed and holds nothing of the plan.
type Refusal struct {
	Code string
	text string
}

func (r *Refusal) Error() string { return r.text }

// NameGrammar is Qargi's grammar of a project name.
var NameGrammar = regexp.MustCompile(`^[a-z][a-z0-9]{0,23}$`)

// commandGrammar is the whole grammar of the command line: the exact pieces
// Emit writes, with the name held to [NameGrammar], and nothing else. Go's $
// without the m flag matches at the end of the text only, so no line may
// follow.
var commandGrammar = regexp.MustCompile(`^qargi project new [a-z][a-z0-9]{0,23} --methodology gitflow$`)

// prefixes is the closed map from a plan's title (a catalog label of the
// software kind's sub-kind) to the prefix of the project's name.
var prefixes = map[string]string{
	"A full web app":  "webapp",
	"A one-page tool": "webtool",
}

// fallbackPrefix is the prefix of a software plan whose title the map does not
// hold.
const fallbackPrefix = "software"

// hostWords are the fixed words of where a plan keeps the files, by the
// plan's closed host id.
var hostWords = map[string]string{
	"own_server":    "your own server",
	"this_computer": "this computer",
	"file_host":     "a file or code host",
}

// serviceWords are the fixed words of the external services a plan may name,
// by the plan's closed service name.
var serviceWords = map[string]string{
	"email delivery":    "email delivery",
	"file or code host": "file or code host",
	"generator service": "generator service",
	"hosting service":   "hosting service",
}

// bandWords are the fixed words of a monthly budget band, by the plan's
// closed band id.
var bandWords = map[string]string{
	"none":   "none (nothing extra)",
	"low":    "low (a little)",
	"medium": "medium (some)",
}

// The fixed header lines.
const (
	lineNothingRun = "# Nothing has been run. Run the command yourself, in the folder where you keep projects."
	lineYourWords  = "# It prints a plan and a token; creating the project needs your own words. The plan's tasks are not imported: Qargi's backlog is where work is tracked."
)

// Handoff is what [Emit] makes.
type Handoff struct {
	// Name is the project's name in the command, made by the host.
	Name string
	// Header are the comment lines, each starting "# ".
	Header []string
	// Command is the one command line, which [ValidateCommand] accepts.
	Command string
}

// Text returns the hand-off as it is printed: the header lines, then the
// command, each ended by a line feed.
func (h Handoff) Text() string {
	var b strings.Builder
	for _, l := range h.Header {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString(h.Command)
	b.WriteByte('\n')
	return b.String()
}

// Emit makes the hand-off of the plan stored in the thread whose id is thread
// (16 lower-case hexadecimal digits). It refuses a plan of any template but
// software with a [*Refusal]; any other error (a thread id that is not one, a
// plan that does not pass [plan.Check], a closed value the maps here do not
// hold) is the host's own fault, in fixed text. The command is checked by
// [ValidateCommand] before it is returned.
func Emit(p plan.Plan, thread string) (Handoff, error) {
	if !isLowerHex(thread, 16) {
		return Handoff{}, errors.New("handoff: the thread id is not 16 lower-case hexadecimal digits")
	}
	if err := plan.Check(p); err != nil {
		return Handoff{}, errors.New("handoff: the plan does not pass its own checks")
	}
	if p.Template.ID != TemplateSoftware {
		return Handoff{}, &Refusal{Code: CodeNotAvailable, text: TextNotAvailable}
	}
	digest, err := plan.Digest(p)
	if err != nil {
		return Handoff{}, errors.New("handoff: the plan has no digest")
	}
	tag := thread[:TagLen]
	prefix, ok := prefixes[p.Title]
	if !ok {
		prefix = fallbackPrefix
	}
	name := prefix + tag
	if !NameGrammar.MatchString(name) {
		return Handoff{}, errors.New("handoff: the project name is not of Qargi's grammar")
	}
	header := []string{
		"# Hand-off for plan " + digest[:DigestShortLen] + " (revision " + strconv.Itoa(p.Revision) + ") of thread " + tag + ".",
		lineNothingRun,
		lineYourWords,
	}
	host, ok := hostWords[p.Hosts]
	if !ok {
		return Handoff{}, errors.New("handoff: the plan names a host there are no words for")
	}
	header = append(header, "# The plan keeps the files on "+host+"; the command sets up no host and chooses no deployment.")
	if len(p.ExternalServices) > 0 {
		names := make([]string, 0, len(p.ExternalServices))
		for _, s := range p.ExternalServices {
			w, ok := serviceWords[s]
			if !ok {
				return Handoff{}, errors.New("handoff: the plan names a service there are no words for")
			}
			names = append(names, w)
		}
		header = append(header, "# The plan names external services the command does not set up: "+strings.Join(names, ", ")+".")
	}
	band, ok := bandWords[p.BudgetBand]
	if !ok {
		return Handoff{}, errors.New("handoff: the plan names a budget band there are no words for")
	}
	header = append(header, "# The plan's monthly budget band is "+band+"; it is advisory, and the command does not carry it.")
	cmd := "qargi project new " + name + " --methodology " + Methodology
	if err := ValidateCommand(cmd); err != nil {
		return Handoff{}, err
	}
	return Handoff{Name: name, Header: header, Command: cmd}, nil
}

// ValidateCommand checks a command line against the closed grammar of the
// hand-off: exactly "qargi project new NAME --methodology gitflow", NAME of
// Qargi's grammar, one line and nothing more. Its error is fixed text and
// holds nothing of the line.
func ValidateCommand(text string) error {
	if !commandGrammar.MatchString(text) {
		return errors.New("handoff: the command is not of the hand-off's grammar")
	}
	return nil
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
