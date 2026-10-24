package output

import (
	"context"
	"sync"
)

// Queue is a bounded FIFO that drops its oldest item when full, so a slow
// or stalled consumer (an SRT feed nobody reads yet, a slow callback) loses
// data but never blocks the producer.
type Queue[T any] struct {
	mu      sync.Mutex
	items   []T
	size    int
	dropped int
	ready   chan struct{}
	closed  bool
}

// NewQueue returns a queue holding at most size items.
func NewQueue[T any](size int) *Queue[T] {
	return &Queue[T]{size: size, ready: make(chan struct{}, 1)}
}

// Push adds an item, dropping the oldest one if the queue is full.
func (q *Queue[T]) Push(v T) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	if len(q.items) >= q.size {
		q.items = q.items[1:]
		q.dropped++
	}
	q.items = append(q.items, v)
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// Pop waits for the next item; false once ctx ends or the queue is closed
// and empty.
func (q *Queue[T]) Pop(ctx context.Context) (T, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			v := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return v, true
		}
		closed := q.closed
		q.mu.Unlock()
		var zero T
		if closed {
			return zero, false
		}
		select {
		case <-q.ready:
		case <-ctx.Done():
			return zero, false
		}
	}
}

// Close ends the queue: Pop returns what is left, then false.
func (q *Queue[T]) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// Dropped returns how many items were dropped so far.
func (q *Queue[T]) Dropped() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}
