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
// pactl, after their default; without pactl, ALSA's default device.
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
		out = append(out, Device{Kind: "audio", Name: "default", Description: "the default microphone", Input: "default"})
		out = append(out, parsePactlSources(string(b))...)
	} else {
		out = append(out, Device{Kind: "audio", Name: "default", Description: "ALSA default", Input: "alsa:default"})
	}
	return out, nil
}

// cameraFormat asks ffmpeg which compressed formats the camera at dev
// sends; ffmpeg lists them on stderr and exits with an error.
func cameraFormat(ctx context.Context, bin, dev string) string {
	if bin == "" {
		return ""
	}
	b, _ := exec.CommandContext(ctx, bin, "-hide_banner", "-f", "v4l2", "-list_formats", "compressed", "-i", dev).CombinedOutput()
	return parseV4L2Formats(string(b))
}

func deviceInput(video, audio *Device, w, h int) (args []string, videoMap, audioMap string) {
	n := 0
	if video != nil {
		args = append(args, "-f", "v4l2")
		if video.Format != "" {
			args = append(args, "-input_format", video.Format)
		}
		args = append(args, "-framerate", "30", "-video_size", fmt.Sprintf("%dx%d", w, h), "-i", video.Input)
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
