package clip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"goalclip/internal/buffer"
	"goalclip/internal/signals"
)

type Meta struct {
	Source     string           `json:"source"`
	File       string           `json:"file"`
	EventTime  float64          `json:"event_time"`
	DetectedAt float64          `json:"detected_at"`
	Confidence float64          `json:"confidence"`
	Home       int              `json:"home"`
	Away       int              `json:"away"`
	Reason     string           `json:"reason"`
	DuringGap  bool             `json:"during_gap"`
	ClipStart  float64          `json:"clip_start"`
	ClipEnd    float64          `json:"clip_end"`
	Trimmed    bool             `json:"trimmed"`
	Reencoded  bool             `json:"reencoded"`
	Signals    []signals.Signal `json:"signals"`

	Review        bool   `json:"review,omitempty"`
	Retracted     bool   `json:"retracted,omitempty"`
	RetractReason string `json:"retract_reason,omitempty"`
}

func WriteMeta(clipPath string, m Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(strings.TrimSuffix(clipPath, ".mp4")+".json", b, 0o644)
}

func MarkRetracted(clipPath, reason string) (string, error) {
	base := strings.TrimSuffix(clipPath, ".mp4")
	newPath := base + "_retracted.mp4"
	if err := os.Rename(clipPath, newPath); err != nil {
		return clipPath, err
	}
	oldMeta, newMeta := base+".json", base+"_retracted.json"
	b, err := os.ReadFile(oldMeta)
	if err != nil {
		return newPath, nil
	}
	var m Meta
	if json.Unmarshal(b, &m) == nil {
		m.Retracted = true
		m.RetractReason = reason
		m.File = filepath.Base(newPath)
		if out, err := json.MarshalIndent(m, "", "  "); err == nil {
			b = out
		}
	}
	if err := os.WriteFile(newMeta, b, 0o644); err != nil {
		return newPath, err
	}
	os.Remove(oldMeta)
	return newPath, nil
}

func run(ctx context.Context, ffmpeg string, args []string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func secs(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

var encodeArgs = []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-c:a", "aac", "-b:a", "160k"}

func CutBuffered(ctx context.Context, ffmpeg string, sel buffer.Selection, outPath string, reencode bool) (start, end float64, err error) {
	if len(sel.Segs) == 0 {
		return 0, 0, errors.New("empty selection")
	}
	list, err := os.CreateTemp("", "clipper-concat-*.txt")
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(list.Name())
	for _, s := range sel.Segs {
		fmt.Fprintf(list, "file '%s'\n", strings.ReplaceAll(s.Path, "'", `'\''`))
	}
	if err := list.Close(); err != nil {
		return 0, 0, err
	}

	args := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-i", list.Name()}
	if reencode {
		off := sel.Start - sel.Segs[0].Start
		args = append(args, "-ss", secs(off), "-t", secs(sel.End-sel.Start))
		args = append(args, encodeArgs...)
		start, end = sel.Start, sel.End
	} else {
		args = append(args, "-c", "copy")
		start, end = sel.Segs[0].Start, sel.Segs[len(sel.Segs)-1].End
	}
	args = append(args, "-movflags", "+faststart", outPath)
	return start, end, run(ctx, ffmpeg, args)
}

func CutFile(ctx context.Context, ffmpeg, input string, start, dur float64, outPath string, reencode bool) error {
	if start < 0 {
		start = 0
	}
	args := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-ss", secs(start), "-i", input, "-t", secs(dur)}
	if reencode {
		args = append(args, encodeArgs...)
	} else {
		args = append(args, "-c", "copy")
	}
	args = append(args, "-movflags", "+faststart", outPath)
	return run(ctx, ffmpeg, args)
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(name string) string {
	name = strings.Trim(unsafeChars.ReplaceAllString(name, "_"), "_")
	if name == "" {
		return "match"
	}
	return name
}

func RunFolder(name string, t time.Time) string {
	return t.Format("2006-01-02_150405") + "_" + sanitize(name)
}

func BaseName(name string, eventTime float64, home, away int) string {
	s := int(eventTime)
	score := "audio"
	if home >= 0 && away >= 0 {
		score = fmt.Sprintf("%d-%d", home, away)
	}
	return fmt.Sprintf("%s_%03dm%02ds_%s", sanitize(name), s/60, s%60, score)
}
