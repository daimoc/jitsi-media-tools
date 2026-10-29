package output

import (
	"context"
	"sync"
	"time"
)

// Intervals the grid, tiles and mix are written at.
const (
	streamChunk = 20 * time.Millisecond
	streamFrame = time.Second / 30
)

// stampedQueue holds video frames and audio chunks for a NUT writer, each
// stamped with the time it was made, and drops the oldest of a kind
// when more than its depth wait: a slow reader stays late by at most that
// much (docs/latency.md).
type stampedQueue struct {
	start      time.Time
	videoDepth int
	audioDepth int
	ready      chan struct{}
	mu         sync.Mutex
	pending    []stampedPacket
	video      int // Video packets pending
	audio      int
	lastVideo  time.Duration // Time of the last frame queued, -1 before
	lastAudio  time.Duration
	closed     bool
}

type stampedPacket struct {
	video bool
	at    time.Duration
	data  []byte
}

func newStampedQueue(videoDepth, audioDepth int) *stampedQueue {
	return &stampedQueue{start: time.Now(), videoDepth: videoDepth, audioDepth: audioDepth,
		ready: make(chan struct{}, 1), lastVideo: -1, lastAudio: -1}
}

// stamp returns made as a time in the stream.
func (q *stampedQueue) stamp(made time.Time) time.Duration { return max(made.Sub(q.start), 0) }

// pushVideo queues a frame made at made; the queue keeps b. A frame closer
// than half a frame interval to the previous one is dropped.
func (q *stampedQueue) pushVideo(b []byte, made time.Time) {
	at := q.stamp(made)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.lastVideo >= 0 && at-q.lastVideo < streamFrame/2 {
		return
	}
	q.lastVideo = at
	q.pushLocked(stampedPacket{video: true, at: at, data: b})
}

// pushAudio queues a 20 ms chunk made at made; the queue keeps b. A chunk
// made no later than the previous one gets the next time, as their samples
// play one after the other.
func (q *stampedQueue) pushAudio(b []byte, made time.Time) {
	at := q.stamp(made)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if q.lastAudio >= 0 {
		at = max(at, q.lastAudio+streamChunk)
	}
	q.lastAudio = at
	q.pushLocked(stampedPacket{at: at, data: b})
}

func (q *stampedQueue) pushLocked(p stampedPacket) {
	if p.video {
		q.video++
	} else {
		q.audio++
	}
	q.pending = append(q.pending, p)
	if q.video > q.videoDepth || q.audio > q.audioDepth {
		for i, o := range q.pending {
			if o.video == p.video {
				q.pending = append(q.pending[:i], q.pending[i+1:]...)
				break
			}
		}
		if p.video {
			q.video--
		} else {
			q.audio--
		}
	}
	q.signal()
}

func (q *stampedQueue) signal() {
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// pop returns the next packet in the order they came; false once ctx ends,
// or the queue is closed and empty.
func (q *stampedQueue) pop(ctx context.Context) (stampedPacket, bool) {
	for {
		q.mu.Lock()
		if len(q.pending) > 0 {
			p := q.pending[0]
			q.pending = q.pending[1:]
			if p.video {
				q.video--
			} else {
				q.audio--
			}
			q.mu.Unlock()
			return p, true
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return stampedPacket{}, false
		}
		select {
		case <-q.ready:
		case <-ctx.Done():
			return stampedPacket{}, false
		}
	}
}

// close refuses new packets; pop drains what is queued, then ends.
func (q *stampedQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.signal()
}

// writeTo writes p to n.
func (p stampedPacket) writeTo(n *nutWriter) error {
	if p.video {
		return n.video(p.at, p.data)
	}
	return n.audio(p.at, p.data)
}
