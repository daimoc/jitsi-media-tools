# jitsi-media-tools

[![CI](https://github.com/daimoc/jitsi-media-tools/actions/workflows/ci.yml/badge.svg)](https://github.com/daimoc/jitsi-media-tools/actions/workflows/ci.yml)
[![Docker](https://github.com/daimoc/jitsi-media-tools/actions/workflows/docker.yml/badge.svg)](https://github.com/daimoc/jitsi-media-tools/pkgs/container/jitsi-media-tools)

A command-line tool and Go library that joins a [Jitsi Meet](https://jitsi.org/jitsi-meet/)
room as one participant, publishes media into it, and plays what the other participants send.

- **Publish**: a media file (once or looped), or a camera and microphone.
- **OBS gateway**: a WHIP endpoint that streams OBS (or any WHIP publisher) into any Jitsi
  meeting as a participant, its H.264 or AV1 video and Opus audio passed through unchanged. See
  [Stream OBS into a meeting](#stream-obs-into-a-meeting-whip-gateway), with or without Docker.
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

![Media pipeline: OBS through the WHIP gateway's ingest peer and forwarder straight to the
WebRTC peer, without ffmpeg; publish source, ffmpeg encoder, RTP sender and WebRTC peer out to
the Jitsi Videobridge; received RTP through the frame assembler and one ffmpeg decoder per
stream into the room's tile grid and audio mix, then to the window, library callbacks,
recording and SRT feeds. Boxes outlined in purple use the Pion libraries named in
them](docs/media-pipeline.svg)

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
  named in its address as a send-only participant, and its H.264 or AV1 and Opus packets go to the
  Videobridge as they came, without ffmpeg. Keyframe requests from the room go back to OBS.
- **Signaling**: XMPP over WebSocket with Jicofo and Prosody. Jingle offers become SDP for
  the WebRTC connection, and presence tells the tool who is in the room, their names and
  whether each source is muted.

[docs/latency.md](docs/latency.md) measures how long media takes through each stage, and
what was changed to make it faster.

## Status

Every feature of `specs/001-jitsi-media-session` is built. [Limits](#limits) says what is
out of scope and what was not tested.

| Feature | State |
|---|---|
| Join a room: local server, guest, JWT | Done |
| Publish a media file (`file:`, `file-loop:`) | Done |
| List capture devices (`devices`) | Done |
| Publish a camera and microphone (`device:`) | Done |
| Stream OBS into meetings: WHIP gateway (`serve`) | Done |
| Tile-grid window (`--display`) | Done |
| Recording (`--record`) | Done |
| SRT feeds for OBS (`--obs`, `--obs-grid`) | Done |
| Decoded frames and samples in the library | Done |
| Large rooms: video limit, pins, budget (`--video-limit`, `--pin`, `--video-budget`) | Done |

## Requirements

- Go 1.23 or newer, to build.
- ffmpeg 6 or newer, built with libvpx, libopus, libx264 and libsrt, plus ffplay for the
  window. The WHIP gateway (`serve`) needs no ffmpeg.
- Docker, for the local test server, or to run the tool in a container instead of
  installing ffmpeg.

## Install

1. Install ffmpeg:

   | System | Command |
   |---|---|
   | Debian, Ubuntu | `sudo apt install ffmpeg` |
   | macOS | `brew install ffmpeg` |
   | Windows | `choco install ffmpeg`, or `winget install Gyan.FFmpeg` |
   | Linux or Windows, no root | `./scripts/get-ffmpeg.sh` (from Git Bash on Windows) downloads a pinned build into `.tools/ffmpeg/bin` |

   The tool looks for `ffmpeg` and `ffplay` in `PATH`; `--ffmpeg` and `--ffplay` point to
   other ones. Before joining, it checks that ffmpeg has the codecs and formats the chosen
   options need, and names what is missing.

2. Build from a clone:

   ```bash
   source scripts/env.sh         # puts .tools/ffmpeg/bin (and /usr/local/go/bin) in PATH
   go build -o jitsi-media-tools ./cmd/jitsi-media-tools
   ```

### Or run it in Docker

The image holds the tool and the pinned ffmpeg build, for linux/amd64 and linux/arm64. It
has no ffplay and no shell, so `--display` is not available; everything else is. CI
publishes it as `ghcr.io/daimoc/jitsi-media-tools`: `main` follows the main branch, and each
release has its version as tag. The examples below use an image built from a clone:

```bash
docker build -t jitsi-media-tools .
# Every platform at once, without emulation:
#   docker buildx build --platform linux/amd64,linux/arm64 -t <registry>/jitsi-media-tools --push .

docker run --rm --network host --user "$(id -u):$(id -g)" -v "$PWD/rec:/rec" \
  jitsi-media-tools join --url https://meet.example.org/MyRoom --name Recorder --record /rec
docker run --rm --network host -v "$PWD/samples:/media:ro" \
  jitsi-media-tools join --url https://meet.example.org/MyRoom --name Clip --publish file-loop:/media/clip.mp4
```

- **Network.** On Linux, `--network host` is the simplest: WebRTC, the SRT feeds and the
  WHIP gateway use the host's addresses as they would without Docker. On Docker's default
  bridge network, joining a meeting normally works, through Docker's NAT. Two things need extra
  flags there:
  - **SRT feeds:** publish the ports and name the host in the feed URLs, for example
    `-p 9000-9016:9000-9016/udp … --obs --obs-url-host 192.168.1.20`.
  - **WHIP gateway:** publish its two ports and give the host's address with `--public-ip`;
    `compose.yaml` does both. See [Stream OBS into a meeting](#stream-obs-into-a-meeting-whip-gateway).
- **Files.** The image runs as an unprivileged user. Use `--user` so that recordings in a
  mounted directory belong to you, and mount the files you publish.
- **Stopping.** `docker stop` ends the session as Ctrl+C does: the tool leaves the meeting,
  closes its recordings and exits with code 0.
- **A Jitsi in Docker on the same machine.** From a container on another bridge network, the
  Videobridge's container address is unreachable. Use `--network host`, attach the
  container to Jitsi's network as well, or set the bridge's `JVB_ADVERTISE_IPS` to the
  host's address.
- Cameras and microphones in a container were not tried.

## Command-line usage

```text
jitsi-media-tools join --url URL [--name NAME] [options]
jitsi-media-tools serve [options]        # WHIP gateway: see "Stream OBS into a meeting"
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

### Publish a camera and microphone

```bash
jitsi-media-tools devices
# video  "Integrated_Webcam_FHD: Integrat"  (/dev/video0)
# audio  "default"  (the default microphone)
# audio  "alsa_input.pci-0000_00_1f.3.analog-stereo"

jitsi-media-tools join --url https://meet.example.org/MyRoom --name Cam \
  --publish "device:Integrated_Webcam_FHD: Integrat,default"
```

`device:VIDEO,AUDIO` takes the names `devices` prints (case does not matter; on Linux the
`/dev/videoN` path works too). Leave one out to publish only the other: `device:,default`
for the microphone alone, `device:/dev/video0` for the camera alone. An unknown name stops
the tool before it joins, listing the devices there are. The camera is captured at
`--publish-size` and 30 fps; on Linux the tool asks for Motion-JPEG when the camera offers
it, since most webcams send raw 720p at 10 fps or less. Devices come from V4L2 and
PulseAudio/PipeWire on Linux, AVFoundation on macOS and DirectShow on Windows.

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
| Recording: one `.mkv` per participant, or one mixed file | `--record DIR --record-mode per-participant\|mixed` | Done |
| SRT feeds of each participant, and of the grid, for OBS Media Sources | `--obs`, `--obs-grid`, `--obs-host`, `--obs-url-host`, `--obs-port`, `--obs-max-feeds` | Done |

Received media is decoded only while an output uses it. Without `--display`, `--record` or `--obs`, the
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
  `--obs-url-host` sets the address in the feed URLs, for example the host's when the tool
  runs in a container.

### Record the meeting

```bash
# One file per participant
jitsi-media-tools join --url https://meet.example.org/MyRoom --name Recorder --record ./rec
# One file of the tile grid and the mixed audio
jitsi-media-tools join --url https://meet.example.org/MyRoom --name Recorder --record ./rec --record-mode mixed
```

The command prints each file as it starts:

```text
+ Alice
record Alice ./rec/myroom-20261028-091500-Alice.mkv
```

- **Files:** `<room>-<start time>-<name>.mkv` in the directory, created if needed, or
  `…-mixed.mkv`. Characters that cannot be in a file name, such as `/`, become `_`.
- **Who gets a file:** each participant from their first video or audio. A participant who
  never sends media gets none. A participant who leaves and comes back keeps their file,
  which shows their name card while they are away.
- **What a file holds:** H.264 and AAC in Matroska. A participant's file is 640x360 (1280x720
  when pinned with `--pin`), the mixed file 1280x720 at 30 fps, with silence while nobody
  speaks.
- **Stopping:** Ctrl+C finishes every file within a second or two, and they play anywhere. If
  the tool is killed instead, a file still plays up to its last second or so.
- **With `--obs`:** `feeds.json` in the same directory lists the feed addresses, for
  example `{"Alice": "srt://192.0.2.10:9001"}`.
- **CPU:** each file is encoded by its own ffmpeg, so recording a participant who also has an
  OBS feed encodes them twice while OBS watches.

### Choose the video you receive

```bash
jitsi-media-tools join --url https://meet.example.org/MyRoom --name Viewer \
  --video-limit 9 --video-height 180 --pin "Alice" --pin "Bob" --pin-height 720

# Keep received video under 3 Mbit/s
jitsi-media-tools join --url https://meet.example.org/MyRoom --name Viewer --display \
  --pin "Alice" --video-budget 3M
```

`--video-budget` (bits/s, e.g. `3M`, `800k`) caps the video received. When the selection
would need more, the tool gives up, in this order: the resolution of unpinned participants,
then their number, then the pinned participants' resolution. A pinned participant is never
dropped. It measures what arrives every 2 s, moves down after two windows over the budget,
and back up once there is room again (10 s after moving down at the earliest). The budget is
also offered to the bridge, which enforces it itself where its operator set
`videobridge.cc.assumed-bandwidth-limit`.

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

## Stream OBS into a meeting (WHIP gateway)

`serve` is a [WHIP](https://www.rfc-editor.org/rfc/rfc9725) endpoint. OBS 30 or newer, or
any other WHIP publisher, streams to it, and each stream joins the Jitsi meeting named in
its address as a participant. The H.264 or AV1 video and Opus audio pass through unchanged, so the
gateway needs no ffmpeg and little CPU. One gateway serves several streams at once, into
any Jitsi meetings. Video: [OBS → Jitsi with the WHIP gateway](https://www.youtube.com/watch?v=xMzMXCI4Mjo).

```text
OBS ── WHIP: HTTP offer/answer, then WebRTC media ──▶ serve ── one participant per stream ──▶ Jitsi meeting
```

### 1. Start the gateway

Pick a secret. OBS sends it as its bearer token, and the gateway refuses any stream without
it. The gateway never logs it.

**Without Docker.** Build the tool (see [Install](#install); ffmpeg is not needed), then:

```bash
WHIP_API_SECRET=change-me jitsi-media-tools serve
```

**With Docker Compose.** No clone is needed, only [`compose.yaml`](compose.yaml):

```bash
curl -O https://raw.githubusercontent.com/daimoc/jitsi-media-tools/main/compose.yaml
WHIP_API_SECRET=change-me JMT_PUBLIC_IP=192.168.1.20 docker compose up -d
docker compose logs -f
```

| Variable | Meaning | Default |
|---|---|---|
| `WHIP_API_SECRET` | The bearer token | required |
| `JMT_PUBLIC_IP` | The address OBS reaches this machine at: its LAN address when OBS is on the same network, its public address on a server. The container cannot find it out | required |
| `JMT_WHIP_PORT` | The HTTP port on this machine. `serve` still prints 8080, the port inside the container | 8080 |
| `JMT_TAG` | The image tag: `main`, or a release such as `0.1.0` | `main` |

`docker compose up -d --build` builds the image from a clone instead of pulling it.

**With `docker run`, on Linux.** On the host network, no port is published and no address
needs to be given on a LAN:

```bash
docker run -d --name jmt-whip --restart unless-stopped --network host \
  -e WHIP_API_SECRET=change-me ghcr.io/daimoc/jitsi-media-tools:main serve
```

On Docker Desktop (macOS, Windows), use Compose.

### 2. Set up OBS

In OBS, go to Settings → Stream:

| Field | Value |
|---|---|
| Service | WHIP |
| Server | `http://<gateway>:8080/whip/meet.example.org/MyRoom?name=OBS` |
| Bearer Token | the secret |

The server address names the meeting and the participant:

- `/whip/<server[:port]>/<room>` joins `https://<server[:port]>/<room>`. Where the
  deployment uses a tenant, add it to the path: `/whip/meet.example.org/acme/MyRoom`.
- `?name=` is the display name in the meeting; it is required.
- `&jwt=<token>` gives a token to a server that needs one.

Then set the encoding (Settings → Output, Output Mode "Advanced"):

- **Video encoder:** H.264 (x264, or a hardware H.264 encoder), or AV1 (SVT-AV1, or a
  hardware AV1 encoder). The gateway refuses HEVC. **Audio:** Opus, OBS's WHIP default.
- **H.264 or AV1?** H.264 unless you know every viewer can decode AV1. The Videobridge
  forwards the stream as it comes and converts nothing, so a viewer without an AV1 decoder
  sees only a name card: Safari without hardware AV1 decoding, older browsers, and
  jitsi-media-tools receivers (`join --display`, `--record`, `--obs`). Chrome and Firefox
  decode AV1. The gateway logs a reminder when it forwards AV1.
- **Keyframe interval: 1 s.** Viewers see video from a keyframe on, and a publisher that
  ignores keyframe requests makes them wait for its next one: about 8 s with OBS's
  "0 (auto)" and x264. The gateway logs a warning when that happens.
- OBS's resolution, frame rate and bitrate are what the meeting gets.
  [docs/obs-whip.md](docs/obs-whip.md) gives settings for a quick start, a low delay or the
  best picture.

### 3. Stream

Start streaming in OBS: "OBS" joins the meeting and shows your scene, about 1.5–2.5 s later
with a 1 s keyframe interval. About 1 s of that is the Jitsi server setting up the
participant's media, which happens for any participant. Stop streaming and it leaves.

- The meeting starts receiving media once a second participant is in it.
- A stream that sends nothing for 10 s leaves the meeting.
- The OBS tile's connection details in the meeting (its signal bars) show the stream's
  bitrate, its packet loss from OBS to the meeting, and a connection quality.
- `serve` prints one line per stream: `+ OBS meet.example.org/MyRoom` when it starts, `-`
  when it ends, and `!` with the reason when it fails.

### Options

| Option | Default |
|---|---|
| `$WHIP_API_SECRET` or `--secret-file FILE`: the bearer token | required |
| `--listen`: HTTP address | `:8080` |
| `--udp-port`: one UDP port for all streams' media | 8189 |
| `--public-ip`: the address OBS reaches, for a server behind NAT or in a container | local addresses |
| `--tls-cert`, `--tls-key`: serve HTTPS (or put a reverse proxy in front) | HTTP |
| `--max-streams` | 10 |
| `--insecure`, `--wait-host`: for the meetings' servers | off, `30s` |

### Network and security

- **Ports:** the HTTP port (TCP, 8080) and one UDP port for every stream's media (8189).
  Open both in the firewall.
- **Behind NAT, or in a container on a bridge network:** set `--public-ip` (`JMT_PUBLIC_IP`
  with Compose). Without it, the gateway gives OBS its own local addresses, which OBS
  cannot reach, and the connection times out.
- **HTTPS:** the bearer token travels in every request, so across the internet use
  `--tls-cert` and `--tls-key`, or a reverse proxy in front of the HTTP port. The media is
  encrypted by WebRTC either way, and goes to the UDP port directly, not through the proxy.
- **A Jitsi Videobridge on the same machine** (docker-jitsi-meet) already uses
  `127.0.0.1:8080`: give the gateway another port, for example `--listen :8090`.

### Try it on the local Jitsi

With the local server running (see [Try it on a local Jitsi](#try-it-on-a-local-jitsi))
and a browser in `https://localhost:8443/test`:

```bash
WHIP_API_SECRET=s3cret jitsi-media-tools serve --insecure --listen :8090
# or: docker run --rm --network host -e WHIP_API_SECRET=s3cret \
#       ghcr.io/daimoc/jitsi-media-tools:main serve --insecure --listen :8090
```

In OBS, use the server `http://127.0.0.1:8090/whip/localhost:8443/test?name=OBS` and the
token `s3cret`. Without OBS, ffmpeg 8 or newer can publish a test pattern:

```bash
ffmpeg -re -f lavfi -i testsrc2=size=1280x720:rate=30 -f lavfi -i sine=frequency=440 \
  -c:v libx264 -preset veryfast -tune zerolatency -g 30 -c:a libopus -ar 48000 -ac 2 \
  -f whip -authorization s3cret "http://127.0.0.1:8090/whip/localhost:8443/test?name=Pattern"
```

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

## Limits

- **Room size.** The tool is designed for rooms of up to 100 participants. It was checked
  with 20: for 30 minutes, with the window, a mixed recording and OBS feeds all on, it used
  about 1.5 CPU cores and the grid never froze.
- **Not supported:** chat, moderation, lobby management, breakout rooms, and end-to-end
  encrypted meetings. The tool declines peer-to-peer calls, so a two-person meeting goes
  through the Videobridge.
- **Codecs.** The tool receives VP8, VP9 and H.264 video and Opus audio. A participant who
  sends AV1 shows a name card, and the tool logs a warning once. It publishes one VP8 layer
  (no simulcast) and Opus; the gateway forwards OBS's H.264 or AV1 and Opus.
- **Selection.** With the command line, pins and limits are set at start-up. A program
  changes them at any time with `SetSelection`.
- **Bitrate budget.** The tool keeps to the budget by asking for less video. The bridge
  enforces it only where its operator allows it (see above).
- **Latency.** On a local server, received video reaches the window about 225 ms after the
  sender produced it, and audio about 185 ms ([docs/latency.md](docs/latency.md)). A real
  network adds its own delay.
- **Received audio.** The first 80 ms after a participant starts sending audio, or after
  10 s without any, are lost.
- **Platforms.** Linux was tested by hand, with a browser, OBS and a webcam. Windows and
  macOS run the automated tests only. Camera capture, the window, and stopping cleanly on
  those systems were not tried by hand.
- **If the tool is killed,** its ffmpeg processes stop too: at once on Linux and Windows,
  and on macOS as soon as their pipes close.

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

## Video tutorials

- [Publish a video file into a meeting](https://www.youtube.com/watch?v=XNfNdoNPYzg)
- [Record a Jitsi meeting](https://www.youtube.com/watch?v=LLoFScThED0)
- [OBS → Jitsi with the WHIP gateway](https://www.youtube.com/watch?v=xMzMXCI4Mjo), natively and in Docker

[tutorials/](tutorials/README.md) makes four narrated videos from scripts, against the local
Jitsi: publishing a file, recording, the WHIP gateway (natively and in Docker), and SRT feeds
in Docker. Each shows a real terminal next to the meeting as a participant sees it. The video
they publish is an excerpt of [Big Buck Bunny](https://peach.blender.org/) (© 2008 Blender
Foundation, CC BY 3.0).

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
| `internal/ffmpeg` | ffmpeg processes: encoders, decoders, feeds, recordings, devices and capability checks |
| `internal/compose`, `internal/output` | Tile grid and audio mix; window, SRT feeds and recording files |
| `internal/session` | Joins signaling and media, and runs the gateway's streams; the only package that imports both |
| `tests/live/` | End-to-end tests (`-tags live`) |
| `tests/platforms/jitsi/` | Local Jitsi in Docker |

Requirements, design and tasks live in `specs/001-jitsi-media-session/`, which uses
[Spec Kit](https://github.com/github/spec-kit). The project rules are in
`.specify/memory/constitution.md`. A test (`internal/boundary_test.go`) enforces the
separation between the signaling and media packages.

## License

MIT, see [LICENSE](LICENSE).
