package jmt

import (
	"context"
	"runtime"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
)

// Device is a capture device.
type Device struct {
	Kind        string // "video" or "audio"
	Name        string // What DeviceSource takes
	Description string
}

// ListDevices returns the machine's cameras and microphones. ffmpeg is the
// executable to ask on macOS and Windows ("" finds it in PATH).
func ListDevices(ctx context.Context, ffmpegPath string) ([]Device, error) {
	bin := ""
	if runtime.GOOS != "linux" { // Linux reads sysfs and pactl
		var err error
		if bin, err = ffmpeg.Locate("ffmpeg", ffmpegPath); err != nil {
			return nil, err
		}
	}
	devs, err := ffmpeg.ListDevices(ctx, bin)
	if err != nil {
		return nil, err
	}
	out := make([]Device, len(devs))
	for i, d := range devs {
		out[i] = Device{Kind: d.Kind, Name: d.Name, Description: d.Description}
	}
	return out, nil
}
