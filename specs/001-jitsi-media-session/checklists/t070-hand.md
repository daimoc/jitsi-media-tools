# T070 hand checks

What T070 still needs is someone looking at a screen or using OBS. The headless parts ran on
2026-10-29 (validation.md). Tick each box that passes, and write a short note under each one
that fails or surprises you: the step number, what you saw, and the tool's last log lines if
they help. The results then go into validation.md and T070 is ticked.

About an hour in all. You can do the sections in any order. Section F changes the local
server's authentication, so do it last.

## Setup (once)

```bash
cd jitsi-media-tools
source scripts/env.sh
(cd tests/platforms/jitsi && docker compose up -d)
R=https://localhost:8443/t070
jmt='go run ./cmd/jitsi-media-tools'
```

- Open `$R` in Chrome as "Me", with the camera and microphone on, and keep it open.
- After each run, `pgrep -a 'ffmpeg|ffplay'` must print nothing. If it prints something,
  note which step it was.

## A. Publish a file (scenario 1)

```bash
$jmt join --url $R --insecure --name Clip --publish file-loop:samples/clip.mp4
```

- [ ] A1. In Chrome, "Clip" appears and plays the clip with its sound within 10 s.
- [ ] A2. Ctrl+C: "Clip" leaves Chrome's view within 5 s. The tool exits with code 0
  (`echo $?`), and no ffmpeg is left.

## B. Display (scenario 2)

```bash
$jmt join --url $R --insecure --name Viewer --display
```

- [ ] B1. A window opens with a tile named "Me" showing your camera, and you hear your
  microphone. Mute Chrome's speakers or use headphones, to avoid echo.
- [ ] B2. Open `$R` in a second Chrome tab: a second tile appears within 5 s. Close the tab:
  the tile goes away within 5 s.
- [ ] B3. Close the window: the tool leaves the room and exits with code 0.

## C. OBS reads the meeting (scenario 5)

```bash
$jmt join --url $R --insecure --name Feeds --obs --obs-grid
```

The tool prints one `srt://` address per participant, plus the grid on port 9000.

- [ ] C1. In OBS, add Sources → Media Source, uncheck "Local file", and set the input to
  `srt://127.0.0.1:9001`. Your camera from Chrome appears, with sound, within about 2 s.
- [ ] C2. A second Media Source on `srt://127.0.0.1:9000` shows the grid.
- [ ] C3. Reload the Chrome tab, so you leave and rejoin. Within about 5 s the
  `srt://127.0.0.1:9001` source shows you again, without being touched.
- [ ] C4. Ctrl+C the tool: both OBS sources stop, and no ffmpeg is left.

## D. Record and play in VLC (scenario 6)

```bash
$jmt join --url $R --insecure --name Rec --record /tmp/t070-rec        # 1 min, then Ctrl+C
$jmt join --url $R --insecure --name Mix --record /tmp/t070-mix --record-mode mixed   # same
```

- [ ] D1. `/tmp/t070-rec` holds one `.mkv` named after "Me". It plays in VLC from start to
  end, with picture and sound in sync, and seeking works.
- [ ] D2. The `/tmp/t070-mix` file plays in VLC and shows the grid with the room's mixed
  sound.
- [ ] D3. Kill a recording with `kill -9 <pid>` instead of Ctrl+C. Its file still plays in
  VLC up to about the kill. Matroska needs no final index for that.

## E. OBS into the meeting over WHIP (scenario 9)

```bash
WHIP_API_SECRET=s3cret $jmt serve --insecure --listen :8090
```

In OBS 30+, set Settings → Stream → Service "WHIP":
- Server: `http://127.0.0.1:8090/whip/localhost:8443/t070?name=OBS`
- Bearer Token: `s3cret`

In Settings → Output (Advanced mode), set the keyframe interval to 1 s.

**H.264.** Use encoder x264 or a hardware H.264 encoder.
- [ ] E1. Start streaming: "OBS" joins Chrome's view. Its scene shows within about 2 s and
  its sound plays.
- [ ] E2. In Chrome's connection stats for "OBS" (the signal-bars icon on its tile), a
  bitrate and a quality show, not blanks.
- [ ] E3. Stop streaming: "OBS" leaves within 2 s.

**AV1.** Use encoder SVT-AV1, AOM AV1, or a hardware AV1 encoder (NVENC, QSV, AMF). Keep
the 1 s keyframe interval.
- [ ] E4. Start streaming: the gateway logs "Forwarding AV1 video…", and Chrome shows the
  scene within about 2 s.
- [ ] E5. In `chrome://webrtc-internals`, the inbound video from "OBS" has codec
  `video/AV1`, a steady frame rate, and frames decoded increasing.
- [ ] E6. Run 2 minutes with a moving scene (a video or a clock): no freeze, no growing
  delay.
- [ ] E7. Optional: open `$R` in Firefox, or in Safari on a Mac without AV1 hardware. Note
  whether "OBS" shows its video or only its name.

**Errors.**
- [ ] E8. With Bearer Token `wrong`, starting the stream makes OBS report an error, and
  nobody joins.
- [ ] E9. With encoder HEVC, if your OBS has one, OBS reports an error. The gateway logs a
  refusal naming H.264 and AV1.

## F. Tokens (scenario 4)

Optional first step: join a public deployment as a guest with only its URL, for example
`$jmt join --url https://meet.jit.si/<a room> --name Guest --display`.
- [ ] F0. It joins, or waits for a moderator, and says so clearly. Skip this if no public
  server lets guests in.

**Turn on token authentication.** In `tests/platforms/jitsi/.env`, uncomment the token block
(`ENABLE_AUTH=1`, `AUTH_TYPE=jwt`, `JWT_APP_ID=my_app`) and set `JWT_APP_SECRET` to a value of
your choice. Then run `docker compose up -d`.

```bash
JITSI_APP_SECRET=<that secret> $jmt join --url $R --insecure --name Tok --jwt-app-id my_app --jwt-moderator --display
JITSI_APP_SECRET=nope          $jmt join --url $R --insecure --name Bad --jwt-app-id my_app
$jmt join --url $R --insecure --name NoTok
```

- [ ] F1. The first command joins and the window opens.
- [ ] F2. The wrong secret exits with code 1 and says "authentication failed".
- [ ] F3. Without a token, the tool exits with a clear error, not a hang.
- [ ] F4. WHIP with a token: in OBS, append `&jwt=<a token>` to the server URL. Print a
  token with any JWT tool, using the app id and secret above. Streaming joins.

**Afterwards:** comment the block out again and run `docker compose up -d`. The other tests
use a server without authentication.

## Result

| Section | Pass | Notes |
|---|---|---|
| A. Publish | | |
| B. Display | | |
| C. OBS feeds | | |
| D. Record / VLC | | |
| E. WHIP H.264 / AV1 | | |
| F. Tokens | | |

OBS version: ____  AV1 encoder used: ____  Chrome version: ____
