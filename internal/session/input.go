package session

import (
	"fmt"
	"os"
)

// Input is a publish source as ffmpeg reads it; built by the constructors
// below.
type Input struct {
	Kind string   // "file", "devices", "obs", "frames"
	Args []string // ffmpeg input arguments (with -i)
	Path string   // File inputs: checked before joining
	Loop bool     // File inputs: play again at the end
}

// FileInput reads a media file in real time, once or looped.
func FileInput(path string, loop bool) Input {
	args := []string{"-re"}
	if loop {
		args = append(args, "-stream_loop", "-1")
	}
	return Input{Kind: "file", Path: path, Loop: loop, Args: append(args, "-i", path)}
}

// check fails before joining when the input cannot work.
func (in Input) check() error {
	if in.Kind == "file" {
		info, err := os.Stat(in.Path)
		if err != nil {
			return fmt.Errorf("media file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("media file %s is not a regular file", in.Path)
		}
	}
	return nil
}
