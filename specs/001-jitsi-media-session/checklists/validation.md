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
