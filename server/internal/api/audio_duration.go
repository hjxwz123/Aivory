package api

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// Measure decoded samples on the server. Neither a duration_ms field nor a
// compressed container's claimed duration is an authoritative billing input.
func measuredAudioSeconds(ctx context.Context, audio []byte) (float64, error) {
	if seconds, ok := wavDurationSeconds(audio); ok && seconds > 0 {
		if seconds > audioMaxReportedSeconds {
			return 0, errors.New("audio duration exceeds the supported limit")
		}
		return seconds, nil
	}
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		return 0, errors.New("compressed audio duration verification requires ffmpeg; upload PCM WAV or install ffmpeg on the server")
	}
	file, err := os.CreateTemp("", "aivory-audio-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(audio); err != nil {
		file.Close()
		return 0, err
	}
	if err := file.Close(); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Count decoded PCM samples instead of container timestamps. Reset sample
	// timestamps so forged MP3/MP4 metadata cannot shorten the billable duration.
	cmd := exec.CommandContext(ctx, binary, "-nostdin", "-v", "error", "-threads", "1",
		"-protocol_whitelist", "file,pipe",
		// Reject playlists/concat inputs that could reference other local files.
		"-format_whitelist", "wav,mp3,ogg,flac,aac,mov,matroska,webm,amr,aiff,asf",
		"-i", file.Name(), "-map", "0:a:0",
		"-vn", "-sn", "-dn", "-af", "asetpts=N/SR/TB", "-ac", "1", "-ar", "8000",
		"-t", strconv.Itoa(audioMaxReportedSeconds+1), "-f", "s16le", "-acodec", "pcm_s16le", "pipe:1")
	pcm := audioSampleCounter{limit: int64(audioMaxReportedSeconds+1) * 16000}
	cmd.Stdout = &pcm
	if err := cmd.Run(); err != nil {
		return 0, errors.New("audio duration could not be verified; upload a valid audio file")
	}
	seconds := float64(pcm.bytes) / 16000
	if seconds <= 0 || seconds > audioMaxReportedSeconds {
		return 0, errors.New("audio duration is invalid or exceeds the supported limit")
	}
	return seconds, nil
}

type audioSampleCounter struct {
	bytes int64
	limit int64
}

func (w *audioSampleCounter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.limit-w.bytes {
		return 0, io.ErrShortWrite
	}
	w.bytes += int64(len(data))
	return len(data), nil
}
