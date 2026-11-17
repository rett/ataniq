//go:build linux

package serve

import (
	"bytes"
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
)

// bareServer is a server with no sockets and no session, for the queue's own
// rules. Its counters have nowhere to go, so it must not overflow.
func bareServer(queue int) (*Server, *client) {
	s := &Server{cfg: Config{QueueBytes: queue, OverflowWindow: time.Second, FlushWait: time.Second}}
	s.cfg.withDefaults()
	s.cfg.QueueBytes = queue
	c := &client{s: s, wake: make(chan struct{}, 1), streaming: true, role: wire.RoleViewer}
	return s, c
}

// Chunks larger than the Output frame's data cap are split.
func TestOutputIsSplitAtTheFrameCap(t *testing.T) {
	s, c := bareServer(16 << 20)
	data := bytes.Repeat([]byte("0123456789abcdef"), (wire.MaxOutputData*5/2)/16)
	s.mu.Lock()
	s.pushOutputLocked(c, 1000, data)
	s.mu.Unlock()
	if len(c.q) != 3 {
		t.Fatalf("%d frames for %d bytes", len(c.q), len(data))
	}
	pos := uint64(1000)
	var got []byte
	for _, it := range c.q {
		if it.typ != wire.TypeOutput || it.pos != pos {
			t.Fatalf("frame %+v at %d", it.typ, pos)
		}
		p, err := it.payload()
		if err != nil {
			t.Fatalf("a frame over the cap: %v", err)
		}
		if len(p) > wire.MaxHolderPayload {
			t.Fatalf("payload of %d bytes", len(p))
		}
		got = append(got, it.data...)
		pos += uint64(len(it.data))
	}
	if !bytes.Equal(got, data) {
		t.Fatal("the frames do not add up to the chunk")
	}
}

// Small chunks that follow each other are one frame, so that a slow client does
// not hold one queue entry for every byte.
func TestSmallChunksAreCoalesced(t *testing.T) {
	s, c := bareServer(1 << 20)
	s.mu.Lock()
	for i := 0; i < 1000; i++ {
		s.pushOutputLocked(c, uint64(50+i), []byte{byte('a' + i%26)})
	}
	s.mu.Unlock()
	if len(c.q) != 1 || len(c.q[0].data) != 1000 || c.q[0].pos != 50 {
		t.Fatalf("%d frames, first %d bytes at %d", len(c.q), len(c.q[0].data), c.q[0].pos)
	}
	if c.qBytes != 1000+posLen {
		t.Errorf("queued %d bytes", c.qBytes)
	}
	// A gap in the positions is a new frame.
	s.mu.Lock()
	s.pushOutputLocked(c, 5000, []byte("x"))
	s.mu.Unlock()
	if len(c.q) != 2 {
		t.Errorf("%d frames", len(c.q))
	}
}

// The queue counts payload bytes, not a snapshot.
func TestQueueBoundAndSnapshotOutsideIt(t *testing.T) {
	s, c := bareServer(1000)
	s.mu.Lock()
	s.pushLocked(c, item{typ: wire.TypeSnapshot, pos: 1, data: make([]byte, 100000), snap: true}, true)
	if c.qBytes != 0 {
		t.Errorf("a snapshot counts %d bytes", c.qBytes)
	}
	if !s.pushLocked(c, item{typ: wire.TypeNotice, data: make([]byte, 900)}, false) {
		t.Error("a frame within the bound was refused")
	}
	s.mu.Unlock()
	if c.qBytes != 900 {
		t.Errorf("counted %d", c.qBytes)
	}
}
