//go:build linux && amd64

package main

import (
	"bytes"
	"errors"
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
