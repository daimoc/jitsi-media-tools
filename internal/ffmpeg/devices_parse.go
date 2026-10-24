package ffmpeg

import (
	"bufio"
	"regexp"
	"strings"
)

// Device is a capture device.
type Device struct {
	Kind        string // "video" or "audio"
	Name        string // What the user gives
	Description string
	Input       string // What ffmpeg's input takes (a path on Linux, the name elsewhere)
}

// parsePactlSources reads "pactl list short sources", leaving out the
// monitors of outputs.
func parsePactlSources(s string) []Device {
	var out []Device
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 2 || strings.HasSuffix(f[1], ".monitor") {
			continue
		}
		out = append(out, Device{Kind: "audio", Name: f[1], Input: f[1]})
	}
	return out
}

var (
	avfSection = regexp.MustCompile(`AVFoundation (video|audio) devices:`)
	avfDevice  = regexp.MustCompile(`\] \[(\d+)\] (.+)$`)
)

// parseAVFoundation reads "ffmpeg -f avfoundation -list_devices true -i ”".
func parseAVFoundation(s string) []Device {
	var out []Device
	kind := ""
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := sc.Text()
		if m := avfSection.FindStringSubmatch(line); m != nil {
			kind = m[1]
			continue
		}
		if m := avfDevice.FindStringSubmatch(line); m != nil && kind != "" {
			name := strings.TrimSpace(m[2])
			if strings.HasPrefix(name, "Capture screen") {
				continue // Screens are not cameras
			}
			out = append(out, Device{Kind: kind, Name: name, Input: name})
		}
	}
	return out
}

var dshowDevice = regexp.MustCompile(`\] "(.+)" \((video|audio|none)\)$`)

// parseDShow reads "ffmpeg -f dshow -list_devices true -i dummy" (ffmpeg 5+
// format). Devices of type "none" (virtual cameras ffmpeg cannot classify)
// are listed as video.
func parseDShow(s string) []Device {
	var out []Device
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		m := dshowDevice.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		kind := m[2]
		if kind == "none" {
			kind = "video"
		}
		out = append(out, Device{Kind: kind, Name: m[1], Input: m[1]})
	}
	return out
}
