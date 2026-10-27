# Feature Specification: Jitsi Media Session

**Feature Branch**: `001-jitsi-media-session`

**Created**: 2026-10-24

**Status**: Draft

**Input**: User description: "A CLI and Go library that joins one Jitsi Meet room as one participant
(local server, real deployments as guest, or JWT), publishes audio+video from a file, camera/mic, or
an OBS SRT/RTMP stream, and plays received media: Jitsi-style tile grid display with mixed audio,
recording (per-participant files or one mixed file), SRT feeds per participant and of the grid for
OBS Media Sources; bounded selection (all audio, last 9 speakers' video low-res, pins by display
name); library exposes decoded frames/samples both ways; Linux/Windows/macOS." Approved design:
`docs/superpowers/specs/2026-10-24-jitsi-media-tools-design.md`.

**Revised 2026-10-27**: OBS sends its program into meetings through a WHIP gateway (User Story 9)
instead of an SRT/RTMP listener in a session; OBS's way out (SRT feeds) is unchanged.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Publish a media file into a room (Priority: P1)

An operator runs the tool with a meeting URL, a display name and a media file. The tool joins the
room as one participant, and the people in the meeting see and hear the file's video and audio,
looped or played once.

**Why this priority**: Joining a room and being seen and heard is the foundation of every other
story, and a file is the simplest source to reproduce.

**Independent Test**: Run the tool against the local Jitsi server with a short clip; open the same
room in a browser and check the participant appears with the given name and plays the clip's
picture and sound.

**Acceptance Scenarios**:

1. **Given** a running Jitsi server and a room URL, **When** the operator starts the tool with a
   display name and a file, **Then** a participant with that name appears in the room within
   10 seconds and the browser shows the file's video and plays its audio.
2. **Given** the tool is publishing a file once, **When** the file ends, **Then** the tool leaves
   the room and exits successfully; with looping on, it starts the file again without leaving.
3. **Given** the tool is in the room, **When** the operator stops it (Ctrl+C), **Then** it leaves
   the room cleanly and the participant disappears from the browser within 5 seconds.

---

### User Story 2 - Watch the room in a tile grid (Priority: P1)

The operator asks the tool to display the room. A window opens showing the other participants in a
Jitsi-style tile grid with their names, and the room's mixed audio plays on the speakers.

**Why this priority**: Playing received media is the second half of the tool's purpose, and the
grid is the most direct way to check it works.

**Independent Test**: With two browser participants in the room sending camera and microphone,
start the tool with display on; both appear as named tiles and both are audible.

**Acceptance Scenarios**:

1. **Given** two participants sending video and audio, **When** the tool joins with display on,
   **Then** a window shows two tiles labeled with their display names and both voices are audible.
2. **Given** the window is showing, **When** a participant joins or leaves, **Then** the grid adds
   or removes that tile within 5 seconds.
3. **Given** a participant has their camera off, **Then** their tile shows their name on a plain
   background instead of video.

---

### User Story 3 - Use the session from another program (Priority: P2)

A developer embeds the library in their own Go program. With a few lines they join a room, receive
each participant's decoded video frames and audio samples with participant name and join/leave
events, and push their own frames and samples to publish.

**Why this priority**: Library use is a stated goal and shapes the whole design; the CLI is built
on the same API.

**Independent Test**: A small example program joins the local room, counts frames received from a
browser participant, and publishes a generated test pattern that the browser shows.

**Acceptance Scenarios**:

1. **Given** a program using the library, **When** it joins a room with one browser participant,
   **Then** it is told the participant joined (with display name) and receives that participant's
   video frames and audio samples in decoded form.
2. **Given** the program publishes generated frames and samples, **Then** the browser shows the
   picture and plays the sound.
3. **Given** a participant leaves, **Then** the program is told the participant left and receives
   no more media for them.

---

### User Story 4 - Bring the meeting into OBS (Priority: P2)

Each participant, and optionally the whole grid, is made available as a stream address that OBS
opens as a Media Source. (OBS's way into the meeting is User Story 9.)

**Why this priority**: OBS integration is the main reason for the project after basic join and
play.

**Independent Test**: a Media Source in OBS opened on a participant's address shows that
participant.

**Acceptance Scenarios**:

1. **Given** OBS feeds are on, **When** a participant is in the room, **Then** the tool prints a
   stream address for that participant, and an OBS Media Source opened on it shows their video and
   plays their audio.
2. **Given** a participant's feed is open in OBS, **When** they leave and rejoin with the same name,
   **Then** the same address shows them again without changing the OBS source; while they are away
   the address shows a placeholder picture.
3. **Given** the grid feed is on, **Then** one more address carries the whole grid with the mixed
   audio.

---

### User Story 5 - Join real deployments as a guest or with a token (Priority: P2)

The operator joins a room on a real Jitsi deployment, either as a guest where the server allows
it, or with a token (one supplied ready-made, or one the tool signs from an app id and secret).

**Why this priority**: The tool is meant for real meetings, not only the local test server.

**Independent Test**: Join a room on a deployment that allows guests, and a room on a
token-authenticated server, from their meeting URLs only.

**Acceptance Scenarios**:

1. **Given** only a meeting URL of a server that allows guests, **When** the operator starts the
   tool, **Then** it finds the server's connection settings by itself and joins.
2. **Given** a server where guests wait for a host, **Then** the tool reports it is waiting and
   joins once the host lets guests in, or stops with a clear message after a timeout.
3. **Given** a token-authenticated server, **When** the operator supplies a token or an app id and
   secret, **Then** the tool joins; with a bad or missing token it stops with a message saying
   authentication failed.
4. **Given** a server with a self-signed certificate, **When** the operator turns off certificate
   checks, **Then** the tool connects; without that option it refuses and says why.

---

### User Story 6 - Record the meeting (Priority: P3)

The operator records the meeting, choosing either one file per participant or one mixed file that
looks like the grid with the mixed audio.

**Why this priority**: Useful, but it builds on the receive path of stories 2 and 4.

**Independent Test**: Record a 1-minute session with two browser participants in each mode and
play the files back.

**Acceptance Scenarios**:

1. **Given** per-participant recording, **When** the session ends, **Then** there is one playable
   file per participant who sent media, named after the participant, with their audio and video.
2. **Given** mixed recording, **When** the session ends, **Then** there is one playable file with
   the grid and mixed audio.
3. **Given** the tool is stopped with Ctrl+C, **Then** the files written so far are playable.

---

### User Story 7 - Publish from a camera and microphone (Priority: P3)

The operator lists the capture devices on their machine and publishes a chosen camera and
microphone into the meeting.

**Why this priority**: A common source, but files and OBS already cover the first uses.

**Independent Test**: List devices, pick a webcam and a microphone, and check the browser shows
and plays them.

**Acceptance Scenarios**:

1. **Given** a machine with a camera and a microphone, **When** the operator asks for the device
   list, **Then** the tool prints their names.
2. **Given** a chosen camera and microphone, **When** the tool joins, **Then** the meeting shows
   the camera and plays the microphone.
3. **Given** a device name that does not exist, **Then** the tool stops before joining and lists
   the available devices.

---

### User Story 8 - Stay usable in large rooms (Priority: P3)

In rooms of up to 100 participants, the tool takes everyone's audio but video only for a limited
number of people (by default the 9 most recent speakers, at low resolution). The operator can pin
participants by display name so they always get their video, optionally at higher resolution.

**Why this priority**: Required for larger rooms, but small rooms work without it.

**Independent Test**: In a room with more participants than the limit, check the shown tiles
follow the speakers, and a pinned participant stays shown when others talk.

**Acceptance Scenarios**:

1. **Given** 20 participants sending video, **When** the tool displays the room, **Then** at most
   9 tiles show video, and the set follows who spoke most recently.
2. **Given** a participant pinned by display name, **Then** their video is always shown,
   including when they are not speaking, and at the higher resolution if one was asked for.
3. **Given** display, recording and OBS feeds are all on, **Then** all three use the same set of
   participants.

---

### User Story 9 - Stream OBS into any meeting through a WHIP gateway (Priority: P2)

An operator runs the tool as a gateway on a public server. A streamer sets OBS's WHIP output to
the gateway's address, followed by the meeting's server and room and a display name, with the
gateway's shared secret as the bearer token. When they start streaming, OBS's program appears in
that meeting as a participant with that name; when they stop, the participant leaves. Several
streamers can use one gateway at the same time, into the same meeting or different ones, on any
Jitsi deployment.

**Why this priority**: it is OBS's way into the meeting, the main reason for the project after
basic join and play. OBS has WHIP built in, so nothing runs next to OBS.

**Independent Test**: start the gateway; in OBS, set Stream → WHIP with the server
`http://127.0.0.1:8080/whip/localhost:8443/test?name=OBS` and the secret, and start streaming;
the browser in `https://localhost:8443/test` shows OBS's scene and hears its audio; stop
streaming and "OBS" leaves.

**Acceptance Scenarios**:

1. **Given** the gateway runs, **When** OBS starts streaming to a meeting address with the right
   secret, **Then** the meeting shows a participant with the given name, with OBS's video and
   audio, within 5 seconds of a second participant being in the room.
2. **Given** OBS is streaming through the gateway, **When** OBS stops streaming, **Then** the
   participant leaves the meeting within 2 seconds.
3. **Given** OBS's connection drops without OBS stopping, **Then** the participant leaves within
   10 seconds of the last media received.
4. **Given** a request without the secret or with a wrong one, **Then** the gateway refuses it
   and joins nothing.
5. **Given** two OBS instances stream to two different meetings at once, **Then** each meeting
   shows its own stream, and stopping one does not affect the other.
6. **Given** OBS streams video in a codec other than H.264 (AV1, HEVC), **Then** the gateway
   refuses the request with a message that names H.264, and joins nothing.

---

### Edge Cases

- The meeting URL points to a server that is not Jitsi, or the room name is invalid: the tool stops
  with a clear message before trying media.
- The connection to the server drops mid-session: the tool reports it and exits with an error; it
  does not hang silently.
- The required media program is missing or too old: the tool says what is missing before joining.
- Two participants share a display name: each gets a distinct label and feed address (a suffix).
- A participant sends audio only, or video only: their tile and feed handle the missing part
  (name card, silence).
- The publish source ends or breaks (a camera is unplugged): the tool keeps
  the participant in the room with a placeholder and resumes when the source returns; a file that
  cannot be read stops the tool before joining.
- A participant's video codec cannot be decoded: their tile shows the name card and the tool logs
  a warning once.
- More pins than the video limit: the tool reports the cap and shows the first ones.
- The tool is killed abruptly: no helper processes are left running afterwards.
- A WHIP stream's meeting cannot be joined (not a Jitsi server, a guest refused, a bad token):
  the gateway refuses the request with the reason, and OBS shows its connection error.
- A WHIP stream arrives in an empty room: the participant joins and waits; its media reaches the
  meeting once a second participant is there (the server sets up media only then).
- The gateway is stopped: every gateway participant leaves its meeting cleanly.

## Requirements *(mandatory)*

### Functional Requirements

**Joining**

- **FR-001**: The tool MUST join a room from a meeting URL and a display name, as exactly one
  participant.
- **FR-002**: The tool MUST discover a server's connection settings from the meeting URL alone, and
  MUST let the operator override each setting.
- **FR-003**: The tool MUST support joining as a guest, waiting for a host where the server requires
  it, with a configurable timeout.
- **FR-004**: The tool MUST support token authentication with a ready-made token, or a token it
  signs from an app id and secret (with audience, subject, lifetime and moderator options).
- **FR-005**: The tool MUST allow turning off certificate checks for servers with self-signed
  certificates, and MUST refuse invalid certificates otherwise.
- **FR-006**: The tool MUST leave the room cleanly when stopped, and MUST report a lost connection
  as an error exit.

**Publishing**

- **FR-007**: The tool MUST publish at most one audio and one video stream, from exactly one of:
  a media file (once or looped), a camera and microphone chosen by name, or frames and samples
  supplied by a program using the library. (OBS comes in through the gateway, FR-024.)
- **FR-008**: The tool MUST list the capture devices available on the machine.
- **FR-009**: The tool MUST allow publishing audio only or video only.
- **FR-010**: When a live source stops, the tool MUST keep the participant in the room, send a
  placeholder, and resume when the source returns.

**Receiving and selection**

- **FR-011**: The tool MUST receive the audio of all participants, and the video of a bounded set:
  by default the 9 most recent speakers at low resolution; the limit and the default resolution
  MUST be configurable.
- **FR-012**: The operator MUST be able to pin participants by display name so their video is
  always received, with an optional higher resolution; the total stays within the limit.
- **FR-013**: Display, recording and OBS feeds MUST all use the same selection.
- **FR-014**: The tool MUST tell which participant each received stream belongs to, by display
  name, and follow participants joining, leaving and renaming.

**Playing**

- **FR-015**: The tool MUST be able to show a window with a tile grid of the selected participants,
  labeled with their display names, and play the mixed audio of the room.
- **FR-016**: The tool MUST be able to record, by option, one file per participant or one mixed
  file of the grid and mixed audio, and the files MUST stay playable if the tool is stopped.
- **FR-017**: The tool MUST be able to provide one stream address per participant, and optionally
  one for the grid, that OBS opens as a Media Source; a participant's address MUST stay the same for
  the whole session, including after leave/rejoin and selection changes, and show a placeholder
  while they have no video.
- **FR-018**: The tool MUST print the addresses it listens on and serves, so the operator can copy
  them into OBS.
- **FR-019**: Display, recording, OBS feeds and publishing MUST be usable in any combination in one
  session.

**Gateway (WHIP)**

- **FR-024**: The tool MUST be able to run as a gateway that accepts WHIP publishers (RFC 9725) on
  HTTP, each at an address naming the meeting (server and room, optionally with a token) and the
  display name; each accepted stream MUST join that meeting as one participant.
- **FR-025**: The gateway MUST require a shared secret as the bearer token of every request, MUST
  refuse to start without one, and MUST refuse requests without it.
- **FR-026**: The gateway MUST join any Jitsi deployment named in the address, with the same
  discovery, guest and token support as a session (FR-002–FR-005).
- **FR-027**: The gateway MUST forward the publisher's H.264 video and Opus audio to the meeting
  without decoding or re-encoding them, and MUST pass the meeting's keyframe requests back to the
  publisher. It MUST refuse a publisher that offers no H.264 video or no Opus audio, unless it
  sends only the other kind.
- **FR-028**: The participant MUST leave when the publisher ends the stream (WHIP resource
  deleted) or after 10 seconds without media, and all participants MUST leave when the gateway
  stops.
- **FR-029**: The gateway MUST serve several publishers at once, independently: one failing or
  ending MUST NOT affect the others; the number at once MUST be capped by a setting.
- **FR-030**: The gateway MUST be usable on a public server: media on one configurable UDP port,
  a configurable public address for publishers behind NAT, and HTTPS either built in (certificate
  and key) or from a reverse proxy in front.
- **FR-031**: The gateway MUST log each stream's start, end and failure with the meeting and
  name, and MUST NOT log the secret or tokens.

**Library**

- **FR-020**: Everything the command-line tool does MUST be available to Go programs through the
  public library.
- **FR-021**: The library MUST deliver received media as decoded video frames and audio samples,
  with participant identity and join/leave events, and accept decoded frames and samples to
  publish; it MUST NOT require its users to handle network packets, session descriptions or codecs.

**Platform**

- **FR-022**: The tool MUST work on Linux, Windows and macOS, including capture devices on each.
- **FR-023**: The tool MUST check that its media helper program is present and usable before
  joining, and say what is missing otherwise; no helper process MAY outlive the session.

### Key Entities

- **Session**: one participant in one room: the server and room, identity (display name, token),
  the publish source, the active outputs, and the selection settings.
- **Participant**: another person in the room: an id, a display name (made unique), whether they
  send audio and video, whether they are pinned, and when they last spoke.
- **Publish source**: where the published media comes from: file, devices, or program frames.
- **Output**: what is done with received media: display, recording (per participant or mixed), OBS
  feeds (per participant and grid), or delivery to a program.
- **Gateway**: a long-running server: its HTTP address, shared secret, UDP port, public address
  and stream cap.
- **WHIP stream**: one publisher on the gateway: its meeting, display name, resource address, and
  the participant it joined as.
- **Selection**: the set of participants whose video is received, with the limit, the pins and the
  resolutions.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: From the command line, an operator joins the local test room and is seen and heard in
  a browser within 10 seconds of starting the tool.
- **SC-002**: A participant who joins the room appears in the grid, the OBS feeds and the library
  events within 5 seconds.
- **SC-003**: The displayed picture and sound lag the browser by under about 1 second; OBS feeds by
  under about 2 seconds.
- **SC-004**: Audio and video of one participant stay in sync within 100 ms in the display,
  recordings and feeds.
- **SC-005**: In a 100-participant room with the default selection and display, recording and OBS
  feeds all on, an ordinary recent laptop keeps up for 30 minutes without dropping out of the room or
  freezing the grid.
- **SC-006**: A developer writes a working program that joins a room and receives frames in under
  30 lines, using only the documented library.
- **SC-007**: The same automated tests pass on Linux, Windows and macOS.
- **SC-008**: After any exit, including Ctrl+C and a lost connection, no helper process from the
  session is left running.
- **SC-009**: The gateway adds under 50 ms to OBS's own delay into the meeting, and one gateway
  serves 10 streams at 720p30 at once on a 2-core server without falling behind.

## Assumptions

- Target servers are Jitsi Meet deployments recent enough to support the bridge's receive
  preferences (as the local docker-jitsi-meet server does); older servers are out of scope.
- A media helper program (ffmpeg, per the design) is installed on the machine or its path is given;
  the tool does not bundle it.
- OBS (30 or newer) uses its built-in WHIP output to come in and Media Source to take feeds out;
  no OBS plugin is installed. OBS's video settings (size, bitrate, keyframe interval) are what the
  meeting receives; the gateway does not change them.
- The gateway's participants send only; they receive no video and ignore received audio.
- Handing out per-stream WHIP addresses and tokens through an API (instead of one shared secret)
  is a later feature.
- Display is watch-only: pins and selection are set at start-up by options, or at any time through
  the library.
- Hand testing happens on Linux with the local server at `https://localhost:8443`; Windows and
  macOS are covered by automated tests only until machines are available.
- Screen sharing from another participant gets its own tile, named "<name> (screen)", next to
  their camera tile and counted in the video limit (revised 2026-10-25: one tile per participant
  showed the camera and the screen share in turn). Library video callbacks carry the camera only.
- Chat, moderation, lobby management, breakout rooms and end-to-end encrypted meetings are out of
  scope.
- The project is independent of `webrtc-load-engine` and starts from a copy of its Jitsi signaling.
