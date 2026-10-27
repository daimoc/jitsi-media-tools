# Quickstart: validating Jitsi Media Session (001)

## Prerequisites

- Go (code kept 1.23-compatible) and ffmpeg ≥ 6 with ffplay (libvpx, libopus, libx264,
  aac, srt): `sudo apt install ffmpeg`, or `./scripts/get-ffmpeg.sh` (pinned build in
  `.tools/ffmpeg`, no root), then `source scripts/env.sh`.
- The local Jitsi server at `https://localhost:8443`: `tests/platforms/jitsi`
  (`./setup.sh && docker compose up -d`, see its README).
- A browser in the same room, e.g. `https://localhost:8443/IntriguingClimatesSupposeProbably`.
- OBS (for scenario 5).

## Automated checks

```bash
go vet ./... && go test -race ./...                       # unit + ffmpeg tests (skip without ffmpeg)
JMT_URL=https://localhost:8443/jmt-live JMT_INSECURE=1 \
  go test -tags live -count=1 -v ./tests/live/             # end-to-end against local Jitsi
```

## Hand scenarios

Let `R=https://localhost:8443/IntriguingClimatesSupposeProbably` and
`jmt='go run ./cmd/jitsi-media-tools'`.

1. **Publish a file** (US1): `$jmt join --url $R --insecure --name Clip --publish file-loop:clip.mp4`
   → the browser shows "Clip" playing the clip within 10 s; Ctrl+C removes it within 5 s.
2. **Display** (US2): `$jmt join --url $R --insecure --name Viewer --display`
   → a window with the browser participants as named tiles, their audio audible; joining
   another browser tab adds a tile within 5 s.
3. **Library** (US3): `go run ./jmt/example_test.go`-style example (`go test -run Example ./jmt`
   with `JMT_URL` set) prints participants and frame counts, and the browser shows its test
   pattern.
4. **Guest / token** (US5): join a real deployment as guest with only its URL; join a
   token-auth server with `--jwt` or `--jwt-app-id` + `JITSI_APP_SECRET`; a wrong token exits 1
   with "authentication failed".
5. **OBS feeds** (US4): `$jmt join --url $R --insecure --name Feeds --obs --obs-grid`. In OBS add a
   Media Source (uncheck "Local file", input `srt://127.0.0.1:9001`) → a browser participant
   appears; leave and rejoin in the browser → the same source shows them again.
   `srt://127.0.0.1:9000` shows the grid.
6. **Record** (US6): `--record /tmp/rec` then `--record /tmp/rec --record-mode mixed`, 1 min each
   → files play in ffplay/VLC, also after Ctrl+C.
7. **Devices** (US7): `$jmt devices`, then `--publish device:"<camera>","<mic>"`.
8. **Large room** (US8): start the load engine with 20 bots in the same room
   (`go run ./cmd/jitsi-connect … --participants 20 --media audio_video` in webrtc-load-engine),
   then `$jmt join … --display --pin "<bot name>"` → at most 9 tiles, the pinned one always shown.
9. **OBS into the meeting** (US9): `WHIP_API_SECRET=s3cret $jmt serve --insecure`. In OBS 30+ set
   Settings → Stream → Service "WHIP", Server
   `http://127.0.0.1:8080/whip/localhost:8443/test?name=OBS`, Bearer Token `s3cret`; with a
   browser in the room, start streaming → "OBS" joins and the browser shows the scene and plays
   its audio; stop streaming → "OBS" leaves within 2 s. A wrong token → OBS reports an error and
   nobody joins.
10. **No leftovers** (SC-008): after each run, `pgrep -a 'ffmpeg|ffplay'` prints nothing.
