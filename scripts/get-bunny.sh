#!/usr/bin/env bash
# Downloads Big Buck Bunny (640x360, H.264 and AAC) and cuts the 30 s the
# video tutorials publish (tutorials/) into .tools/bunny/bunny.mp4. No root.
# Big Buck Bunny is (c) 2008 Blender Foundation, www.bigbuckbunny.org,
# licensed under CC BY 3.0 (https://creativecommons.org/licenses/by/3.0/):
# credit it wherever the tutorials are shown.
#
#   ./scripts/get-bunny.sh      # needs ffmpeg (./scripts/get-ffmpeg.sh) and unzip
set -euo pipefail
cd "$(dirname "$0")/.."

DEST=.tools/bunny
URL=https://download.blender.org/peach/bigbuckbunny_movies/BigBuckBunny_640x360.m4v.zip
SHA=7118242b6728d40c871479c5b3c0f0fb27d748089df15d7f1b469f297c74a2d6
START=115 # The bunny under the tree, the butterfly, the squirrels
LENGTH=30

if [ -f "$DEST/bunny.mp4" ]; then
    echo "Big Buck Bunny already in $DEST"
    exit 0
fi
FFMPEG=.tools/ffmpeg/bin/ffmpeg
[ -x "$FFMPEG" ] || FFMPEG=$(command -v ffmpeg) || { echo "ffmpeg not found: run ./scripts/get-ffmpeg.sh" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "downloading $(basename "$URL") (121 MB)"
curl -fSL --progress-bar -o "$tmp/bunny.zip" "$URL"
echo "$SHA  $tmp/bunny.zip" | sha256sum -c --quiet - || { echo "checksum mismatch for $(basename "$URL")" >&2; exit 1; }
unzip -q "$tmp/bunny.zip" -d "$tmp"

mkdir -p "$DEST"
"$FFMPEG" -hide_banner -loglevel error -y -ss "$START" -t "$LENGTH" -i "$tmp/BigBuckBunny_640x360.m4v" \
  -vf "scale=640:360,setsar=1" -c:v libx264 -preset slow -crf 22 -pix_fmt yuv420p -g 48 \
  -c:a aac -b:a 128k -movflags +faststart "$DEST/bunny.mp4"
echo "Big Buck Bunny, ${LENGTH} s from ${START} s, in $DEST/bunny.mp4"
