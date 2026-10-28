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
- [ ] Watched in a browser (to do by hand).

Found and fixed during the check:

- The camera gave 10 fps: in raw YUYV at 1280x720 it fills USB 2. The tool now asks ffmpeg
  for the camera's compressed formats and captures Motion-JPEG when offered.
- ffmpeg 8 lists V4L2 as `video4linux2,v4l2`; the capability probe read the first alias
  only, so a device need of `v4l2` looked missing. Both aliases now count.
