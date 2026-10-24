package rtpmedia

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestPump_PlaceholderWhileSilentThenSource(t *testing.T) {
	rec := &recorder{}
	in := make(chan Frame)
	ph := &Placeholder{Frame: Frame{Data: []byte{0xaa}, Duration: 20 * time.Millisecond, Samples: OpusSamples}, After: 50 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- Pump(context.Background(), NewAudioSender(rec), in, ph) }()

	time.Sleep(200 * time.Millisecond) // Silent source: placeholders every 20 ms after 50 ms
	for i := 0; i < 5; i++ {
		in <- Frame{Data: []byte{0xbb}, Duration: 20 * time.Millisecond, Samples: OpusSamples}
	}
	close(in)
	if err := <-done; err != nil {
		t.Fatalf("Pump: %v", err)
	}

	var placeholders, real int
	for _, p := range rec.pkts {
		switch {
		case bytes.Equal(p.Payload, []byte{0xaa}):
			if real > 0 {
				t.Fatal("placeholder sent while the source was sending")
			}
			placeholders++
		case bytes.Equal(p.Payload, []byte{0xbb}):
			real++
		}
	}
	if placeholders < 4 || placeholders > 9 || real != 5 {
		t.Errorf("%d placeholders, %d source frames; want about 7 and 5", placeholders, real)
	}
	for i := 1; i < len(rec.pkts); i++ {
		if rec.pkts[i].Timestamp <= rec.pkts[i-1].Timestamp {
			t.Fatalf("timestamps not increasing at %d", i)
		}
	}
	if rec.pkts[0].Timestamp != initialRTPTimestamp {
		t.Errorf("first timestamp %d, want %d", rec.pkts[0].Timestamp, initialRTPTimestamp)
	}
}

func TestPump_StopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Pump(ctx, NewAudioSender(&recorder{}), make(chan Frame), nil) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("want the context's error")
		}
	case <-time.After(time.Second):
		t.Fatal("Pump did not stop")
	}
}
