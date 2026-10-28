//go:build linux

package ffmpeg

import (
	"slices"
	"strings"
	"testing"
)

// A camera and a microphone keep their capture times (-copyts, the camera's
// timestamps on the wall clock), so that ffmpeg does not hold the camera
// back by the gap between opening the two.
func TestDeviceInput_LinuxClocks(t *testing.T) {
	cam := &Device{Kind: "video", Input: "/dev/video0", Format: "mjpeg"}
	mic := &Device{Kind: "audio", Input: "default"}
	args, vmap, amap := deviceInput(cam, mic, 1280, 720)
	got := strings.Join(args, " ")
	want := "-copyts -f v4l2 -ts abs -input_format mjpeg -framerate 30 -video_size 1280x720 -i /dev/video0 -f pulse -fragment_size 3840 -i default"
	if got != want || vmap != "0:v:0" || amap != "1:a:0" {
		t.Errorf("both: %s (%s, %s)\nwant %s", got, vmap, amap, want)
	}
	for _, one := range [][]string{
		func() []string { a, _, _ := deviceInput(cam, nil, 1280, 720); return a }(),
		func() []string { a, _, _ := deviceInput(nil, mic, 1280, 720); return a }(),
	} {
		if slices.Contains(one, "-copyts") || slices.Contains(one, "abs") {
			t.Errorf("one device: %v, want its own clock", one)
		}
	}
}
