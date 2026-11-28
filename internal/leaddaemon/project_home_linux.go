package leaddaemon

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/instanceid"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// A scratch project is named scratch-<key>. The seat limit applies to the
// key alone, including for a recorded project absent from today's config.
func hiddenScratchKey(project string) string {
	key, scratch := strings.CutPrefix(project, leadrole.ScratchPrefix)
	if scratch && !ownerwire.ValidChatLead(ownerwire.ChatKindDaemon+":"+key) {
		return key
	}
	return ""
}

var homeName = regexp.MustCompile("[a-z0-9-]+")

// Alongside the configured projects, include scratch names found in registry
// text, which can come from a journal written under an older configuration.
func homeKeyRedactor(configured []leadrole.Role, lines []string) *strings.Replacer {
	var keys []string
	for _, role := range configured {
		if key := hiddenScratchKey(role.Project()); key != "" {
			keys = append(keys, key)
		}
	}
	for _, line := range lines {
		for _, project := range homeName.FindAllString(line, -1) {
			if key := hiddenScratchKey(project); key != "" {
				keys = append(keys, key)
			}
		}
	}
	slices.Sort(keys)
	var pairs []string
	for _, key := range slices.Backward(slices.Compact(keys)) {
		pairs = append(pairs, key, "[key too long to show]")
	}
	return strings.NewReplacer(pairs...)
}

func visibleHomeWarnings(warnings []string, configured []leadrole.Role) []string {
	redactor := homeKeyRedactor(configured, warnings)
	var visible []string
	hidden := 0
	// MissingHomeWarnings emits "project <name> ..." once per project.
	for _, warning := range warnings {
		project, _, _ := strings.Cut(strings.TrimPrefix(warning, "project "), " ")
		if hiddenScratchKey(project) != "" {
			hidden++
			continue
		}
		// Another project's name or a coordinator role can contain the key.
		visible = append(visible, redactor.Replace(warning))
	}
	if hidden > 0 {
		visible = append(visible, fmt.Sprintf("missing project homes block define and start for %d leads with keys too long to show", hidden))
	}
	return visible
}

// ReadHomeWarningsAt gives read-only preflight the same report as startup.
func ReadHomeWarningsAt(root *os.File, binding instanceid.Binding, configured []leadrole.Role) ([]string, error) {
	warnings, err := leadregistry.ReadMissingHomesAt(root, binding, configured)
	if err != nil {
		// A rejected journal record can quote a role in the replay diagnostic.
		var corrupt *leadregistry.CorruptError
		if errors.As(err, &corrupt) {
			reason := homeKeyRedactor(configured, []string{corrupt.Reason}).Replace(corrupt.Reason)
			return nil, &leadregistry.CorruptError{Line: corrupt.Line, Reason: reason}
		}
		return nil, err
	}
	return visibleHomeWarnings(warnings, configured), nil
}
