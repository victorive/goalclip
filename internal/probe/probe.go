package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrNoFFprobe = errors.New("ffprobe not found")

type Stream struct {
	Codec      string
	Width      int
	Height     int
	SampleRate int
}

type Info struct {
	Format   string
	Duration float64
	Video    *Stream
	Audio    *Stream
}

type Error struct {
	Stderr string
	Hint   string
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if e.Hint != "" {
		msg += "\nhint: " + e.Hint
	}
	return msg
}

func FFprobeFor(ffmpeg string) string {
	if ffmpeg == "" {
		return "ffprobe"
	}
	dir, base := filepath.Split(ffmpeg)
	if strings.HasPrefix(base, "ffmpeg") {
		return filepath.Join(dir, "ffprobe"+strings.TrimPrefix(base, "ffmpeg"))
	}
	return "ffprobe"
}

func Run(ctx context.Context, ffprobe, input string) (*Info, error) {
	if _, err := exec.LookPath(ffprobe); err != nil {
		return nil, ErrNoFFprobe
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error",
		"-show_entries", "format=format_name,duration:stream=codec_type,codec_name,width,height,sample_rate",
		"-of", "json", "-i", input)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := errb.String()
		if ctx.Err() == context.DeadlineExceeded {
			msg = "timed out after 40 seconds waiting for the input: " + msg
		}
		return nil, &Error{Stderr: msg, Hint: Diagnose(msg)}
	}
	return parseInfo(out.Bytes())
}

func parseInfo(data []byte) (*Info, error) {
	var raw struct {
		Format struct {
			Name     string `json:"format_name"`
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Type       string `json:"codec_type"`
			Codec      string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			SampleRate string `json:"sample_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("could not read ffprobe output: %w", err)
	}
	info := &Info{Format: raw.Format.Name}
	info.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	for _, s := range raw.Streams {
		rate, _ := strconv.Atoi(s.SampleRate)
		st := &Stream{Codec: s.Codec, Width: s.Width, Height: s.Height, SampleRate: rate}
		switch {
		case s.Type == "video" && info.Video == nil:
			info.Video = st
		case s.Type == "audio" && info.Audio == nil:
			info.Audio = st
		}
	}
	return info, nil
}

var rules = []struct {
	match []string
	hint  string
}{
	{[]string{"ebml header parsing failed"},
		"ffmpeg expected a Matroska or WebM file but got something else. The link usually returned a web page or an error " +
			"instead of the video (for example an expired, blocked or rate-limited download), or the file is incomplete or damaged. " +
			"Open the link in a browser, try another format of the same video (such as MP4), or download it again."},
	{[]string{"moov atom not found"},
		"The MP4 file is incomplete, often an unfinished download. Download it again."},
	{[]string{"matches no streams"},
		"The input has no audio (or no video) track. Without audio, set \"audio\": {\"enabled\": false} in your config."},
	{[]string{"server returned 401", "http error 401"},
		"The server wants a login (401). Use a link that is public; streams behind a login cannot be read directly."},
	{[]string{"server returned 403", "http error 403"},
		"The server refused access (403). The link may need a login, may have expired, or may block downloads from tools. " +
			"Check that it plays in a browser while signed out."},
	{[]string{"server returned 404", "http error 404"},
		"Nothing exists at this address (404). Check the URL for typos. On archive.org the file name must match exactly, " +
			"with spaces written as %20."},
	{[]string{"server returned 5", "http error 5"},
		"The server reported an error. Try again later."},
	{[]string{"connection refused"},
		"Nothing is listening at that address. For an OBS setup, start MediaMTX first, then start streaming from OBS, " +
			"and check the host, port and stream key."},
	{[]string{"connection timed out", "timed out"},
		"The connection timed out. Check your network and firewall, and that the host and port are reachable."},
	{[]string{"failed to resolve hostname", "name or service not known", "could not resolve"},
		"The host name could not be found. Check the URL for typos and your internet connection."},
	{[]string{"protocol not found"},
		"This ffmpeg build does not support that protocol (SRT is a common case). Run `ffmpeg -protocols` to see what it supports, " +
			"or install a fuller ffmpeg build."},
	{[]string{"ssl handshake", "tls error", "tls handshake"},
		"The secure connection failed. Check the date and time on this computer, and that the address is correct."},
	{[]string{"no such file or directory"},
		"The file was not found. Check the path, and put quotes around paths that contain spaces."},
	{[]string{"permission denied"},
		"Permission was denied reading the file. Check its permissions."},
	{[]string{"invalid data found when processing input"},
		"ffmpeg could not read this as video. The address is probably a web page (for example an archive.org /details/ link " +
			"or a streaming site's watch page) rather than a media file, or the download is damaged. Use the direct link to the video file or stream."},
}

func Diagnose(msg string) string {
	low := strings.ToLower(msg)
	for _, r := range rules {
		for _, m := range r.match {
			if strings.Contains(low, m) {
				return r.hint
			}
		}
	}
	return ""
}
