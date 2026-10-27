# End-to-end latency

How long a picture or a sound takes to go from one participant's program to another
participant's screen and speakers, through the tool and a Jitsi server. The numbers below
were measured on 2026-10-25 against the local Jitsi server
(`tests/platforms/jitsi`). This document says where the time goes and what was done to
reduce it.

## Summary

| Measured from the publisher's `FrameSource` write to… | Video | Audio |
|---|---|---|
| the viewer's library callbacks (`OnGrid`, `OnMixedAudio`) | **≈ 110 ms** (was 390) | **≈ 95 ms** (was 455) |
| the viewer's screen and speakers (`--display`) | **≈ 225 ms** (was 555) | **≈ 185 ms** (was 640) |

All values are medians of 6 to 10 flashes or tone bursts. Video to the callbacks varies
between 89 and 124 ms from one session to the next: 0 to 2 frames stay queued from the
encoder's start. Four changes made the difference; see [How it was reduced](#how-it-was-reduced).
The rest of this section, and the next one, describe the pipeline as it was before them.

- The network and the Videobridge take **less than 1 ms** on localhost. Almost all the
  latency is inside the tool and ffmpeg.
- **About 310 ms of it is a queue that fills once and never drains.** The `FrameSource`
  queue (10 video frames, 20 audio chunks) is full when the encoder starts. The encoder
  takes it in one burst, and the sender then paces that burst at exactly real-time speed,
  so the backlog stays for the whole session. With 2-item queues, video reaches the grid
  in **130 ms** and audio reaches the mix in **147 ms** (measured).
- The display writer's queues hold a smaller standing backlog of the same kind: about
  50 ms for video and 90 ms for audio (measured).
- Audio is **65–90 ms behind video** at every stage. Its backlog is deeper, and the
  encoder's audio input arrives in 85 ms blocks.

## Pipeline and where the time goes

See [media-pipeline.svg](media-pipeline.svg) for the stages. Medians, in ms after the
flash or tone was started by the publishing program:

| Stage | Video | Audio | What takes the time |
|---|---:|---:|---|
| Next frame or chunk written | 0–33 | 0–20 | The program writes at 30 fps or every 20 ms. Part of the next row. |
| **Leaves the publisher as RTP** | **372** | **400** | `FrameSource` queue backlog (~310), then ffmpeg encoding (video ~45, audio ~40–85), then the sender's pacing |
| Received and reassembled by the viewer | 372 | 400 | Network and Videobridge: < 1 ms on localhost |
| Decoded (`OnVideo`) / in the mix (`OnMixedAudio`) | 373–384 | 440–460 | Video decoder ~1–2 ms. Audio: ffmpeg's Ogg handling (~20 ms) and the 20 ms mix clock |
| In the grid (`OnGrid`) | 391–396 | — | 30 fps grid clock and the callback queue: +20–33 ms |
| **On screen / at the speakers** | **554** | **642** | Display writer queues (video ~50, audio ~90), then ffplay, SDL, PipeWire and the USB headset (video ~110, audio ~90) |

### Publisher: `FrameSource` queue and pacing (~310 ms)

`FrameSource` keeps up to `framesVideoQueue = 10` frames (333 ms) and
`framesAudioQueue = 20` chunks (400 ms) for the encoder, and drops the oldest when full
(`internal/session/frames.go`). When the encoder process starts, the queue is already
full: frames were written while ffmpeg was starting. ffmpeg reads the whole queue at once
and encodes it as a burst.

The sender (`rtpmedia.Sender`, `internal/rtpmedia/sender.go`) paces each frame one frame
duration after the previous one. That is exactly the rate at which new frames come in, so
the burst never drains. Every later frame waits behind it for the whole session: about
10 video frames and 15 audio frames in these runs.

**Evidence:** timestamps at the sender show the flash frames leaving at +372 ms. The local
chain alone (encoder, packetizer, receiver, decoder, with no network) takes 59 ms for video
and 85 ms for audio. With both queues set to 2, everything after the encoder moves about
270–310 ms earlier.

### Publisher: ffmpeg encoder (video ~45 ms, audio ~40–85 ms)

- **Video.** The `fps=30` filter in the encoder's `-vf` (`internal/ffmpeg/encoder.go`)
  holds each frame until the next one arrives, so it can decide whether to drop or
  duplicate. Encoding takes 43 ms with the filter and 3 ms without it (640x360, VP8
  realtime, `-cpu-used 8`).
- **Audio.** ffmpeg's raw PCM demuxer (`-f s16le`) reads packets of about 100 ms rounded
  down to a power of two: 4096 samples, or 85 ms. The Opus encoder therefore gets audio in
  85 ms blocks and outputs 4 frames at a time. That is 0–85 ms of waiting, depending on
  where a sound starts within a block. Plain PCM passed through ffmpeg, with no codec,
  shows the same 85 ms bursts. Reading options (`-avioflags direct`, `-blocksize`) and
  Ogg options (`-page_duration`, `-max_delay`) don't change it.

### Network and Videobridge (< 1 ms locally)

The viewer received the flash frames 0.3–0.5 ms after the publisher sent them. On a real
network, add the one-way path from publisher to bridge and from bridge to viewer, and any
retransmission delays. The receiver has no jitter buffer: the frame assembler waits a
bounded time for missing packets, then moves on.

### Viewer: reassembly, decoding and composition (video ~20–35 ms, audio ~40–60 ms)

- **Video.** The frame assembler releases a frame when its last packet arrives. The ffmpeg
  decoder takes 1–2 ms at 180p and 2–5 ms at 720p. The grid is drawn every 33 ms from the
  latest frame of each tile, which adds 0–33 ms.
- **Audio.** ffmpeg's Ogg demuxer outputs a packet one page later (20 ms, measured with
  `-c:a copy`). The mixer then works on a 20 ms clock.

### Viewer: display (video ~160 ms, audio ~185 ms)

- **Display writer queues.** `displayVideoQueue = 3` and `displayAudioQueue = 10`
  (`internal/output/display.go`) hold a standing backlog. Shrinking them to 1 and 2 cut
  the step to the screen from 160 to 110 ms, and the step to the speakers from 185 to
  92 ms.
- **ffplay and the output devices.** The remaining 110 ms (video) and 92 ms (audio) are
  ffplay's frame and sample queues, the SDL audio buffer, PipeWire and the USB headset.
  Switching ffplay from following the audio clock to its own clock (`-sync ext`) changed
  nothing. The screen capture (x11grab at 60 fps) can add up to 17 ms of its own, and the
  audio capture about 4 ms.

## Not latency, but found along the way

Pion's `oggwriter`, which the receiver uses to wrap Opus as Ogg, writes a **pre-skip of
3840 samples** in the Ogg header. ffmpeg honours it and drops the first 80 ms of audio
each time an audio decoder starts. Decoders also restart after 10 s without packets, so
the first 80 ms of speech after a long silence or a mute are lost.

## How it was reduced

| Change | Where | Gain |
|---|---|---|
| `FrameSource` queues of 2 video frames and 2 audio chunks, instead of 10 and 20: less is queued while the encoder starts, so the standing backlog is at most 2 items | `internal/session/frames.go` | ~270 ms video, ~300 ms audio |
| Display writer queues of 1 video frame and 2 audio chunks, instead of 3 and 10 | `internal/output/display.go` | ~50 ms video, ~90 ms audio |
| No `fps` filter for `FrameSource`, whose raw input is already 30 fps (`EncodeOptions.ConstantRate`) | `internal/ffmpeg/encoder.go` | ~40 ms video |
| `FrameSource` audio reaches the encoder as NUT, one packet per 20 ms chunk (`output.AudioNUT`), instead of raw `s16le` read in 85 ms blocks | `internal/output/nut.go`, `internal/session/frames.go` | ~55 ms audio; audio no longer trails video |

With all four, measured together (live test runs and one screen-and-speaker run):

| | Video | Audio |
|---|---|---|
| Write → callbacks | 89–124 ms | 77–103 ms |
| Write → screen and speakers | 227 ms | 184 ms |

What is left, and not done:
- **ffplay and the output devices** (~110 ms video, ~90 ms audio), outside the tool.
- **0–2 frames queued at the encoder's start**, which then stay for the session because
  the sender paces them at real time. A sender that catches up when frames wait would
  remove it. Measure it on a real network first: catching up sends a burst, which a lossy
  path may handle worse than a steady stream.
- **File, device and OBS sources** were not measured. They don't use `FrameSource`, but
  the encoder's own 8-frame output queue could hold a backlog in the same way after a
  burst.
- **Pre-skip:** received Ogg could declare 312 samples (the Opus encoder's real delay)
  instead of 3840. No latency gain, but it keeps the first 80 ms of each audio stream.

## How it was measured

- **Machine:** Linux on X11, 8 cores; ffmpeg n8.1.3 from `scripts/get-ffmpeg.sh`. The
  local Jitsi ran in Docker on the same machine. Audio played on a USB headset (Jabra
  Evolve) through PipeWire.
- **Media:** one publisher and one viewer in a fresh room. The publisher used
  `FrameSource` at 640x360 and 30 fps, with 48 kHz stereo audio in 20 ms chunks. The
  viewer used the default selection (decoding at 180p). Every 2 s, the publisher switched
  from gray video and silence to white video and a 1 kHz tone for 200 ms.
- **Timing:** each stage records when it first sees the change: a bright tile centre, a
  loud audio peak, or a different frame size at the sender and receiver. All clocks are
  the same machine's clock.
  - Sender and receiver timestamps came from a temporary hook in `rtpmedia`, which is not
    in the repository.
  - The screen was captured with `ffmpeg -f x11grab` over the ffplay window, at 60 fps.
  - The speakers were captured from the PulseAudio monitor of the output device.
- **Local stages:** the encoder and decoder were also run on their own, with the tool's
  own arguments, fed in real time without the network. A sound's start was used rather
  than counting frames, because ffmpeg drops the first 80 ms of decoded audio (see above),
  which makes frame counts misleading.

To check the totals again:

```bash
source scripts/env.sh
# Write → OnGrid / OnMixedAudio, 6 flashes; fails over 1 s median
JMT_URL=https://localhost:8443/jmt-latency JMT_INSECURE=1 \
  go test -tags live -count=1 -run TestLive_Latency -v ./tests/live/
# Write → screen, by eye: screenshot the window and the terminal clock together
go run ./tests/displaycheck --url https://localhost:8443/jmt-display --insecure
```
