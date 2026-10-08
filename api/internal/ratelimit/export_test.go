package ratelimit

// Test-only hook (compiled only by `go test`): how many sweeps have run.
func (l *Limiter) Sweeps() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sweeps
}
