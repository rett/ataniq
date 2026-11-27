package protocol_test

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/agentsession/protocol"
)

func TestFramerDropsWholeOversizedLine(t *testing.T) {
	atLimit := strings.Repeat("x", protocol.MaxFrameBytes-1) + "\n"
	over := strings.Repeat("y", protocol.MaxFrameBytes) + "\n"
	stream := []byte(atLimit + over + over + "after\n\n")
	for _, step := range []int{1, 11, protocol.MaxFrameBytes - 1, protocol.MaxFrameBytes, len(stream)} {
		var f protocol.Framer
		var got []string
		var drops int
		for chunk := range slices.Chunk(stream, step) {
			drops += f.Feed(chunk, func(line []byte) { got = append(got, string(line)) })
		}
		if drops != 2 || !slices.Equal(got, []string{atLimit, "after\n", "\n"}) || f.Finish() != nil {
			t.Fatalf("chunk size %d: %d drops, %d frames", step, drops, len(got))
		}
	}
}

func TestFramerEndOfStream(t *testing.T) {
	var f protocol.Framer
	unexpected := func([]byte) { t.Fatal("emitted unterminated frame") }
	if n := f.Feed(nil, unexpected); n != 0 || f.Finish() != nil {
		t.Fatal("empty stream")
	}
	if n := f.Feed([]byte("tail"), unexpected); n != 0 || !errors.Is(f.Finish(), io.ErrUnexpectedEOF) {
		t.Fatal("truncated stream")
	}
	if n := f.Feed(bytes.Repeat([]byte{'x'}, protocol.MaxFrameBytes*2), unexpected); n != 1 {
		t.Fatal("oversized fragment", n)
	}
	if n := f.Feed([]byte("tail"), unexpected); n != 0 || f.Finish() != nil {
		t.Fatal("oversized fragment counted twice")
	}
	var got string
	f.Feed([]byte("new\n"), func(line []byte) { got = string(line) })
	if got != "new\n" || f.Finish() != nil {
		t.Fatal("new stream retained old bytes")
	}
}
