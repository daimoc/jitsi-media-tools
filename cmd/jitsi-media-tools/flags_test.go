package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/daimoc/jitsi-media-tools/jmt"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParseJoin_Maps(t *testing.T) {
	f, err := parseJoin([]string{"--url", "https://localhost:8443/Room", "--name", "Bot", "--insecure",
		"--publish", "file-loop:clip.mp4", "--publish-size", "640x360", "--display",
		"--record", "/tmp/rec", "--record-mode", "mixed", "--obs-grid", "--obs-port", "9100",
		"--pin", "Alice", "--pin", "Bob", "--video-limit", "4", "--jwt-app-id", "app"},
		env(map[string]string{"JITSI_APP_SECRET": "s3cret"}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	o := f.opts
	if o.MeetingURL != "https://localhost:8443/Room" || o.DisplayName != "Bot" || !o.Insecure {
		t.Errorf("basic options %+v", o)
	}
	if o.Publish == nil || o.Display == nil || o.Record == nil || o.Record.Mode != jmt.RecordMixed || o.Record.Dir != "/tmp/rec" {
		t.Errorf("outputs %+v %+v %+v", o.Publish, o.Display, o.Record)
	}
	if o.OBS == nil || !o.OBS.Grid || o.OBS.BasePort != 9100 || o.OBS.MaxFeeds != 16 {
		t.Errorf("obs %+v", o.OBS)
	}
	if strings.Join(o.Selection.Pins, ",") != "Alice,Bob" || o.Selection.Limit != 4 || o.Selection.Height != 180 {
		t.Errorf("selection %+v", o.Selection)
	}
	if o.AppID != "app" || o.AppSecret != "s3cret" {
		t.Errorf("jwt %q %q", o.AppID, o.AppSecret)
	}
}

func TestParseJoin_TokenFromEnv(t *testing.T) {
	f, err := parseJoin([]string{"--url", "https://h/r"}, env(map[string]string{"JITSI_JWT": "tok"}), io.Discard)
	if err != nil || f.opts.Token != "tok" {
		t.Fatalf("token %q err %v", f.opts.Token, err)
	}
}

func TestParseJoin_Errors(t *testing.T) {
	for _, args := range [][]string{
		{},                                   // No --url
		{"--url", "u", "--publish", "foo"},   // Bad source
		{"--url", "u", "--publish", "file:"}, // No path
		{"--url", "u", "--publish-size", "640"},
		{"--url", "u", "--record", "d", "--record-mode", "x"},
		{"--url", "u", "--jwt-app-id", "a"}, // No secret
		{"--url", "u", "extra"},
	} {
		if _, err := parseJoin(args, env(nil), io.Discard); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

func TestRun_ExitCodes(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 2 {
		t.Errorf("no args: %d", code)
	}
	if code := run([]string{"join"}, &out, &errb); code != 2 {
		t.Errorf("join without url: %d", code)
	}
	if code := run([]string{"bogus"}, &out, &errb); code != 2 {
		t.Errorf("unknown command: %d", code)
	}
}
