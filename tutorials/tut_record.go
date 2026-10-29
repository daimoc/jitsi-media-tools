package main

import (
	"os"
	"path/filepath"
	"time"
)

var recordTutorial = tutorial{
	name:  "record",
	title: "Record a meeting",
	prepare: func(s *Stage) {
		s.Background("clip", "jitsi-media-tools", "join", "--url", s.cfg.jitsi+"/tut-record", "--insecure",
			"--name", "Clip", "--publish", "file-loop:samples/clip.mp4")
		s.Meeting("tut-record")
		s.Card("", titleCard("Record a meeting", "One file per participant, or one mixed file of the whole room."))
		s.Pause(10 * time.Second)
	},
	run: func(s *Stage) {
		const steps = 4
		s.Step(steps, "Introduction")
		s.Say("jitsi-media-tools can record a Jitsi meeting, without a browser.")
		s.Say("It makes one file per participant, or one mixed file of the whole room.")

		s.Step(steps, "One file per participant")
		s.Meeting("tut-record")
		s.Say("In the meeting tut-record, a participant called Clip is publishing a video. The browser on the right is muted.")
		s.Run("R=https://localhost:8443/tut-record", 5*time.Second)
		n := s.Narrate("Dash dash record names the directory for the files. By default, each participant who sends media gets their own file.")
		s.Type("jitsi-media-tools join --url $R --insecure --name Recorder --record rec --log-level warn")
		n.Wait()
		s.WaitFor("record ", 20*time.Second)
		s.Say("The tool prints each file as it starts. Clip gets one. The browser sends nothing, so it gets none.")
		s.Pause(4 * time.Second)
		s.Say("Control C stops the recording and closes the files properly.")
		s.Ctrl("c")
		s.WaitFor("$ ", 15*time.Second)
		s.Run("ls rec", 5*time.Second)
		n = s.Narrate("Each file is Matroska, with H.264 video and AAC audio, which any player opens.")
		s.Run("ffprobe -v error -show_entries stream=codec_name,width,height -of compact rec/*.mkv", 10*time.Second)
		n.Wait()
		playable(s, "rec", "clip-file.mp4")
		s.Video("The file of Clip, played back", "clip-file.mp4")
		s.Say("On the right, Clip's recording plays back.")
		s.Pause(2 * time.Second)

		s.Step(steps, "One mixed file")
		s.Meeting("tut-record")
		s.Off("sh", "-c", "mkdir -p old && mv rec/* old/")
		n = s.Narrate("With dash dash record mode mixed, the tool records a single file: the room as a tile grid, with everyone's audio mixed.")
		s.Type("jitsi-media-tools join --url $R --insecure --name Recorder --record rec --record-mode mixed --log-level warn")
		n.Wait()
		s.WaitFor("record ", 20*time.Second)
		s.Pause(6 * time.Second)
		s.Ctrl("c")
		s.WaitFor("$ ", 15*time.Second)
		s.Run("ls rec", 5*time.Second)
		playable(s, "rec", "mixed-file.mp4")
		s.Video("The mixed file, played back", "mixed-file.mp4")
		s.Say("The mixed file shows every participant in a grid: Clip's video, and a name card for the browser, which has no camera.")
		s.Pause(2 * time.Second)

		s.Step(steps, "Summary")
		s.Card("Recording options", tableCard("Recording", "",
			[2]string{"Directory", code("--record DIR")},
			[2]string{"One file per participant", code("--record-mode per-participant") + " (default)"},
			[2]string{"One grid file, mixed audio", code("--record-mode mixed")},
			[2]string{"Files", "Matroska (.mkv): H.264 and AAC"},
			[2]string{"Stop", "Ctrl+C, or " + code("docker stop") + " in a container: the files are complete"},
		))
		s.Say("Files are complete whether you stop with Control C, or with docker stop when the tool runs in a container.")
		s.Card("", endCard("Next: stream OBS into a meeting with the WHIP gateway."))
		s.Say("Next, we stream O B S into a meeting.")
		s.ClearCaption()
		s.Pause(time.Second)
	},
}

// playable remuxes the newest .mkv of dir into a .mp4 the stage can play.
func playable(s *Stage, dir, out string) {
	files, _ := filepath.Glob(filepath.Join(s.dir, dir, "*.mkv"))
	var newest string
	var when time.Time
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(when) {
			newest, when = f, st.ModTime()
		}
	}
	if newest == "" {
		panic("no recording in " + dir)
	}
	s.Off(s.cfg.ffmpeg, "-v", "error", "-y", "-i", newest, "-c", "copy", "-movflags", "+faststart", out)
}
