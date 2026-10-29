# Validation: hand checks of quickstart.md

Server: local platform `tests/platforms/jitsi` (docker-jitsi-meet stable-11248),
room `https://localhost:8443/IntriguingClimatesSupposeProbably`, Chrome 154 on Linux.

## Scenario 1: Publish a file (US1, T030)

Date: 2026-10-24. Commit: b50efd3.

```bash
jitsi-media-tools join --url https://localhost:8443/IntriguingClimatesSupposeProbably \
  --insecure --name Clip --publish file-loop:samples/clip.mp4
```

- [x] The browser shows "Clip" playing the clip (moving test pattern) and its beep is heard.
- [x] Media connected about 1 s after joining with a browser already in the room
  (SC-001: within 10 s; live test `TestLive_PublishFile`: 8.2 s including join).
- [x] Stopping the tool removes "Clip": jicofo saw it leave 2 ms after the signal (target 5 s).
- [x] No ffmpeg or tool process left after stopping.

Found and fixed during the check:

- Two copies of the tool in one room: jicofo refused the second one ("MSID is already used")
  because every peer used the same stream ID. Each peer now has its own (b50efd3).
- A browser opened on `127.0.0.1` cannot join: the platform's `config.js` points at
  `wss://localhost:8443`. Use `localhost` (documented in the READMEs).

Noted, not fixed: the browser's peer-to-peer offer, which the tool declines, is followed by a
`transport-info` that the tool logs as an error ("Rejected Jingle IQ … unknown session"); it
is harmless and should be logged at debug level.

## Scenario 2: Show the room (US2, T041)

Date: 2026-10-25. Commit: aff984b.

```bash
jitsi-media-tools join --url https://localhost:8443/IntriguingClimatesSupposeProbably \
  --insecure --name Viewer --display
```

Two browser tabs in the room ("ssds" and "ssds (2)"), camera and microphone on.

- [x] The window shows one tile per tab, with its camera video and its name; their audio is heard.
- [x] Camera off in one tab: about 2 s later its tile shows the name card.
- [x] A tab leaving: its tile and decoders went away when its presence left (target 5 s).
- [x] Closing the window: the tool left the room and exited with code 0.
- [x] Live test `TestLive_ReceiveGrid` passes (2.4 s).

Found and fixed during the check (aff984b):

- The browsers sent AV1, which the tool does not decode: the server's `config.js` prefers AV1
  and the tool listed no codecs. It now announces `vp8,vp9,h264` in its presence.
- The browsers ignored that list: lib-jitsi-meet reads it only from a participant whose
  presence has `<SourceInfo>`. The tool now always sends one (`{}` when it publishes nothing).
- Pion dropped a track for good when its first packets used an unregistered codec (AV1 before
  the switch). AV1 is now registered, and each receive pipe restarts its decoder when the
  track's codec changes. The browsers switched from AV1 to VP9 within 0.3 s.

## Scenario 5: Join a real deployment as a guest (US5, T056)

Date: 2026-10-28. Server: meet.jit.si (shard eu-frankfurt-1, release 7156),
room `https://meet.jit.si/HorribleArrowsRememberEach`, no token.

```bash
jitsi-media-tools join --url https://meet.jit.si/HorribleArrowsRememberEach --name "jmt test" \
  --publish file-loop:clip.webm --publish-size 640x360
```

- [x] config.js read: guest domain `guest.meet.jit.si`, MUC `conference.meet.jit.si`,
  WebSocket `wss://meet.jit.si/xmpp-websocket`.
- [x] Guest login (SASL ANONYMOUS) and the conference request succeed.
- [x] With no host in the room, the tool prints `waiting for host…` and joins again every
  5 s; Ctrl+C while waiting exits with no error and no ffmpeg left.
- [x] Joined once a host was in the room (`+ dsds`); the host saw the test pattern and heard
  its tone ("ok good").
- [x] Received the host's Opus audio and VP9 video (320x180) in 0.3 s; with `--record`, a
  59 s file of their video and audio (H.264 640x360, AAC); no warnings, no ffmpeg left.

Found and fixed during the check:

- Login failed with `xmpp authentication failed: bad-format`: meet.jit.si sends a message
  before the result of the bind request, which mellium rejected. Custom bind feature.
- Joining failed with `room unavailable: registration-required`: meet.jit.si refuses
  guests' joins (not the conference request) until a host is in the room. Now waits.
- `waiting for host…` was never printed: the CLI reads events only after Join returns.
  `jmt.Options.OnWaitingForHost` is called during Join.
- Ctrl+C while waiting reported "timed out waiting for a host after 2m0s" and exit 1; it
  now exits 0 quietly.
- With the host in the room, the tool still waited: without a token, the WebSocket URL had
  no `?room=`, which meet.jit.si uses to route the connection to the room's shard, so the
  tool reached a shard where the room had no host. The room is now always in the URL, as
  lib-jitsi-meet does.

Not checked: a deployment that refuses a guest for lack of SSRC rewriting or visitor
support (R12); meet.jit.si offers visitors (`visitors-supported`) but did not use them for
a room this small.

## Scenario 7: Publish a camera and microphone (US7, T062)

Date: 2026-10-28. Linux (PipeWire), Dell integrated webcam, local Jitsi
(`https://localhost:8443/jmt-cam`). The meeting was watched by a second tool session
recording per participant, not by a browser.

```bash
jitsi-media-tools devices
jitsi-media-tools join --url https://localhost:8443/jmt-cam --insecure --name Cam \
  --publish "device:Integrated_Webcam_FHD: Integrat,default"
```

- [x] `devices` lists the camera (`/dev/video0`, not its metadata node `/dev/video1`), a
  `default` microphone and the PipeWire sources, without the outputs' monitors.
- [x] Joined and published: the recorder's file of "Cam" shows the room seen by the camera,
  and holds the microphone's sound (mean -50 dB, peak -26 dB in a quiet room; silence would
  be about -91 dB).
- [x] The encoder captures in Motion-JPEG at 30 fps (`-input_format mjpeg`); no warnings,
  no ffmpeg left after Ctrl+C.
- [x] An unknown name fails before joining, listing the devices there are (unit test).
- [x] Watched in a browser: camera and microphone OK; about 1 s of delay before the
  latency fix below, then judged good by the user.

Found and fixed during the check:

- The camera gave 10 fps: in raw YUYV at 1280x720 it fills USB 2. The tool now asks ffmpeg
  for the camera's compressed formats and captures Motion-JPEG when offered.
- The user saw about 1 s of delay on the camera in the browser. Frames left as RTP about
  490 ms after capture: ffmpeg held the camera back by the gap between opening it and the
  microphone, and the sender kept the start-up burst as a lasting queue. Now about 120 ms
  (docs/latency.md, "camera and microphone").
- ffmpeg 8 lists V4L2 as `video4linux2,v4l2`; the capability probe read the first alias
  only, so a device need of `v4l2` looked missing. Both aliases now count.

## Scenario 8 (partial): received-video budget (US8, T084)

Date: 2026-10-29. Local Jitsi, room `jmt-budget`: four tool publishers looping a 1280x720
test pattern (single-layer VP8, no simulcast), one viewer recording per participant.

```bash
jitsi-media-tools join --url https://localhost:8443/jmt-budget --insecure --name Viewer \
  --record /tmp/brec --video-budget 600k --log-level debug
```

- [x] With 3M the four videos (about 1 Mbps in all) stay as they are.
- [x] With 600k the first estimate asks for 3 videos (`lastN` 3); about 700 kbps measured two
  windows in a row moves it to 2 videos; then 450–540 kbps, steady for 20 s, no move back up
  (3 videos estimated over 85% of the budget).
- [x] `assumedBandwidthBps` is in every `ReceiverVideoConstraints`; the local bridge does not
  enforce it (`assumed-bandwidth-limit` unset).
- [x] Resolution steps with simulcast senders: see Scenario 8 below.

## Scenario 8: Large room (US8, T065)

Date: 2026-10-29. Local Jitsi, room `jmt-large`: 20 webrtc-load-engine bots (commit
356166e) sending Opus and three-layer VP8 simulcast up to 720p, each receiving 4 streams:

```bash
# webrtc-load-engine
JITSI_ROOM_URL=http://localhost:8000/jmt-large JITSI_XMPP_DOMAIN=meet.jitsi \
JITSI_MUC_DOMAIN=muc.meet.jitsi JITSI_WS_URL=ws://localhost:8000/xmpp-websocket \
JITSI_PARTICIPANTS=20 JITSI_MEDIA=audio_video JITSI_SIMULCAST=720 JITSI_MAX_RECEIVED=4 \
JITSI_RAMP_UP=10 JITSI_HOLD=40m go test -tags live -count=1 -timeout 60m ./tests/live/ -run Jitsi
```

Videos arriving were counted from the session's per-window debug log (a 100M budget, which
never acts, turns it on).

- [x] Default selection, one bot pinned: 9 videos arrive (the pin and 8 others) of 20.
- [x] The pin comes at 720p: 2.9 Mbps in all, 1.5 Mbps with `--pin-height 180`.
- [x] `--video-budget 1.2M`: the pin alone at 720p (≈ 1.6 Mbps measured) cannot fit, so the
  others go, then the pin drops to 360p: ≈ 0.5 Mbps, steady.
- [x] `--video-budget 2.4M`: from the pin alone at 360p, two steps up to the pin at 720p and
  3 others; ≈ 2.05 Mbps (85% of the budget), steady for 40 s.
- [x] SC-005 (30 min, display, mixed recording and OBS feeds with the grid all on, default
  selection): the tool stayed in the room and exited 0; no warnings or errors; 1.4–1.5 cores
  (median 1.505, max 1.535) for the tool and its 42 ffmpeg/ffplay processes; their summed
  resident memory 1.54 → 1.63 GB. The 30 min 08 s grid recording: 53 906 frames (29.8 fps),
  no freeze over 1 s (`freezedetect`), no silence over 1 s; 9 tiles at 180p with names.
- [ ] Not checked: 100 participants (SC-005's figure; 20 here); the tiles following the
  speaker (the bots all send the same tone, so the dominant speaker hardly changes); the
  unpinned 360 → 180 step (default height already 180).

Found and fixed during the check:

- With a 2.4M budget the level climbed twice in 2 s and overshot (6 videos, 2.47 Mbps),
  then came back down: the window after a move still measured the old videos. Windows are
  now ignored for 5 s after each move.

## Quickstart end to end (T069, T070)

2026-10-29, local Jitsi, ffmpeg n8.1.3. Headless: a looping "Clip" publisher, checked by a
second participant rather than a browser.

- [x] `go vet ./... && go test -race ./...` green; live tests 6/6 pass (latency: video
  62–87 ms, audio 87–95 ms to the callbacks over three runs).
- [x] `go build`, `go vet` and every test package compile for Windows and macOS (amd64 and
  arm64).
- [ ] CI on Linux, macOS and Windows (SC-007): not run, the repository has no remote yet.
- [x] Scenario 1: "Clip" published and recorded by a second participant (58.6 s, 640x360,
  H.264 and AAC); Ctrl+C ends it with code 0 in under 0.1 s.
- [x] Scenario 3: the example program printed `Clip: 285 frames` after 10 s. The quickstart
  said to run it with `go test -run Example`, which only compiles it; corrected.
- [x] Scenario 6: per-participant and mixed recordings, 60 s each, decode without errors;
  Ctrl+C exits 0 and both files play.
- [x] Scenario 10: no ffmpeg or ffplay left after any run. Edge-case and exit tests (T066)
  cover Ctrl+C, SIGKILL and a lost connection automatically.
- [ ] Not run again today, need a person: the browser view (1, 2), OBS Media Sources (5) and
  WHIP in real OBS (9), VLC playback (6), a token server and a wrong token (4). Scenarios 7
  and 8 were checked on 2026-10-28 (above).

Found and fixed during the run (092e143):

- The mixed recording's frames were 21–48 ms apart, and ffmpeg guessed 179/6 fps and warned
  of repeated timestamps when transcoding it. Frames were stamped when queued, after drawing,
  so drawing time became jitter. They now carry their clock tick's time: 33–34 ms apart,
  30/1 fps, in both kinds of file.

## Docker image (T071), 2026-10-29

Image `jitsi-media-tools` (297 MB) against the local Jitsi; publisher ffmpeg 8.1 `-f whip`
on the host stands in for OBS.

- [x] `--network host`: one container publishes `file-loop:` the sample clip, another
  records it (13.8 s, H.264 and AAC, decodes without errors). `docker stop` ends both with
  code 0 in 0.1–0.2 s; the recording is complete.
- [x] `compose.yaml` (bridge network, published ports, `--public-ip`): the WHIP stream joins
  and a recorder on the host gets it (12.9 s, 385 frames).
- [x] Bridge network, `--obs --obs-grid --obs-url-host <host IP>`, ports 9000–9002
  published: the feed URLs name the host, and the host's ffmpeg reads the participant feed
  (640x360, 30 fps, audio).
- [x] `docker buildx build --platform linux/arm64` on an amd64 machine without QEMU: arm64
  binaries, and every library the arm64 ffmpeg needs is in the image. Not run on arm64.
- The local Jitsi advertises only its container address (no `JVB_ADVERTISE_IPS`), which a
  container on another bridge network cannot reach; the bridge tests attached the
  container to Jitsi's network as well, and pointed `localhost` at the host.

Found and fixed (334ef68): with `--public-ip`, the gateway answered no WHIP publisher whose
packets arrived on an interface other than the first one listened on (loopback), so on a
NATed server or in a container the DTLS handshake timed out. Running natively on the host
it failed too as soon as `--public-ip` was set.
