package osc7501

import (
	"testing"

	"ataniq/internal/inject/versions"
)

// TestRecordedAppsAreTheTableApps: the reports the package was written from
// carry exactly the app names of the table in package versions (a report names
// an app, not a version, so the package has no version of its own): every
// entry's app was recorded, and every recorded app belongs to an entry.
func TestRecordedAppsAreTheTableApps(t *testing.T) {
	recordedApps := map[string]bool{}
	for _, r := range recorded(t) {
		if r.want.App != "" {
			recordedApps[r.want.App] = true
		}
	}
	tableApps := map[string]bool{}
	for _, e := range versions.Tested() {
		tableApps[e.App] = true
		if !recordedApps[e.App] {
			t.Errorf("%s %s: the table says its reports carry app %q, and testdata/reports.txt has none", e.Runtime, e.Version, e.App)
		}
	}
	for app := range recordedApps {
		if !tableApps[app] {
			t.Errorf("testdata/reports.txt has reports of app %q, which no table entry names", app)
		}
	}
}
