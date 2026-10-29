# jitsi-media-tools with a pinned ffmpeg, for headless use: serve (WHIP),
# --record, --obs, --publish file:. No ffplay, so no --display.
#
#   docker build -t jitsi-media-tools .
#   docker run --rm --network host -v "$PWD/rec:/rec" --user "$(id -u):$(id -g)" \
#     jitsi-media-tools join --url https://meet.example.org/Room --name Rec --record /rec
#
# Every stage runs on the build machine, so other platforms need no emulation:
#   docker buildx build --platform linux/amd64,linux/arm64 -t … --push .

FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/jitsi-media-tools ./cmd/jitsi-media-tools

# The same build as scripts/get-ffmpeg.sh, checksum checked. It needs only
# glibc and libgcc_s, which the runtime image has.
FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS ffmpeg
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl xz-utils \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /f
COPY scripts/get-ffmpeg.sh scripts/
RUN case "$TARGETARCH" in amd64) a=x86_64 ;; arm64) a=aarch64 ;; *) echo "no ffmpeg for $TARGETARCH" >&2; exit 1 ;; esac \
 && FFMPEG_ARCH=$a ./scripts/get-ffmpeg.sh

# No shell. The tool, as PID 1, stops on docker stop's SIGTERM and waits for
# its ffmpeg processes.
FROM gcr.io/distroless/cc-debian12:nonroot
COPY --from=ffmpeg /f/.tools/ffmpeg/bin/ffmpeg /usr/local/bin/
COPY --from=build /out/jitsi-media-tools /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/jitsi-media-tools"]
CMD ["--help"]
