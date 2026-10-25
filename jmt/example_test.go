package jmt_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/jmt"
)

// ExampleJoin joins the meeting at $JMT_URL, publishes a moving test
// pattern, and counts the video frames received from each participant for
// 10 seconds. It is compiled but not run by go test (it needs a server).
func ExampleJoin() {
	pub, src := jmt.FrameSource(640, 360, false, true)
	s, err := jmt.Join(context.Background(), jmt.Options{MeetingURL: os.Getenv("JMT_URL"), DisplayName: "Example", Publish: src})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer s.Close()

	var mu sync.Mutex
	frames := map[string]int{}
	s.OnVideo(func(f jmt.VideoFrame) {
		mu.Lock()
		frames[f.ParticipantID]++
		mu.Unlock()
	})

	f := jmt.VideoFrame{Width: 640, Height: 360, Y: make([]byte, 640*360), U: make([]byte, 320*180), V: make([]byte, 320*180)}
	for j := range f.U {
		f.U[j], f.V[j] = 128, 128 // No color: a grey moving gradient
	}
	for i := 0; i < 300; i++ { // 10 s at 30 fps
		for j := range f.Y {
			f.Y[j] = byte(j%640 + i*4)
		}
		pub.WriteVideo(f)
		time.Sleep(time.Second / 30)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range s.Participants() {
		fmt.Printf("%s: %d frames\n", p.Name, frames[p.ID])
	}
}
