package output

import (
	"context"
	"testing"
	"time"
)

// Packets carry the time they were made, not the time they were queued, so
// a clock's late or caught-up ticks keep a regular rate in the file.
func TestStampedQueue_KeepsMadeTimes(t *testing.T) {
	q := newStampedQueue(10, 10)
	start := q.start
	frame := func(n int) time.Time { return start.Add(time.Duration(n) * streamFrame) }
	q.pushVideo([]byte{1}, frame(1))
	time.Sleep(15 * time.Millisecond) // Queued late: still frame 2's time
	q.pushVideo([]byte{2}, frame(2))
	q.pushVideo([]byte{3}, frame(3)) // Caught up, back to back: kept
	q.pushVideo([]byte{4}, start.Add(-time.Second))
	q.pushAudio([]byte{5}, start.Add(streamChunk))
	q.pushAudio([]byte{6}, start.Add(streamChunk)) // Same tick: plays after the first
	want := []struct {
		data byte
		at   time.Duration
	}{
		{1, streamFrame}, {2, 2 * streamFrame}, {3, 3 * streamFrame},
		{5, streamChunk}, {6, 2 * streamChunk},
	}
	for _, w := range want {
		p, ok := q.pop(context.Background())
		if !ok || p.data[0] != w.data || p.at != w.at {
			t.Errorf("got %v at %v, want %d at %v", p.data, p.at, w.data, w.at)
		}
	}
}
