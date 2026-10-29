package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// tts says text with Piper, caching each line's audio by its text.
type tts struct {
	piper, model, cache string
}

func (t tts) say(text string) (string, time.Duration, error) {
	sum := sha256.Sum256([]byte(t.model + "\x00" + text))
	wav := filepath.Join(t.cache, hex.EncodeToString(sum[:8])+".wav")
	if _, err := os.Stat(wav); err != nil {
		c := exec.Command(t.piper, "--model", t.model, "--output_file", wav, "--sentence_silence", "0.25")
		c.Stdin = strings.NewReader(text)
		if out, err := c.CombinedOutput(); err != nil {
			os.Remove(wav)
			return "", 0, fmt.Errorf("piper: %v\n%s", err, out)
		}
	}
	d, err := wavLength(wav)
	return wav, d, err
}

// wavLength reads a PCM WAV file's length from its header.
func wavLength(path string) (time.Duration, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0, errors.New(path + ": not a WAV file")
	}
	var byteRate uint32
	for i := 12; i+8 <= len(b); {
		id, size := string(b[i:i+4]), int(binary.LittleEndian.Uint32(b[i+4:i+8]))
		switch id {
		case "fmt ":
			byteRate = binary.LittleEndian.Uint32(b[i+16 : i+20])
		case "data":
			if byteRate == 0 {
				return 0, errors.New(path + ": data before fmt")
			}
			if i+8+size > len(b) {
				size = len(b) - i - 8
			}
			return time.Duration(float64(size) / float64(byteRate) * float64(time.Second)), nil
		}
		i += 8 + size + size%2
	}
	return 0, errors.New(path + ": no data chunk")
}

// finish puts the narration on the video: out.mp4 with an AAC track, and
// out.srt with the captions.
func finish(ffmpeg, video, out string, length time.Duration, cues []cue) error {
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", video}
	var filter strings.Builder
	n := 0
	for _, c := range cues {
		if c.wav == "" {
			continue
		}
		args = append(args, "-i", c.wav)
		n++
		ms := c.start.Milliseconds()
		fmt.Fprintf(&filter, "[%d:a]aresample=48000,adelay=%d:all=1[a%d];", n, ms, n)
	}
	if n == 0 {
		return errors.New("no narration")
	}
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&filter, "[a%d]", i)
	}
	fmt.Fprintf(&filter, "amix=inputs=%d:normalize=0:duration=longest,volume=-3dB,apad,atrim=0:%.3f[aout]", n, length.Seconds())
	args = append(args, "-filter_complex", filter.String(), "-map", "0:v", "-map", "[aout]",
		"-c:v", "copy", "-c:a", "aac", "-b:a", "128k", "-ac", "1", "-movflags", "+faststart", out)
	if b, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %v\n%s", err, b)
	}
	return os.WriteFile(strings.TrimSuffix(out, filepath.Ext(out))+".srt", []byte(srt(cues)), 0o644)
}

func srt(cues []cue) string {
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTime(c.start), srtTime(c.end), c.text)
	}
	return b.String()
}

func srtTime(d time.Duration) string {
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
