package ffmpeg

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseDevices_Recorded(t *testing.T) {
	pactl := parsePactlSources(readTestdata(t, "devices/linux-pactl.txt"))
	if len(pactl) != 2 || pactl[0].Name != "alsa_input.pci-0000_00_1f.3.analog-stereo" || pactl[1].Kind != "audio" {
		t.Errorf("pactl %+v", pactl)
	}
	avf := parseAVFoundation(readTestdata(t, "devices/darwin-avfoundation.txt"))
	want := []Device{
		{Kind: "video", Name: "FaceTime HD Camera", Input: "FaceTime HD Camera"},
		{Kind: "video", Name: "OBS Virtual Camera", Input: "OBS Virtual Camera"},
		{Kind: "audio", Name: "MacBook Pro Microphone", Input: "MacBook Pro Microphone"},
		{Kind: "audio", Name: "ZoomAudioDevice", Input: "ZoomAudioDevice"},
	}
	if !reflect.DeepEqual(avf, want) {
		t.Errorf("avfoundation %+v", avf)
	}
	ds := parseDShow(readTestdata(t, "devices/windows-dshow.txt"))
	want = []Device{
		{Kind: "video", Name: "Integrated Camera", Input: "Integrated Camera"},
		{Kind: "video", Name: "OBS Virtual Camera", Input: "OBS Virtual Camera"},
		{Kind: "audio", Name: "Microphone Array (Realtek(R) Audio)", Input: "Microphone Array (Realtek(R) Audio)"},
	}
	if !reflect.DeepEqual(ds, want) {
		t.Errorf("dshow %+v", ds)
	}
}

func TestFindDevice(t *testing.T) {
	devs := []Device{{Kind: "video", Name: "Cam", Input: "/dev/video0"}, {Kind: "audio", Name: "Mic", Input: "mic0"}}
	if d, err := FindDevice(devs, "video", "cam"); err != nil || d.Input != "/dev/video0" {
		t.Errorf("by name: %+v %v", d, err)
	}
	if d, err := FindDevice(devs, "video", "/dev/video0"); err != nil || d.Name != "Cam" {
		t.Errorf("by input: %+v %v", d, err)
	}
	_, err := FindDevice(devs, "video", "Nope")
	if err == nil || !strings.Contains(err.Error(), `available: "Cam"`) {
		t.Errorf("unknown device error %v", err)
	}
}
