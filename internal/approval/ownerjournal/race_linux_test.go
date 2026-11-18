package ownerjournal

import (
	"sync"
	"testing"
	"time"
)

// Appends from several goroutines lose nothing and keep the order: every
// append gets its own sequence number, the numbers run from 1 without a
// gap, each goroutine sees its own numbers rise, and a reader that runs
// beside them never sees a gap either.
func TestAppendsFromSeveralGoroutinesKeepTheOrder(t *testing.T) {
	const writers, each = 8, 25
	path := newPath(t)
	s := mustOpen(t, path)
	seqs := make([][]int64, writers)
	errs := make(chan error, writers+1)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				// The time says who wrote the entry and which of its
				// entries it is.
				e, err := addLooked(s, base.Add(time.Duration(w*1000+i)*time.Millisecond))
				if err != nil {
					errs <- err
					return
				}
				seqs[w] = append(seqs[w], e.Seq)
			}
		}(w)
	}
	done := make(chan struct{})
	go func() {
		defer close(errs)
		for {
			entries, err := s.Entries(0, 0)
			if err != nil {
				errs <- err
				return
			}
			for i, e := range entries {
				if e.Seq != int64(i+1) {
					t.Errorf("a reader saw sequence number %d at place %d", e.Seq, i)
					return
				}
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	wg.Wait()
	close(done)
	for err := range errs {
		t.Fatal(err)
	}
	seen := make(map[int64]bool)
	for w, mine := range seqs {
		if len(mine) != each {
			t.Fatalf("writer %d made %d appends, want %d", w, len(mine), each)
		}
		for i, seq := range mine {
			if i > 0 && seq <= mine[i-1] {
				t.Fatalf("writer %d: sequence number %d after %d", w, seq, mine[i-1])
			}
			if seen[seq] {
				t.Fatalf("sequence number %d was given twice", seq)
			}
			seen[seq] = true
		}
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	entries := mustEntries(t, s)
	if len(entries) != writers*each {
		t.Fatalf("%d entries, want %d", len(entries), writers*each)
	}
	next := make([]int, writers)
	for i, e := range entries {
		if e.Seq != int64(i+1) {
			t.Fatalf("sequence number %d at place %d", e.Seq, i)
		}
		ms := int(e.At.Sub(base) / time.Millisecond)
		w, n := ms/1000, ms%1000
		if w < 0 || w >= writers || n != next[w] {
			t.Fatalf("entry %d is writer %d's entry %d, want its entry %d", e.Seq, w, n, next[w])
		}
		next[w]++
		if seqs[w][n] != e.Seq {
			t.Fatalf("writer %d's entry %d was told sequence number %d and has %d", w, n, seqs[w][n], e.Seq)
		}
	}
}
