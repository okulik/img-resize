package queue

import "time"

// WaiterCount returns how many Wait calls are registered.
func (q *Queue) WaiterCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	n := 0
	for _, set := range q.waiters {
		n += len(set)
	}

	return n
}

// SetRecheckInterval changes how often Wait rereads the job state.
func (q *Queue) SetRecheckInterval(d time.Duration) { q.recheck = d }

// ProbeWaiter registers a waiter for id like Wait does, and returns the channel
// that Listen wakes and a function to unregister it.
func (q *Queue) ProbeWaiter(id string) (<-chan struct{}, func()) {
	w := q.addWaiter(id)
	return w, func() { q.removeWaiter(id, w) }
}
