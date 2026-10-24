#!/usr/bin/env bash
# Regenerates samples/clip.mp4: 10 s of a moving test pattern (640x360,
# 30 fps, H.264) with a beeping 880 Hz tone (AAC). Needs ffmpeg with libx264.
set -euo pipefail
cd "$(dirname "$0")"
ffmpeg -hide_banner -loglevel error -y \
  -f lavfi -i "testsrc2=size=640x360:rate=30:duration=10" \
  -f lavfi -i "sine=frequency=880:beep_factor=4:duration=10" \
  -c:v libx264 -preset veryslow -crf 30 -pix_fmt yuv420p -g 60 \
  -c:a aac -b:a 64k -movflags +faststart -shortest clip.mp4
