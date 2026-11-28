package agentsession

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
)

type HookReport struct {
	Kind    HookKind
	Session string
	Age     time.Duration
}
type classifiedReport struct {
	HookReport
	Class Class
}

// HookView is an immutable snapshot. The binding and foreign count outlive
// removal of individual reports, and a report's class is fixed on arrival.
type HookView struct {
	bound    string
	ageLimit time.Duration
	form     IDForm
	foreign  int
	reports  []classifiedReport
}

func NewHookView(limit time.Duration, form IDForm) (HookView, error) {
	h := HookView{ageLimit: limit, form: form}
	if err := ValidateHooks(h); err != nil {
		return HookView{}, err
	}
	return h, nil
}
func sessionReason(s string, form IDForm) InputReason {
	if len(s) == 0 {
		return "empty-value"
	}
	if len(s) > 128 {
		return "too-long"
	}
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e {
			return "not-printable"
		}
	}
	if form == "uuid" {
		if len(s) != 36 {
			return "inconsistent"
		}
		for i := range len(s) {
			switch i {
			case 8, 13, 18, 23:
				if s[i] != '-' {
					return "inconsistent"
				}
			default:
				// The canonical lowercase spelling makes byte equality UUID equality.
				if !strings.ContainsRune("0123456789abcdef", rune(s[i])) {
					return "inconsistent"
				}
			}
		}
	}
	return ""
}
func reportIssue(r HookReport, form IDForm) (string, InputReason) {
	if !r.Kind.Valid() {
		if r.Kind == "" {
			return ".Kind", "empty-value"
		}
		return ".Kind", "unknown-value"
	}
	if why := sessionReason(r.Session, form); why != "" {
		return ".Session", why
	}
	if r.Age < 0 {
		return ".Age", "negative"
	}
	return "", ""
}
func ValidateHooks(h HookView) error {
	if h.bound != "" {
		if why := sessionReason(h.bound, h.form); why != "" {
			return invalid("HookView.Bound", why)
		}
	}
	if h.ageLimit < 0 {
		return invalid("HookView.AgeLimit", "negative")
	}
	if err := enum("HookView.Form", h.form); err != nil {
		return err
	}
	if h.foreign < 0 {
		return invalid("HookView.Foreign", "negative")
	}
	foreign := 0
	for _, r := range h.reports {
		if r.Class == "foreign" {
			foreign++
		}
	}
	if foreign > h.foreign {
		return invalid("HookView.Foreign", "inconsistent")
	}
	bindingSeen := false
	for i, r := range h.reports {
		if err := h.validateReport(index("HookView.Reports", i), r, &bindingSeen); err != nil {
			return err
		}
	}
	return nil
}

func (h HookView) validateReport(path string, r classifiedReport, bindingSeen *bool) error {
	if field, why := reportIssue(r.HookReport, h.form); why != "" {
		return invalid(path+field, why)
	}
	switch r.Class {
	case "bound":
		if r.Session != h.bound {
			return invalid(path+".Class", "inconsistent")
		}
		*bindingSeen = true
	case "foreign":
		// Same-id foreign reports are possible only before the first start.
		// Any retained start, even a foreign one, proves binding by that point.
		if r.Session == h.bound && (*bindingSeen || r.Kind == "session-start") || h.bound == "" && r.Kind == "session-start" {
			return invalid(path+".Class", "inconsistent")
		}
		*bindingSeen = *bindingSeen || r.Kind == "session-start"
	default:
		return enum(path+".Class", r.Class)
	}
	return nil
}
func Bind(h HookView, r HookReport) (HookView, error) {
	if err := ValidateHooks(h); err != nil {
		return h, err
	}
	if field, why := reportIssue(r, h.form); why != "" {
		return h, invalid(index("HookView.Reports", len(h.reports))+field, why)
	}
	if h.bound == "" && r.Kind == "session-start" {
		h.bound = r.Session
	}
	class := Class("bound")
	if r.Session != h.bound {
		if h.foreign == int(^uint(0)>>1) {
			return h, invalid("HookView.Foreign", "too-long")
		}
		class = "foreign"
		h.foreign++
	}
	h.reports = append(slices.Clip(h.reports), classifiedReport{r, class})
	return h, nil
}
func Reage(h HookView, ages []time.Duration) (HookView, error) {
	if err := ValidateHooks(h); err != nil {
		return h, err
	}
	if len(ages) != len(h.reports) {
		return h, invalid("HookView.Reports", "inconsistent")
	}
	for i, age := range ages {
		if age < 0 {
			return h, invalid(index("HookView.Reports", i)+".Age", "negative")
		}
	}
	h.reports = slices.Clone(h.reports)
	for i, age := range ages {
		h.reports[i].Age = age
	}
	return h, nil
}
func Drop(h HookView, i int) HookView {
	if i < 0 || i >= len(h.reports) || ValidateHooks(h) != nil {
		return h
	}
	h.reports = slices.Delete(slices.Clone(h.reports), i, i+1)
	return h
}
func Observation(h HookView) Obs {
	if ValidateHooks(h) != nil {
		return Obs{}
	}
	return Obs{h.bound, h.foreign}
}
func (h HookView) lowering() DenyReason {
	if h.bound == "" || h.ageLimit == 0 {
		return ""
	}
	var why DenyReason
	for _, r := range h.reports {
		if r.Class != "bound" || r.Age > h.ageLimit {
			continue
		}
		switch r.Kind {
		case "drift":
			return "pack-mode-drift"
		case "config-change":
			why = "pack-hooks-unverified"
		}
	}
	return why
}
func (h HookView) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Bound    string
		AgeLimit time.Duration
		Form     IDForm
		Foreign  int
		Reports  []classifiedReport
	}{h.bound, h.ageLimit, h.form, h.foreign, h.reports})
}
func DecodeHookView(b []byte) (HookView, error) {
	var h HookView
	bindingSeen := false
	readReport := func(b []byte, path string, r *classifiedReport) error {
		return object(b, path, func() error { return h.validateReport(path, *r, &bindingSeen) },
			value("Kind", &r.Kind), value("Session", &r.Session), value("Age", &r.Age), value("Class", &r.Class))
	}
	err := object(b, "HookView", func() error { return ValidateHooks(h) },
		value("Bound", &h.bound), value("AgeLimit", &h.ageLimit), value("Form", &h.form),
		value("Foreign", &h.foreign), list("Reports", &h.reports, readReport))
	if err != nil {
		return HookView{}, err
	}
	return h, nil
}
