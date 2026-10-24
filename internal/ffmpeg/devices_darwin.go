//go:build darwin

package ffmpeg

import (
	"context"
	"os/exec"
)

func listDevices(ctx context.Context, bin string) ([]Device, error) {
	// ffmpeg lists the devices on stderr and exits with an error.
	b, _ := exec.CommandContext(ctx, bin, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "").CombinedOutput()
	return parseAVFoundation(string(b)), nil
}

func deviceInput(video, audio *Device, _, _ int) (args []string, videoMap, audioMap string) {
	spec := ""
	if video != nil {
		spec = video.Input
		videoMap = "0:v:0"
	}
	if audio != nil {
		spec += ":" + audio.Input
		audioMap = "0:a:0"
	} else {
		spec += ":none"
	}
	if video == nil {
		spec = "none" + spec
	}
	return []string{"-f", "avfoundation", "-framerate", "30", "-i", spec}, videoMap, audioMap
}
