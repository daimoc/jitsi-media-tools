# Implementation Plan: Jitsi Media Session

**Branch**: `001-jitsi-media-session` | **Date**: 2026-10-24 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `/specs/001-jitsi-media-session/spec.md`; approved design
`docs/superpowers/specs/2026-10-24-jitsi-media-tools-design.md`.

## Summary

A Go library (`jmt`) and CLI that join one Jitsi Meet room as one participant, publish one
audio+video stream (file, devices, or program frames; OBS comes in through a WHIP gateway, R14) and play what others
send (tile-grid window, recordings, SRT feeds for OBS, decoded frames for programs). Jitsi
signaling, Jingle↔SDP, bridge channel and the WebRTC peer are copied from
`webrtc-load-engine`; all media processing runs in ffmpeg processes fed and read by Go over
stdin/stdout and loopback TCP. Go owns the clock: the compositor and mixer emit constant-rate
raw streams to every consumer, which keeps audio and video in sync. The bridge chooses which
video to forward (`lastN`, pins as selected sources), so reception stays bounded.

## Technical Context

**Language/Version**: Go, `go 1.23.0` in go.mod (code 1.23-compatible; local toolchain 1.27)

**Primary Dependencies**: `pion/webrtc/v3`, `pion/rtp`, `pion/rtcp`, `pion/interceptor`,
`mellium.im/xmpp`, `nhooyr.io/websocket`, `google/uuid` (all as in webrtc-load-engine);
new: `golang.org/x/image` (grid labels). Runtime: ffmpeg ≥ 6 and ffplay (R3).

**Storage**: files only: recordings (`.mkv`) and `feeds.json` in the record directory.

**Testing**: `go test -race`; golden stanza tests; ffmpeg tests skip without ffmpeg; live tests
behind `-tags live` against `https://localhost:8443`; CI on Linux, macOS, Windows.

**Target Platform**: Linux, Windows, macOS (amd64, arm64).

**Project Type**: library + CLI.

**Performance Goals**: join ≤ 10 s; participant visible ≤ 5 s; display latency < ~1 s, OBS
feeds < ~2 s; A/V sync within 100 ms; 100-participant room with default selection on an
ordinary laptop for 30 min (SC-001…SC-005).

**Constraints**: no cgo; no helper process outlives the session; one session = one
participant; bounded video (≤ 25, default 9).

**Scale/Scope**: rooms of 2–100 participants; ~9–25 video decoders, audio decoders only for
participants sending audio, ≤ 16 + 1 OBS feed encoders.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Check | Status |
|---|---|---|
| I. Library-First | All behavior in `jmt`; CLI maps flags to `jmt.Options` (contracts/cli.md) | PASS |
| II. Frames-and-Samples API | `jmt` exposes `VideoFrame` (yuv420p) / `AudioChunk` (s16 48k stereo) only (contracts/library-api.md) | PASS |
| III. Signaling isolated | `internal/jitsi` and `internal/signaling` import no media package; `internal/rtpmedia`, `ffmpeg`, `compose`, `output` import no signaling package; `internal/session` is the only glue. Enforced by a test that checks package imports | PASS |
| IV. ffmpeg as the engine | Encode/decode/capture/mux/SRT via ffmpeg processes; Go only (de)packetizes RTP, parses IVF/Ogg, scales/composes raw yuv and mixes PCM (no codecs) | PASS — see note |
| V. Cross-platform | Per-OS code in `devices_*.go` and `proc_*.go`; loopback TCP instead of extra fds; CI matrix | PASS |
| VI. Bounded reception | `lastN` + pins via `ReceiverVideoConstraints`; lazy audio decoders | PASS |
| VII. Test-first | Tasks put tests before code; golden files; fake signaling; live tag | PASS |
| Scope | Jitsi only; no metrics/load; new module justified below | PASS |

Note on IV: composing the grid and mixing audio in Go is raw-pixel/sample arithmetic, not
"media processing" in the codec sense; doing it in ffmpeg would mean rebuilding a filter
graph on every join and per-OS font files (R10). Recorded in Complexity Tracking.

Post-design re-check (after Phase 1): unchanged, PASS.

## Project Structure

### Documentation (this feature)

```text
specs/001-jitsi-media-session/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── library-api.md
│   └── cli.md
├── checklists/requirements.md
└── tasks.md             # /speckit-tasks
```

### Source Code (repository root)

```text
go.mod                      # github.com/daimoc/jitsi-media-tools
jmt/                        # public library (Options, Join, Session, Source, frames, events)
  example_test.go           # runnable examples (skip without JMT_URL)
cmd/jitsi-media-tools/      # CLI: flag parsing → jmt.Options, event printing
internal/
  signaling/                # handler interface between signaling and peer (from src/platform)
  jitsi/                    # copied signaling + presence/participants, config.js discovery,
    jwt/                    #   guest wait, selection messages; testdata/golden
  peer/                     # PeerConnection: media engine, consent, TWCC, send tracks, OnTrack
  rtpmedia/                 # IVF/Ogg parsing, pacing+packetizing (send), frame assembly → IVF/Annex-B/Ogg (receive)
  ffmpeg/                   # locate+probe, process supervision, loopback sockets,
                            #   encoder/decoder/output arg builders, devices_{linux,darwin,windows}.go,
                            #   proc_{unix,windows}.go
  compose/                  # clock, grid layout, yuv scaling, labels, placeholder cards, mixer, jitter buffer
  output/                   # display (ffmpeg→ffplay), recording, OBS feeds (ports, feeds.json)
  whip/                     # WHIP HTTP endpoint (RFC 9725): auth, addresses, resources (media side, R14)
  session/                  # glue: participants, selection policy, wiring signaling ↔ peer ↔ media;
                            #   gateway streams: WHIP peer → forward source → Jitsi session
tests/live/                 # -tags live end-to-end tests
.github/workflows/ci.yml    # Linux/macOS/Windows matrix
CLAUDE.md, README.md
```

**Structure Decision**: single Go module, one public package `jmt`, all implementation in
`internal/` split along Principle III (signaling vs media) with `internal/session` as the
only package importing both sides.

## Build order (for /speckit-tasks)

1. Setup: module, CI, copied code compiling with its tests green, import-boundary test.
2. Foundation: ffmpeg locate/probe/supervise/loopback; peer; session state machine with fake
   signaling; participants from presence.
3. US1 publish file → MVP. 4. US2 display. 5. US3 library frames both ways.
6. US5 config.js / guest / JWT / TLS. 7. US4 OBS feeds; US9 WHIP gateway (R14). 8. US6 recording.
9. US7 devices. 10. US8 selection and pins. 11. Polish: README, examples, live tests, CI green on 3 OSes.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| Grid composition and audio mixing in Go (raw pixels/samples), alongside "all media via ffmpeg" | Constant-rate streams owned by one clock keep A/V in sync across many ffmpeg consumers; the library needs the grid frames anyway | ffmpeg `xstack`/`amix`/`drawtext`: graph must be rebuilt on each join/leave (gaps), drawtext needs fontconfig and font files that differ per OS |
| New module `golang.org/x/image` | Draw name labels on tiles without system fonts | Hand-made bitmap font: more code for the same result; ffmpeg drawtext: see above |
