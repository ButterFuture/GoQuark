package download

import (
	"context"
	"sync"
)

// slots hands out the file slots: how many files are fetched at once. Unlike a
// buffered channel its limit can change while tasks are waiting, so a new
// setting applies to the queue that is already there.
type slots struct {
	mu    sync.Mutex
	limit int
	used  int
	// wake is closed, and replaced, whenever a slot may have come free.
	wake chan struct{}
}

func newSlots(limit int) *slots {
	return &slots{limit: limit, wake: make(chan struct{})}
}

// acquire waits for a free slot. It reports false, holding nothing, if ctx
// ends first.
func (s *slots) acquire(ctx context.Context) bool {
	for {
		s.mu.Lock()
		if s.used < s.limit {
			s.used++
			s.mu.Unlock()
			return true
		}
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return false
		}
	}
}

func (s *slots) release() {
	s.mu.Lock()
	s.used--
	s.broadcastLocked()
	s.mu.Unlock()
}

// setLimit changes the limit. Raising it starts waiting tasks at once;
// lowering it lets the running ones finish and holds new ones back until the
// count is under the new limit.
func (s *slots) setLimit(n int) {
	s.mu.Lock()
	s.limit = n
	s.broadcastLocked()
	s.mu.Unlock()
}

func (s *slots) broadcastLocked() {
	close(s.wake)
	s.wake = make(chan struct{})
}
