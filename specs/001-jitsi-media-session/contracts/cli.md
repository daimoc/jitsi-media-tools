# Contract: command line `jitsi-media-tools`

The CLI only maps flags to `jmt.Options` and prints events (Principle I).

```
jitsi-media-tools devices [--ffmpeg PATH]
jitsi-media-tools join --url URL --name NAME [options]
```

## `join` options

| Flag | Maps to | Default |
|---|---|---|
| `--url` | MeetingURL | required |
| `--name` | DisplayName | `jitsi-media-tools` |
| `--domain`, `--muc`, `--anonymous-domain`, `--ws`, `--focus` | server overrides | from config.js |
| `--insecure` | Insecure | false |
| `--jwt` / `$JITSI_JWT` | Token | — |
| `--jwt-app-id`, `$JITSI_APP_SECRET`, `--jwt-audience`, `--jwt-subject`, `--jwt-ttl`, `--jwt-moderator` | AppID… | — |
| `--wait-host` | WaitHost | `5m` |
| `--publish` | Publish: `file:PATH`, `file-loop:PATH`, `device:VIDEO,AUDIO`, `obs-srt::PORT`, `obs-rtmp::PORT` | none |
| `--publish-size` | publish WxH | `1280x720` |
| `--display` | Display | off |
| `--record DIR` / `--record-mode per-participant\|mixed` | Record | off / per-participant |
| `--obs` / `--obs-host` / `--obs-port` / `--obs-max-feeds` / `--obs-grid` | OBS | off / 0.0.0.0 / 9000 / 16 / off |
| `--video-limit`, `--video-height`, `--pin NAME` (repeatable), `--pin-height` | Selection | 9, 180, —, 720 |
| `--ffmpeg`, `--ffplay` | executables | PATH |
| `--log-level` | debug/info/warn | info |

## Output

- stdout, one line per event: `joined room R as NAME`, `waiting for host…`,
  `+ Alice`, `- Alice`, `feed Alice srt://192.0.2.10:9001`, `obs input srt://0.0.0.0:9100`.
- stderr: logs (`slog` text).
- Exit codes: 0 after Ctrl+C or end of a once-played file; 1 on join failure,
  lost connection or missing ffmpeg; 2 on bad flags.

`devices` prints one line per device: `video  "Integrated Camera"  (/dev/video0)`.
