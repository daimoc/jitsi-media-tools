#!/usr/bin/env bash
# Downloads a pinned Piper text-to-speech build and one English voice into
# .tools/piper, for the video tutorials' narration (tutorials/). No root.
# Voice: en_GB-cori-medium, trained on public-domain LibriVox recordings
# (its MODEL_CARD: https://huggingface.co/rhasspy/piper-voices/blob/v1.0.0/en/en_GB/cori/medium/MODEL_CARD).
#
#   ./scripts/get-piper.sh
#   echo hello | .tools/piper/piper/piper --model .tools/piper/en_GB-cori-medium.onnx --output_file hello.wav
set -euo pipefail
cd "$(dirname "$0")/.."

DEST=.tools/piper
PIPER_URL=https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_x86_64.tar.gz
PIPER_SHA=a50cb45f355b7af1f6d758c1b360717877ba0a398cc8cbe6d2a7a3a26e225992
VOICE_URL=https://huggingface.co/rhasspy/piper-voices/resolve/v1.0.0/en/en_GB/cori/medium
VOICE=en_GB-cori-medium
VOICE_SHA=1899f98e5fb8310154f3c2973f4b8a929ba7245e722b3d3a85680b833d95f10d
VOICE_JSON_SHA=e262c16d7f192f69d4edd6b4ef8a5915379e67495fcc402f1ab15eeb33da3d36

case "$(uname -s)-$(uname -m)" in
    Linux-x86_64) ;;
    *) echo "no pinned Piper build for $(uname -s) $(uname -m)" >&2; exit 1 ;;
esac

if [ -x "$DEST/piper/piper" ] && [ -f "$DEST/$VOICE.onnx" ]; then
    echo "Piper already in $DEST"
    exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fetch() { # url file sha
    echo "downloading $(basename "$2")"
    curl -fSL --progress-bar -o "$tmp/$2" "$1"
    echo "$3  $tmp/$2" | sha256sum -c --quiet - || { echo "checksum mismatch for $2" >&2; exit 1; }
}
fetch "$PIPER_URL" piper.tar.gz "$PIPER_SHA"
fetch "$VOICE_URL/$VOICE.onnx" "$VOICE.onnx" "$VOICE_SHA"
fetch "$VOICE_URL/$VOICE.onnx.json" "$VOICE.onnx.json" "$VOICE_JSON_SHA"

rm -rf "$DEST"
mkdir -p "$DEST"
tar -xzf "$tmp/piper.tar.gz" -C "$DEST"
mv "$tmp/$VOICE.onnx" "$tmp/$VOICE.onnx.json" "$DEST/"
echo "Piper in $DEST/piper, voice $DEST/$VOICE.onnx"
