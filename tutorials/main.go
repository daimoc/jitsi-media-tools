// Command tutorials records the video tutorials of jitsi-media-tools: each
// is one take of a headless Chrome page with a real terminal and the
// meeting as a participant sees it, narrated by Piper. See README.md.
//
//	(cd tutorials && go run . all)
package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	root   string // The repository
	chrome string
	ffmpeg string
	jitsi  string // Base URL of the Jitsi server
	binDir string // Holds jitsi-media-tools and ffmpeg for the shell
	cache  string
	tts    tts
}

// tutorial is one video: prepare runs off camera, then run is recorded.
type tutorial struct {
	name, title string
	prepare     func(s *Stage)
	run         func(s *Stage)
}

var tutorials = []tutorial{publishTutorial, recordTutorial, whipTutorial, feedsTutorial}

func main() {
	jitsi := flag.String("jitsi", "https://localhost:8443", "Jitsi server the tutorials use")
	out := flag.String("out", "out", "directory for the videos")
	chrome := flag.String("chrome", "google-chrome", "Chrome or Chromium executable")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: go run . [flags] all | NAME...\n\ntutorials:\n")
		for _, t := range tutorials {
			fmt.Fprintf(flag.CommandLine.Output(), "  %-10s %s\n", t.name, t.title)
		}
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	cfg, err := setup(*jitsi, *chrome)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tutorials:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	failed := false
	for _, t := range selected(flag.Args()) {
		start := time.Now()
		fmt.Printf("== %s: %s\n", t.name, t.title)
		if err := record(cfg, t, filepath.Join(*out, t.name+".mp4")); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", t.name, err)
			failed = true
			continue
		}
		fmt.Printf("   %s (%s to make)\n", filepath.Join(*out, t.name+".mp4"), time.Since(start).Round(time.Second))
	}
	if failed {
		os.Exit(1)
	}
}

func selected(names []string) []tutorial {
	if len(names) == 1 && names[0] == "all" {
		return tutorials
	}
	var ts []tutorial
	for _, n := range names {
		found := false
		for _, t := range tutorials {
			if t.name == n {
				ts, found = append(ts, t), true
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "no tutorial %q\n", n)
			os.Exit(2)
		}
	}
	return ts
}

// setup finds the tools, builds jitsi-media-tools and checks the server.
func setup(jitsi, chrome string) (config, error) {
	root, err := filepath.Abs("..")
	if err != nil {
		return config{}, err
	}
	if b, err := os.ReadFile(filepath.Join(root, "go.mod")); err != nil || !strings.Contains(string(b), "module github.com/daimoc/jitsi-media-tools\n") {
		return config{}, errors.New("run from the tutorials directory of the repository")
	}
	cache := filepath.Join(root, "tutorials", ".cache")
	cfg := config{root: root, jitsi: strings.TrimSuffix(jitsi, "/"), cache: cache, binDir: filepath.Join(cache, "bin")}
	for _, d := range []string{cfg.binDir, filepath.Join(cache, "tts")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return cfg, err
		}
	}
	if cfg.chrome, err = exec.LookPath(chrome); err != nil {
		return cfg, fmt.Errorf("%s not found: install Chrome or pass -chrome", chrome)
	}
	cfg.ffmpeg = filepath.Join(root, ".tools", "ffmpeg", "bin", "ffmpeg")
	if _, err := os.Stat(cfg.ffmpeg); err != nil {
		if cfg.ffmpeg, err = exec.LookPath("ffmpeg"); err != nil {
			return cfg, errors.New("ffmpeg not found: run ./scripts/get-ffmpeg.sh")
		}
	}
	// The shell finds the same ffmpeg (8 or newer, for -f whip), and its ffprobe.
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		os.Remove(filepath.Join(cfg.binDir, tool))
		if err := os.Symlink(filepath.Join(filepath.Dir(cfg.ffmpeg), tool), filepath.Join(cfg.binDir, tool)); err != nil {
			return cfg, err
		}
	}
	cfg.tts = tts{piper: filepath.Join(root, ".tools", "piper", "piper", "piper"),
		model: filepath.Join(root, ".tools", "piper", "en_GB-cori-medium.onnx"), cache: filepath.Join(cache, "tts")}
	if _, err := os.Stat(cfg.tts.model); err != nil {
		return cfg, errors.New("Piper not found: run ./scripts/get-piper.sh")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return cfg, errors.New("docker not found: the WHIP and feeds tutorials use it")
	}
	build := exec.Command("go", "build", "-o", filepath.Join(cfg.binDir, "jitsi-media-tools"), "./cmd/jitsi-media-tools")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		return cfg, fmt.Errorf("building jitsi-media-tools: %v\n%s", err, out)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := client.Get(cfg.jitsi + "/config.js")
	if err != nil {
		return cfg, fmt.Errorf("Jitsi at %s: %v (start tests/platforms/jitsi)", cfg.jitsi, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return cfg, nil
}

// record makes one tutorial's video.
func record(cfg config, t tutorial, out string) (err error) {
	work := filepath.Join(cfg.cache, "work", t.name)
	os.RemoveAll(work)
	if err := os.MkdirAll(filepath.Join(work, "samples"), 0o755); err != nil {
		return err
	}
	clip, err := os.ReadFile(filepath.Join(cfg.root, "samples", "clip.mp4"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(work, "samples", "clip.mp4"), clip, 0o644); err != nil {
		return err
	}
	logf := func(f string, a ...any) { fmt.Fprintf(os.Stderr, "   "+f+"\n", a...) }
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	s, err := newStage(cfg, t.title, work, logf)
	if s != nil {
		defer s.Close()
	}
	if err != nil {
		return err
	}
	if t.prepare != nil {
		t.prepare(s)
	}
	s.Type("clear")
	s.WaitFor("$ ", 5*time.Second)
	video := filepath.Join(cfg.cache, t.name+".video.mp4")
	if err := s.Record(video); err != nil {
		return err
	}
	t.run(s)
	length, err := s.StopRecording()
	if err != nil {
		return fmt.Errorf("video: %w", err)
	}
	return finish(cfg.ffmpeg, video, out, length, s.cues)
}
