// Command jitsi-media-tools joins a Jitsi Meet room to publish and play
// media, or serves a WHIP gateway into meetings. It is a thin layer over
// the jmt package (contracts/cli.md).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/daimoc/jitsi-media-tools/jmt"
)

const usage = `usage:
  jitsi-media-tools join --url URL [--name NAME] [options]   (join --help for options)
  jitsi-media-tools serve [options]                          WHIP gateway; secret in $WHIP_API_SECRET (serve --help)
  jitsi-media-tools devices [--ffmpeg PATH]
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run returns the exit code: 0 after Ctrl+C or a file played once, 1 on a
// session or server error, 2 on bad arguments.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "join":
		f, err := parseJoin(args[1:], os.Getenv, stderr)
		if err != nil {
			if !errors.Is(err, flag.ErrHelp) {
				fmt.Fprintln(stderr, "error:", err)
			}
			return 2
		}
		return join(f, stdout, stderr)
	case "serve":
		f, err := parseServe(args[1:], os.Getenv, stderr)
		if err != nil {
			if !errors.Is(err, flag.ErrHelp) {
				fmt.Fprintln(stderr, "error:", err)
			}
			return 2
		}
		return serve(f, stdout, stderr)
	case "devices":
		fs := flag.NewFlagSet("devices", flag.ContinueOnError)
		fs.SetOutput(stderr)
		bin := fs.String("ffmpeg", "", "ffmpeg executable (default from PATH)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		devices, err := jmt.ListDevices(context.Background(), *bin)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		for _, d := range devices {
			fmt.Fprintf(stdout, "%-6s %q", d.Kind, d.Name)
			if d.Description != "" && d.Description != d.Name {
				fmt.Fprintf(stdout, "  (%s)", d.Description)
			}
			fmt.Fprintln(stdout)
		}
		return 0
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}

func newLogger(level string, w io.Writer) *slog.Logger {
	l := slog.LevelInfo
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: l}))
}

func join(f *joinFlags, stdout, stderr io.Writer) int {
	f.opts.Logger = newLogger(f.logLevel, stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	f.opts.OnWaitingForHost = func() { printEvent(stdout, jmt.Event{Kind: jmt.WaitingForHost}) }
	s, err := jmt.Join(ctx, f.opts)
	if err != nil {
		if ctx.Err() != nil {
			return 0 // Ctrl+C before joining
		}
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(stdout, "joined room %s as %s\n", s.Room(), f.opts.DisplayName)
	for _, l := range s.Listening() {
		fmt.Fprintf(stdout, "%s %s\n", l.Label, l.URL)
	}
	go func() {
		<-ctx.Done()
		s.Close()
	}()
	for e := range s.Events() {
		printEvent(stdout, e)
	}
	if err := s.Wait(); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func printEvent(w io.Writer, e jmt.Event) {
	switch e.Kind {
	case jmt.ParticipantJoined:
		fmt.Fprintf(w, "+ %s\n", e.Participant.Name)
	case jmt.ParticipantLeft:
		fmt.Fprintf(w, "- %s\n", e.Participant.Name)
	case jmt.WaitingForHost:
		fmt.Fprintln(w, "waiting for host…")
	case jmt.FeedReady:
		name := e.Participant.Name
		if name == "" {
			name = "grid"
		}
		fmt.Fprintf(w, "feed %s %s\n", name, e.FeedURL)
	case jmt.RecordingStarted:
		name := e.Participant.Name
		if name == "" {
			name = "mixed"
		}
		fmt.Fprintf(w, "record %s %s\n", name, e.Path)
	}
}
