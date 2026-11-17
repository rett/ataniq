//go:build !race

package sessionpane_test

// raceEnabled is true when the tests run under the race detector, which makes
// the big-screen tests ten times slower.
const raceEnabled = false
