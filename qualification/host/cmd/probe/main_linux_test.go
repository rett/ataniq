//go:build linux && amd64

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"ataniq/qualification/host/internal/qualification"
)

const futureRequest = `{"version":1,"run_id":"01234567","request_id":"89abcdef","case":"KR1","max_duration_ms":100,"max_bytes":4096}`

func TestRunOneBoundedFrame(t *testing.T) {
	for _, input := range []string{futureRequest, `{}`, futureRequest + `{}`, `{"private-input-secret":1}`, futureRequest + strings.Repeat(" ", qualification.MaxInputBytes)} {
		var out bytes.Buffer
		code := run(nil, strings.NewReader(input), &out)
		if strings.Count(out.String(), "\n") != 1 || out.Len() > qualification.MaxOutputBytes || strings.Contains(out.String(), "private-input-secret") {
			t.Fatal("unbounded/leaking output", out.String())
		}
		o, err := qualification.DecodeObservation(&out)
		if err != nil {
			t.Fatal(err)
		}
		if input == futureRequest {
			if code != 0 || o.Status != "unsupported" || o.RunID != "01234567" {
				t.Fatal(code, o)
			}
		} else if code != 2 || o.Status != "error" || o.RunID != "" {
			t.Fatal(code, o)
		}
	}
	for _, args := range [][]string{{"--private-secret"}, {"extra"}, {"--fixture-root"}} {
		var out bytes.Buffer
		if run(args, strings.NewReader(futureRequest), &out) != 2 || strings.Contains(out.String(), "private-secret") {
			t.Fatal(out.String())
		}
		if _, err := qualification.DecodeObservation(&out); err != nil {
			t.Fatal(err)
		}
	}
}

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type failedInput struct{}

func (failedInput) Read([]byte) (int, error) { return 0, errors.New("private input error") }
func TestRunIOFailure(t *testing.T) {
	if run(nil, strings.NewReader(futureRequest), brokenOutput{}) != 1 {
		t.Fatal("output failure hidden")
	}
	var out bytes.Buffer
	if run(nil, failedInput{}, &out) != 2 || strings.Contains(out.String(), "private input error") {
		t.Fatal("input error leaked")
	}
}

func TestTypedRequestsUseExistingFixtureRoot(t *testing.T) {
	for _, selected := range []struct {
		name string
		fd   int
	}{{"fake-dir-fd", 128}, {"fake-pipe-fd", 256}, {"fake-socket-fd", 384}} {
		leaf := ""
		if selected.fd == 128 {
			leaf = `,"leaf":{"name":"leaf","device":1,"inode":2,"size":4,"sha256":"` + strings.Repeat("a", 64) + `"}`
		}
		input := fmt.Sprintf(`{"version":1,"run_id":"01234567","request_id":"89abcdef","case":%q,"payload":{"typed_fd":{"name":"inherited-bindings.json","fd":%d,"profile":"fd1-typed","attempt_id":"aabbccdd","nonce":"01234567"%s}},"max_duration_ms":100,"max_bytes":4096}`, selected.name, selected.fd, leaf)
		for _, withRoot := range []bool{false, true} {
			var args []string
			if withRoot {
				args = []string{"--fixture-root", t.TempDir()}
			}
			var out bytes.Buffer
			code := run(args, strings.NewReader(input), &out)
			o, err := qualification.DecodeObservation(&out)
			if err != nil {
				t.Fatal(err)
			}
			if withRoot {
				if code != 0 || o.Status != "inconclusive" || o.Errno == nil {
					t.Fatal(code, o)
				}
				if len(o.Facts) != 1 || o.Facts[0].Name != "stage" || *o.Facts[0].Value != "binding-read" {
					t.Fatal(o)
				}
			} else if code != 2 || o.Status != "error" {
				t.Fatal(code, o)
			}
		}
	}
}

func TestR1ModesAreExplicitFixedAndExclusive(t *testing.T) {
	for _, tc := range []struct {
		args          []string
		memory, child bool
		code          int
	}{
		{[]string{"--r1-memory"}, true, false, 0},
		{[]string{"--private-r1-child"}, false, true, 20},
		{[]string{"--r1-memory", "--private-r1-child"}, false, false, 2},
		{[]string{"--r1-memory", "--private-ready-hold"}, false, false, 2},
		{[]string{"--r1-memory", "--fixture-root=/elsewhere"}, false, false, 2},
		{[]string{"--private-r1-child", "--fixture-root="}, false, false, 2},
		{[]string{"--private-r1-child", "--private-ready-hold=false"}, false, false, 2},
		{[]string{"--r1-memory", "--max-bytes=1"}, false, false, 2},
		{[]string{"--r1-memory", "--lifetime=1"}, false, false, 2},
		{[]string{"--r1-memory", "extra"}, false, false, 2},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out bytes.Buffer
			m, c := false, false
			code := runWithActors(tc.args, strings.NewReader("immutable input"), &out, func(in io.Reader, w io.Writer) error {
				m = true
				b, _ := io.ReadAll(in)
				if string(b) != "immutable input" {
					t.Fatal("input rewritten")
				}
				return nil
			}, func() int { c = true; return 20 })
			if code != tc.code || m != tc.memory || c != tc.child {
				t.Fatal(code, m, c)
			}
		})
	}
}
