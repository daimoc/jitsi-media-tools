package session

import (
	"fmt"
	"os"
)

// Input is a publish source as ffmpeg reads it; built by the constructors
// below.
type Input struct {
	Kind string   // "file", "devices", "frames", "forward"
	Args []string // ffmpeg input arguments (with -i)
	Path string   // File inputs: checked before joining
	Loop bool     // File inputs: play again at the end
	// Frames is what a program writes (frames inputs); the encoder's raw
	// inputs are opened per run.
	Frames *Frames
	// Forwarded is an encoded source sent as it comes, without ffmpeg
	// (forward inputs, research R14).
	Forwarded *Forwarded
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
	if in.Kind == "forward" && in.Forwarded == nil {
		return fmt.Errorf("forward input without a source")
	}
	if in.Kind == "frames" {
		f := in.Frames
		if f == nil || !f.Audio && !f.Video {
			return fmt.Errorf("frame source publishes nothing")
		}
		if f.Video && (f.W <= 0 || f.H <= 0) {
			return fmt.Errorf("frame source size %dx%d", f.W, f.H)
		}
	}
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
