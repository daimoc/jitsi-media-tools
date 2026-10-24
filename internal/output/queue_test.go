package output

import (
	"context"
	"testing"
	"time"
)

func TestQueue_DropsOldest(t *testing.T) {
	q := NewQueue[int](3)
	for i := 1; i <= 5; i++ {
		q.Push(i)
	}
	if q.Dropped() != 2 {
		t.Errorf("dropped %d", q.Dropped())
	}
	ctx := context.Background()
	for want := 3; want <= 5; want++ {
		if v, ok := q.Pop(ctx); !ok || v != want {
			t.Fatalf("pop %d %v, want %d", v, ok, want)
		}
	}
	q.Close()
	if _, ok := q.Pop(ctx); ok {
		t.Error("pop after close and empty")
	}
}

func TestQueue_PopWaitsAndCancels(t *testing.T) {
	q := NewQueue[int](1)
	go func() { time.Sleep(20 * time.Millisecond); q.Push(7) }()
	if v, ok := q.Pop(context.Background()); !ok || v != 7 {
		t.Fatalf("pop %d %v", v, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, ok := q.Pop(ctx); ok {
		t.Error("pop should fail when ctx ends")
	}
}
