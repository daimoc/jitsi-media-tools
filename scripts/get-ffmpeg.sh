#!/usr/bin/env bash
# Downloads a pinned ffmpeg build (ffmpeg, ffplay, ffprobe) into
# .tools/ffmpeg/bin, for machines without a system ffmpeg (no root needed).
# Builds: github.com/BtbN/FFmpeg-Builds, GPL variant (libvpx, libopus,
# libx264, libsrt). macOS: use Homebrew (brew install ffmpeg).
#
#   ./scripts/get-ffmpeg.sh
#   export PATH="$PWD/.tools/ffmpeg/bin:$PATH"   # or: source scripts/env.sh
#
# FFMPEG_ARCH (x86_64 or aarch64) picks another Linux machine's build, as the
# Dockerfile does when it builds for another platform.
set -euo pipefail
cd "$(dirname "$0")/.."

TAG=autobuild-2026-09-30-13-08
BUILD=ffmpeg-n8.1.3-9-g29e619e767
DEST=.tools/ffmpeg

case "$(uname -s)-${FFMPEG_ARCH:-$(uname -m)}" in
    Linux-x86_64)  ASSET=$BUILD-linux64-gpl-8.1.tar.xz;    SHA=97ce978979194b5cf7e06a5e68020dbdaa7a4f3294c5452b6a1bc347100dbb79 ;;
    Linux-aarch64) ASSET=$BUILD-linuxarm64-gpl-8.1.tar.xz; SHA=10523d1e0be6b3ce62a9708cc50815c4b5eb5f36ae147fbe2afeecda6f2d6845 ;;
    MINGW*-x86_64|MSYS*-x86_64|CYGWIN*-x86_64)
                   ASSET=$BUILD-win64-gpl-8.1.zip;         SHA=7b801cdd3a1a0bb54ae6f572187e68b4ed54f52086cfee133fab2180bfb429fa ;;
    Darwin-*) echo "macOS: install ffmpeg with Homebrew: brew install ffmpeg" >&2; exit 1 ;;
    *) echo "no pinned ffmpeg build for $(uname -s) $(uname -m); install ffmpeg 6 or newer" >&2; exit 1 ;;
esac

if [ -x "$DEST/bin/ffmpeg" ] && [ "$(cat "$DEST/VERSION" 2>/dev/null)" = "$ASSET" ]; then
    echo "ffmpeg already in $DEST/bin"
    exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "downloading $ASSET"
curl -fSL --progress-bar -o "$tmp/$ASSET" "https://github.com/BtbN/FFmpeg-Builds/releases/download/$TAG/$ASSET"
echo "$SHA  $tmp/$ASSET" | sha256sum -c --quiet - || { echo "checksum mismatch for $ASSET" >&2; exit 1; }

rm -rf "$DEST"
mkdir -p "$DEST"
case "$ASSET" in
    *.tar.xz) tar -xJf "$tmp/$ASSET" -C "$tmp" ;;
    *.zip)    unzip -q "$tmp/$ASSET" -d "$tmp" ;;
esac
mv "$tmp/${ASSET%.tar.xz}/bin" "$DEST/bin" 2>/dev/null || mv "$tmp/${ASSET%.zip}/bin" "$DEST/bin"
echo "$ASSET" > "$DEST/VERSION"
echo "ffmpeg in $DEST/bin: export PATH=\"$PWD/$DEST/bin:\$PATH\" (or source scripts/env.sh)"
