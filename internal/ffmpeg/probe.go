// Package ffmpeg runs ffmpeg and ffplay as the session's media engine
// (constitution IV): locating and probing them, starting and stopping their
// processes, wiring their streams, and building their arguments. It knows
// nothing of signaling.
package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// MinMajorVersion is the oldest ffmpeg release the session's arguments
// support (-fps_mode, current libsrt options).
const MinMajorVersion = 6

// Locate returns the executable to run for name ("ffmpeg" or "ffplay"):
// path itself when it is set (a path or a name looked up in PATH), name in
// PATH otherwise. The error says what is missing and how to give it.
func Locate(name, path string) (string, error) {
	if path == "" {
		path = name
	}
	found, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("%s not found (%s): install it or give its path: %w", name, path, err)
	}
	return found, nil
}

// Caps is what an ffmpeg build supports.
type Caps struct {
	Version  string // As printed, e.g. "6.1.1-3ubuntu5"
	Major    int    // 0 when the version is not numeric (git builds): assumed recent
	Encoders map[string]bool
	Decoders map[string]bool
	Protocol map[string]bool // Input and output protocols
	Devices  map[string]bool // Device formats (v4l2, pulse, dshow, avfoundation, ...)
}

// Needs a session may have, as names in Caps.Missing.
const (
	NeedVP8Encoder  = "encoder:libvpx"
	NeedOpusEncoder = "encoder:libopus"
	NeedH264Encoder = "encoder:libx264"
	NeedAACEncoder  = "encoder:aac"
	NeedSRT         = "protocol:srt"
	NeedRTMP        = "protocol:rtmp"
	NeedTCP         = "protocol:tcp"
)

// Missing returns an error naming every need the build lacks (needs are
// "encoder:<name>", "decoder:<name>", "protocol:<name>" or "device:<name>"),
// or an error if the version is too old; nil when everything is there.
func (c *Caps) Missing(needs ...string) error {
	var missing []string
	if c.Major != 0 && c.Major < MinMajorVersion {
		missing = append(missing, fmt.Sprintf("ffmpeg %d or newer (found %s)", MinMajorVersion, c.Version))
	}
	for _, n := range needs {
		kind, name, _ := strings.Cut(n, ":")
		var set map[string]bool
		switch kind {
		case "encoder":
			set = c.Encoders
		case "decoder":
			set = c.Decoders
		case "protocol":
			set = c.Protocol
		case "device":
			set = c.Devices
		}
		if !set[name] {
			missing = append(missing, kind+" "+name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("ffmpeg lacks: %s", strings.Join(missing, ", "))
}

var (
	probeMu    sync.Mutex
	probeCache = map[string]*Caps{}
)

// Probe runs bin once per listing and returns its capabilities; results are
// kept per executable for the process.
func Probe(ctx context.Context, bin string) (*Caps, error) {
	probeMu.Lock()
	defer probeMu.Unlock()
	if c := probeCache[bin]; c != nil {
		return c, nil
	}
	run := func(arg string) (string, error) {
		out, err := exec.CommandContext(ctx, bin, "-hide_banner", arg).Output()
		if err != nil {
			return "", fmt.Errorf("running %s %s: %w", bin, arg, err)
		}
		return string(out), nil
	}
	version, err := exec.CommandContext(ctx, bin, "-version").Output()
	if err != nil {
		return nil, fmt.Errorf("running %s -version: %w", bin, err)
	}
	outs := map[string]string{}
	for _, arg := range []string{"-encoders", "-decoders", "-protocols", "-devices"} {
		if outs[arg], err = run(arg); err != nil {
			return nil, err
		}
	}
	c, err := parseCaps(string(version), outs["-encoders"], outs["-decoders"], outs["-protocols"], outs["-devices"])
	if err != nil {
		return nil, err
	}
	probeCache[bin] = c
	return c, nil
}

var versionRE = regexp.MustCompile(`^ffmpeg version (\S+)`)

func parseCaps(version, encoders, decoders, protocols, devices string) (*Caps, error) {
	m := versionRE.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return nil, errors.New("not an ffmpeg executable: no version line")
	}
	c := &Caps{Version: m[1]}
	digits := strings.TrimLeft(m[1], "n") // "n6.1" tags of git release builds
	if i := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }); i > 0 {
		c.Major, _ = strconv.Atoi(digits[:i])
	} else if i < 0 {
		c.Major, _ = strconv.Atoi(digits)
	}
	c.Encoders = parseCodecList(encoders)
	c.Decoders = parseCodecList(decoders)
	c.Protocol = parseProtocols(protocols)
	c.Devices = parseDevices(devices)
	return c, nil
}

// parseCodecList reads "-encoders"/"-decoders": after the " ------" line,
// " V....D name description" lines.
func parseCodecList(s string) map[string]bool {
	set := map[string]bool{}
	started := false
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "---") {
			started = true
			continue
		}
		if f := strings.Fields(line); started && len(f) >= 2 {
			set[f[1]] = true
		}
	}
	return set
}

// parseProtocols reads "-protocols": names under "Input:" and "Output:".
func parseProtocols(s string) map[string]bool {
	set := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, " ") {
			set[strings.TrimSpace(line)] = true
		}
	}
	return set
}

// parseDevices reads "-devices": after the " ---" line, " DE name desc".
func parseDevices(s string) map[string]bool {
	set := map[string]bool{}
	started := false
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "---") {
			started = true
			continue
		}
		if !started || len(line) < 4 {
			continue
		}
		if f := strings.Fields(line[3:]); len(f) > 0 {
			for _, name := range strings.Split(f[0], ",") { // "video4linux2,v4l2"
				set[name] = true
			}
		}
	}
	return set
}
