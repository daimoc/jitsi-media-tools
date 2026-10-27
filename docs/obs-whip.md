# OBS settings for the WHIP gateway

How to set up OBS to stream into a Jitsi meeting through `jitsi-media-tools serve`. This
page covers how to make the stream start quickly, keep its delay low, or get the best
picture. Menu names are those of OBS 30 and 31.

The gateway forwards OBS's video and audio to the meeting as they are: it does not decode
or re-encode them. So **what OBS sends is exactly what every viewer receives**: its size,
frame rate, bitrate and keyframes. The Jitsi Videobridge cannot lower the quality for a
viewer with a weak connection either, because OBS sends a single layer. All the choices
below are made in OBS.

## Connect

Settings → **Stream**:

| Field | Value |
|---|---|
| Service | WHIP |
| Server | `https://<gateway>/whip/<jitsi server>/<room>?name=<display name>` |
| Bearer Token | the gateway's secret (`WHIP_API_SECRET`) |

Add `&jwt=<token>` to the server address when the Jitsi server needs a token. A tenant goes
in the path, as in a meeting URL: `/whip/meet.example.org/acme/Standup`.

## Settings that matter for every stream

Settings → **Output**, Output Mode **Advanced**, **Streaming** tab:

| Setting | Value | Why |
|---|---|---|
| Video Encoder | an **H.264** encoder: x264, NVIDIA NVENC H.264, QuickSync H.264, AMD H.264, Apple VT H.264 | The gateway refuses AV1 and HEVC: Jitsi and browsers expect H.264 |
| Audio Encoder | Opus (the WHIP default) | Opus is the only audio codec WebRTC meetings use |
| Rate Control | **CBR** | A steady bitrate; bursts are lost on viewers' connections |
| **Keyframe Interval** | **1 s** | Sets how long viewers wait for the picture; see [Start time](#start-time) |
| B-frames | **0** | WebRTC sends frames in order; B-frames add delay and may not play |

With **x264**, also set **Tune: zerolatency**, which turns off B-frames and look-ahead.
With **NVENC**, set **Max B-frames: 0** and turn **Look-ahead** off.

Settings → **Audio**: Sample Rate **48 kHz**.

Settings → **Advanced** → Stream Delay: **off**.

## Start time

The time from **Start Streaming** to the picture in the meeting, measured on a local Jitsi
server with a WHIP publisher configured like OBS:

| Step | Time |
|---|---|
| The gateway joins the meeting and answers OBS | 0.13 s |
| The Jitsi server sets up the participant's media | ~1.0 s (server side, the same for any participant) |
| Viewers wait for a keyframe from OBS | from almost 0 up to OBS's keyframe interval |

A viewer can only start showing video from a keyframe. The gateway asks OBS for one as soon
as the meeting is connected, and asks again every 500 ms. A publisher that ignores these
requests makes viewers wait for its next scheduled keyframe. With a keyframe every 250
frames, which is what OBS's **0 (auto)** gives with x264, the measured wait was **7.4 s**.
With a 2 s interval it was 1.4 s.

- **Set Keyframe Interval to 1 s.** The stream then appears about 1.5–2.5 s after you start.
- If the gateway logs `ignores keyframe requests`, the keyframe interval is too long.
  If it logs `Video forwarded from the publisher's first keyframe waited=…`, the wait was short.
- A meeting with nobody else in it receives no media: the Jitsi server sets media up only
  once a second participant joins. Video then starts from the next keyframe.
- A viewer who joins later also needs a keyframe. The gateway passes their request on to
  OBS, so the same 1 s interval keeps their wait short too.

## Delay

Delay is the time from a change in OBS's picture to the same change on a viewer's screen.
The gateway itself adds almost nothing: it forwards each packet as it arrives, without
buffering. The rest comes from:

| Source | What to do |
|---|---|
| The encoder's look-ahead and B-frames | x264 Tune **zerolatency**; NVENC Look-ahead **off**, B-frames **0** |
| Stream Delay (Settings → Advanced) | **off** |
| A bitrate above what the network carries | Lower the bitrate: congestion queues packets, then they are lost |
| Rendering and encoding load | A hardware encoder (NVENC, QuickSync, AMD, Apple VT), if OBS shows dropped or lagged frames |
| The network between OBS, the gateway, the Jitsi server and viewers | Run the gateway near the Jitsi server |

Settings → Advanced → Network → "Enable network optimizations" and "Low latency mode" only
affect RTMP outputs, not WHIP.

## Quality

What gives a better picture, and what it costs:

| Setting | For more quality | Cost |
|---|---|---|
| Bitrate | Higher: about 2,500 kbps for 720p30, 4,500 kbps for 1080p30 | Every viewer must be able to receive it: the bridge cannot send them less |
| Settings → Video → Output (Scaled) Resolution | 1280x720 is a safe choice for meetings; 1920x1080 for a large shared screen | More bitrate needed for the same sharpness |
| Settings → Video → FPS | 30; 60 only for fast motion | Twice the bitrate for the same sharpness |
| x264 CPU Usage Preset | `veryfast`, or `faster` / `fast` if the CPU allows | CPU; dropped frames if the encoder falls behind |
| NVENC Preset | P5 or higher (Quality) | GPU load |
| Profile | `high` | None found: High-profile video played in Chrome 154 and Firefox 155, although the meeting negotiates baseline. Not tried with Safari |
| Keyframe Interval | 2 s instead of 1 s | Saves a little bitrate; viewers may wait up to 2 s for the picture |
| Audio bitrate (Output → Audio) | 128 kbps for music; 64–96 kbps is enough for speech | Small |

## Three starting points

| | Quick start, low delay | Balanced | Best picture |
|---|---|---|---|
| Resolution, FPS | 1280x720, 30 | 1280x720, 30 | 1920x1080, 30 |
| Encoder | NVENC/QuickSync H.264, or x264 `veryfast` | x264 `veryfast` or a hardware H.264 encoder | x264 `faster`/`fast`, or NVENC P6 |
| Rate control, bitrate | CBR, 2,000 kbps | CBR, 2,500 kbps | CBR, 4,500 kbps |
| Keyframe interval | 1 s | 1 s | 2 s |
| Tune / B-frames | zerolatency / 0 | zerolatency / 0 | zerolatency / 0 |
| Profile | high | high | high |
| Audio | Opus, 96 kbps | Opus, 128 kbps | Opus, 128 kbps |

Check OBS's status bar while streaming. Dropped frames (network) mean the bitrate is too
high for the connection. Lagged or skipped frames (encoding or rendering) mean the encoder
preset or the resolution is too heavy for the machine.

## What was measured and what was not

- **Measured** on a local Jitsi server, with ffmpeg 8's WHIP client standing in for OBS (x264,
  High profile, Opus):
  - the start-up steps and the keyframe waits above;
  - High-profile H.264 playing in Chrome 154 and Firefox 155 at 1280x720 and 30 fps.
- **Not measured with OBS itself:**
  - whether it honours keyframe requests (ffmpeg does not);
  - the delay each setting adds;
  - the bitrate and quality figures, which are common starting points, not results.
- **Not tried:** Safari, and real networks with packet loss.
