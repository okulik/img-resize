package queue

import (
	"context"
	"log"
	"time"
)

const (
	defaultRecheck = time.Second
	// resubscribeDelay is the pause before Listen subscribes again after an error.
	resubscribeDelay = time.Second
)

// Outcome is how a Wait ended.
type Outcome int

const (
	// WaitDone means the job finished and its state is gone.
	WaitDone Outcome = iota
	// WaitFailed means the job failed; WaitResult.Error holds the reason.
	WaitFailed
	// WaitTimedOut means the job was still unfinished when the timeout passed.
	WaitTimedOut
)

type WaitResult struct {
	Outcome Outcome
	Error   string
}

// Listen holds the one subscription to the done channel and wakes the Wait
// calls of the jobs it announces. It subscribes again after an error, pausing
// a second so a broken Redis is not hammered, and returns once ctx ends.
// Announcements of jobs nobody here waits for are ignored.
func (q *Queue) Listen(ctx context.Context) {
	for ctx.Err() == nil {
		if err := q.listenOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("listening for finished jobs: %v", err)
		}

		select {
		case <-ctx.Done():
		case <-time.After(resubscribeDelay):
		}
	}
}

// listenOnce subscribes and dispatches announcements until an error or ctx end.
func (q *Queue) listenOnce(ctx context.Context) error {
	sub := q.client.Subscribe(ctx, doneChannel)
	defer func() { _ = sub.Close() }()
	// A blocked receive ignores ctx, but returns once the subscription closes.
	defer context.AfterFunc(ctx, func() { _ = sub.Close() })()

	// Wait for the confirmation, so a failed subscription shows up here.
	if _, err := sub.Receive(ctx); err != nil {
		return err
	}

	for {
		msg, err := sub.ReceiveMessage(ctx)
		if err != nil {
			return err
		}
		q.wake(msg.Payload)
	}
}

// wake tells the waiters of a job that it finished. A waiter that was told
// already needs no second notice.
func (q *Queue) wake(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for w := range q.waiters[id] {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}

func (q *Queue) addWaiter(id string) chan struct{} {
	w := make(chan struct{}, 1)

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.waiters[id] == nil {
		q.waiters[id] = map[chan struct{}]struct{}{}
	}
	q.waiters[id][w] = struct{}{}

	return w
}

func (q *Queue) removeWaiter(id string, w chan struct{}) {
	q.mu.Lock()
	defer q.mu.Unlock()

	delete(q.waiters[id], w)
	if len(q.waiters[id]) == 0 {
		delete(q.waiters, id)
	}
}

// Wait blocks until the job has finished or timeout passes. A job whose state
// is gone has finished, and the caller finds its image in the cache. A job
// whose state says failed reports that failure. It returns ctx.Err() if ctx
// ends first, leaving the job running. Wait relies on Listen for prompt
// answers and rereads the state every second in case a notice was lost.
func (q *Queue) Wait(ctx context.Context, id string, timeout time.Duration) (WaitResult, error) {
	// Register before reading the state, so a job finishing in between is
	// announced to this waiter instead of being missed.
	w := q.addWaiter(id)
	defer q.removeWaiter(id, w)

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	recheck := time.NewTicker(q.recheck)
	defer recheck.Stop()

	for {
		state, found, err := q.State(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return WaitResult{}, ctx.Err()
			}
			return WaitResult{}, err
		}
		if !found {
			return WaitResult{Outcome: WaitDone}, nil
		}
		if state.Status == StatusFailed {
			return WaitResult{Outcome: WaitFailed, Error: state.Error}, nil
		}

		select {
		case <-ctx.Done():
			return WaitResult{}, ctx.Err()
		case <-deadline.C:
			return WaitResult{Outcome: WaitTimedOut}, nil
		case <-w:
		case <-recheck.C:
		}
	}
}
