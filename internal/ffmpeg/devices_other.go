//go:build !linux && !darwin && !windows

package ffmpeg

import (
	"context"
	"errors"
)

func listDevices(context.Context, string) ([]Device, error) {
	return nil, errors.New("capture devices are supported on Linux, macOS and Windows")
}

func cameraFormat(context.Context, string, string) string { return "" }

func deviceInput(_, _ *Device, _, _ int) ([]string, string, string) { return nil, "", "" }
