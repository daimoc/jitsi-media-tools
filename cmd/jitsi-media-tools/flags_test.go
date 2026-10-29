package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daimoc/jitsi-media-tools/jmt"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParseJoin_Maps(t *testing.T) {
	f, err := parseJoin([]string{"--url", "https://localhost:8443/Room", "--name", "Bot", "--insecure",
		"--publish", "file-loop:clip.mp4", "--publish-size", "640x360", "--display",
		"--record", "/tmp/rec", "--record-mode", "mixed", "--obs-grid", "--obs-port", "9100", "--obs-url-host", "192.168.1.20",
		"--pin", "Alice", "--pin", "Bob", "--video-limit", "4", "--video-budget", "2.5M", "--jwt-app-id", "app"},
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
	if o.OBS == nil || !o.OBS.Grid || o.OBS.BasePort != 9100 || o.OBS.MaxFeeds != 16 || o.OBS.URLHost != "192.168.1.20" {
		t.Errorf("obs %+v", o.OBS)
	}
	if strings.Join(o.Selection.Pins, ",") != "Alice,Bob" || o.Selection.Limit != 4 || o.Selection.Height != 180 || o.Selection.Budget != 2_500_000 {
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
		{"--url", "u", "--publish", "device:,"},
		{"--url", "u", "--video-budget", "fast"},
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

func TestParseServe_Maps(t *testing.T) {
	f, err := parseServe([]string{"--listen", ":9090", "--udp-port", "10000", "--public-ip", "203.0.113.5",
		"--tls-cert", "c.pem", "--tls-key", "k.pem", "--max-streams", "3", "--insecure", "--wait-host", "1m"},
		env(map[string]string{"WHIP_API_SECRET": "s3cret"}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	o := f.opts
	if o.Listen != ":9090" || o.Secret != "s3cret" || o.UDPPort != 10000 || o.PublicIP != "203.0.113.5" ||
		o.TLSCert != "c.pem" || o.TLSKey != "k.pem" || o.MaxStreams != 3 || !o.Insecure || o.WaitHost.String() != "1m0s" {
		t.Errorf("options %+v", o)
	}
}

func TestParseServe_SecretFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := parseServe([]string{"--secret-file", path}, env(map[string]string{"WHIP_API_SECRET": "env"}), io.Discard)
	if err != nil || f.opts.Secret != "from-file" {
		t.Fatalf("secret %q err %v, want the file's", f.opts.Secret, err)
	}
}

func TestParseServe_Errors(t *testing.T) {
	for _, c := range []struct {
		args []string
		env  map[string]string
	}{
		{nil, nil}, // No secret
		{[]string{"--secret-file", "/nonexistent/secret"}, nil},
		{[]string{"extra"}, map[string]string{"WHIP_API_SECRET": "s"}},
	} {
		if _, err := parseServe(c.args, env(c.env), io.Discard); err == nil {
			t.Errorf("%v: expected an error", c.args)
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{"serve"}, &out, &errb); code != 2 && os.Getenv("WHIP_API_SECRET") == "" {
		t.Errorf("serve without a secret: exit %d, want 2", code)
	}
}

func TestParsePublish_NoOBSInput(t *testing.T) {
	for _, p := range []string{"obs-srt::9100", "obs-rtmp::1935"} {
		if _, err := parseJoin([]string{"--url", "u", "--publish", p}, env(nil), io.Discard); err == nil {
			t.Errorf("--publish %s accepted; OBS comes in through serve", p)
		}
	}
}

func TestWHIPAddress(t *testing.T) {
	for _, c := range []struct{ addr, public, scheme, want string }{
		{"[::]:8080", "", "http", "http://localhost:8080/whip/<server>/<room>?name=<name>"},
		{"0.0.0.0:8080", "203.0.113.5", "https", "https://203.0.113.5:8080/whip/<server>/<room>?name=<name>"},
		{"127.0.0.1:9000", "", "http", "http://127.0.0.1:9000/whip/<server>/<room>?name=<name>"},
	} {
		if got := whipAddress(c.addr, c.public, c.scheme); got != c.want {
			t.Errorf("whipAddress(%q, %q) = %q, want %q", c.addr, c.public, got, c.want)
		}
	}
}

func TestParsePublish_Devices(t *testing.T) {
	for arg, want := range map[string]jmt.Source{
		"device:Integrated Camera,default": jmt.DeviceSource("Integrated Camera", "default"),
		"device:Cam":                       jmt.DeviceSource("Cam", ""),
		"device:,Mic":                      jmt.DeviceSource("", "Mic"),
	} {
		got, err := parsePublish(arg, 640, 360)
		if err != nil || !reflect.DeepEqual(got, jmt.WithSize(want, 640, 360)) {
			t.Errorf("%s: %#v %v", arg, got, err)
		}
	}
}
