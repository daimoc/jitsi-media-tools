# Tasks: Jitsi Media Session

**Input**: Design documents from `/specs/001-jitsi-media-session/`

**Prerequisites**: plan.md, spec.md, research.md (R1–R13), data-model.md, contracts/library-api.md,
contracts/cli.md, quickstart.md

**Tests**: required (constitution VII, test-first): each story's tests are written first and
must fail before its implementation. ffmpeg tests call `ffmpegtest.Require(t)` and skip when
ffmpeg is absent. Source repo for copies: `../webrtc-load-engine` (abbreviated `LE/`).
Always `export PATH=/usr/local/go/bin:$PATH`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on unfinished tasks)
- **[Story]**: user story from spec.md (US1…US8)

---

## Phase 1: Setup (Shared Infrastructure)

- [X] T001 Create `go.mod` (module `github.com/daimoc/jitsi-media-tools`, `go 1.23.0`), `.gitignore` (`/recordings/`, `/jitsi-media-tools`, `*.exe`), `LICENSE` copied from `LE/LICENSE`, and empty `README.md`
- [X] T002 Copy `LE/src/platform/platform.go` (and any other non-test file of `LE/src/platform/*.go`) to `internal/signaling/` as `package signaling`, keeping `SignalingHandler`, `DataChannel`, `DataChannelHandler`, `TrickleFunc`; drop `PlatformAdapter`/`PublishSessionAdapter` if unused after T003
- [X] T003 (kept the copied `Adapter`, used with one peer id, to stay close to the origin and keep its fake-server tests) Copy `LE/src/platform/jitsi/` (all `.go`, `_test.go`, `testdata/`) to `internal/jitsi/` and `LE/src/platform/internal/jwt/` to `internal/jitsi/jwt/`; rewrite imports to `internal/signaling` and `internal/jitsi/jwt`; set `botIdentity` name to `jitsi-media-tools`; replace the multi-bot `Adapter` with a single-client constructor `jitsi.NewClient(logger, Config)` (keep `JingleConfig` fields as `Config`); `go test ./internal/jitsi/...` green
- [X] T004 [P] Copy RFC 7675 consent checks `LE/src/execution/consent.go` (+ test) and TWCC `LE/src/execution/twcc.go` (+ test) to `internal/peer/` as `package peer`, removing imports of scenario/metrics/hostload; tests green
- [X] T005 [P] Copy IVF and Ogg parsers (`parseIVF`, `parseOgg`, `isVP8Keyframe`, `Frame`) from `LE/src/media/content.go` and RTP pacing/packetizing from `LE/src/media/source.go` (keep the 2^30 initial RTP timestamp and its comment) to `internal/rtpmedia/` with their tests, removing embedded assets, layers and `SendTimes` measurement
- [X] T006 [P] Write import-boundary test `internal/boundary_test.go` using `go/build` (or `go list -deps -json`): `internal/jitsi`, `internal/signaling` must not import `internal/{rtpmedia,ffmpeg,compose,output,peer}`; `internal/{rtpmedia,ffmpeg,compose,output}` must not import `internal/{jitsi,signaling}`; `cmd/...` must import only `jmt` from this module
- [X] T007 [P] Add CI `.github/workflows/ci.yml`: matrix `ubuntu-latest`, `macos-latest`, `windows-latest`; Go 1.23.x; install ffmpeg (`apt-get install -y ffmpeg`, `brew install ffmpeg`, `choco install ffmpeg -y`); run `go vet ./...` and `go test -race ./...`
- [X] T008 Commit setup: `go mod tidy`, `go vet ./... && go test -race ./...` green

---

## Phase 2: Foundational (Blocking Prerequisites)

### ffmpeg runtime (R3, R4)

- [ ] T009 [P] Test then implement `internal/ffmpeg/ffmpegtest/require.go` (`Require(t)` skips without ffmpeg) and `internal/ffmpeg/probe.go`: `Locate(path string)`, `Probe(ctx) (*Caps, error)` parsing `-version` (major ≥ 6), `-encoders`, `-decoders`, `-protocols`, `-devices` into sets; `Caps.Missing(needs ...string) error` naming the missing parts (FR-023); tests on recorded outputs in `internal/ffmpeg/testdata/` plus a real-ffmpeg test
- [ ] T010 [P] Test then implement `internal/ffmpeg/loopback.go`: `Listen() (*Loopback, error)` on `127.0.0.1:0` with `URL()` (`tcp://127.0.0.1:port`), `Accept(ctx) (net.Conn, error)`, closed with its process
- [ ] T011 Test then implement `internal/ffmpeg/process.go`: `Start(ctx, Spec) (*Process, error)` where `Spec{Bin, Args, Stdin io.Reader?, Stdout bool, Name}`; stderr logged at debug with the name; `Stop()` closes stdin, waits 2 s, then kills; `Done() <-chan error`; plus `proc_unix.go` (`Setpgid`, kill process group; `Pdeathsig` on Linux under `//go:build linux`) and `proc_windows.go` (Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` via `golang.org/x/sys/windows`, justified: stdlib cannot do it); test that a `-f lavfi -i testsrc2 -f null -` process is gone after `Stop` and after context cancel (SC-008)
- [ ] T012 Implement restart supervision `internal/ffmpeg/supervise.go`: `Supervise(ctx, start func() (*Process, error), backoff)` restarting with back-off 0.5 s → 5 s, reporting restarts to a callback; test with a process that exits immediately

### Peer (R2, R7)

- [X] T013 Test then implement `internal/peer/peer.go` from `LE/src/execution/{pion,peer}.go`: `New(Config{Logger})` building a Pion API with VP8, VP9, H264 (packetization-mode=1) and Opus registered, TWCC + NACK + RTCP reports interceptors, consent; implements `signaling.SignalingHandler` (OnOffer → answer with trickle, OnICECandidate, OpenDataChannel, Renegotiate, OnSignalingLost); `AddSendTrack(kind) (*webrtc.TrackLocalStaticRTP, error)`; `OnTrack(func(RemoteTrack))` where `RemoteTrack{Kind, Codec, SSRC, SourceName, ReadRTP}`; `RequestKeyframe(ssrc)`; no stats/metrics; tests with two in-process peers (`LE/src/execution/peer_test.go` style)

### Connection settings and session skeleton (R12, R9)

- [ ] T014 [P] Test then implement `internal/jitsi/configjs.go`: `FetchConfig(ctx, meetingURL, insecure) (ServerConfig, room string, token string, err)` fetching `/<tenant>/config.js?room=`; parse both `config.hosts = {domain: '…', muc: '…', anonymousdomain: '…', focus: '…'}` object literals and `config.hosts.domain = '…';` assignments, plus `websocket`, `bosh`, `focusUserJid`; defaults MUC `conference.<domain>`, WebSocket `wss://<host>/xmpp-websocket`; `jwt` URL parameter as token; room lower-cased, rejected if empty or containing `/` or `@`; testdata: docker-jitsi-meet config.js and a meet.jit.si-style one; non-Jitsi page → error "not a Jitsi Meet server"
- [ ] T015 [P] Test then implement TLS option in `internal/jitsi`: `Config.Insecure` sets `InsecureSkipVerify` on the WebSocket dialer and config.js HTTP client; without it an invalid certificate fails with a message containing "certificate" (FR-005); test with `httptest.NewTLSServer`
- [ ] T016 Test then implement occupant tracking in `internal/jitsi/occupants.go`: from MUC presence (in `handlePresence`) emit `OccupantEvent{Kind: Joined|Left|Changed, ID (occupant nick), DisplayName (<nick xmlns='http://jabber.org/protocol/nick'>), AudioMuted, VideoMuted}` through `Client.OnOccupant(func(OccupantEvent))`; ignore our own occupant and focus; golden presence stanzas in `internal/jitsi/testdata/`
- [ ] T017 Test then implement bridge-channel events in `internal/jitsi/bridge_channel.go`: parse `DominantSpeakerEndpointChangeEvent` (`dominantSpeakerEndpoint`, `previousSpeakers`) into `Client.OnDominantSpeaker(func(id string, previous []string))`; add `Client.SetReceive(ReceivePolicy{LastN int, Selected []string, OnStage []string, DefaultMaxHeight int, MaxHeights map[string]int})` sending `ReceiverVideoConstraints` with those fields (replace the load-engine cap policy; keep the "open/reopen resends" behavior); golden JSON tests
- [ ] T018 Implement `internal/session/participants.go` (test first): participant registry per data-model "Participant": unique names (`""` → "Participant"; duplicates get ` (2)`, ` (3)` in join order; a rejoin gets its old unique name back if free), `LastSpoke`, `Pinned`, `Selected`; source-name → participant id mapping fed from Jingle source owners (`internal/jitsi/sources.go` owner, exposed as `Client.OnSourceOwner(func(sourceName, ssrc, ownerID, kind))`)
- [ ] T019 Implement `internal/session/session.go` (test first with a fake signaling client and in-process peer): state machine `connecting → waiting_host → joining → joined → leaving → closed`, any → `failed` (data-model "Session"); `Start(ctx, Config)` resolves settings (T014, overrides win), probes ffmpeg for the needed parts (T009) **before** contacting the server, connects, joins under the display name; `Close()` idempotent: leaves the room, stops every process, returns when all are gone; lost signaling → `failed` with the error
- [ ] T020 Implement the public skeleton `jmt/options.go`, `jmt/types.go`, `jmt/session.go` exactly as in contracts/library-api.md (`Options`, `Selection`, `DisplayOptions`, `RecordOptions`, `RecordMode`, `OBSOptions`, `Source`, `Participant`, `VideoFrame`, `AudioChunk`, `Event`, `EventKind`, `Join`, `Session` methods) delegating to `internal/session`; defaults: Selection `Limit 9` (max 25), `Height 180`, `PinHeight 720` (allowed 180/360/720), `WaitHost 5m`, OBS `Host "0.0.0.0"`, `BasePort 9000`, `MaxFeeds 16`; validation errors for out-of-range values; `Events()` closed at end
- [ ] T021 Implement CLI skeleton `cmd/jitsi-media-tools/main.go`: subcommands `join` and `devices`, flags of contracts/cli.md mapped to `jmt.Options`, event lines on stdout, slog on stderr (`--log-level`), Ctrl+C → `Close`, exit codes 0/1/2; test flag mapping in `cmd/jitsi-media-tools/flags_test.go`
- [ ] T022 Commit foundation; tests green

**Checkpoint**: the CLI joins the local room (`--insecure`) as a participant with no media and leaves on Ctrl+C.

---

## Phase 3: User Story 1 — Publish a media file into a room (P1) 🎯 MVP

**Goal**: participants see and hear a file published by the tool.

**Independent Test**: quickstart scenario 1.

### Tests for US1 ⚠️ write first

- [ ] T023 [P] [US1] Test `internal/ffmpeg/encoder_test.go`: an encoder built from a `lavfi` input (`testsrc2=size=1280x720:rate=30`, `sine`) yields IVF VP8 frames on stdout and Ogg Opus pages on the loopback, keyframe at least every 2 s, 20 ms Opus packets
- [ ] T024 [P] [US1] Test `internal/session/publish_test.go`: with fake signaling + in-process receiving peer, a file source produces RTP on both send tracks with increasing timestamps from 2^30; file-once ends the session with state `closed`; loop keeps sending past the file's duration

### Implementation for US1

- [ ] T025 [US1] Implement `internal/ffmpeg/encoder.go`: `EncoderArgs(Input, EncodeOptions)` and `StartEncoder(ctx, …) (*Encoder, error)` exposing `Video() <-chan rtpmedia.Frame` and `Audio() <-chan rtpmedia.Frame`; inputs: file (`-re`, `-stream_loop -1` when loop), raw program frames (used by US3), devices (US7), OBS (US4); VP8 `-c:v libvpx -deadline realtime -cpu-used 8 -lag-in-frames 0 -error-resilient 1 -g 60 -b:v 1500k`, scaled/padded to the publish size (default 1280x720), Opus `-c:a libopus -ar 48000 -frame_duration 20 -application voip`; audio-only / video-only (FR-009)
- [ ] T026 [US1] Implement `internal/rtpmedia/sender.go`: pace frames from the encoder into send tracks (from T005), placeholder frames (black VP8 keyframe + Opus silence encoded once at start) while the source has no data (FR-010)
- [ ] T027 [US1] Wire publishing in `internal/session/publish.go`: add send tracks before answering, announce sources in presence (existing `sourcesPresence`), start the encoder after ICE connects, `FileSource(path, loop)` in `jmt/source.go`; file not readable → error before joining
- [ ] T028 [US1] CLI `--publish file:PATH|file-loop:PATH` and `--publish-size WxH` in `cmd/jitsi-media-tools/flags.go`
- [ ] T029 [US1] Live test `tests/live/publish_test.go` (`//go:build live`, `JMT_URL`, `JMT_INSECURE`): session A publishes a lavfi-generated file; session B (receive only) gets RTP for both kinds within 10 s (SC-001)
- [ ] T030 [US1] Hand check quickstart scenario 1 against `https://localhost:8443/IntriguingClimatesSupposeProbably` in a browser; record result in `specs/001-jitsi-media-session/checklists/validation.md`; commit

**Checkpoint**: MVP — a file is seen and heard in a Jitsi room.

---

## Phase 4: User Story 2 — Watch the room in a tile grid (P1)

**Goal**: a window shows named tiles of the selected participants with mixed audio.

**Independent Test**: quickstart scenario 2.

### Tests for US2 ⚠️ write first

- [ ] T031 [P] [US2] Test `internal/rtpmedia/receiver_test.go`: RTP of VP8, VP9, H.264 and Opus (generated by packetizing known frames) is reassembled into IVF / Annex-B / Ogg byte streams identical frame-by-frame; a sequence gap triggers the keyframe-request callback
- [ ] T032 [P] [US2] Test `internal/ffmpeg/decoder_test.go`: IVF VP8 at 640x360 then 320x180 decodes to fixed 320x180 `yuv420p` frames (`w*h*3/2` bytes each); Ogg Opus decodes to `s16le` 48 kHz stereo
- [ ] T033 [P] [US2] Test `internal/compose/grid_test.go`: layout for n = 1…25 (columns = ceil(sqrt(n)), 16:9 tiles, centered), pinned first then by `LastSpoke` desc then join order; bilinear scale of a known pattern; label drawn bottom-left; camera-off tile = name card on dark grey
- [ ] T034 [P] [US2] Test `internal/compose/mixer_test.go`: 20 ms ticks of 960 stereo frames; saturation at int16 bounds; a late participant's jitter buffer holds ~60 ms (insert silence when empty, drop when > 120 ms)

### Implementation for US2

- [ ] T035 [US2] Implement `internal/rtpmedia/receiver.go`: per remote track, Pion `samplebuilder` with VP8/VP9/H264/Opus depacketizers → `ivfwriter`/Annex-B/`oggwriter` into an `io.Writer`; keyframe callback on gaps and at start
- [ ] T036 [US2] Implement `internal/ffmpeg/decoder.go`: `StartVideoDecoder(ctx, codec, height)` (`-fflags nobuffer -flags low_delay`, `-vf scale=W:H:force_original_aspect_ratio=decrease,pad=W:H:(ow-iw)/2:(oh-ih)/2`, `-pix_fmt yuv420p -fps_mode passthrough -f rawvideo pipe:1`; W = H*16/9) and `StartAudioDecoder(ctx)` (`-f s16le -ar 48000 -ac 2 pipe:1`), each with `Frames() <-chan` of fixed-size buffers
- [ ] T037 [US2] Implement `internal/compose/clock.go`, `grid.go`, `label.go` (with `golang.org/x/image/font/basicfont`, 2x scale), `card.go`, `mixer.go`, `jitter.go` per research R10; grid output size 1280x720 default, 30 fps, latest-frame-per-tile
- [ ] T038 [US2] Wire reception in `internal/session/receive.go`: on `OnTrack` map SSRC → participant (T018); start a video decoder only for selected participants (bridge decides, R8; initial `SetReceive{LastN: Limit, DefaultMaxHeight: Height}`); start audio decoders on first packet and stop after 10 s without packets; feed compositor and mixer; tiles follow joins/leaves within 5 s
- [ ] T039 [US2] Implement `internal/output/display.go`: one ffmpeg reading grid raw video (stdin, `-f rawvideo -pix_fmt yuv420p -s 1280x720 -r 30`) and mixed audio (loopback, `-f s16le -ar 48000 -ac 2`) and writing `-c:v rawvideo -c:a pcm_s16le -f nut pipe:1`, piped into `ffplay -fflags nobuffer -flags low_delay -framedrop -window_title <title> -`; consumers fed through bounded drop-oldest queues (`internal/output/queue.go`, test first)
- [ ] T040 [US2] `jmt.Options.Display` and CLI `--display`; ffplay required only then (probe message names ffplay)
- [ ] T041 [US2] Live test `tests/live/receive_test.go`: session A publishes testsrc2; session B with a grid callback sees a tile named after A within 5 s and non-silent mixed audio (SC-002); hand check quickstart scenario 2; commit

---

## Phase 5: User Story 3 — Use the session from another program (P2)

**Goal**: Go programs get decoded frames/samples with participant events, and publish raw frames.

**Independent Test**: quickstart scenario 3.

- [ ] T042 [P] [US3] Test `jmt/session_test.go` (fake signaling via an internal test hook): `Events()` yields `ParticipantJoined`/`Left`/`Changed`; `OnVideo` frames carry participant id and fixed size; `OnAudio` per participant and `OnMixedAudio` chunks have 1920 samples; buffers reused after the callback returns; a blocking callback drops frames but does not stall others
- [ ] T043 [P] [US3] Test `jmt/publisher_test.go`: `FrameSource(640, 360, true, true)` → `WriteVideo` with wrong size returns an error; written frames reach the send track as VP8
- [ ] T044 [US3] Implement callbacks and events in `jmt/session.go` + `internal/session/deliver.go` (non-blocking fan-out, one goroutine per callback with a 2-item queue, drop-oldest)
- [ ] T045 [US3] Implement `jmt/publisher.go` (`FrameSource`, `Publisher.WriteVideo/WriteAudio/Close`) feeding the encoder's raw input (stdin video `-f rawvideo -pix_fmt yuv420p -s WxH -r 30`, loopback audio `-f s16le -ar 48000 -ac 2`)
- [ ] T046 [US3] Write `jmt/doc.go` package docs and `jmt/example_test.go` (`ExampleJoin`: join, count frames per participant, publish a moving test pattern; under 30 lines of user code, SC-006; skipped without `JMT_URL`); commit

---

## Phase 6: User Story 4 — Bridge OBS into and out of the meeting (P2)

**Goal**: OBS streams into the meeting; participants and the grid are SRT feeds for OBS Media Sources.

**Independent Test**: quickstart scenario 5.

- [ ] T047 [P] [US4] Test `internal/output/feeds_test.go`: port assignment — grid on BasePort, participants BasePort+1… in order of first selection, same port after leave/rejoin with the same unique name, at most MaxFeeds (default 16), `feeds.json` content `{"grid": "srt://host:9000", "Alice": "srt://host:9001"}`
- [ ] T048 [P] [US4] Test `internal/ffmpeg/srt_test.go` (needs ffmpeg with srt): an output to `srt://127.0.0.1:P?mode=listener` is readable by a second ffmpeg caller; when the caller leaves, the supervised output restarts and accepts a new caller
- [ ] T049 [US4] Implement OBS input in `internal/ffmpeg/encoder.go`: `OBSInput{Protocol: srt|rtmp, Listen}` → `-i srt://ADDR?mode=listener` or `-listen 1 -i rtmp://ADDR/live/obs`; supervised so OBS can reconnect; placeholder while no stream (FR-010); `jmt.OBSSource`; CLI `--publish obs-srt::PORT|obs-rtmp::PORT`; print `obs input <url>`
- [ ] T050 [US4] Implement `internal/output/feeds.go`: per participant feed = constant-rate tile stream (latest frame repeated, name card when no video, silence when no audio) at 1280x720 (pinned high-res) or 640x360, encoded `-c:v libx264 -preset veryfast -tune zerolatency -g 60 -c:a aac -f mpegts srt://HOST:PORT?mode=listener&latency=120000`, supervised (T012), fed through drop-oldest queues; grid feed from the compositor when `Grid`; `FeedReady` events and stdout lines `feed NAME srt://host:port` (FR-017, FR-018)
- [ ] T051 [US4] `jmt.Options.OBS`, `Session.FeedURLs()`, CLI `--obs --obs-host --obs-port --obs-max-feeds --obs-grid`; live test `tests/live/obs_test.go` reading a participant feed with ffmpeg as SRT caller; hand check quickstart scenario 5 with OBS; commit

---

## Phase 7: User Story 5 — Join real deployments as a guest or with a token (P2)

**Goal**: guests and token users join real servers from the meeting URL.

**Independent Test**: quickstart scenario 4.

- [ ] T052 [P] [US5] Test `internal/jitsi/guest_test.go`: with `anonymousdomain` set and no token, SASL ANONYMOUS against that domain; conference IQ answered `ready='false'` or `not-authorized` → retry every 5 s, `OnWaitingForHost` fired once, joined when ready, error "timed out waiting for host" after `WaitHost` (golden IQs)
- [ ] T053 [P] [US5] Test JWT paths in `internal/jitsi/jwt_test.go` (copied tests + new): ready token from `--jwt`, from URL `?jwt=`, signed from AppID/AppSecret with audience/subject/TTL/moderator; rejected auth → error containing "authentication failed"
- [ ] T054 [US5] Implement guest login and wait-for-host retry in `internal/jitsi/signaling.go` / `internal/jitsi/guest.go`; `WaitingForHost` event and session state `waiting_host`; CLI `--wait-host`
- [ ] T055 [US5] Map auth errors to "authentication failed" in `internal/jitsi`; CLI `--jwt` / `$JITSI_JWT`, `--jwt-app-id` + `$JITSI_APP_SECRET`, `--jwt-audience`, `--jwt-subject`, `--jwt-ttl`, `--jwt-moderator`; server override flags `--domain --muc --anonymous-domain --ws --focus`
- [ ] T056 [US5] Hand check: join a real deployment as guest with only its URL; if the server refuses a guest for lack of SSRC rewriting or visitor support, record it in `checklists/validation.md` and open a follow-up feature (do not implement here, R12); commit

---

## Phase 8: User Story 6 — Record the meeting (P3)

**Goal**: per-participant files or one mixed file, playable after Ctrl+C.

**Independent Test**: quickstart scenario 6.

- [ ] T057 [P] [US6] Test `internal/output/record_test.go` (ffmpeg): per-participant and mixed modes write `<dir>/<room>-<YYYYMMDD-HHMMSS>-<name>.mkv` / `…-mixed.mkv`; after SIGINT-equivalent stop (`Process.Stop`) the file is readable by `ffprobe`-equivalent (`ffmpeg -i file -f null -`) with audio and video
- [ ] T058 [US6] Implement `internal/output/record.go`: mixed = extra Matroska output on the grid ffmpeg (`-c:v libx264 -preset veryfast -c:a aac`, `-flush_packets 1`, `-cluster_time_limit 1000`); per participant = Matroska output on the participant's feed ffmpeg via `-f tee` (`[f=mpegts]srt://…|[f=matroska]file.mkv`) or a dedicated ffmpeg when no feed; file names sanitized (no path separators)
- [ ] T059 [US6] `jmt.Options.Record`, CLI `--record DIR --record-mode per-participant|mixed`; `feeds.json` written in DIR when OBS is on; hand check scenario 6; commit

---

## Phase 9: User Story 7 — Publish from a camera and microphone (P3)

**Goal**: list devices and publish a chosen camera and microphone on Linux, macOS, Windows.

**Independent Test**: quickstart scenario 7.

- [ ] T060 [P] [US7] Tests `internal/ffmpeg/devices_test.go` on recorded listings in `internal/ffmpeg/testdata/devices/{linux-v4l2.txt,linux-pactl.txt,darwin-avfoundation.txt,windows-dshow.txt}` (parsers are OS-independent, so all run on every OS)
- [ ] T061 [US7] Implement listing parsers `internal/ffmpeg/devices_parse.go` and per-OS dispatch `devices_linux.go` (`/sys/class/video4linux/*/name` + `pactl list short sources`, ALSA fallback; input `-f v4l2 -i /dev/videoN`, `-f pulse -i <source>`), `devices_darwin.go` (`-f avfoundation -list_devices true -i ""`; input `-f avfoundation -i "<video>:<audio>"`), `devices_windows.go` (`-f dshow -list_devices true -i dummy`; input `-f dshow -i video="<v>":audio="<a>"`)
- [ ] T062 [US7] `jmt.ListDevices`, `jmt.DeviceSource`; unknown device → error listing available ones before joining; CLI `devices` subcommand and `--publish device:VIDEO,AUDIO`; hand check on Linux (scenario 7); commit

---

## Phase 10: User Story 8 — Stay usable in large rooms (P3)

**Goal**: bounded video with speaker-following and pins by name.

**Independent Test**: quickstart scenario 8.

- [ ] T063 [P] [US8] Test `internal/session/selection_test.go`: policy → `ReceivePolicy` (`LastN = Limit`; pinned participants' video source names in `Selected` and `OnStage`; `DefaultMaxHeight = Height`; `MaxHeights[pinSource] = PinHeight`); pins matched case-insensitively, re-applied on join/rename; more pins than Limit → first Limit kept + one warning; re-sent on change
- [ ] T064 [US8] Implement `internal/session/selection.go` and `Session.SetSelection`; decoders restarted at the new height when a participant's requested height changes; tile order from dominant-speaker events (T017); display, recording and feeds share the one selection (FR-013)
- [ ] T065 [US8] CLI `--video-limit --video-height --pin (repeatable) --pin-height`; hand check scenario 8 with 20 load-engine bots; record CPU and the 30-minute run (SC-005) in `checklists/validation.md`; commit

---

## Phase 11: Polish & Cross-Cutting Concerns

- [ ] T066 [P] Edge cases from spec.md, tests first, in `internal/session/edge_test.go`: duplicate names, audio-only/video-only participants, undecodable codec → name card + one warning, lost connection → exit 1, no leftover processes after `Close`, kill and Ctrl+C (SC-008)
- [ ] T067 [P] `README.md`: install (ffmpeg per OS), CLI examples for each scenario, OBS setup (stream to SRT/RTMP, Media Source on feed URLs), library example, limits
- [ ] T068 [P] Update `CLAUDE.md` commands and recent changes
- [ ] T069 Full `go vet ./... && go test -race ./...` locally; live tests against local Jitsi; CI green on Linux, macOS and Windows (SC-007)
- [ ] T070 Run quickstart.md end to end; final commit

---

## Dependencies & Execution Order

### Phase Dependencies

- Setup (1) → Foundational (2) → user stories.
- US1 (3) first (MVP). US2 (4) needs US1's tracks for its live test (receive side needs a publisher).
- US3 (5) needs US1 + US2 (publish and receive paths).
- US4 (6) needs US1 (encoder) and US2 (compositor). US6 (8) needs US4's feed processes for
  per-participant `tee` (or builds its own). US7 (9) needs US1 only. US5 (7) needs Foundational
  only. US8 (10) needs US2.
- Polish (11) after the desired stories.

### Within Each User Story

Tests first (must fail) → internal packages → session wiring → `jmt` → CLI → live/hand check → commit.

### Parallel Opportunities

- Setup: T004, T005, T006, T007 in parallel after T003.
- Foundational: T009, T010, T014, T015 in parallel; T016/T017 in parallel after T003.
- US2 tests T031–T034 in parallel; US4 tests T047/T048; US5 and US7 can run alongside US2–US4.

## Parallel Example: User Story 2

```text
Task: "T031 receiver_test.go"   Task: "T032 decoder_test.go"
Task: "T033 grid_test.go"       Task: "T034 mixer_test.go"
```

## Implementation Strategy

### MVP First (User Story 1 Only)

Phases 1–3, then stop and validate scenario 1 with a browser.

### Incremental Delivery

US1 → US2 (tool now publishes and watches) → US3 (library) → US4 (OBS) → US5 (real servers) →
US6 → US7 → US8. Each story is committed with tests green.

## Notes

- ffmpeg is not installed on the development machine yet (`sudo apt install ffmpeg`); ffmpeg tests
  skip until then, live tests need it and the local Jitsi server.
- Keep copied code close to its origin; note deviations in commit messages.
