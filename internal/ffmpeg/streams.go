package ffmpeg

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Streams tells which kinds of stream a media file has.
type Streams struct {
	Audio, Video bool
}

// streamLine matches ffmpeg's description of an input stream, e.g.
// "  Stream #0:1[0x2](und): Audio: aac (LC) ...".
var streamLine = regexp.MustCompile(`^\s*Stream #0:\d+\S*: (Audio|Video):`)

// parseStreams reads ffmpeg's input description (stderr of "ffmpeg -i").
// Attached pictures (cover art) are not video.
func parseStreams(stderr string) Streams {
	var s Streams
	for _, line := range strings.Split(stderr, "\n") {
		m := streamLine.FindStringSubmatch(line)
		switch {
		case m == nil:
		case m[1] == "Audio":
			s.Audio = true
		case !strings.Contains(line, "(attached pic)"):
			s.Video = true
		}
	}
	return s
}

// FileStreams asks ffmpeg which streams the file at path has.
func FileStreams(ctx context.Context, bin, path string) (Streams, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// "ffmpeg -i file" without an output describes the input and exits 1.
	p, err := Start(ctx, Spec{Bin: bin, Name: "probe-file", Args: []string{"-hide_banner", "-nostdin", "-i", path}, Stdout: true, KeepStderr: true})
	if err != nil {
		return Streams{}, err
	}
	go io.Copy(io.Discard, p.Stdout)
	<-p.Done()
	if ctx.Err() != nil {
		return Streams{}, fmt.Errorf("reading %s: %w", path, ctx.Err())
	}
	s := parseStreams(p.stderrAll())
	if !s.Audio && !s.Video {
		return s, fmt.Errorf("%s: no audio or video stream%s", path, p.stderrTail())
	}
	return s, nil
}
