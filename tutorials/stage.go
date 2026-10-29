package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

//go:embed stage.html
var stageHTML string

// Terminal size, to fit the left pane at 21 px DejaVu Sans Mono.
const termCols, termRows = 72, 31

// The Viewer that shows the meeting: no prejoin screen, muted, no camera.
const viewerHash = "#config.prejoinConfig.enabled=false&config.startWithAudioMuted=true" +
	"&config.startWithVideoMuted=true&config.disableDeepLinking=true&userInfo.displayName=%22Viewer%22"

// cue is one narration line, at its time in the video.
type cue struct {
	start, end time.Duration
	text       string
	wav        string
}

// Stage is one take: a headless Chrome page with a terminal (a real bash
// in a PTY) and a right pane (the meeting, a card, an image or a video),
// recorded by screencast, and the narration said over it.
type Stage struct {
	cfg    config
	dir    string // The shell's working directory, served at /files/
	logf   func(string, ...any)
	chrome *rod.Browser
	page   *rod.Page
	srv    *http.Server
	base   string // http://127.0.0.1:port

	ptmx  *os.File
	shell *exec.Cmd
	evals chan string // Base64 PTY output for the page, in order

	outMu  sync.Mutex
	output strings.Builder // Everything the terminal printed, ANSI codes removed
	mark   int             // WaitFor looks after this offset

	recMu   sync.Mutex
	ffmpeg  *exec.Cmd
	frames  io.WriteCloser
	last    []byte    // Last frame, written again while the page does not change
	t0      time.Time // First frame written: time zero of the video
	stopRec chan struct{}
	recDone chan struct{}

	cues    []cue
	bg      []*exec.Cmd // Off-camera processes
	atClose []func()
	step    int
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\r`)

func newStage(cfg config, title, workDir string, logf func(string, ...any)) (*Stage, error) {
	s := &Stage{cfg: cfg, dir: workDir, logf: logf, evals: make(chan string, 256)}
	if err := s.serve(); err != nil {
		return nil, err
	}
	u, err := launcher.New().Bin(cfg.chrome).Headless(true).
		Set("ignore-certificate-errors").
		Set("use-fake-ui-for-media-stream").
		Set("use-fake-device-for-media-stream").
		Set("autoplay-policy", "no-user-gesture-required").
		Set("window-size", "1920,1080").
		Launch()
	if err != nil {
		return s, fmt.Errorf("chrome: %w", err)
	}
	s.chrome = rod.New().ControlURL(u)
	if err := s.chrome.Connect(); err != nil {
		return s, err
	}
	if s.page, err = s.chrome.Page(proto.TargetCreateTarget{}); err != nil {
		return s, err
	}
	if err := s.page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: 1920, Height: 1080, DeviceScaleFactor: 1}); err != nil {
		return s, err
	}
	if err := s.page.Navigate(s.base + "/stage.html"); err != nil {
		return s, err
	}
	if err := s.page.WaitLoad(); err != nil {
		return s, err
	}
	if _, err := s.page.Timeout(30 * time.Second).Eval(`() => new Promise(r => { const t = () => window.stageReady ? r() : setTimeout(t, 50); t(); })`); err != nil {
		return s, fmt.Errorf("stage page: %w", err)
	}
	s.js(`setText`, "title", title)
	if err := s.startShell(); err != nil {
		return s, err
	}
	return s, nil
}

// serve serves the stage page and the working directory.
func (s *Stage) serve() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	page := strings.NewReplacer("COLS", fmt.Sprint(termCols), "ROWS", fmt.Sprint(termRows)).Replace(stageHTML)
	mux := http.NewServeMux()
	mux.HandleFunc("/stage.html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page)
	})
	mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(http.Dir(s.dir))))
	s.srv = &http.Server{Handler: mux}
	go s.srv.Serve(ln)
	s.base = "http://" + ln.Addr().String()
	return nil
}

// js calls a function of the stage page with arguments.
func (s *Stage) js(fn string, args ...any) {
	params := make([]string, len(args))
	for i := range args {
		params[i] = fmt.Sprintf("a%d", i)
	}
	js := fmt.Sprintf("(%s) => %s(%s)", strings.Join(params, ","), fn, strings.Join(params, ","))
	if _, err := s.page.Eval(js, args...); err != nil {
		s.logf("stage %s: %v", fn, err)
	}
}

func (s *Stage) startShell() error {
	sh := exec.Command("bash", "--norc", "--noprofile", "-i")
	sh.Dir = s.dir
	sh.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"PATH="+s.cfg.binDir+":"+os.Getenv("PATH"),
		`PS1=\[\e[1;32m\]$ \[\e[0m\]`,
		"HISTFILE=/dev/null")
	ptmx, err := pty.StartWithSize(sh, &pty.Winsize{Cols: termCols, Rows: termRows})
	if err != nil {
		return err
	}
	s.ptmx, s.shell = ptmx, sh
	go func() { // Page writes, one at a time
		for b := range s.evals {
			s.js("termWrite", b)
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				s.evals <- base64.StdEncoding.EncodeToString(buf[:n])
				s.outMu.Lock()
				s.output.WriteString(ansi.ReplaceAllString(string(buf[:n]), ""))
				s.outMu.Unlock()
			}
			if err != nil {
				close(s.evals)
				return
			}
		}
	}()
	s.WaitFor("$ ", 10*time.Second)
	return nil
}

// Record starts the video. Everything before is set-up, off camera.
func (s *Stage) Record(out string) error {
	s.ffmpeg = exec.Command(s.cfg.ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "image2pipe", "-use_wallclock_as_timestamps", "1", "-c:v", "mjpeg", "-i", "-",
		"-vf", "fps=30,format=yuv420p", "-c:v", "libx264", "-preset", "veryfast", "-crf", "20",
		"-movflags", "+faststart", out)
	s.ffmpeg.Stderr = os.Stderr
	w, err := s.ffmpeg.StdinPipe()
	if err != nil {
		return err
	}
	if err := s.ffmpeg.Start(); err != nil {
		return err
	}
	s.frames, s.stopRec, s.recDone = w, make(chan struct{}), make(chan struct{})
	first := make(chan struct{})
	var once sync.Once
	go s.page.EachEvent(func(e *proto.PageScreencastFrame) {
		_ = proto.PageScreencastFrameAck{SessionID: e.SessionID}.Call(s.page)
		s.writeFrame(e.Data)
		once.Do(func() { close(first) })
	})()
	if err := (proto.PageStartScreencast{Format: proto.PageStartScreencastFormatJpeg, Quality: gson(90),
		MaxWidth: gson(1920), MaxHeight: gson(1080), EveryNthFrame: gson(1)}).Call(s.page); err != nil {
		return err
	}
	// A still page sends no frames: repeat the last one so time goes on.
	go func() {
		defer close(s.recDone)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-s.stopRec:
				return
			case <-t.C:
				s.recMu.Lock()
				last := s.last
				s.recMu.Unlock()
				if last != nil {
					s.writeFrame(last)
				}
			}
		}
	}()
	select {
	case <-first:
	case <-time.After(10 * time.Second):
		return fmt.Errorf("no screencast frame")
	}
	return nil
}

func gson[T any](v T) *T { return &v }

func (s *Stage) writeFrame(b []byte) {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if s.frames == nil {
		return
	}
	if s.t0.IsZero() {
		s.t0 = time.Now()
	}
	s.last = b
	if _, err := s.frames.Write(b); err != nil {
		s.logf("frame: %v", err)
	}
}

// now is the current time in the video.
func (s *Stage) now() time.Duration {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if s.t0.IsZero() {
		return 0
	}
	return time.Since(s.t0)
}

// StopRecording ends the video and returns its length.
func (s *Stage) StopRecording() (time.Duration, error) {
	_ = proto.PageStopScreencast{}.Call(s.page)
	close(s.stopRec)
	<-s.recDone
	length := s.now()
	s.recMu.Lock()
	s.frames.Close()
	s.frames = nil
	s.recMu.Unlock()
	return length, s.ffmpeg.Wait()
}

// Close stops everything the take started.
func (s *Stage) Close() {
	for _, f := range s.atClose {
		f()
	}
	for _, c := range s.bg {
		stopProcess(c)
	}
	if s.shell != nil {
		s.ptmx.Write([]byte("\x03exit\n"))
		done := make(chan struct{})
		go func() { s.shell.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			s.shell.Process.Kill()
		}
		s.ptmx.Close()
	}
	if s.chrome != nil {
		s.chrome.Close()
	}
	if s.srv != nil {
		s.srv.Close()
	}
}

func stopProcess(c *exec.Cmd) {
	if c.Process == nil || c.ProcessState != nil {
		return
	}
	c.Process.Signal(syscall.SIGINT)
	done := make(chan struct{})
	go func() { c.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.Process.Kill()
		<-done
	}
}

// --- Steps ---

// Step shows "n/total name" in the header.
func (s *Stage) Step(total int, name string) {
	s.step++
	s.js("setText", "step", fmt.Sprintf("%d/%d  %s", s.step, total, name))
}

// Say speaks text, shows it as the caption, and returns when it is said.
func (s *Stage) Say(text string) { s.Narrate(text).Wait() }

// Narration is a line being said; Wait returns once it is.
type Narration struct{ done chan struct{} }

func (n Narration) Wait() { <-n.done }

// Narrate starts saying text and returns at once, so the take can act while
// it is said.
func (s *Stage) Narrate(text string) Narration {
	n := Narration{done: make(chan struct{})}
	wav, length, err := s.cfg.tts.say(text)
	if err != nil {
		s.logf("tts: %v", err)
		length = time.Duration(len(text)) * 65 * time.Millisecond
	}
	start := s.now()
	s.cues = append(s.cues, cue{start: start, end: start + length, text: text, wav: wav})
	s.js("setText", "caption", text)
	go func() {
		time.Sleep(length + 350*time.Millisecond)
		close(n.done)
	}()
	return n
}

// Caption clears the caption.
func (s *Stage) ClearCaption() { s.js("setText", "caption", "") }

// Type types cmd at a human pace and presses Enter.
func (s *Stage) Type(cmd string) {
	s.Mark()
	for _, r := range cmd {
		s.ptmx.Write([]byte(string(r)))
		d := 28 + rand.Intn(30)
		if r == ' ' {
			d += 25
		}
		time.Sleep(time.Duration(d) * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	s.ptmx.Write([]byte("\r"))
}

// Run types cmd and waits for the prompt to come back.
func (s *Stage) Run(cmd string, timeout time.Duration) {
	s.Type(cmd)
	s.WaitFor("\n$ ", timeout)
}

// Key sends a control character: "c" for Ctrl+C.
func (s *Stage) Ctrl(key string) {
	s.Mark()
	s.ptmx.Write([]byte{key[0] & 0x1f})
}

// Mark makes WaitFor look only at what is printed from now on.
func (s *Stage) Mark() {
	s.outMu.Lock()
	s.mark = s.output.Len()
	s.outMu.Unlock()
}

// WaitFor waits until the terminal prints text after the mark; the take
// fails if it does not.
func (s *Stage) WaitFor(text string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.outMu.Lock()
		found := strings.Contains(s.output.String()[s.mark:], text)
		s.outMu.Unlock()
		if found {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.outMu.Lock()
	tail := s.output.String()[s.mark:]
	s.outMu.Unlock()
	panic(fmt.Errorf("terminal did not print %q within %v; it printed:\n%s", text, timeout, tail))
}

// Pause holds the picture.
func (s *Stage) Pause(d time.Duration) { time.Sleep(d) }

// Meeting shows the room as a participant sees it.
func (s *Stage) Meeting(room string) {
	s.js("showMeeting", "The meeting "+room+", as a participant sees it", s.cfg.jitsi+"/"+url.PathEscape(room)+viewerHash)
}

// Card shows HTML in the right pane.
func (s *Stage) Card(label, html string) { s.js("showCard", label, html) }

// Video plays a file of the working directory in the right pane.
func (s *Stage) Video(label, file string) { s.js("showVideo", label, "/files/"+file) }

// Image shows a file of the working directory in the right pane.
func (s *Stage) Image(label, file string) {
	s.js("showImage", label, fmt.Sprintf("/files/%s?%d", file, time.Now().UnixNano()))
}

// Background starts a process off camera, in the working directory, until
// the take ends. Its output goes to a log next to the video.
func (s *Stage) Background(name string, args ...string) *exec.Cmd {
	c := exec.Command(s.bin(args[0]), args[1:]...)
	c.Dir = s.dir
	c.Env = append(os.Environ(), "PATH="+s.cfg.binDir+":"+os.Getenv("PATH"))
	if f, err := os.Create(filepath.Join(s.cfg.cache, name+".log")); err == nil {
		c.Stdout, c.Stderr = f, f
	}
	if err := c.Start(); err != nil {
		panic(fmt.Errorf("%s: %w", name, err))
	}
	s.bg = append(s.bg, c)
	return c
}

// bin returns the tutorial's copy of a tool when it has one: exec looks
// commands up in the driver's PATH, not the one given to the child.
func (s *Stage) bin(name string) string {
	p := filepath.Join(s.cfg.binDir, name)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return name
}

// AtClose runs f when the take ends, even when it fails.
func (s *Stage) AtClose(f func()) { s.atClose = append(s.atClose, f) }

// Stop stops a Background process.
func (s *Stage) Stop(c *exec.Cmd) { stopProcess(c) }

// Off runs a command off camera and waits for it.
func (s *Stage) Off(args ...string) {
	c := exec.Command(s.bin(args[0]), args[1:]...)
	c.Dir = s.dir
	c.Env = append(os.Environ(), "PATH="+s.cfg.binDir+":"+os.Getenv("PATH"))
	if out, err := c.CombinedOutput(); err != nil {
		panic(fmt.Errorf("%s: %v\n%s", strings.Join(args, " "), err, out))
	}
}
