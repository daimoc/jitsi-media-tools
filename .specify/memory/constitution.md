
# jitsi-media-tools Constitution

## Core Principles

### I. Library-First

Every capability lives in the public Go package first; the command-line tool is a
thin layer over it.

- The CLI MUST NOT contain media, signaling or selection logic that the library
  does not expose.
- Anything the CLI can do, a Go program MUST be able to do through the public
  package, with no access to internal packages.
- The public API MUST be documented with Go doc comments and at least one runnable
  example.

Rationale: the tool is meant to be embedded in other software; a CLI-only feature
would break that promise.

### II. Media Behind a Frames-and-Samples API

The public API exchanges decoded media only: raw video frames and PCM audio
samples, tagged with the participant they belong to.

- Public types MUST NOT expose RTP packets, SDP, Jingle, codecs or Pion types.
- Publishing MUST accept raw frames and samples, as well as the built-in sources
  (file, capture devices, OBS stream).

Rationale: users of the library should not need to know WebRTC.

### III. Signaling Isolated from Media

Jitsi signaling (XMPP, Jicofo, Jingle, Colibri bridge channel) and media handling
MUST stay in separate packages that meet only through a narrow interface (SDP
offers/answers, ICE candidates, source ownership, receive preferences, lifecycle
events).

- Media code MUST NOT import signaling packages, and signaling code MUST NOT
  import media packages.

Rationale: keeps Jitsi protocol changes from spreading into the media pipeline,
and keeps the code copied from `webrtc-load-engine` easy to compare with its origin.

### IV. ffmpeg as the Media Engine (NON-NEGOTIABLE)

All encoding, decoding, device capture, SRT/RTMP input and output, mixing and
composition MUST go through ffmpeg run as an external process.

- No cgo, and no linked codec libraries.
- The ffmpeg executable is found in PATH or given explicitly; a missing or
  unusable ffmpeg MUST produce a clear error naming what is missing.
- ffmpeg processes MUST be stopped when the session that started them closes;
  none may outlive the session.

Rationale: one pure-Go binary per OS, with media support that does not depend on
the build machine.

### V. Cross-Platform by Construction

The tool and library MUST build and pass their automated tests on Linux, Windows
and macOS.

- OS-specific code (device inputs, paths, process handling) MUST be confined to
  files with build constraints or one dispatch point per concern.
- CI MUST run the test suite on all three systems for every change.
- Hand testing is done on Linux; Windows and macOS behavior is checked by CI until
  machines are available.

Rationale: OBS users are on all three systems; "works on Linux only" counts as
failure.

### VI. Bounded Reception

Received video MUST stay bounded so a 100-participant room is usable on an
ordinary laptop.

- By default all audio is received, but video only for a limited number of
  participants (default 9 recent speakers) at low resolution, requested from the
  bridge through receive constraints.
- Pinned participants are added by display name; the total stays capped.
- Display, recording and OBS feeds MUST all follow the same selection.

Rationale: decoding everyone at full resolution does not scale and is not what
users need.

### VII. Test-First

New behavior is written test first: a failing test, then the code, then the
refactor.

- Signaling MUST be covered by unit tests with recorded stanzas (golden files).
- Media pipelines MUST be testable without a real Jitsi server or capture device
  (ffmpeg test sources, fake signaling).
- A live test, behind the `live` build tag, joins the local Jitsi server
  (`https://localhost:8443`) and checks publish and play end to end.
- `go vet ./... && go test -race ./...` MUST stay green.

Rationale: signaling regressions are hard to see by hand, and the live server is
not always running.

## Scope and Technology Constraints

- One session is one participant in one room. Load testing, multiple bots,
  metrics, quality scores and run reports are out of scope.
- Jitsi Meet only, at the local docker-jitsi-meet server and real deployments,
  joined as a guest or with a JWT.
- Go, kept compatible with Go 1.23; `pion/webrtc/v3`, `mellium.im/xmpp`,
  `nhooyr.io/websocket`. New modules need a justification in the plan.
- OBS integration is a streaming-protocol bridge (SRT/RTMP into the tool, SRT out
  to OBS Media Sources); no native OBS plugin.
- The project is independent from `webrtc-load-engine`: code is copied, not shared,
  and the two may drift.

## Development Workflow

- Spec Kit is the source of truth for requirements, plans and tasks:
  `specs/NNN-<feature>/spec.md`, `plan.md`, `tasks.md`. The design document in
  `docs/superpowers/specs/` is the input for the first feature, not a second spec.
- Each plan MUST include a Constitution Check against these principles; any
  deviation is recorded with its justification in the plan's Complexity Tracking.
- Commits are small and each one leaves the build and tests green.

## Governance

This constitution overrides other practices in this repository. Amendments are
made with `/speckit-constitution`, recorded with a version bump and a Sync Impact
Report, and reviewed before they are committed.

- Versioning: MAJOR for removing or redefining a principle, MINOR for a new
  principle or materially expanded guidance, PATCH for clarifications.
- Every plan and review checks compliance; complexity that a principle forbids
  must be justified in writing or removed.
- Runtime development guidance for agents lives in `CLAUDE.md`.

**Version**: 1.0.0 | **Ratified**: 2026-10-24 | **Last Amended**: 2026-10-24
