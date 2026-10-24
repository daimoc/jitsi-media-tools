# jitsi-media-tools — design

Date: 2026-10-24
Status: approved 2026-10-24

## Intent

A standalone tool that joins a given Jitsi Meet room as a participant,
**publishes** audio/video into it and **plays** what the other participants
send: shows it, records it, and hands it to OBS. It is used from the command
line and as a Go library embedded in other software.

It is built from the Jitsi signaling and WebRTC code of `webrtc-load-engine`
(sibling repo), which already joins Jitsi rooms reliably, but it is a separate
project: no load testing, no metrics, no reports.

## Goals

1. **Join a room** from a meeting URL (e.g. `https://localhost:8443/IntriguingClimatesSupposeProbably`),
   with a display name, anonymous or with token auth (ready JWT, or app id +
   secret to sign one, as the load engine does).
2. **Publish** one audio + one video stream from exactly one of:
   - a media file (looped or played once),
   - a camera and microphone (chosen by device name),
   - an OBS stream: OBS pushes its program output to the tool (SRT or RTMP,
     both built into OBS) and the tool re-sends it into the meeting.
3. **Play** the received media, any combination of:
   - **display**: one window with a Jitsi-style tile grid (participant names
     on the tiles) and the room's mixed audio on the speakers;
   - **record**: per participant files, or one mixed file (the grid + mixed
     audio), chosen by an option;
   - **OBS feed**: one stream per participant that OBS opens as a Media
     Source, and optionally one stream of the whole grid (*my call*, mirroring
     the recording option).
4. **Selection**, for rooms of 2 to 100 participants: by default all audio,
   and video of the last N speakers (default 9) at low resolution; participants
   can be pinned by display name to always get their video, optionally at a
   higher resolution. Display, recording and OBS feeds all follow this
   selection. This rides on Jitsi's bridge channel
   (`ReceiverVideoConstraints`: last-N, selected/on-stage endpoints, per
   endpoint max height), which the load engine already sends.
5. **Library**: a Go package a program uses in a few lines to join a room,
   get each participant's **decoded** video frames and audio samples (with
   participant id / display name, join/leave events), and push its own raw
   frames and samples to publish. Encoded packets are not part of the public
   API.
6. **Linux, Windows and macOS.**

## Non-goals

- Load testing, multiple bots, Prometheus metrics, run reports, media
  quality scores — those stay in `webrtc-load-engine`.
- Platforms other than Jitsi (LiveKit, mediasoup, Janus).
- A native OBS plugin. OBS integration is a streaming-protocol bridge only.
- Sharing code with `webrtc-load-engine`: the two projects are independent
  for now; the new one starts from a copy and they may drift.
- Chat, screen-share as a second video, moderation, lobby, breakout rooms,
  end-to-end encryption.
- Publishing more than one audio + one video at a time.

## Anti-goals (failure even if it "works")

- Something that only works on the developer's Linux box.
- A library that forces its users to deal with RTP, codecs or SDP.
- Pulling all 100 participants' video at full resolution and falling over.
- Being a fork of the load engine in disguise: scenario/engine/metrics code
  must not come along.

## Constraints and decided "how"

- **Language: Go**, to reuse the existing code. Module
  `github.com/daimoc/jitsi-media-tools` (*my call*), repository
  `../jitsi-media-tools` next to `webrtc-load-engine` (name decided by the
  user, location *my call*). Same Go/Pion versions as the load engine to start
  (Go 1.23-compatible, `pion/webrtc/v3`, `mellium.im/xmpp`,
  `nhooyr.io/websocket`).
- **Media processing through ffmpeg as an external program** (*my call*,
  accepted): encoding, decoding, device capture, SRT/RTMP in and out,
  composition of the mixed recording. ffmpeg is required at runtime and
  found in PATH or given by flag. No cgo codec bindings. Device capture uses
  ffmpeg's per-OS inputs (V4L2 + PulseAudio/ALSA on Linux, DirectShow on
  Windows, AVFoundation on macOS); the Go code chooses the input by OS.
- **OBS bridge** (decided by the user):
  - into the meeting: OBS → tool via SRT or RTMP (the tool listens);
  - out to OBS: tool → OBS via SRT (one port or stream per participant, plus
    the grid), which OBS opens with a Media Source.
  Some extra latency is accepted.
- **Library hands decoded frames/samples both ways** (decided by the user).

## Servers and platforms (decided by the user)

- **Servers**: the local docker-jitsi-meet (`https://localhost:8443`, XMPP
  domain `meet.jitsi`), and real deployments, joined **as a guest**
  (anonymous or guest domain, including waiting for the host where the server
  requires it) **or with a JWT** (ready-made, or signed from app id + secret).
  To join from a meeting URL alone, the tool reads the server's `config.js`
  (`hosts.domain`, `hosts.muc`, `hosts.anonymousdomain`, `websocket`,
  `focusUserJid`, and the flags it needs) and lets flags override every
  value. Features real servers may require that the copied code leaves off
  (SSRC rewriting, JSON sources, visitors) are checked against current
  Jitsi; supporting what a real server needs to admit a guest is in scope
  (*my call*: SSRC rewriting and visitors only if a target server requires
  them). TLS verification can be skipped by flag for self-signed servers.
- **Platforms**: only Linux is available for testing by hand now. Windows
  and macOS must build and pass the automated tests in CI (e.g. GitHub
  Actions runners), and the per-OS device inputs are written for them, but
  hand testing there comes later.

## Settled after review (*my calls*)

- **Display is watch-only**: the grid updates itself as people join, leave
  and speak, but has no clicks or GUI controls. Pins and selection are set
  by CLI flags or through the library. No GUI toolkit, no cgo.
- **Performance target**: an ordinary recent laptop with the default
  selection (9 low-res videos) and display + recording + OBS feeds all on.
  Prefer few shared ffmpeg processes (e.g. one audio mixer) over one process
  per stream where that is what it takes.
- **Latency**: display aims under about 1 s; OBS feeds may take 1–2 s.
  Restarting the grid's ffmpeg when someone joins is acceptable if it is
  brief.
- **Stable OBS addresses**: each participant's OBS feed is keyed by display
  name and keeps its address across leave/rejoin and selection changes
  (an empty slot sends a placeholder picture); duplicate names get a suffix.
  The grid feed has its own fixed address.
- **Process** (decided by the user): the new repo uses Spec Kit, like the
  load engine; this design is the input to its first feature spec.

## What comes from `webrtc-load-engine`

Reused nearly as-is (copied, then adapted):

- `src/platform/jitsi/*` (~2,900 lines): XMPP over WebSocket, conference
  request to Jicofo, MUC join, Jingle ↔ SDP, source-add/remove,
  renegotiation, transport-info, bridge channel messages, JWT. It only
  depends on the small `src/platform` interface and `src/platform/internal/jwt`;
  both come along. Drop the multi-bot `Adapter` map if it no longer fits.
- `src/execution/consent.go` (RFC 7675 consent checks) and the TWCC
  feedback (`twcc.go`).
- The useful parts of `src/execution/peer.go` / `pion.go`: PeerConnection
  setup, local tracks, simulcast layers, keyframe requests (PLI),
  `OnTrack` — **without** the scenario state machine, stats and metrics.
- `src/media/source.go` RTP packetization/pacing (note: RTP timestamps start
  at 2^30; 2^31 makes the Jitsi bridge rebase video) and the ffmpeg file
  transcoding idea of `src/media/file_content.go`.

New work (nothing of this exists in the load engine): live encoding from
devices/OBS, decoding received media, per-participant identity (map Jicofo's
`ssrc-info` owner / endpoint id to the display name from MUC presence),
selection driven by display names and speaker changes (dominant speaker
messages on the bridge channel), audio mixing, grid composition, display
window, recording, SRT outputs, and the public library API.

## Shape (suggested; details left to the builder)

- `jitsi/` — signaling (from the load engine).
- `peer/` — WebRTC peer: one PeerConnection that receives the bridge's
  offer and also sends local tracks.
- `media/` — ffmpeg pipelines: inputs (file, devices, SRT/RTMP listener) to
  encoded RTP; received RTP to decoded frames/samples; outputs (display,
  recording, SRT).
- public package (e.g. `jmt`) — `Join(ctx, Options) (*Session, error)`;
  session events for participants joining/leaving and their decoded
  frames/samples; `Publish` taking raw frames/samples; `Pin`/selection
  setters; `Close`.
- `cmd/jitsi-media-tools` — the CLI on top of the public package, e.g.
  `jitsi-media-tools --url https://host/Room --name Bot --publish file:clip.mp4 --display --record per-participant --obs-out srt://:9000 --pin "Alice"`.

## Left to the builder

- Exact CLI flags and library type names.
- Codecs negotiated: at least VP8 + Opus (what the load engine uses);
  whether to accept VP9/H.264/AV1 from other clients. Decoding goes through
  ffmpeg either way.
- How the grid window is drawn (e.g. composing with ffmpeg and showing with
  ffplay/SDL output), as long as it works on the three OSes.
- How frames move between Go and ffmpeg (pipes, raw formats, IVF/Ogg/NUT
  containers), A/V sync approach, port allocation for SRT outputs.
- Simulcast on publish (likely yes, as in the load engine) and the layer
  heights.
- Tests: unit tests on signaling (golden files can be copied), and a manual
  or tagged live test against the local Jitsi at
  `https://localhost:8443` (self-signed certificate: needs an option to skip
  TLS verification).
