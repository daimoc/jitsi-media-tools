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
// the video and audio in them.
func DeviceInput(video, audio *Device, width, height int) (args []string, videoMap, audioMap string) {
	return deviceInput(video, audio, width, height)
}

// ListDevices lists the machine's cameras and microphones.
func ListDevices(ctx context.Context, bin string) ([]Device, error) {
	return listDevices(ctx, bin)
}
