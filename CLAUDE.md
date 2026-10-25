# jitsi-media-tools Development Guidelines

Auto-generated from all feature plans. Last updated: 2026-10-24

## Development workflow

Spec Kit is the source of truth for requirements, specifications, architecture and
implementation tasks (`specs/NNN-*/`). Superpowers skills may be used for implementation
practices (TDD, debugging, code review). Do not create a second specification or plan when
Spec Kit already provides one. Constitution: `.specify/memory/constitution.md`.

## Active Technologies

- Go (`go 1.23.0` in go.mod, keep 1.23-compatible) + `pion/webrtc/v3`, `pion/rtp`, `pion/rtcp`,
  `pion/interceptor`, `mellium.im/xmpp`, `nhooyr.io/websocket`, `golang.org/x/image`;
  ffmpeg ≥ 6 and ffplay as external processes, no cgo (001-jitsi-media-session)
- Storage: recordings (`.mkv`) and `feeds.json` in the record directory (001-jitsi-media-session)

## Project Structure

```text
jmt/                     # public library
cmd/jitsi-media-tools/   # CLI over jmt
internal/{signaling,jitsi,peer,rtpmedia,ffmpeg,compose,output,session}
tests/live/              # -tags live
```

## Commands

```bash
./scripts/get-ffmpeg.sh                 # pinned ffmpeg+ffplay into .tools/ffmpeg (no root; macOS: brew)
source scripts/env.sh                   # .tools/ffmpeg/bin and /usr/local/go/bin in PATH
(cd tests/platforms/jitsi && ./setup.sh && docker compose up -d)   # local Jitsi on https://localhost:8443
go build ./...
go vet ./... && go test -race ./...     # keep green
JMT_URL=https://localhost:8443/jmt-live JMT_INSECURE=1 go test -tags live -count=1 -v ./tests/live/
go run ./cmd/jitsi-media-tools join --url https://localhost:8443/IntriguingClimatesSupposeProbably --insecure --name Bot --display
go run ./tests/displaycheck --url https://localhost:8443/jmt-display --insecure   # latency to the screen, by screenshot
```

## Code Style

Standard Go conventions. Signaling packages (`internal/jitsi`, `internal/signaling`) never
import media packages and vice versa; `internal/session` is the only glue.

## Recent Changes

- 001-jitsi-media-session: initial library + CLI (copied Jitsi signaling from webrtc-load-engine)

<!-- MANUAL ADDITIONS START -->
<!-- MANUAL ADDITIONS END -->
