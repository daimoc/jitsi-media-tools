# Video tutorials

This folder makes the narrated video tutorials of jitsi-media-tools. Each one is a single
take of a headless Chrome page:
- **Left:** a real terminal, where a script types the commands and they really run.
- **Right:** the meeting as a participant sees it, or a card, or a recording played back.
- **Bottom:** the narration, as captions.

The voice is [Piper](https://github.com/rhasspy/piper) with the `en_GB-cori-medium` voice,
which was trained on public-domain LibriVox recordings. Nothing records your desktop.

| Name | Video |
|---|---|
| `publish` | Publish a media file into a meeting |
| `record` | Record a meeting: per participant and mixed |
| `whip` | Stream OBS into a meeting with the WHIP gateway, natively and in Docker |
| `feeds` | Bring a meeting into OBS with SRT feeds, in Docker |

## Make them

What you need:
- the local Jitsi server, running (`tests/platforms/jitsi`);
- ffmpeg (`./scripts/get-ffmpeg.sh`);
- Piper (`./scripts/get-piper.sh`);
- Google Chrome;
- Docker, with access to `ghcr.io/daimoc/jitsi-media-tools:main`.

```bash
./scripts/get-ffmpeg.sh && ./scripts/get-piper.sh
cd tutorials
go run . all          # or: go run . publish whip
```

Each tutorial writes `out/<name>.mp4` (1920x1080, 30 fps, H.264 with AAC narration) and
`out/<name>.srt` (the captions, for players and video sites). One takes 1.5–2.5 minutes to
make; the narration is cached in `.cache/tts`.

Each tutorial uses its own room (`tut-publish`, `tut-record`…). The driver builds
jitsi-media-tools from this clone. The Docker parts use the published image, so they show
the code of the last push to `main`.

## How it works

| File | What it does |
|---|---|
| `main.go` | Finds the tools, builds the tool, runs each take and adds the narration |
| `stage.go` | Chrome (go-rod), the screencast, the bash in a PTY, and the steps: `Say`, `Type`, `WaitFor`, `Meeting`, `Card`… |
| `stage.html` | The page: xterm.js, the right pane, the caption bar |
| `media.go` | Piper, the audio mix, the `.srt` |
| `tut_*.go` | One tutorial each: its steps and its narration |

`Say` waits as long as the line takes to say, so the narration sets the pace. The video and
the voice are recorded on the same clock, which keeps the captions in step. This folder is
its own Go module, so its dependencies stay out of the tool's `go.mod`.
