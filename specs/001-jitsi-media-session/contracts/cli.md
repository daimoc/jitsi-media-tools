# Contract: command line `jitsi-media-tools`

The CLI only maps flags to `jmt.Options` and prints events (Principle I).

```
jitsi-media-tools devices [--ffmpeg PATH]
jitsi-media-tools join --url URL --name NAME [options]
jitsi-media-tools serve [options]      # WHIP gateway (US9); secret in $WHIP_API_SECRET
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
| `--publish` | Publish: `file:PATH`, `file-loop:PATH`, `device:VIDEO,AUDIO` | none |
| `--publish-size` | publish WxH | `1280x720` |
| `--display` | Display | off |
| `--record DIR` / `--record-mode per-participant\|mixed` | Record | off / per-participant |
| `--obs` / `--obs-host` / `--obs-port` / `--obs-max-feeds` / `--obs-grid` | OBS | off / 0.0.0.0 / 9000 / 16 / off |
| `--video-limit`, `--video-height`, `--pin NAME` (repeatable), `--pin-height` | Selection | 9, 180, —, 720 |
| `--video-budget` | Selection.Budget, e.g. `3M`, `800k` (bits/s) | none |
| `--ffmpeg`, `--ffplay` | executables | PATH |
| `--log-level` | debug/info/warn | info |

## `serve` options

| Flag | Maps to | Default |
|---|---|---|
| `$WHIP_API_SECRET` / `--secret-file FILE` | Secret | required |
| `--listen` | Listen | `:8080` |
| `--udp-port` | UDPPort | 8189 |
| `--public-ip` | PublicIP | — |
| `--tls-cert`, `--tls-key` | TLSCert, TLSKey | — (plain HTTP) |
| `--max-streams` | MaxStreams | 10 |
| `--insecure`, `--wait-host`, `--log-level` | as for `join` | off, `30s`, info |

`serve` prints `whip http://HOST:8080/whip/<server>/<room>?name=<name>` once listening, then
one line per stream: `+ OBS meet.example.org/MyRoom`, `- OBS meet.example.org/MyRoom`,
`! OBS meet.example.org/MyRoom: <reason>`. Exit codes: 0 after Ctrl+C; 1 when it cannot listen;
2 on bad flags or no secret.

## Output

- stdout, one line per event: `joined room R as NAME`, `waiting for host…`,
  `+ Alice`, `- Alice`, `feed Alice srt://192.0.2.10:9001`,
  `record Alice /tmp/rec/myroom-20261028-091500-Alice.mkv` (`record mixed …` for the mixed file).
- stderr: logs (`slog` text).
- Exit codes: 0 after Ctrl+C or end of a once-played file; 1 on join failure,
  lost connection or missing ffmpeg; 2 on bad flags.

`devices` prints one line per device: `video  "Integrated Camera"  (/dev/video0)`.
