package main

import (
	"os/exec"
	"time"
)

var feedsTutorial = tutorial{
	name:  "feeds",
	title: "Bring a meeting into OBS (SRT feeds, in Docker)",
	prepare: func(s *Stage) {
		s.Off("docker", "pull", "-q", image)
		s.AtClose(func() { exec.Command("docker", "rm", "-f", "jmt-feeds").Run() })
		s.Background("bunny", "jitsi-media-tools", "join", "--url", s.cfg.jitsi+"/tut-feeds", "--insecure",
			"--name", "Bunny", "--publish", "file-loop:samples/bunny.mp4")
		s.Meeting("tut-feeds")
		s.Card("", titleCard("Bring a meeting into OBS", "SRT feeds of each participant and of the whole room, for OBS Media Sources. Run in Docker."))
		s.Pause(10 * time.Second)
	},
	run: func(s *Stage) {
		const steps = 4
		s.Step(steps, "Introduction")
		s.Say("jitsi-media-tools can bring a Jitsi meeting into O B S.")
		s.Say("It serves each participant, and the whole room, as an S R T feed that an O B S Media Source plays. This time, the tool runs in Docker.")

		s.Step(steps, "Serve the feeds")
		s.Meeting("tut-feeds")
		s.Say("In the meeting tut-feeds, Bunny is publishing a video.")
		s.Run("R=https://localhost:8443/tut-feeds", 5*time.Second)
		n := s.Narrate("Dash dash obs serves a feed for each participant, and dash dash obs grid adds the whole room as a tile grid. Dash dash obs host keeps the feeds on this machine, and the host network keeps their ports as they are.")
		s.Type("docker run --rm --name jmt-feeds --network host " + image + " join --url $R --insecure --name Feeds --obs --obs-grid --obs-host 127.0.0.1 --video-height 360 --log-level warn")
		s.WaitFor("feed Bunny", 40*time.Second)
		n.Wait()
		s.Say("Each feed has its own address: the grid on port 9000, and Bunny on port 9001. A participant keeps the same address for the whole session.")
		s.Say("A feed is as sharp as the video the tool receives. Dash dash video height 360 asks the bridge for more than the default, 180.")

		s.Step(steps, "Add them to OBS")
		s.Card("OBS: add a Media Source", tableCard("OBS: Sources → + → Media Source", "",
			[2]string{"Local File", "unticked"},
			[2]string{"Input", code("srt://127.0.0.1:9001") + " (Bunny)<br>" + code("srt://127.0.0.1:9000") + " (the grid)"},
			[2]string{"Video", "640x360 per participant, 1280x720 for the grid"},
			[2]string{"Audio", "the participant's own; the grid has everyone mixed"},
		))
		s.Say("In O B S, add a Media Source, untick Local File, and set the input to the feed's address.")
		feedFrame(s, "srt://127.0.0.1:9000", "grid.png")
		s.Image("A frame of the grid feed, srt://127.0.0.1:9000", "grid.png")
		s.Say("Here is a frame of the grid feed, read the way O B S reads it: Bunny's video, and a name card for the browser.")
		feedFrame(s, "srt://127.0.0.1:9001", "bunny.png")
		s.Image("A frame of Bunny's feed, srt://127.0.0.1:9001", "bunny.png")
		s.Say("And Bunny's own feed. Each feed only uses the processor while O B S is reading it.")

		s.Step(steps, "Stop, and other networks")
		s.Meeting("tut-feeds")
		s.Say("Control C stops the container. Docker stop does the same: the tool leaves the meeting and exits cleanly.")
		s.Ctrl("c")
		s.WaitFor("$ ", 20*time.Second)
		s.Card("Docker without the host network", codeCard("On Docker's bridge network",
			"Publish the feed ports, and put the host's address in the feed URLs:",
			"$ docker run --rm -p 9000-9016:9000-9016/udp "+image+" \\\n  join --url … --obs --obs-grid --obs-url-host 192.168.1.20",
			"Recording in Docker: mount a directory, and run as yourself so the files are yours:",
			"$ docker run --rm --network host --user \"$(id -u):$(id -g)\" -v \"$PWD/rec:/rec\" \\\n  "+image+" join --url … --record /rec",
		))
		s.Say("On Docker's bridge network, publish the feed ports, and use dash dash obs url host so the addresses name the host, not the container.")
		s.Say("To record in Docker, mount a directory and run as yourself, so that the files belong to you.")
		s.Card("", endCard("All tutorials: publish, record, the WHIP gateway, and SRT feeds."))
		s.Say("That's the end of this series. The README on GitHub has every option.")
		s.ClearCaption()
		s.Pause(time.Second)
	},
}

// feedFrame saves a clean frame of a feed: its second keyframe, as the
// encoder starts when a reader connects and the first is still empty.
func feedFrame(s *Stage, url, file string) {
	s.Off("ffmpeg", "-v", "error", "-y", "-skip_frame", "nokey", "-i", url,
		"-vf", "select=gte(n\\,1)", "-frames:v", "1", file)
}
