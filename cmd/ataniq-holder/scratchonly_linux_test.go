//go:build linux && !ataniq_canary

package main

import (
	"strings"
	"testing"
)

// Without the ataniq_canary tag the flags of canary mode do not exist: each is
// an unknown flag, with or without --scratch, and nothing is started.
func TestCanaryFlagsAreUnknownWithoutTheTag(t *testing.T) {
	for _, extra := range [][]string{
		{"--canary"}, {"--inject"}, {"--sandbox-inputs", "/x/inputs.json"}, {"--expected-mode", "sample mode"},
		{"--record-dir", "/x/record"},
		{"--canary", "--inject", "--sandbox-inputs", "/x/inputs.json", "--expected-mode", "sample mode"},
	} {
		for _, scratch := range []bool{true, false} {
			r := newRig(t)
			a := r.spec("wait")
			a.scratch = scratch
			a.extraBefore = extra
			if code := r.runNow(a.build()); code != exitRefuse {
				t.Errorf("%v (scratch %v): exit %d, want %d", extra, scratch, code, exitRefuse)
			}
			if !strings.Contains(r.errbuf.String(), "error: invalid flags") {
				t.Errorf("%v (scratch %v): stderr %q is not the refusal of an unknown flag", extra, scratch, r.errbuf.String())
			}
			nothingStarted(t, r)
		}
	}
}

// The version line of a build without the tag names no tag, and the help says
// nothing of canary mode.
func TestNoTagInTheVersionOrTheHelp(t *testing.T) {
	if versionSuffix != "" {
		t.Errorf("the version line has the suffix %q", versionSuffix)
	}
	r := newRig(t)
	if code := r.runNow([]string{"--help"}); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, word := range []string{"canary", "--inject", "sandbox-inputs", "expected-mode", "record-dir"} {
		if strings.Contains(r.errbuf.String(), word) {
			t.Errorf("the help of a build without the tag says %q", word)
		}
	}
}
