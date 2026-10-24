//go:build linux

package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// listDevices reads the V4L2 capture nodes from sysfs (index 0: the node of
// a camera, not its metadata node) and the PulseAudio/PipeWire sources from
// pactl; without pactl, ALSA's default device.
func listDevices(ctx context.Context, _ string) ([]Device, error) {
	var out []Device
	nodes, _ := filepath.Glob("/sys/class/video4linux/video*")
	sort.Strings(nodes)
	for _, n := range nodes {
		if idx, err := os.ReadFile(filepath.Join(n, "index")); err == nil && strings.TrimSpace(string(idx)) != "0" {
			continue
		}
		name, err := os.ReadFile(filepath.Join(n, "name"))
		if err != nil {
			continue
		}
		dev := "/dev/" + filepath.Base(n)
		out = append(out, Device{Kind: "video", Name: strings.TrimSpace(string(name)), Description: dev, Input: dev})
	}
	if b, err := exec.CommandContext(ctx, "pactl", "list", "short", "sources").Output(); err == nil {
		out = append(out, parsePactlSources(string(b))...)
	} else {
		out = append(out, Device{Kind: "audio", Name: "default", Description: "ALSA default", Input: "alsa:default"})
	}
	return out, nil
}

func deviceInput(video, audio *Device, w, h int) (args []string, videoMap, audioMap string) {
	n := 0
	if video != nil {
		args = append(args, "-f", "v4l2", "-framerate", "30", "-video_size", fmt.Sprintf("%dx%d", w, h), "-i", video.Input)
		videoMap = fmt.Sprintf("%d:v:0", n)
		n++
	}
	if audio != nil {
		if dev, ok := strings.CutPrefix(audio.Input, "alsa:"); ok {
			args = append(args, "-f", "alsa", "-i", dev)
		} else {
			args = append(args, "-f", "pulse", "-i", audio.Input)
		}
		audioMap = fmt.Sprintf("%d:a:0", n)
	}
	return args, videoMap, audioMap
}
