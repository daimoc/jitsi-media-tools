//go:build windows

package ffmpeg

import (
	"context"
	"os/exec"
)

func listDevices(ctx context.Context, bin string) ([]Device, error) {
	// ffmpeg lists the devices on stderr and exits with an error.
	b, _ := exec.CommandContext(ctx, bin, "-hide_banner", "-f", "dshow", "-list_devices", "true", "-i", "dummy").CombinedOutput()
	return parseDShow(string(b)), nil
}

func cameraFormat(context.Context, string, string) string { return "" }

func deviceInput(video, audio *Device, _, _ int) (args []string, videoMap, audioMap string) {
	spec := ""
	if video != nil {
		spec = "video=" + video.Input
		videoMap = "0:v:0"
	}
	if audio != nil {
		if spec != "" {
			spec += ":"
		}
		spec += "audio=" + audio.Input
		audioMap = "0:a:0"
	}
	return []string{"-f", "dshow", "-rtbufsize", "64M", "-i", spec}, videoMap, audioMap
}
