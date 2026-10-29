package main

import "time"

var publishTutorial = tutorial{
	name:  "publish",
	title: "Publish a media file into a meeting",
	prepare: func(s *Stage) {
		s.Meeting("tut-publish")
		s.Card("", titleCard("Publish a media file", "Put a video file into a Jitsi meeting as a participant, from the command line."))
		s.Pause(8 * time.Second) // The Viewer joins under the card
	},
	run: func(s *Stage) {
		const steps = 4
		s.Step(steps, "Introduction")
		s.Say("jitsi-media-tools joins a Jitsi Meet room as a participant, without a browser.")
		s.Say("In this tutorial, we publish a video file into a meeting.")

		s.Step(steps, "The meeting")
		s.Meeting("tut-publish")
		s.Say("On the right, a browser is already in the meeting called tut-publish. It is the only participant.")
		n := s.Narrate("In the terminal, we keep the meeting address in a variable.")
		s.Run("R=https://localhost:8443/tut-publish", 5*time.Second)
		n.Wait()

		s.Step(steps, "Join and publish")
		n = s.Narrate("The join command enters the room. Dash dash name is the participant's display name, and dash dash publish file loop plays the file over and over.")
		s.Type("jitsi-media-tools join --url $R --insecure --name Clip --publish file-loop:samples/clip.mp4 --log-level warn")
		n.Wait()
		s.WaitFor("joined room", 20*time.Second)
		s.Say("Dash dash insecure accepts the self-signed certificate of this local test server. You would leave it out for a real one.")
		s.Say("Clip is now in the meeting. Everyone sees its video and hears its sound.")
		s.Pause(3 * time.Second)

		s.Step(steps, "Leave")
		s.Say("The tool stays in the meeting until you press Control C.")
		s.Ctrl("c")
		s.WaitFor("$ ", 15*time.Second)
		s.Say("Clip leaves the meeting at once.")
		s.Pause(2 * time.Second)

		s.Card("Other sources", tableCard("Other ways to publish", "",
			[2]string{"Play once, then leave", code("--publish file:clip.mp4")},
			[2]string{"Loop until Ctrl+C", code("--publish file-loop:clip.mp4")},
			[2]string{"Camera and microphone", code("--publish device:VIDEO,AUDIO") + "<br>" + code("jitsi-media-tools devices") + " lists them"},
			[2]string{"Video size", code("--publish-size 640x360") + " (default 1280x720)"},
			[2]string{"From OBS", "the WHIP gateway, " + code("serve") + ": see its tutorial"},
		))
		s.Say("The same option also plays a file once, or publishes a camera and a microphone. To bring O B S into a meeting, use the WHIP gateway, shown in another tutorial.")
		s.Card("", endCard("Next: record the meeting."))
		s.Say("That's it. Next, we record a meeting.")
		s.ClearCaption()
		s.Pause(time.Second)
	},
}
