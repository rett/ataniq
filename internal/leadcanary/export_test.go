//go:build linux && ataniq_canary

package leadcanary

// WrapWrite replaces the evidence file's write with wrap(real), where real is
// the file's own write. It is for tests of a failed or partial write.
func (e *Evidence) WrapWrite(wrap func(real func([]byte) (int, error)) func([]byte) (int, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.write = wrap(e.write)
}

// WrapSync replaces the evidence file's sync with wrap(real), where real is
// the file's own sync. It is for tests of a failed sync.
func (e *Evidence) WrapSync(wrap func(real func() error) func() error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sync = wrap(e.sync)
}
