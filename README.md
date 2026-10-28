# jitsi-media-tools

A command-line tool and Go library that joins a [Jitsi Meet](https://jitsi.org/jitsi-meet/)
room as one participant, publishes media into it, and plays what the other participants send.

- **Publish**: a media file (once or looped), or a camera and microphone.
- **OBS gateway**: a WHIP endpoint that streams OBS (or any WHIP publisher) into any Jitsi
  meeting as a participant, its H.264 and Opus passed through unchanged.
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

## How it works

![Media pipeline: publish source, ffmpeg encoder, RTP sender and WebRTC peer out to the Jitsi
Videobridge; received RTP through the frame assembler and one ffmpeg decoder per stream into
the room's tile grid and audio mix, then to the window, library callbacks, recording and SRT
feeds](docs/media-pipeline.svg)

- **Publish**: one ffmpeg process encodes the source to VP8 and Opus. Go reads the encoded
  frames, paces them in real time and sends them as RTP over a Pion WebRTC connection to the
  Videobridge.
- **Receive**: Go rebuilds each received video stream into whole frames, releasing each
  frame as soon as its last packet arrives, and wraps audio packets as Ogg. Then one ffmpeg
  process per stream decodes it to raw video or audio.
- **Room**: the decoded frames are drawn into a 1280x720 tile grid at 30 fps, and the audio
  is mixed. Each participant's screen share gets its own tile. The window plays the grid and
  the mix through ffplay, and the library callbacks get the same media.
- **Gateway**: `serve` answers WHIP publishers such as OBS. Each stream joins the meeting
  named in its address as a send-only participant, and its H.264 and Opus packets go to the
  Videobridge as they came, without ffmpeg. Keyframe requests from the room go back to OBS.
- **Signaling**: XMPP over WebSocket with Jicofo and Prosody. Jingle offers become SDP for
  the WebRTC connection, and presence tells the tool who is in the room, their names and
  whether each source is muted.

[docs/latency.md](docs/latency.md) measures how long media takes through each stage, and
what was changed to make it faster.

## Status

Work in progress: feature `specs/001-jitsi-media-session`.

| Feature | State |
|---|---|
| Join a room: local server, guest, JWT | Done |
| Publish a media file (`file:`, `file-loop:`) | Done |
| List capture devices (`devices`) | Done |
| Publish a camera and microphone (`device:`) | Not yet |
| Stream OBS into meetings: WHIP gateway (`serve`) | Done |
| Tile-grid window (`--display`) | Done |
| Recording (`--record`) | Not yet |
| SRT feeds for OBS (`--obs`, `--obs-grid`) | Done |
| Decoded frames and samples in the library | Done |

The flags for the unfinished features are already parsed, but they do nothing yet.

## Requirements

- Go 1.23 or newer, to build.
- ffmpeg 6 or newer, built with libvpx, libopus and libsrt, plus ffplay for the window.
  `./scripts/get-ffmpeg.sh` downloads a pinned build into `.tools/ffmpeg/bin` on Linux and
  Windows; no root is needed. On macOS, use `brew install ffmpeg`.
- Docker, only for the local test server.

The WHIP gateway (`serve`) needs no ffmpeg.

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
jitsi-media-tools serve [options]
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

### Watch and listen to the room

```bash
# Receive only: show the room in a window
jitsi-media-tools join --url https://meet.example.org/MyRoom --name Viewer --display

# Publish and receive at the same time
jitsi-media-tools join --url https://meet.example.org/MyRoom --name Clip \
  --publish file-loop:samples/clip.mp4 --display
```

`--display` opens an ffplay window showing the room as a Jitsi-style tile grid
(1280x720, 30 fps). You hear everyone's audio mixed together. Each participant gets a
tile, and a screen share gets its own tile, named "<name> (screen)", until the share stops.
Participants without video show a name card. The grid shows at most `--video-limit` tiles,
those with video first. Closing the window leaves the room. Your own published media is not
shown or played back to you.

What the tool can do with received media:

| Output | How | State |
|---|---|---|
| Tile-grid window and mixed audio | `--display` (needs ffplay) | Done |
| Decoded frames and samples, per participant, the grid and the mix | Library callbacks: `OnVideo`, `OnAudio`, `OnGrid`, `OnMixedAudio` | Done |
| Recording: one `.mkv` per participant, or one mixed file | `--record DIR --record-mode per-participant\|mixed` | Not yet |
| SRT feeds of each participant, and of the grid, for OBS Media Sources | `--obs`, `--obs-grid`, `--obs-host`, `--obs-port`, `--obs-max-feeds` | Done |

Received media is decoded only while an output uses it. Without `--display` or `--obs`, the
command line only publishes and prints who joins and leaves.

### Bring the meeting into OBS (SRT feeds)

```bash
jitsi-media-tools join --url https://meet.example.org/MyRoom --name Feeds --obs --obs-grid
```

The command prints one address per feed:

```text
feed grid srt://192.0.2.10:9000
+ Alice
feed Alice srt://192.0.2.10:9001
```

In OBS, add a **Media Source**, untick "Local file", and set **Input** to the address, for
example `srt://192.0.2.10:9001`.

- **Which feeds:** each participant whose video the tool has received gets a feed, at most
  `--obs-max-feeds` (16 by default). `--obs-grid` adds the whole tile grid with the mixed
  audio.
- **What a feed carries:** the participant's video and their own audio. A feed is 640x360,
  or 1280x720 for a participant pinned with `--pin`; the grid is 1280x720. The video and
  audio are H.264 and AAC in MPEG-TS over SRT, with 120 ms of SRT latency.
- **Addresses stay the same.** A participant keeps their address for the whole session, so
  OBS scenes keep working. While they are away, or without video, their feed shows their
  name card.
- **CPU:** a feed only encodes while OBS is connected to it.
- **Ports:** the grid is on `--obs-port` (9000 by default) and participants on the next
  ports, all UDP. `--obs-host` sets the address to listen on (every interface by default).

### Stream OBS into a meeting (WHIP gateway)

```bash
WHIP_API_SECRET=change-me jitsi-media-tools serve --public-ip 203.0.113.5
```

Then, in OBS 30 or newer, go to Settings → Stream:

| Field | Value |
|---|---|
| Service | WHIP |
| Server | `http://203.0.113.5:8080/whip/meet.example.org/MyRoom?name=OBS` |
| Bearer Token | the secret |

When you start streaming, "OBS" joins `https://meet.example.org/MyRoom` and shows your
scene. When you stop streaming, it leaves. The server address names the meeting:
`/whip/<server[:port]>/<room>`, with a tenant path such as `/whip/meet.example.org/acme/MyRoom`
where the deployment uses one, and `&jwt=<token>` for a server that needs a token. One
gateway serves several streams at once, into any Jitsi meetings.

- Set OBS's encoder to H.264 (x264 or a hardware H.264 encoder) and the audio to Opus, its
  WHIP default. The gateway passes them through unchanged, so OBS's resolution, bitrate and
  keyframe interval are what the meeting gets. The gateway refuses AV1 and HEVC.
- **Set OBS's keyframe interval to 1 s** (Settings → Output, Output Mode "Advanced",
  Keyframe Interval). Viewers see video from a keyframe on, and a publisher that ignores
  keyframe requests makes them wait for its next one: about 8 s with OBS's "0 (auto)" and
  x264. The gateway logs a warning when that happens. With a 1 s interval, the meeting shows
  the stream about 1.5–2.5 s after you start streaming. About 1 s of that is the Jitsi
  server setting up the participant's media, which happens for any participant.
- [docs/obs-whip.md](docs/obs-whip.md) gives the OBS settings for a quick start, a low delay
  or the best picture.
- The meeting starts receiving media once a second participant is in it.
- A stream that sends nothing for 10 s leaves the meeting.
- The OBS tile's connection details in the meeting (its signal bars) show the stream's
  bitrate, its packet loss from OBS to the meeting, and a connection quality.

| Option | Default |
|---|---|
| `$WHIP_API_SECRET` or `--secret-file FILE`: the bearer token | required |
| `--listen`: HTTP address | `:8080` |
| `--udp-port`: one UDP port for all streams' media | 8189 |
| `--public-ip`: the address OBS reaches, for a server behind NAT | local addresses |
| `--tls-cert`, `--tls-key`: serve HTTPS (or put a reverse proxy in front) | HTTP |
| `--max-streams` | 10 |
| `--insecure`, `--wait-host`: for the meetings' servers | off, `30s` |

On a public server, open the HTTP port (TCP) and the UDP port. The bearer token travels in
every request, so use HTTPS across the internet. `serve` prints one line per stream: `+ OBS
meet.example.org/MyRoom` when it starts, `-` when it ends, and `!` with the reason when it
fails.

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

`jmt.NewGateway` runs the WHIP gateway from a program, with the same options as `serve`;
`Events()` reports streams starting, ending and failing.

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
| `internal/peer`, `internal/rtpmedia` | WebRTC connections (to the bridge, and from WHIP publishers) and RTP framing |
| `internal/whip` | WHIP HTTP endpoint: secret, addresses, stream resources |
| `internal/ffmpeg` | ffmpeg processes: encoding and stream probing |
| `internal/session` | Joins signaling and media, and runs the gateway's streams; the only package that imports both |
| `tests/live/` | End-to-end tests (`-tags live`) |
| `tests/platforms/jitsi/` | Local Jitsi in Docker |

Requirements, design and tasks live in `specs/001-jitsi-media-session/`, which uses
[Spec Kit](https://github.com/github/spec-kit). The project rules are in
`.specify/memory/constitution.md`. A test (`internal/boundary_test.go`) enforces the
separation between the signaling and media packages.

## License

MIT, see [LICENSE](LICENSE).
