package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"goalclip/internal/clip"
	"goalclip/internal/config"
	"goalclip/internal/eval"
	"goalclip/internal/pipeline"
	"goalclip/internal/probe"
)

const usage = `clipper: detect goals in a football stream and save highlight clips

usage:
  clipper watch     <url>  [--config f.json] [--out dir] [--json] [--audio-only] [--reencode]
  clipper replay    <file> [--config f.json] [--out dir] [--speed 4] [--json] [--audio-only] [--reencode]
  clipper check     <file|url>                  (test that an input can be read, and say how to fix it if not)
  clipper calibrate <file|url> [--out dir]      (find the scoreboard and show where it is)
  clipper clip      <file|url> --at 00:42:10 [--pre 20] [--post 15] [--out dir] [--reencode]
  clipper eval      <dir>  [--config f.json] [--speed 0] [--tolerance 25] [--clips]
  clipper labels    <Labels-v2.json> [--half 1]  (SoccerNet labels -> goals.json on stdout)

The scoreboard is found automatically; no config is needed. A config only
tunes settings or pins the scoreboard region by hand.
Clips for each run are saved in their own folder inside --out.
Use replay (not watch) for recorded files and on-demand URLs.

Common flags: --ffmpeg path, --tesseract path, --debug
Press Ctrl-C during watch/replay to stop; pending goals are still clipped.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	var err error
	switch os.Args[1] {
	case "watch":
		err = cmdRun(ctx, os.Args[2:], true)
	case "replay":
		err = cmdRun(ctx, os.Args[2:], false)
	case "check":
		err = cmdCheck(ctx, os.Args[2:])
	case "calibrate":
		err = cmdCalibrate(ctx, os.Args[2:])
	case "clip":
		err = cmdClip(ctx, os.Args[2:])
	case "eval":
		err = cmdEval(ctx, os.Args[2:])
	case "labels":
		err = cmdLabels(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		stop()
		os.Exit(2)
	}
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func newLogger(debug bool) *slog.Logger {
	lvl := slog.LevelInfo
	if debug {
		lvl = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		return config.Default(), nil
	}
	return config.Load(path)
}

func cmdRun(ctx context.Context, args []string, live bool) error {
	name, noun := "replay", "file"
	if live {
		name, noun = "watch", "url"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	cfgPath := fs.String("config", "", "optional config (JSON); without one the scoreboard is found automatically")
	out := fs.String("out", "./clips", "directory for clips")
	asJSON := fs.Bool("json", false, "print one JSON event per line")
	reencode := fs.Bool("reencode", false, "re-encode clips for frame-exact cuts")
	audioOnly := fs.Bool("audio-only", false, "ignore the scoreboard")
	speed := fs.Float64("speed", 1, "replay speed (1 = real time, 0 = as fast as possible)")
	ffmpeg := fs.String("ffmpeg", "ffmpeg", "ffmpeg binary")
	tess := fs.String("tesseract", "tesseract", "tesseract binary")
	label := fs.String("name", "", "name used in clip file names")
	debug := fs.Bool("debug", false, "verbose logs")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: clipper %s <%s> [flags]", name, noun)
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}

	clipName := *label
	if clipName == "" {
		clipName = cfg.Name
	}
	if clipName == "" {
		clipName = deriveName(pos[0])
	}
	if live && *speed != 1 {
		fmt.Fprintln(os.Stderr, "note: --speed only applies to replay; live streams run in real time")
	}

	_, err = pipeline.Run(ctx, pipeline.Options{
		Input:     pos[0],
		Cfg:       cfg,
		OutDir:    *out,
		Name:      clipName,
		Live:      live,
		Speed:     *speed,
		FFmpeg:    *ffmpeg,
		Tesseract: *tess,
		Reencode:  *reencode,
		AudioOnly: *audioOnly,
		Out:       os.Stdout,
		JSON:      *asJSON,
		Log:       newLogger(*debug),
	})
	return err
}

func deriveName(input string) string {
	if strings.Contains(input, "://") {
		return "stream"
	}
	base := filepath.Base(input)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func cmdCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	ffmpeg := fs.String("ffmpeg", "ffmpeg", "ffmpeg binary (ffprobe is looked up next to it)")
	ffprobe := fs.String("ffprobe", "", "ffprobe binary")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: clipper check <file|url> [flags]")
	}
	probePath := *ffprobe
	if probePath == "" {
		probePath = probe.FFprobeFor(*ffmpeg)
	}

	info, err := probe.Run(ctx, probePath, pos[0])
	if errors.Is(err, probe.ErrNoFFprobe) {
		return fmt.Errorf("ffprobe was not found; it ships with ffmpeg, so install ffmpeg fully or pass --ffprobe")
	}
	if err != nil {
		return err
	}

	fmt.Println("Input: ", pos[0])
	fmt.Println("Format:", info.Format)
	if info.Video != nil {
		fmt.Printf("Video:  %s, %dx%d\n", info.Video.Codec, info.Video.Width, info.Video.Height)
	} else {
		fmt.Println("Video:  none")
	}
	if info.Audio != nil {
		fmt.Printf("Audio:  %s, %d Hz\n", info.Audio.Codec, info.Audio.SampleRate)
	} else {
		fmt.Println("Audio:  none (crowd-noise detection will be switched off)")
	}

	if info.Video == nil {
		return fmt.Errorf("this input has no video track, so there is nothing to analyse")
	}
	if info.Duration > 0 {
		fmt.Printf("Length: %s (recorded, so use `clipper replay`)\n", hms(info.Duration))
	} else {
		fmt.Println("Length: unknown (looks live, so use `clipper watch`)")
	}
	fmt.Println("\nOK: this input can be read.")
	return nil
}

func hms(sec float64) string {
	s := int(sec)
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
}

func cmdCalibrate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "optional config (JSON)")
	out := fs.String("out", "./clips", "where to save scoreboard-detected.png")
	ffmpeg := fs.String("ffmpeg", "ffmpeg", "ffmpeg binary")
	tess := fs.String("tesseract", "tesseract", "tesseract binary")
	timeout := fs.Duration("timeout", 3*time.Minute, "give up after this long (matters for live URLs)")
	debug := fs.Bool("debug", false, "verbose logs")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: clipper calibrate <file|url> [flags]")
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	det, err := pipeline.DetectScoreboard(ctx, pipeline.DetectOptions{
		Input: pos[0], Cfg: cfg, FFmpeg: *ffmpeg, Tesseract: *tess, Log: newLogger(*debug),
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	preview := filepath.Join(*out, "scoreboard-detected.png")
	if err := pipeline.SavePreview(det, preview); err != nil {
		return err
	}
	fmt.Printf("Found the scoreboard. It currently reads %d-%d, and was seen in %.0f%% of samples.\n",
		det.Score.Home, det.Score.Away, 100*det.Presence)
	fmt.Printf("Check the red box in %s\n\n", preview)
	fmt.Println("Detection is automatic. To pin it by hand instead, put this in a config file:")
	snippet, err := json.MarshalIndent(map[string]any{"scoreboard": det.Scoreboard}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(snippet))
	return nil
}

func cmdClip(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("clip", flag.ContinueOnError)
	at := fs.String("at", "", "moment of the goal: HH:MM:SS, MM:SS or seconds")
	pre := fs.Float64("pre", 20, "seconds before --at")
	post := fs.Float64("post", 15, "seconds after --at")
	out := fs.String("out", "./clips", "directory for the clip")
	reencode := fs.Bool("reencode", false, "re-encode for a frame-exact cut")
	ffmpeg := fs.String("ffmpeg", "ffmpeg", "ffmpeg binary")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *at == "" {
		return fmt.Errorf("usage: clipper clip <file|url> --at 00:42:10 [flags]")
	}
	t, err := parseTimestamp(*at)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	name := clip.BaseName(deriveName(pos[0]), t, -1, -1)
	path := filepath.Join(*out, name+".mp4")
	start := t - *pre
	if err := clip.CutFile(ctx, *ffmpeg, pos[0], start, *pre+*post, path, *reencode); err != nil {
		return err
	}
	fmt.Println("saved", path)
	return nil
}

func parseTimestamp(s string) (float64, error) {
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("bad timestamp %q", s)
	}
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 {
			return 0, fmt.Errorf("bad timestamp %q", s)
		}
		total = total*60 + v
	}
	return total, nil
}

func cmdEval(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "shared broadcast config (JSON)")
	speed := fs.Float64("speed", 0, "replay speed (0 = as fast as possible)")
	tol := fs.Float64("tolerance", 25, "seconds either side that still counts as the same goal")
	clips := fs.Bool("clips", false, "also cut real clips (slower)")
	out := fs.String("out", "./eval-clips", "where clips go with --clips")
	ffmpeg := fs.String("ffmpeg", "ffmpeg", "ffmpeg binary")
	tess := fs.String("tesseract", "tesseract", "tesseract binary")
	debug := fs.Bool("debug", false, "verbose logs")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: clipper eval <dir> [flags]")
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	rep, err := eval.Run(ctx, eval.Options{
		Dir: pos[0], Cfg: cfg, Speed: *speed, Tolerance: *tol, Clips: *clips,
		OutDir: *out, FFmpeg: *ffmpeg, Tesseract: *tess,
		Log: newLogger(*debug), Out: os.Stdout,
	})
	if err != nil {
		return err
	}
	rep.Print(os.Stdout)
	if !rep.MeetsTargets() {
		return fmt.Errorf("below target")
	}
	return nil
}

func cmdLabels(args []string) error {
	fs := flag.NewFlagSet("labels", flag.ContinueOnError)
	half := fs.Int("half", 1, "half to extract (1 or 2)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: clipper labels <Labels-v2.json> [--half 1]")
	}
	b, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	var in struct {
		Annotations []struct {
			GameTime string `json:"gameTime"`
			Label    string `json:"label"`
			Position string `json:"position"`
		} `json:"annotations"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	type goal struct {
		Time float64 `json:"time"`
	}
	goals := []goal{}
	prefix := fmt.Sprintf("%d - ", *half)
	for _, a := range in.Annotations {
		if a.Label != "Goal" || !strings.HasPrefix(a.GameTime, prefix) {
			continue
		}
		ms, err := strconv.ParseFloat(a.Position, 64)
		if err != nil {
			continue
		}
		goals = append(goals, goal{Time: ms / 1000})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"goals": goals})
}
