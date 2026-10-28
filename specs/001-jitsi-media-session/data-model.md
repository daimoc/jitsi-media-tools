# Data Model: Jitsi Media Session (001)

In-memory only; nothing persists except recording files and `feeds.json`.

## Session

One participant in one room (public: `jmt.Session`).

| Field | Description | Rules |
|---|---|---|
| Server | resolved connection settings (R12) | from URL + overrides; WebSocket URL and XMPP domain required |
| Room | room name | from URL path; lower-cased; non-empty, no `/` or `@` |
| DisplayName | our name in the room | non-empty |
| Auth | none (guest) / Token / AppID+Secret (+audience, subject, TTL, moderator) | Token and AppID are exclusive |
| Publish | at most one PublishSource | — |
| Outputs | set of Output | any combination (FR-019) |
| Selection | Selection | — |
| State | see below | — |

**States**: `connecting → waiting_host? → joining → joined → leaving → closed`;
any state → `failed` (error kept, returned by `Wait`). `waiting_host` only for guests on
servers that need a host (timeout → `failed`). `joined → reconnect` is out of scope: a lost
connection is `failed` (FR-006).

## Participant

Another occupant (public: `jmt.Participant`, value type).

| Field | Description | Rules |
|---|---|---|
| ID | endpoint id (MUC nick) | stable for the occupant's stay |
| Name | unique display name | presence nick; empty → "Participant"; duplicates get ` (2)`, ` (3)` in join order |
| AudioMuted / VideoMuted | from presence | — |
| Pinned | matches a pin | case-insensitive match on display name |
| LastSpoke | time of last dominant-speaker event | zero if never |
| Selected | video currently received | ≤ Selection.Limit selected at once |

Events: `Joined`, `Left`, `Changed` (name, mute, pinned, selected). A rejoin with the same
name gets the same unique name back when it is free, so its feed address is reused.

## PublishSource

Exactly one kind:

| Kind | Fields | Rules |
|---|---|---|
| File | Path, Loop | file must exist and contain audio or video |
| Devices | VideoDevice, AudioDevice (names from `ListDevices`) | either may be empty (audio-only / video-only); unknown name → error listing devices |
| Frames | written by the program (`Publisher`) | frames `yuv420p`, Width×Height fixed at creation; audio s16le 48 kHz stereo |

Common: Width, Height (default 1280x720), FPS 30, Audio/Video enabled flags.

## Output

| Kind | Fields | Notes |
|---|---|---|
| Display | window title | needs ffplay |
| Record | Dir, Mode (per_participant / mixed) | files `<dir>/<room>-<time>-<name>.mkv` or `<dir>/<room>-<time>-mixed.mkv` |
| OBSFeeds | Host, BasePort (default 9000), MaxFeeds (default 16), Grid bool | grid on BasePort; participants on BasePort+1… in order of first selection; kept for the session |
| Program | callbacks / channels in the library | always available through `jmt.Session` |

## Selection

| Field | Default | Rules |
|---|---|---|
| Limit | 9 | 1–25 videos at once |
| Height | 180 | 180, 360 or 720 |
| Pins | [] | display names; at most Limit applied, extra pins logged |
| PinHeight | 720 | 180, 360 or 720 |
| Budget | 0 (none) | received video in bits/s; ≥ 100 kbps when set (FR-033) |

The bridge receives `lastN = Limit`, pinned sources as selected/on-stage, Height as the
default max height and PinHeight for pins (R8). With a Budget, the session sends a reduced
selection fitting it (R8, budget) and `assumedBandwidthBps = Budget`.

## Media units (public)

- **VideoFrame**: Participant ID, Width, Height, Y/U/V planes (`yuv420p`, tightly packed),
  Time (arrival on the session clock).
- **AudioChunk**: Participant ID (empty for the room mix), 960 frames of 48 kHz stereo
  `int16` interleaved (20 ms), Time.

## Gateway (US9)

| Entity | Fields | Rules |
|---|---|---|
| Gateway | Listen, Secret, UDPPort, PublicIP, TLS files, MaxStreams | refuses to start without a secret; past MaxStreams, requests get 503 |
| WHIP stream | ID (random, 128 bits), MeetingURL, DisplayName, Started, the Jitsi session, the publisher's peer | states: joining → waiting for media (alone in the room) → forwarding → ended; ends on DELETE, publisher peer failed/closed, 10 s without RTP, or gateway stop |
