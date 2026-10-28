package ffmpeg

import (
	"context"
	"fmt"
	"strings"
)

// FindDevice returns the device of kind named name (case-insensitive, or
// its Input), or an error listing the available ones.
func FindDevice(devices []Device, kind, name string) (Device, error) {
	var names []string
	for _, d := range devices {
		if d.Kind != kind {
			continue
		}
		if strings.EqualFold(d.Name, name) || d.Input == name {
			return d, nil
		}
		names = append(names, fmt.Sprintf("%q", d.Name))
	}
	if len(names) == 0 {
		return Device{}, fmt.Errorf("no %s device %q: none found", kind, name)
	}
	return Device{}, fmt.Errorf("no %s device %q; available: %s", kind, name, strings.Join(names, ", "))
}

// DeviceInput returns the ffmpeg input arguments that capture video and
// audio (either may be nil) at width×height, 30 fps, and the stream maps of
// the video and audio in them. On Linux it asks bin for the camera's
// formats first: Motion-JPEG when offered, since most webcams send raw
// 720p at 10 fps or less over USB 2.
func DeviceInput(ctx context.Context, bin string, video, audio *Device, width, height int) (args []string, videoMap, audioMap string) {
	if video != nil {
		v := *video
		v.Format = cameraFormat(ctx, bin, v.Input)
		video = &v
	}
	return deviceInput(video, audio, width, height)
}

// ListDevices lists the machine's cameras and microphones.
func ListDevices(ctx context.Context, bin string) ([]Device, error) {
	return listDevices(ctx, bin)
}

// InputFormats returns the needs ("device:<format>") of input arguments'
// -f formats.
func InputFormats(args []string) []string {
	var needs []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" {
			needs = append(needs, "device:"+args[i+1])
		}
	}
	return needs
}
