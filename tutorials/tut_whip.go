package main

import (
	"os/exec"
	"time"
)

const image = "ghcr.io/daimoc/jitsi-media-tools:main"

// whipPublisher stands in for OBS: ffmpeg 8 sends a test pattern by WHIP,
// as OBS would.
func whipPublisher(s *Stage, room, name string) *exec.Cmd {
	return s.Background("publisher-"+name, "ffmpeg", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30", "-f", "lavfi", "-i", "sine=frequency=440",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "30",
		"-c:a", "libopus", "-ar", "48000", "-ac", "2",
		"-f", "whip", "-authorization", "s3cret", "http://127.0.0.1:8090/whip/localhost:8443/"+room+"?name="+name)
}

var whipTutorial = tutorial{
	name:  "whip",
	title: "Stream OBS into a meeting (WHIP gateway)",
	prepare: func(s *Stage) {
		s.Off("docker", "pull", "-q", image)
		s.AtClose(func() { exec.Command("docker", "rm", "-f", "jmt-whip").Run() })
		s.Meeting("tut-whip")
		s.Card("", titleCard("Stream OBS into a meeting", "The WHIP gateway: OBS streams to it, and each stream joins a Jitsi meeting as a participant."))
		s.Pause(8 * time.Second)
	},
	run: func(s *Stage) {
		const steps = 5
		s.Step(steps, "Introduction")
		s.Say("The WHIP gateway brings O B S into a Jitsi meeting.")
		s.Say("O B S streams to the gateway, and each stream joins the meeting named in its address, as a participant. The video and audio pass through unchanged, so the gateway needs no f f m peg.")

		s.Step(steps, "Start the gateway")
		s.Meeting("tut-whip")
		s.Say("On the right, a browser is in the meeting tut-whip.")
		n := s.Narrate("First, choose a secret. O B S sends it with every stream, and the gateway refuses streams without it.")
		s.Run("export WHIP_API_SECRET=s3cret", 5*time.Second)
		n.Wait()
		n = s.Narrate("Then serve starts the gateway. Here it listens on port 8090, because the local Jitsi server already uses port 8080.")
		s.Type("jitsi-media-tools serve --insecure --listen :8090 --log-level warn")
		s.WaitFor("whip http", 15*time.Second)
		n.Wait()

		s.Step(steps, "Set up OBS")
		s.Card("OBS: Settings, Stream", tableCard("OBS: Settings → Stream", "",
			[2]string{"Service", "WHIP"},
			[2]string{"Server", code("http://127.0.0.1:8090/whip/localhost:8443/tut-whip?name=OBS")},
			[2]string{"Bearer Token", "the secret: " + code("s3cret")},
			[2]string{"Video encoder", "H.264 (x264 or hardware)"},
			[2]string{"Audio encoder", "Opus"},
			[2]string{"Keyframe interval", "1 s"},
		))
		s.Say("In O B S, open Settings, Stream. Choose the WHIP service.")
		s.Say("The server address names the meeting: the Jitsi server, then the room, then the participant's name.")
		s.Say("The bearer token is the secret. Set the encoder to H.264, and the keyframe interval to one second, so new viewers see the picture quickly.")

		s.Step(steps, "Stream")
		s.Meeting("tut-whip")
		s.Say("O B S is not installed here, so f f m peg plays its part. It sends a test pattern by WHIP, exactly as O B S would.")
		pub := whipPublisher(s, "tut-whip", "Pattern")
		s.WaitFor("+ Pattern", 20*time.Second)
		s.Pause(3 * time.Second) // The picture follows the participant by about 2 s
		s.Say("The gateway prints a plus line: Pattern has joined the meeting, and the browser shows its picture.")
		s.Pause(5 * time.Second)
		s.Stop(pub)
		s.WaitFor("- Pattern", 20*time.Second)
		s.Say("When the stream stops, the participant leaves, and the gateway prints a minus line.")
		s.Ctrl("c")
		s.WaitFor("$ ", 15*time.Second)

		s.Step(steps, "With Docker")
		n = s.Narrate("The gateway also runs in Docker, from the published image. On Linux, the host network keeps its ports as they are.")
		s.Type("docker run --rm --name jmt-whip --network host -e WHIP_API_SECRET " + image + " serve --insecure --listen :8090 --log-level warn")
		s.WaitFor("whip http", 30*time.Second)
		n.Wait()
		pub = whipPublisher(s, "tut-whip", "OBS")
		s.WaitFor("+ OBS", 20*time.Second)
		s.Pause(3 * time.Second)
		s.Say("A stream named O B S joins, this time through the container.")
		s.Pause(4 * time.Second)
		s.Stop(pub)
		s.WaitFor("- OBS", 20*time.Second)
		s.Ctrl("c")
		s.WaitFor("$ ", 20*time.Second)

		s.Card("Docker Compose", codeCard("On a server: Docker Compose",
			"$ curl -O https://raw.githubusercontent.com/daimoc/jitsi-media-tools/main/compose.yaml\nWHIP_API_SECRET=change-me JMT_PUBLIC_IP=203.0.113.10 docker compose up -d",
			code("JMT_PUBLIC_IP")+": the address OBS reaches the server at. The container cannot see it.",
			"Open the HTTP port (TCP 8080) and the media port (UDP 8189). Across the internet, use HTTPS: "+code("--tls-cert")+" and "+code("--tls-key")+", or a reverse proxy.",
		))
		s.Say("On a server, Docker Compose runs the gateway with its two ports published. Give it the secret, and the public address that O B S reaches.")
		s.Say("Open the HTTP port and the media port in the firewall, and use HTTPS across the internet, since the token travels with every request.")
		s.Card("", endCard("Next: bring the meeting into OBS, with SRT feeds and Docker."))
		s.Say("Next, the other direction: bringing the meeting into O B S.")
		s.ClearCaption()
		s.Pause(time.Second)
	},
}
