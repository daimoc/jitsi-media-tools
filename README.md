# jitsi-media-tools

A command-line tool and Go library that joins a [Jitsi Meet](https://jitsi.org/jitsi-meet/)
room as one participant, publishes media into it, and plays what the other participants send.

- **Publish**: a media file (once or looped), a camera and microphone, or an OBS stream
  over SRT or RTMP.
- **Play**: show the room in a Jitsi-style tile grid, record it (one file per participant or
  one mixed file), and serve SRT feeds that OBS Media Sources can read.
- **Library**: the `jmt` package gives the same features to other programs, such as an OBS
  plugin. It hands decoded frames and samples both ways and never exposes RTP, SDP or codecs.
- **Servers**: a local Jitsi, real deployments joined as a guest, and token (JWT)
  authentication.
- **Platforms**: Linux, Windows and macOS. ffmpeg runs as a separate process and there is
  no cgo, so a plain `go build` works everywhere.

Rooms can hold up to 100 participants. To stay usable on a laptop, the tool receives
everyone's audio but video from only the 9 most recent speakers, at low resolution, plus
any participants you pin by display name.

## Status

Work in progress: feature `specs/001-jitsi-media-session`.

| Feature | State |
|---|---|
| Join a room: local server, guest, JWT | Done |
| Publish a media file (`file:`, `file-loop:`) | Done |
| List capture devices (`devices`) | Done |
| Publish a camera and microphone (`device:`) | Not yet |
| Publish an OBS stream (`obs-srt:`, `obs-rtmp:`) | Not yet |
| Tile-grid window (`--display`) | Done |
| Recording (`--record`) | Not yet |
| SRT feeds for OBS (`--obs`, `--obs-grid`) | Not yet |
| Decoded frames and samples in the library | Done |

The flags for the unfinished features are already parsed, but they do nothing yet.

## Requirements

- Go 1.23 or newer, to build.
- ffmpeg 6 or newer, built with libvpx, libopus and libsrt, plus ffplay for the window.
  `./scripts/get-ffmpeg.sh` downloads a pinned build into `.tools/ffmpeg/bin` on Linux and
  Windows; no root is needed. On macOS, use `brew install ffmpeg`.
- Docker, only for the local test server.

## Install

Build from a clone:

```bash
./scripts/get-ffmpeg.sh       # if ffmpeg is not installed
source scripts/env.sh         # puts .tools/ffmpeg/bin (and /usr/local/go/bin) in PATH
go build -o jitsi-media-tools ./cmd/jitsi-media-tools
```

## Command-line usage

```text
jitsi-media-tools join --url URL [--name NAME] [options]
jitsi-media-tools devices [--ffmpeg PATH]
```

`join` stays in the room until you press Ctrl+C. A file published once ends the session
when it finishes. Exit codes: 0 for a normal end, 1 for a session error, 2 for bad
arguments. Run `jitsi-media-tools join --help` to see every option.

### Publish a file

```bash
# Play once, then leave
jitsi-media-tools join --url https://meet.example.org/MyRoom --name Clip --publish file:samples/clip.mp4

# Loop until Ctrl+C, at 640x360
jitsi-media-tools join --url https://meet.example.org/MyRoom --name Clip \
  --publish file-loop:samples/clip.mp4 --publish-size 640x360
```

The tool publishes the file's first audio and video streams. It sends VP8 video, scaled to
fit `--publish-size` (1280x720 by default) and padded, at 30 fps, and Opus audio. When a
file has no video or no audio, only the kind it has is published. If the source has a gap,
or ffmpeg restarts, the room sees a black picture and hears silence; the participant does
not drop out.

### Choose the video you receive

```bash
jitsi-media-tools join --url https://meet.example.org/MyRoom --name Viewer \
  --video-limit 9 --video-height 180 --pin "Alice" --pin "Bob" --pin-height 720
```

### Servers

| Server | Flags |
|---|---|
| Self-signed certificate | `--insecure` |
| Guests wait for a host | `--wait-host 5m` (the default) |
| Ready-made JWT | `--jwt TOKEN`, `$JITSI_JWT`, or `?jwt=` in the URL |
| Sign a JWT | `--jwt-app-id APP` and `$JITSI_APP_SECRET`; `--jwt-moderator`, `--jwt-ttl`, `--jwt-audience`, `--jwt-subject` |
| Non-standard deployment | `--domain`, `--muc`, `--anonymous-domain`, `--ws`, `--focus` (read from the server's `config.js` by default) |

Other options: `--ffmpeg` and `--ffplay` set the executables, and `--log-level`
takes `debug`, `info` or `warn`.

## Library usage

```go
import "github.com/daimoc/jitsi-media-tools/jmt"

s, err := jmt.Join(ctx, jmt.Options{
	MeetingURL:  "https://meet.example.org/MyRoom",
	DisplayName: "Bot",
	Publish:     jmt.WithSize(jmt.FileSource("samples/clip.mp4", true), 640, 360),
	Selection:   jmt.Selection{Limit: 9, Pins: []string{"Alice"}},
})
if err != nil {
	return err
}
defer s.Close()

for e := range s.Events() { // closed when the session ends
	switch e.Kind {
	case jmt.ParticipantJoined:
		fmt.Println("+", e.Participant.Name)
	case jmt.ParticipantLeft:
		fmt.Println("-", e.Participant.Name)
	}
}
return s.Wait()
```

- `ctx` bounds only the join. After that, the session runs until `Close`, until it fails,
  or until a file published once ends.
- `Participants()` lists the people in the room, and `SetSelection` changes which video
  the session receives.
- Zero values in `Options` take the defaults described in [jmt/options.go](jmt/options.go).

### Received media and your own frames

```go
s.OnVideo(func(f jmt.VideoFrame) { /* f.ParticipantID, f.Width×f.Height yuv420p in f.Y, f.U, f.V */ })
s.OnGrid(func(f jmt.VideoFrame) { /* the tile grid, 1280x720 at 30 fps */ })
s.OnAudio(func(c jmt.AudioChunk) { /* 20 ms of one participant: 48 kHz stereo, 1920 int16 */ })
s.OnMixedAudio(func(c jmt.AudioChunk) { /* 20 ms of the room's mix */ })

pub, src := jmt.FrameSource(640, 360, true, true) // Pass src as Options.Publish
pub.WriteVideo(frame)                              // 640x360 yuv420p, at 30 fps
pub.WriteAudio(chunk)                              // every 20 ms
```

- Each callback runs on its own goroutine. A slow callback loses frames but delays nothing
  else. Buffers are valid only during the call, so copy what you keep. Pass `nil` to stop one.
- Received video and audio are decoded only while a callback or the window uses them.
- Write your frames in real time. While none come, the session sends black and silence.
- [jmt/example_test.go](jmt/example_test.go) is a complete example: it publishes a moving
  pattern and counts the frames received from each participant.

## Try it on a local Jitsi

```bash
(cd tests/platforms/jitsi && ./setup.sh && docker compose up -d)
source scripts/env.sh
go run ./cmd/jitsi-media-tools join --url https://localhost:8443/test --insecure \
  --name Clip --publish file-loop:samples/clip.mp4
```

Open `https://localhost:8443/test` in a browser and accept the certificate. Use
`localhost`, not `127.0.0.1`: the page connects to `wss://localhost:8443`, so a certificate
accepted for `127.0.0.1` leaves the meeting unable to join. "Clip" appears
once someone else is in the room, because the server sets up media only when there is a
second participant. See [tests/platforms/jitsi/README.md](tests/platforms/jitsi/README.md)
for the guest and token setups.

## Development

```bash
go vet ./... && go test -race ./...    # unit tests; those that need ffmpeg skip without it
JMT_URL=https://localhost:8443/jmt-live JMT_INSECURE=1 \
  go test -tags live -count=1 -v ./tests/live/    # against the local Jitsi
```

| Path | Contents |
|---|---|
| `jmt/` | Public library |
| `cmd/jitsi-media-tools/` | Command-line tool, a thin layer over `jmt` |
| `internal/jitsi`, `internal/signaling` | XMPP and Jingle signaling |
| `internal/peer`, `internal/rtpmedia` | WebRTC connection and RTP framing |
| `internal/ffmpeg` | ffmpeg processes: encoding and stream probing |
| `internal/session` | Joins signaling and media; the only package that imports both |
| `tests/live/` | End-to-end tests (`-tags live`) |
| `tests/platforms/jitsi/` | Local Jitsi in Docker |

Requirements, design and tasks live in `specs/001-jitsi-media-session/`, which uses
[Spec Kit](https://github.com/github/spec-kit). The project rules are in
`.specify/memory/constitution.md`. A test (`internal/boundary_test.go`) enforces the
separation between the signaling and media packages.

## License

MIT, see [LICENSE](LICENSE).
