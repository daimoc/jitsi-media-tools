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
