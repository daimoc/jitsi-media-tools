package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/daimoc/jitsi-media-tools/jmt"
)

// joinFlags are the join subcommand's flags (contracts/cli.md).
type joinFlags struct {
	opts     jmt.Options
	logLevel string
}

// stringList is a repeatable flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// parseJoin maps join's arguments to session options. getenv reads the
// secrets that may come from the environment.
func parseJoin(args []string, getenv func(string) string, stderr io.Writer) (*joinFlags, error) {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		f            joinFlags
		o            = &f.opts
		publish      string
		publishSize  string
		display      bool
		recordDir    string
		recordMode   string
		obs, obsGrid bool
		obsHost      string
		obsPort      int
		obsMax       int
		pins         stringList
		ttl          time.Duration
	)
	fs.StringVar(&o.MeetingURL, "url", "", "meeting URL, e.g. https://meet.example.org/Room (required)")
	fs.StringVar(&o.DisplayName, "name", "jitsi-media-tools", "display name in the room")
	fs.StringVar(&o.XMPPDomain, "domain", "", "XMPP domain (default from config.js)")
	fs.StringVar(&o.MUCDomain, "muc", "", "MUC domain (default from config.js)")
	fs.StringVar(&o.AnonymousDomain, "anonymous-domain", "", "guest XMPP domain (default from config.js)")
	fs.StringVar(&o.WebSocketURL, "ws", "", "XMPP WebSocket URL (default from config.js)")
	fs.StringVar(&o.FocusJID, "focus", "", "focus JID (default focus.<domain>)")
	fs.BoolVar(&o.Insecure, "insecure", false, "skip TLS certificate verification")
	fs.StringVar(&o.Token, "jwt", getenv("JITSI_JWT"), "JWT for token authentication (or $JITSI_JWT)")
	fs.StringVar(&o.AppID, "jwt-app-id", "", "sign a JWT with this app id and $JITSI_APP_SECRET")
	fs.StringVar(&o.Audience, "jwt-audience", "", "JWT audience (default jitsi)")
	fs.StringVar(&o.Subject, "jwt-subject", "", "JWT subject (default the XMPP domain)")
	fs.DurationVar(&ttl, "jwt-ttl", 0, "signed JWT lifetime (default 1h)")
	fs.BoolVar(&o.Moderator, "jwt-moderator", false, "sign the JWT as a moderator")
	fs.DurationVar(&o.WaitHost, "wait-host", 5*time.Minute, "how long a guest waits for a host")
	fs.StringVar(&publish, "publish", "", "publish source: file:PATH, file-loop:PATH, device:VIDEO,AUDIO (OBS: use serve)")
	fs.StringVar(&publishSize, "publish-size", "1280x720", "published video size WxH")
	fs.BoolVar(&display, "display", false, "show the room in a tile-grid window (needs ffplay)")
	fs.StringVar(&recordDir, "record", "", "record into this directory")
	fs.StringVar(&recordMode, "record-mode", "per-participant", "per-participant or mixed")
	fs.BoolVar(&obs, "obs", false, "serve SRT feeds for OBS")
	fs.StringVar(&obsHost, "obs-host", "0.0.0.0", "address the OBS feeds listen on")
	fs.IntVar(&obsPort, "obs-port", 9000, "first OBS feed port (the grid)")
	fs.IntVar(&obsMax, "obs-max-feeds", 16, "maximum participant feeds")
	fs.BoolVar(&obsGrid, "obs-grid", false, "also serve the grid as a feed")
	fs.IntVar(&o.Selection.Limit, "video-limit", 9, "videos received at once (1-25)")
	fs.IntVar(&o.Selection.Height, "video-height", 180, "received video height: 180, 360 or 720")
	fs.Var(&pins, "pin", "always receive this participant's video (repeatable)")
	fs.IntVar(&o.Selection.PinHeight, "pin-height", 720, "pinned video height: 180, 360 or 720")
	fs.StringVar(&o.FFmpeg, "ffmpeg", "", "ffmpeg executable (default from PATH)")
	fs.StringVar(&o.FFplay, "ffplay", "", "ffplay executable (default from PATH)")
	fs.StringVar(&f.logLevel, "log-level", "info", "debug, info or warn")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if o.MeetingURL == "" {
		return nil, errors.New("--url is required")
	}
	o.TokenTTL = ttl
	o.Selection.Pins = pins
	if o.AppID != "" {
		if o.AppSecret = getenv("JITSI_APP_SECRET"); o.AppSecret == "" {
			return nil, errors.New("--jwt-app-id needs $JITSI_APP_SECRET")
		}
	}
	w, h, err := parseSize(publishSize)
	if err != nil {
		return nil, err
	}
	if publish != "" {
		if o.Publish, err = parsePublish(publish, w, h); err != nil {
			return nil, err
		}
	}
	if display {
		o.Display = &jmt.DisplayOptions{Title: o.MeetingURL}
	}
	if recordDir != "" {
		mode := jmt.RecordPerParticipant
		switch recordMode {
		case "per-participant":
		case "mixed":
			mode = jmt.RecordMixed
		default:
			return nil, fmt.Errorf("--record-mode %q: want per-participant or mixed", recordMode)
		}
		o.Record = &jmt.RecordOptions{Dir: recordDir, Mode: mode}
	}
	if obs || obsGrid {
		o.OBS = &jmt.OBSOptions{Host: obsHost, BasePort: obsPort, MaxFeeds: obsMax, Grid: obsGrid}
	}
	return &f, nil
}

func parseSize(s string) (int, int, error) {
	ws, hs, ok := strings.Cut(s, "x")
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if !ok || err1 != nil || err2 != nil || w <= 0 || h <= 0 || w%2 != 0 || h%2 != 0 {
		return 0, 0, fmt.Errorf("size %q: want WxH with even numbers, e.g. 1280x720", s)
	}
	return w, h, nil
}

// parsePublish maps --publish to a source.
func parsePublish(s string, w, h int) (jmt.Source, error) {
	kind, arg, _ := strings.Cut(s, ":")
	var src jmt.Source
	switch kind {
	case "file", "file-loop":
		if arg == "" {
			return nil, fmt.Errorf("--publish %s: needs a path", kind)
		}
		src = jmt.FileSource(arg, kind == "file-loop")
	default:
		if strings.HasPrefix(kind, "obs") {
			return nil, fmt.Errorf("--publish %q: OBS streams in through the WHIP gateway, jitsi-media-tools serve", s)
		}
		return nil, fmt.Errorf("--publish %q: want file:PATH, file-loop:PATH or device:VIDEO,AUDIO", s)
	}
	return jmt.WithSize(src, w, h), nil
}
