package jmt

import (
	"context"
	"errors"
)

// Device is a capture device.
type Device struct {
	Kind        string // "video" or "audio"
	Name        string // What DeviceSource takes
	Description string
}

// ListDevices returns the machine's cameras and microphones.
func ListDevices(ctx context.Context, ffmpeg string) ([]Device, error) {
	return nil, errors.New("device listing is not available yet")
}
