package probe

import (
	"strings"
	"testing"
)

func TestDiagnoseRealMessages(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"[matroska,webm @ 0x55] EBML header parsing failed\nfake.mkv: Invalid data found when processing input", "Matroska"},
		{"[mov,mp4,m4a,3gp,3g2,mj2 @ 0x56] moov atom not found\ntrunc.mp4: Invalid data found when processing input", "incomplete"},
		{"page.html: Invalid data found when processing input", "web page"},
		{"nope.mp4: No such file or directory", "not found"},
		{"[tcp @ 0x55] Connection to tcp://127.0.0.1:1 failed: Connection refused", "listening"},
		{"[tcp @ 0x55] Failed to resolve hostname nonexistent.invalid: Name or service not known", "host name"},
		{"Stream map '0:a:0' matches no streams.", "no audio"},
		{"HTTP error 403 Forbidden", "403"},
		{"Server returned 404 Not Found", "404"},
		{"Protocol not found", "protocol"},
	}
	for _, c := range cases {
		got := Diagnose(c.msg)
		if !strings.Contains(got, c.want) {
			t.Errorf("Diagnose(%q) = %q, want it to mention %q", c.msg, got, c.want)
		}
	}
}

func TestDiagnoseUnknownMessageGivesNoHint(t *testing.T) {
	if got := Diagnose("something entirely unexpected"); got != "" {
		t.Errorf("want no hint, got %q", got)
	}
}

func TestParseInfo(t *testing.T) {
	data := []byte(`{"streams":[
		{"codec_name":"h264","codec_type":"video","width":1280,"height":720},
		{"codec_name":"aac","codec_type":"audio","sample_rate":"44100"}],
		"format":{"format_name":"mov,mp4","duration":"180.000000"}}`)
	info, err := parseInfo(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.Video == nil || info.Video.Width != 1280 || info.Video.Codec != "h264" {
		t.Errorf("video %+v", info.Video)
	}
	if info.Audio == nil || info.Audio.SampleRate != 44100 {
		t.Errorf("audio %+v", info.Audio)
	}
	if info.Duration != 180 {
		t.Errorf("duration %v", info.Duration)
	}
}

func TestLiveStreamHasNoDuration(t *testing.T) {
	info, err := parseInfo([]byte(`{"streams":[{"codec_type":"video","codec_name":"h264","width":1,"height":1}],"format":{"format_name":"flv","duration":"N/A"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if info.Duration != 0 || info.Audio != nil {
		t.Errorf("unexpected %+v", info)
	}
}

func TestFFprobeFor(t *testing.T) {
	cases := map[string]string{
		"":                       "ffprobe",
		"ffmpeg":                 "ffprobe",
		"/opt/ff/bin/ffmpeg":     "/opt/ff/bin/ffprobe",
		"/opt/ff/bin/ffmpeg.exe": "/opt/ff/bin/ffprobe.exe",
		"/usr/bin/custom":        "ffprobe",
	}
	for in, want := range cases {
		if got := FFprobeFor(in); got != want {
			t.Errorf("FFprobeFor(%q) = %q, want %q", in, got, want)
		}
	}
}
