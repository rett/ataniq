package main

import (
	"context"
	"slices"
	"strings"
	"time"

	"ataniq/internal/seatgate"
	"ataniq/internal/seatlink"
)

// helloEvery is how often the command looks for a new connection of the
// gate link, to log what its gate's hello listed. A test makes it shorter.
var helloEvery = time.Second

// logHellos logs, once for each connection of the link, the capabilities the
// gate's hello listed, in its order and in words, and whether the phone then
// offers "New project" and read-only Leads. It ends with ctx. The words
// are the gate's, of the grammar [a-z0-9.-]{1,40} the transport holds them
// to; nothing else of the hello is logged.
func logHellos(ctx context.Context, c *seatgate.Client, every time.Duration, logf func(format string, args ...any)) {
	t := time.NewTicker(every)
	defer t.Stop()
	var last time.Time
	for {
		if since, ok := c.Connected(); ok && !since.Equal(last) {
			last = since
			logf("ataniq-seat: %s\n", helloLine(c.Caps()))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// helloLine is the log line of a hello's capabilities.
func helloLine(caps []string) string {
	line := "the seat gate offers " + inWords(caps)
	if slices.Contains(caps, seatlink.CapGenesis) {
		line += "; the phone offers New project"
	} else {
		line += "; the phone offers no New project"
	}
	if slices.Contains(caps, seatlink.CapChat) {
		line += "; the phone offers read-only Leads"
	}
	return line
}

// inWords writes a list as "a", "a and b" or "a, b and c", and "nothing" for
// an empty one, as the daemon's start line does.
func inWords(list []string) string {
	switch len(list) {
	case 0:
		return "nothing"
	case 1:
		return list[0]
	}
	return strings.Join(list[:len(list)-1], ", ") + " and " + list[len(list)-1]
}
