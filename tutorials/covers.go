package main

import (
	_ "embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

//go:embed cover.html
var coverHTML string

// cover is a tutorial's YouTube thumbnail: text on the left, a picture on
// the right. The picture is the meeting pane when the caption containing
// at is said, or else file: a file the take left in its working directory
// (an image, or a video read at 3 s). A tag shows the picture whole, as
// a screen labelled with the tag.
type cover struct {
	name, badge, title, sub, chip, accent string
	at, file, tag                         string
}

var covers = []cover{
	{name: "publish", badge: "TUTORIAL 1/4", title: "Publish <em>a video</em>", sub: "into a Jitsi meeting, from the command line",
		chip: "join --publish", accent: "#ffcf4a", at: "Bunny is now in the meeting"},
	{name: "record", badge: "TUTORIAL 2/4", title: "Record <em>Jitsi</em>", sub: "one file per participant, or the whole room",
		chip: "join --record", accent: "#ff6b6b", file: "bunny-file.mp4", tag: "● REC  Bunny.mkv"},
	{name: "whip", badge: "TUTORIAL 3/4", title: "OBS <em>→</em> Jitsi", sub: "stream OBS into any meeting: the WHIP gateway",
		chip: "serve", accent: "#57d48a", at: "A stream named O B S joins"},
	{name: "feeds", badge: "TUTORIAL 4/4", title: "Jitsi <em>→</em> OBS", sub: "every participant as an SRT feed, in Docker",
		chip: "join --obs", accent: "#c08bff", file: "bunny.png", tag: "srt://127.0.0.1:9001"},
}

// The right pane of the stage, where the meeting is (stage.html).
const paneCrop = "crop=918:766:981:133"

// makeCovers writes out/<name>-cover.jpg for each tutorial already made.
func makeCovers(cfg config, out string, names []string) error {
	dir := filepath.Join(cfg.cache, "covers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/cover.html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, coverHTML)
	})
	mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(http.Dir(dir))))
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	u, err := launcher.New().Bin(cfg.chrome).Headless(true).Launch()
	if err != nil {
		return fmt.Errorf("chrome: %w", err)
	}
	browser := rod.New().ControlURL(u)
	if err := browser.Connect(); err != nil {
		return err
	}
	defer browser.Close()
	page, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return err
	}
	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: 1280, Height: 720, DeviceScaleFactor: 1}); err != nil {
		return err
	}
	if err := page.Navigate("http://" + ln.Addr().String() + "/cover.html"); err != nil {
		return err
	}
	if err := page.WaitLoad(); err != nil {
		return err
	}

	for _, c := range covers {
		if len(names) > 0 && !(len(names) == 1 && names[0] == "all") && !contains(names, c.name) {
			continue
		}
		shot := c.name + ".png"
		var args []string
		switch {
		case c.at != "":
			at, err := cueTime(filepath.Join(out, c.name+".srt"), c.at)
			if err != nil {
				return fmt.Errorf("%s: %w (make the video first)", c.name, err)
			}
			args = []string{"-ss", fmt.Sprintf("%.2f", (at + 2*time.Second).Seconds()), "-i", filepath.Join(out, c.name+".mp4"), "-vf", paneCrop}
		case strings.HasSuffix(c.file, ".mp4"):
			args = []string{"-ss", "3", "-i", filepath.Join(cfg.cache, "work", c.name, c.file)}
		default:
			args = []string{"-i", filepath.Join(cfg.cache, "work", c.name, c.file)}
		}
		args = append(append([]string{"-v", "error", "-y"}, args...), "-frames:v", "1", filepath.Join(dir, shot))
		if b, err := exec.Command(cfg.ffmpeg, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: picture (make the video first): %v\n%s", c.name, err, b)
		}
		data := map[string]any{"badge": c.badge, "title": c.title, "sub": c.sub, "chip": c.chip, "accent": c.accent,
			"tag": c.tag, "card": c.tag != "",
			"shot": fmt.Sprintf("/files/%s?%d", shot, time.Now().UnixNano())}
		if _, err := page.Eval(`(c) => setCover(c)`, data); err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
		png, err := page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
		if err != nil {
			return err
		}
		pngPath := filepath.Join(dir, c.name+"-cover.png")
		if err := os.WriteFile(pngPath, png, 0o644); err != nil {
			return err
		}
		// YouTube takes JPEG or PNG up to 2 MB.
		jpg := filepath.Join(out, c.name+"-cover.jpg")
		if b, err := exec.Command(cfg.ffmpeg, "-v", "error", "-y", "-i", pngPath, "-q:v", "2", jpg).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: jpeg: %v\n%s", c.name, err, b)
		}
		fmt.Println("  ", jpg)
	}
	return nil
}

var srtStart = regexp.MustCompile(`^(\d+):(\d+):(\d+),(\d+) -->`)

// cueTime returns when the first caption containing text starts.
func cueTime(srtPath, text string) (time.Duration, error) {
	b, err := os.ReadFile(srtPath)
	if err != nil {
		return 0, err
	}
	var start time.Duration
	for _, line := range strings.Split(string(b), "\n") {
		if m := srtStart.FindStringSubmatch(line); m != nil {
			var v [4]int
			for i := range v {
				v[i], _ = strconv.Atoi(m[i+1])
			}
			start = time.Duration(v[0])*time.Hour + time.Duration(v[1])*time.Minute +
				time.Duration(v[2])*time.Second + time.Duration(v[3])*time.Millisecond
		} else if strings.Contains(line, text) {
			return start, nil
		}
	}
	return 0, fmt.Errorf("no caption with %q", text)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
