# Local Jitsi Meet test platform

A minimal [docker-jitsi-meet](https://github.com/jitsi/docker-jitsi-meet)
(web, prosody, jicofo, jvb) for the live tests and hand checks of
`specs/001-jitsi-media-session/quickstart.md`. Copied from
webrtc-load-engine; the two use the same ports, so stop one before starting
the other.

- Web and meeting URLs: `https://localhost:8443/<Room>` (self-signed
  certificate: use `--insecure` / `JMT_INSECURE=1`). In a browser, open
  exactly `localhost`, not `127.0.0.1`: `config.js` points the page at
  `wss://localhost:8443/xmpp-websocket`, and a certificate accepted for
  `127.0.0.1` does not cover it, so the meeting never joins.
- XMPP domain `meet.jitsi`, MUC `muc.meet.jitsi`, WebSocket
  `wss://localhost:8443/xmpp-websocket`: the tool reads them from
  `config.js`.
- Ports: 8000/tcp, 8443/tcp, 10000/udp (media), 127.0.0.1:8080 (bridge
  REST), 127.0.0.1:8888 (jicofo REST).

## Start

```bash
cd tests/platforms/jitsi
./setup.sh            # .env with fresh passwords, ~/.jitsi-media-tools-cfg
docker compose up -d
```

Then, from the repository root:

```bash
source scripts/env.sh   # project ffmpeg (scripts/get-ffmpeg.sh) and Go in PATH
go run ./cmd/jitsi-media-tools join --url https://localhost:8443/test --insecure --name Bot --display
JMT_URL=https://localhost:8443/jmt-live JMT_INSECURE=1 go test -tags live -count=1 -v ./tests/live/
```

Open the same room in a browser to see and hear the tool.

## Guests and tokens

`env.example` has two commented blocks: a secure domain with guests (a
guest waits until a host logs in) and token authentication. Uncomment one in
`.env`, then `docker compose up -d` (the containers rewrite their
configuration at each start). For the secure domain, register the host
account with the command shown in `env.example`.

## Stop

```bash
docker compose down
```
