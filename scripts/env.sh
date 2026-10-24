# Source this file to use the project's ffmpeg (scripts/get-ffmpeg.sh) and
# Go toolchain: source scripts/env.sh
_jmt_root=$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)
export PATH="$_jmt_root/.tools/ffmpeg/bin:/usr/local/go/bin:$PATH"
unset _jmt_root
