# Research: Jitsi Media Session (001)

Decisions taken for the plan. Each one: decision, rationale, alternatives considered.

## R1. Go and module layout

- **Decision**: module `github.com/daimoc/jitsi-media-tools`, `go 1.23.0` in `go.mod`
  (code stays 1.23-compatible; the machine's toolchain is newer). Public package at
  `jmt/` (import `github.com/daimoc/jitsi-media-tools/jmt`); everything else under
  `internal/`; CLI at `cmd/jitsi-media-tools`.
- **Rationale**: Principle I (library-first): only `jmt` is importable, so the CLI cannot
  reach around the library. A short package name reads well in user code (`jmt.Join`).
- **Alternatives**: public package at the module root (name clash with the hyphenated
  module name, clutter); several public packages (more API surface to keep stable).

## R2. What is copied from webrtc-load-engine

- **Decision**: copy into `internal/jitsi` the whole of `src/platform/jitsi` (signaling,
  Jingle↔SDP, sources, bridge channel, disco, framing, JWT) with its tests and golden
  files, `src/platform/internal/jwt` to `internal/jitsi/jwt`, and the `src/platform`
  interface file to `internal/signaling`. Copy `consent.go`, `twcc.go` and the
  PeerConnection/media-engine setup from `src/execution/{pion,peer}.go` into
  `internal/peer`, dropping the scenario state machine, stats, metrics, quality and
  simulcast-layer code. Copy RTP packetization/pacing from `src/media/source.go` into
  `internal/rtpmedia` (keeping RTP timestamps starting at 2^30: 2^31 makes the bridge
  rebase video). The multi-bot `Adapter` map is replaced by one client per session.
- **Rationale**: this code already joins Jitsi rooms reliably; the projects are
  independent (copy, not shared module), per the user.
- **Alternatives**: rewriting signaling (risk, no gain); depending on the load engine as
  a module (rejected by the user).

## R3. ffmpeg availability

- **Decision**: ffmpeg ≥ 6.0 and ffplay (display only) are runtime requirements, found in
  PATH or given with `--ffmpeg` / `--ffplay` (`jmt.Options.FFmpeg`). At start the tool runs
  `ffmpeg -version`, `-encoders`, `-decoders`, `-protocols` once and checks the parts the
  session needs (libvpx, libopus, libx264, aac, srt, rtmp, the OS capture devices); a
  missing part fails before joining with its name. The development machine currently has
  **no ffmpeg installed** (`apt install ffmpeg` provides ffmpeg + ffplay with libsrt,
  libvpx, libopus, libx264). CI installs it per OS (apt, brew, choco).
- **Rationale**: Principle IV; the check gives the clear error the spec asks for (FR-023).
- **Alternatives**: bundling ffmpeg (size, licences); cgo bindings (forbidden).

## R4. Moving media between Go and ffmpeg

- **Decision**: each ffmpeg process gets its main stream on stdin/stdout, and any
  extra stream through a **loopback TCP socket**: Go listens on `127.0.0.1:0` and passes
  `tcp://127.0.0.1:<port>` to ffmpeg (as input or output). One helper (`internal/ffmpeg`)
  starts processes, wires pipes and sockets, logs stderr at debug level, and kills the
  process (and closes its sockets) when its context ends. Processes are started with
  `exec.CommandContext`; on Linux/macOS they also get `Pdeathsig`/process-group handling
  where available, on Windows a Job Object, so none outlive the tool (SC-008), in
  `proc_{linux,darwin,windows}.go`.
- **Rationale**: `exec.Cmd.ExtraFiles` (extra pipe fds) does not work on Windows; loopback
  TCP works the same on all three OSes (Principle V). stdin/stdout keep the common case
  simple.
- **Alternatives**: named pipes (different APIs per OS); temporary files (latency).

## R5. Publishing

- **Decision**: every publish source is an ffmpeg *input* in one encoder process:
  file (`-re`, `-stream_loop -1` for loop), devices (per-OS input format, R6), OBS
  (`-listen 1 -i rtmp://0.0.0.0:<port>/live/obs` or `-i srt://0.0.0.0:<port>?mode=listener`),
  or library frames (`-f rawvideo -pix_fmt yuv420p -s WxH -r 30 -i pipe:0` plus
  s16le 48 kHz stereo as NUT, `-f nut -i tcp://…`, one packet per 20 ms chunk: ffmpeg
  reads raw `-f s16le` in 85 ms blocks; revised 2026-10-25, docs/latency.md). Library
  frames skip the `fps` filter, which holds each frame until the next one, and wait in
  2-item queues. The encoder outputs VP8 (libvpx, realtime,
  `-deadline realtime -cpu-used 8 -lag-in-frames 0 -error-resilient 1`, keyframe every 2 s)
  as IVF to stdout and Opus (48 kHz, 20 ms frames) as Ogg to a loopback socket. Go parses
  IVF/Ogg frames (parsers copied from `src/media/content.go`) and paces them into
  `TrackLocalStaticRTP` tracks. Default publish size 1280x720@30, configurable; one video
  layer (no simulcast in this feature). Keyframe on PLI: the encoder is asked for frequent
  keyframes instead (2 s), since ffmpeg cannot be poked at runtime.
- **Live source loss** (FR-010): when the encoder exits or stops producing (OBS stops),
  Go sends a placeholder (black frame, silence, encoded once by a short ffmpeg run at
  start) and restarts the encoder with back-off; OBS listener inputs are restarted the
  same way, so OBS can reconnect to the same address.
- **Rationale**: one path for all sources; VP8/Opus is what every Jitsi client decodes and
  what the copied code already negotiates.
- **Alternatives**: simulcast (3 live encodes, CPU; the bridge can still forward one layer
  to everyone; may come later); H.264 publish (licence/encoder availability varies).

## R6. Capture devices per OS

- **Decision**: `internal/ffmpeg/devices_{linux,darwin,windows}.go` give the input args and
  the device listing: Linux `-f v4l2 -i /dev/videoN` and `-f pulse -i <source>` (listing:
  `/sys/class/video4linux/*/name` and `pactl list short sources`, falling back to
  `-f alsa`); macOS `-f avfoundation -list_devices true -i ""` and `-i "<video>:<audio>"`;
  Windows `-f dshow -list_devices true -i dummy` and `-i video="<name>":audio="<name>"`.
  Listing parses ffmpeg/OS output with tests on recorded output from each OS.
- **Rationale**: Principle V, one dispatch point per concern; parsing is unit-testable on
  any OS from recorded text.
- **Alternatives**: a Go device library (cgo).

## R7. Receiving and decoding

- **Decision**: the peer answers the bridge's offer with VP8, VP9 and H.264 for video and
  Opus for audio (AV1 not offered). For each received stream Go depacketizes RTP (a frame
  assembler with Pion's VP8/VP9/H.264 depacketizers, releasing a frame at its marker
  packet; Pion's `samplebuilder` waits for the next frame, one frame interval more —
  revised 2026-10-25) into IVF (VP8/VP9) or Annex-B
  (H.264), and Opus into Ogg, written to one ffmpeg decoder per stream on stdin; the
  decoder writes raw frames on stdout:
  - video: `yuv420p` scaled and padded to a **fixed 16:9 size per participant** (the
    height asked for: 180 → 320x180 by default, 720 → 1280x720 for a high-res pin), so the
    bridge's layer switches never change the frame size; `-fps_mode passthrough`;
  - audio: `s16le`, 48 kHz, stereo.
  Video decoders exist only for the selected participants (≤ limit). Audio decoders are
  started on a participant's first audio packet and stopped after 10 s without packets,
  so muted participants (who send nothing in Jitsi) cost nothing. Missing keyframes:
  send PLI when a decoder starts and when the depacketizer sees a gap.
- **Rationale**: decoding through ffmpeg (Principle IV); fixed frame size keeps the raw
  pipes simple and the library's frames predictable; lazy audio decoders bound process
  count in 100-person rooms (Principle VI).
- **Alternatives**: one ffmpeg for all streams (inputs cannot be added at runtime); pure-Go
  Opus decoder (no complete one exists: CELT missing); remuxing without decoding (the
  library needs decoded frames).

## R8. Selection and the bridge

- **Decision**: the bridge does the speaker-based choice. The session sends
  `ReceiverVideoConstraints` with `lastN` = limit (default 9), `selectedSources` /
  `onStageSources` = the pinned participants' video source names, `defaultConstraints`
  `{maxHeight: 180}` and per-source `{maxHeight: 720}` for high-res pins. It re-sends when
  pins or participants change. The order of tiles follows `DominantSpeakerEndpointChangeEvent`
  (and its `previousSpeakers`) from the bridge channel; pinned tiles come first. Pins are
  display names matched case-insensitively against current participants, re-applied when
  someone joins or renames. More pins than the limit: the first `limit` win and a warning
  is logged.
- **Rationale**: matches Jitsi's own clients; avoids receiving video the tool then drops.
  The copied `bridge_channel.go` already sends these messages (with a different policy);
  it is extended, not rewritten.
- **Alternatives**: receiving everything and choosing locally (bandwidth, decoders).

## R9. Participant identity

- **Decision**: `internal/jitsi` reports MUC occupants from presence: endpoint id
  (occupant nick / resource), display name (`<nick xmlns='http://jabber.org/protocol/nick'>`),
  audio/video muted, joins, leaves and renames, and keeps mapping Jingle sources to their
  owner (`ssrc-info owner`, already done in `sources.go`). Display names are made unique
  in the session with a ` (2)` suffix in join order. Feed addresses and recording file
  names use the unique name.
- **Rationale**: FR-014; source ownership works because the copied code does not
  advertise SSRC rewriting (checked in `disco.go`).
- **Alternatives**: endpoint ids only (not usable for pins or OBS scenes).

## R10. Composition, mixing and A/V sync

- **Decision**: Go owns the clock. A compositor ticks at 30 fps and keeps the latest
  frame of each selected participant; it builds the grid (Jitsi tile layout: columns =
  ceil(sqrt(n)), 16:9 tiles, black background, name label bottom-left) in `yuv420p` with a
  bilinear scaler and labels drawn with `golang.org/x/image/font/basicfont` (scaled 2x).
  A mixer ticks every 20 ms, takes 960 samples from each active participant's jitter
  buffer (60 ms target, drop/insert silence to hold it), sums with saturation. Every
  consumer receives constant-rate streams (one frame per 33.3 ms, one chunk per 20 ms), so
  ffmpeg consumers read `-f rawvideo -r 30` and `-f s16le -ar 48000 -ac 2` and stay in
  sync by construction. Per-participant streams (feeds, recordings) are built the same
  way: latest frame repeated, placeholder card (name on dark grey) when there is no video,
  silence when there is no audio.
- **Rationale**: constant-rate raw streams keep audio and video aligned (SC-004) without
  timestamp plumbing between processes; the grid in Go also serves the library.
- **Alternatives**: ffmpeg `xstack`/`drawtext` filters (graph rebuilt on every join;
  drawtext needs fontconfig and font files that differ per OS).
- **New module**: `golang.org/x/image` (font drawing), justified in the plan.

## R11. Outputs

- **Decision**:
  - **Display**: the session writes grid video + mixed audio as one NUT stream straight
    into `ffplay -fflags nobuffer -flags low_delay -framedrop -f nut -i pipe:0`, each frame
    and chunk stamped with the time it was made (revised 2026-10-25: an ffmpeg muxing the
    two raw inputs counted frames and samples instead, so a stream that started later or
    lost data stayed behind and the muxer held the other one back, 0.1–0.3 s measured).
  - **Mixed recording / grid feed**: the same grid ffmpeg gets extra outputs (one process,
    several outputs): a Matroska file (H.264 `-preset veryfast` + AAC, readable everywhere,
    playable after Ctrl+C) and MPEG-TS over an SRT listener for the grid feed.
  - **Per participant**: one ffmpeg per participant who has an output, encoding H.264
    (`veryfast`, `zerolatency`) + AAC once and writing to its recording file and/or its SRT
    feed (`-f tee`).
  - **OBS feeds**: `srt://0.0.0.0:<port>?mode=listener&latency=120000` — the grid on
    `--obs-port` (default 9000), participants on the following ports assigned in order of
    first appearance and kept for the session (FR-017). Feeds exist for participants who
    have been in the video selection at least once, capped by `--obs-max-feeds` (default 16).
    The tool prints `name → srt://host:port` lines and writes them to `feeds.json` next to
    recordings when recording. An SRT listener with no caller blocks ffmpeg's output open:
    Go writes to consumers through bounded queues that drop the oldest data when full, and
    restarts an output process that exits (OBS disconnects) with back-off.
- **Rationale**: few processes; H.264/AAC MPEG-TS is what OBS Media Source plays best;
  stable ports make OBS scenes reusable.
- **Alternatives**: UDP multicast (no backpressure but unreliable over networks); RTMP out
  (needs a server); NDI (SDK, cgo).

## R12. Joining from a meeting URL

- **Decision**: fetch `https://<host>/<tenant?>/config.js` (and `?room=`), extract
  `hosts.domain`, `hosts.muc`, `hosts.anonymousdomain`, `hosts.focus`, `focusUserJid`,
  `websocket` and `bosh` with a small parser that accepts both styles (`config.hosts = {…}`
  object literal and `config.hosts.domain = '…';` assignments, as docker-jitsi-meet
  writes); flags override every value. Defaults when absent: MUC `conference.<domain>`,
  WebSocket `wss://<host>/xmpp-websocket`. With `anonymousdomain` set and no token, log in
  anonymously to that domain (guest). The `jwt` URL parameter is used as a token. Tested
  with recorded config.js files (docker-jitsi-meet, meet.jit.si style).
- **Guest waiting for host**: Jicofo answers the conference request with `ready='false'`
  or a not-authorized error while no authenticated host is in the room; the session
  retries every 5 s and reports "waiting for host" until `--wait-host` (default 5 min).
- **TLS**: `--insecure` skips certificate verification for HTTPS and WebSocket; otherwise
  normal verification (FR-005).
- **Rationale**: FR-002/003/005; one URL is how users share meetings.
- **Alternatives**: requiring all settings as flags (poor UX).
- **Open for implementation**: features newer real servers may require (SSRC rewriting,
  visitors) are added only if a target server refuses the guest without them.

## R13. Testing

- **Decision**: unit tests with golden stanzas for signaling (copied + new presence
  tests); `internal/ffmpeg` tests that skip when ffmpeg is absent, using `lavfi`
  (`testsrc2`, `sine`) sources; compositor/mixer tests in pure Go; a fake signaling +
  two in-process Pion peers for session tests; `tests/live` behind `-tags live` against
  `https://localhost:8443` (variables `JMT_URL`, `JMT_INSECURE`). CI: GitHub Actions on
  `ubuntu-latest`, `macos-latest`, `windows-latest`, installing ffmpeg, running
  `go vet ./... && go test -race ./...`.
- **Rationale**: Principle VII; tests run without a server or devices.

## R14. WHIP gateway for OBS (added 2026-10-27)

- **Decision**: OBS's way into a meeting is a WHIP gateway (RFC 9725), replacing the SRT/RTMP
  listener of a session (`obs-srt:`, `obs-rtmp:` are dropped).
  - **Library-first**: `jmt.Gateway` (options: listen address, secret, UDP port, public
    address, TLS files, stream cap, the same server overrides as a session); the CLI command
    `serve` maps flags to it. Public types carry addresses and names only (Principle II).
  - **Address**: `POST /whip/<host[:port]>/<room path>?name=<display name>[&jwt=…]` →
    meeting URL `https://<host[:port]>/<room path>[?jwt=…]`; the room path is the rest of
    the path, so a tenant (`/whip/meet.example.org/acme/Standup`) works as in a meeting URL. `201 Created` with the SDP answer and
    `Location: /whip/session/<random id>`; `DELETE` on it ends the stream. `OPTIONS` answers
    CORS. Trickle ICE (`PATCH`) is answered `405`: the gateway gives a complete answer and
    OBS does not need trickle. Bearer token compared in constant time with
    `WHIP_API_SECRET` (env, or `--secret-file`).
  - **Timing**: the WHIP answer cannot wait for the meeting's media (Jicofo offers media only
    once a second participant is there, which may be never). The gateway answers OBS as soon as
    the meeting is joined (the join fails → 4xx/5xx with the reason, nothing kept), then
    forwards packets once the Jitsi peer connects, dropping them before; when it connects, it
    asks OBS for a keyframe (PLI) so the room starts on one.
  - **Pass-through**: a second Pion peer per stream, facing OBS, with only H.264
    (packetization-mode 1) and Opus. Received RTP goes unchanged except payload type and SSRC
    (rewritten by the send track, `TrackLocalStaticRTP`) to an H.264 send track and an Opus
    send track on the Jitsi peer; the session's publish path is a new "forward" source with no
    encoder. PLI/FIR from the bridge on the Jitsi side → PLI to OBS. RTCP receiver reports and
    NACKs stay per hop (each peer's interceptors). No ffmpeg runs for a gateway stream.
  - **One layer**: OBS sends one H.264 layer and no simulcast, so the bridge cannot lower its
    resolution for a receiver with little bandwidth; the streamer sets a bitrate the room can
    take.
  - **Receive side**: gateway participants send a receiver constraint of 0 video and do not
    decode or play received audio.
  - **End**: DELETE, OBS's peer reaching ICE failed/closed, or 10 s without RTP → leave the
    meeting, close both peers. Gateway stop → all streams end; the HTTP server shuts down.
  - **Public server**: one UDP port for all OBS peers (Pion `ICEUDPMux`, `--udp-port`, default
    8189), `--public-ip` as a 1:1 NAT host candidate; plain HTTP by default, `--tls-cert`
    `--tls-key` for built-in HTTPS; a reverse proxy may terminate TLS instead. Cap
    `--max-streams` (default 10) → `503` past it.
- **Rationale**: OBS 30+ has a WHIP output built in, so nothing runs next to OBS; pass-through
  adds almost no delay or CPU, and OBS already encodes H.264 that Jitsi and browsers accept.
  Principle IV is kept: nothing is encoded or decoded, Go forwards packets as it already does
  for the encoder's frames.
- **Alternatives**: SRT/RTMP into ffmpeg and re-encode to VP8 (≈1 core and 50–100 ms per
  stream, one session per CLI run); re-encoding WHIP input to VP8 (same cost, only needed if a
  deployment refuses H.264 — left as a later fallback); per-stream tokens minted through
  `POST /api/v1/whip` (later feature).
- **Risks to check first** (spike in the first task): Jicofo's offer on the target
  deployments includes H.264, and the bridge forwards it to browsers; OBS's profile (High by
  default with x264) plays in Chrome and Firefox when the meeting negotiated
  `profile-level-id=42e01f`; the Jitsi peer can signal an H.264 send source.
