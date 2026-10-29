# jitsi-media-tools Development Guidelines

Auto-generated from all feature plans. Last updated: 2026-10-29

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
internal/{signaling,jitsi,peer,rtpmedia,ffmpeg,compose,output,whip,session}
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
WHIP_API_SECRET=s3cret go run ./cmd/jitsi-media-tools serve --insecure   # WHIP gateway; OBS server http://127.0.0.1:8080/whip/localhost:8443/test?name=OBS
go run ./cmd/jitsi-media-tools join --url https://localhost:8443/test --insecure --name Rec --record /tmp/rec   # --record-mode mixed
go run ./cmd/jitsi-media-tools devices && go run ./cmd/jitsi-media-tools join --url https://localhost:8443/test --insecure --name Cam --publish device:/dev/video0,default
go run ./cmd/jitsi-media-tools join --url https://localhost:8443/test --insecure --name Viewer --display --pin Alice --video-budget 3M
docker build -t jitsi-media-tools .     # distroless image, tool + pinned ffmpeg; buildx cross-builds arm64 without emulation
WHIP_API_SECRET=s3cret JMT_PUBLIC_IP=<host IP> docker compose up -d   # WHIP gateway in a container (compose.yaml)
```

## Code Style

Standard Go conventions. Signaling packages (`internal/jitsi`, `internal/signaling`) never
import media packages and vice versa; `internal/session` is the only glue. `cmd/` imports only
`jmt`, its tests included (`internal/boundary_test.go`): tests that need the fake server
(`internal/session/sessiontest`) go in `jmt` or `internal/session`.

## Recent Changes

- 001-jitsi-media-session: Docker image (`Dockerfile`, `compose.yaml`); `--obs-url-host`; the
  WHIP mux with `--public-ip` is one socket on every interface (Pion ties a 1:1 public
  candidate to the first socket only)
- 001-jitsi-media-session: edge cases and exits (T066): `internal/session/edge_test.go`,
  `jmt/exit_test.go` (Ctrl+C and SIGKILL of a recording session leave no ffmpeg)
- 001-jitsi-media-session: received-video budget (FR-033, R8): `internal/session/budget.go`,
  `--video-budget`; `assumedBandwidthBps` offered to the bridge
- 001-jitsi-media-session: camera and microphone (US7): `jmt.DeviceSource`, names resolved
  before joining; Linux captures Motion-JPEG when the camera offers it
- 001-jitsi-media-session: recording (US6): one ffmpeg per file (not a tee on the feed's
  ffmpeg, R11 revised); `internal/session/outputs.go` drives feeds and recordings
- 001-jitsi-media-session: WHIP gateway for OBS (US9, research R14): `serve`, `jmt.Gateway`,
  H.264/Opus pass-through; the live test uses ffmpeg ≥ 8 `-f whip` as the publisher
- 001-jitsi-media-session: initial library + CLI (copied Jitsi signaling from webrtc-load-engine)

<!-- MANUAL ADDITIONS START -->
<!-- MANUAL ADDITIONS END -->
