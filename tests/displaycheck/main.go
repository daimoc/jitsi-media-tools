// Command displaycheck measures display latency by eye. A participant
// "Clock" publishes frames showing the time each was written; a "Viewer"
// shows the room in the ffplay window; the terminal prints the time now.
// A screenshot of both gives the latency from frame to screen: terminal
// time minus the time in the window.
//
//	go run ./tests/displaycheck --url https://localhost:8443/jmt-display --insecure
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/daimoc/jitsi-media-tools/jmt"
)

const (
	width, height = 1280, 720
	scale         = 8 // Glyph pixels per font pixel
	timeLayout    = "15:04:05.000"
)

func main() {
	url := flag.String("url", "", "meeting URL (required)")
	insecure := flag.Bool("insecure", false, "skip TLS certificate verification")
	debug := flag.Bool("debug", false, "log the sessions")
	flag.Parse()
	if *url == "" {
		fmt.Fprintln(os.Stderr, "--url is required")
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if *debug {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *url, *insecure, log); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, url string, insecure bool, log *slog.Logger) error {
	viewer, err := jmt.Join(ctx, jmt.Options{
		MeetingURL: url, DisplayName: "Viewer", Insecure: insecure, Logger: log,
		Display:   &jmt.DisplayOptions{Title: "displaycheck"},
		Selection: jmt.Selection{Height: 720},
	})
	if err != nil {
		return fmt.Errorf("viewer: %w", err)
	}
	defer viewer.Close()

	src, source := jmt.FrameSource(width, height, false, true)
	clock, err := jmt.Join(ctx, jmt.Options{
		MeetingURL: url, DisplayName: "Clock", Insecure: insecure, Logger: log,
		Publish: source,
	})
	if err != nil {
		return fmt.Errorf("clock: %w", err)
	}
	defer clock.Close()

	fmt.Fprintln(os.Stderr, "Clock publishes the time each frame is written; the window shows it.")
	fmt.Fprintln(os.Stderr, "Screenshot the window and this terminal: latency = now - window. Ctrl+C to stop.")

	frames := time.NewTicker(time.Second / 30)
	defer frames.Stop()
	now := time.NewTicker(10 * time.Millisecond)
	defer now.Stop()
	f := jmt.VideoFrame{Width: width, Height: height,
		Y: make([]byte, width*height), U: fill(width*height/4, 128), V: fill(width*height/4, 128)}
	for {
		select {
		case <-ctx.Done():
			fmt.Println()
			return nil
		case <-viewer.Done():
			return viewer.Wait()
		case <-clock.Done():
			return clock.Wait()
		case t := <-now.C:
			fmt.Printf("\rnow  %s", t.Format(timeLayout))
		case <-frames.C:
			draw(f.Y, time.Now().Format(timeLayout))
			if err := src.WriteVideo(f); err != nil {
				return err
			}
		}
	}
}

// draw writes text in white on black, scaled up and centered, into a luma
// plane.
func draw(y []byte, text string) {
	face := basicfont.Face7x13
	small := image.NewGray(image.Rect(0, 0, face.Advance*len(text), face.Height))
	d := font.Drawer{Dst: small, Src: image.NewUniform(color.Gray{255}), Face: face,
		Dot: fixed.P(0, face.Ascent)}
	d.DrawString(text)
	for i := range y {
		y[i] = 16
	}
	b := small.Bounds()
	x0, y0 := (width-b.Dx()*scale)/2, (height-b.Dy()*scale)/2
	for sy := 0; sy < b.Dy(); sy++ {
		for sx := 0; sx < b.Dx(); sx++ {
			if small.GrayAt(sx, sy).Y < 128 {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				row := (y0+sy*scale+dy)*width + x0 + sx*scale
				for dx := 0; dx < scale; dx++ {
					y[row+dx] = 235
				}
			}
		}
	}
}

func fill(n int, v byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = v
	}
	return b
}
