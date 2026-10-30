package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"goalclip/internal/audio"
	"goalclip/internal/buffer"
	"goalclip/internal/clip"
	"goalclip/internal/config"
	"goalclip/internal/fusion"
	"goalclip/internal/ingest"
	"goalclip/internal/ocr"
	"goalclip/internal/probe"
	"goalclip/internal/signals"
)

type Options struct {
	Input     string
	Cfg       *config.Config
	OutDir    string
	Name      string
	Live      bool
	Speed     float64
	FFmpeg    string
	Tesseract string
	NoClips   bool
	Reencode  bool
	AudioOnly bool
	Out       io.Writer
	JSON      bool
	Log       *slog.Logger
}

type Result struct {
	Goal      fusion.Goal
	ClipPath  string
	ClipStart float64
	ClipEnd   float64
	Retracted bool
	Err       error
}

type runner struct {
	o   Options
	cfg *config.Config
	log *slog.Logger
	em  *emitter

	buf *buffer.Buffer

	mu       sync.Mutex
	reader   *ocr.Reader
	tracker  *ocr.Tracker
	det      *audio.Detector
	engine   *fusion.Engine
	frozen   bool
	sameN    int
	lastThmb []uint8

	finder      *ocr.Finder
	jobs        chan calibJob
	calibrating bool
	nextSample  float64
	searchSlow  bool
	lastRemind  float64
	lastRead    float64

	interval float64

	clipMu    sync.Mutex
	clipPaths map[int]string
	retracted map[int]string

	clock atomic.Uint64
	goals chan fusion.Goal
}

type calibJob struct {
	t   float64
	img image.Image
}

func Run(ctx context.Context, o Options) ([]Result, error) {
	cfg := o.Cfg
	if cfg == nil {
		cfg = config.Default()
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.FFmpeg == "" {
		o.FFmpeg = "ffmpeg"
	}
	em := &emitter{w: o.Out, asJSON: o.JSON}

	if !o.Live {
		info, err := probe.Run(ctx, probe.FFprobeFor(o.FFmpeg), o.Input)
		switch {
		case errors.Is(err, probe.ErrNoFFprobe):
			o.Log.Debug("ffprobe not found; skipping the input check")
		case err != nil:
			return nil, fmt.Errorf("cannot read the input %q: %w", o.Input, err)
		case info.Video == nil:
			return nil, fmt.Errorf("the input %q has no video track", o.Input)
		case info.Audio == nil && cfg.Audio.Enabled:
			o.Log.Warn("the input has no audio track: crowd-noise detection is switched off")
			c2 := *cfg
			c2.Audio.Enabled = false
			cfg = &c2
		}
	}

	if !o.NoClips {
		o.OutDir = filepath.Join(o.OutDir, clip.RunFolder(o.Name, time.Now()))
	}

	wantOCR := !o.AudioOnly && (cfg.Scoreboard != nil || cfg.Auto.Enabled)
	if wantOCR {
		if err := ocr.CheckTesseract(o.Tesseract); err != nil {
			return nil, err
		}
	}

	if wantOCR && !o.Live && cfg.Scoreboard == nil {
		em.emit("scoreboard", 0, fmt.Sprintf(
			"searching the first %.0f minutes for the scoreboard (pre-match coverage can delay it)", cfg.Auto.SearchSeconds/60), nil)
		det, err := DetectScoreboard(ctx, DetectOptions{
			Input: o.Input, Cfg: cfg, FFmpeg: o.FFmpeg, Tesseract: o.Tesseract, Log: o.Log,
		})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			em.emit("scoreboard", 0, "no scoreboard in the opening part of the video; the search continues while it plays", nil)
			o.Log.Debug("scoreboard search before replay ended", "err", err)
		} else {
			c2 := *cfg
			sb := det.Scoreboard
			c2.Scoreboard = &sb
			cfg = &c2
			announceScoreboard(em, o, det, 0)
		}
	}

	manual := wantOCR && cfg.Scoreboard != nil
	auto := wantOCR && !manual
	scoreMode := manual || auto
	if !scoreMode && !cfg.Audio.Enabled {
		return nil, errors.New("audio-only mode needs audio.enabled")
	}

	segDir, err := os.MkdirTemp("", "clipper-seg-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(segDir)
	if !o.NoClips {
		if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
			return nil, err
		}
		defer os.Remove(o.OutDir)
		em.emit("run", 0, "clips for this run go to "+o.OutDir, map[string]any{"dir": o.OutDir})
	}

	r := &runner{
		o: o, cfg: cfg, log: o.Log, em: em,
		buf:   buffer.New(cfg.Buffer.RetainSeconds),
		goals: make(chan fusion.Goal, 256),

		interval:  cfg.Frame.IntervalSeconds,
		clipPaths: map[int]string{},
		retracted: map[int]string{},
	}
	switch {
	case manual:
		r.reader, err = ocr.NewReader(*cfg.Scoreboard, o.Tesseract)
		if err != nil {
			return nil, err
		}
		r.tracker = ocr.NewTracker(cfg.Scoreboard.StableFrames)
	case auto:
		r.finder = ocr.NewFinder(o.Tesseract, cfg.Frame.Width, cfg.Frame.Height, cfg.Auto.MinPresence, cfg.Auto.MaxSamples)
		r.jobs = make(chan calibJob, 2)
		r.calibrating = true
		r.log.Info("looking for the scoreboard automatically")
	default:
		r.log.Warn("no scoreboard in use: audio-only detection, expect false positives")
	}
	if cfg.Audio.Enabled {
		r.det = audio.New(cfg.Audio, func(s signals.Signal) {
			r.log.Debug("audio spike", "time", s.Time, "confidence", s.Confidence)
			r.engine.Add(s)
		})
	}
	r.engine = fusion.New(cfg.Fusion, cfg.Clip, scoreMode, fusion.Hooks{
		Goal:    r.onGoal,
		Event:   r.onFusionEvent,
		Retract: r.onRetract,
	})

	var calDone chan struct{}
	calStop := func() {}
	if auto {
		var calCtx context.Context
		calCtx, calStop = context.WithCancel(ctx)
		calDone = make(chan struct{})
		go r.calibLoop(calCtx, calDone)
	}

	done := make(chan []Result, 1)
	go r.clipWorker(done)

	ing := ingest.Options{
		Input:         o.Input,
		FFmpeg:        o.FFmpeg,
		SegDir:        segDir,
		SegSeconds:    cfg.Buffer.SegmentSeconds,
		FrameW:        cfg.Frame.Width,
		FrameH:        cfg.Frame.Height,
		FrameInterval: cfg.Frame.IntervalSeconds,
		Audio:         cfg.Audio.Enabled,
		Live:          o.Live,
		StallTimeout:  time.Duration(cfg.Ingest.StallTimeoutSeconds * float64(time.Second)),
		BackoffMax:    time.Duration(cfg.Ingest.BackoffMaxSeconds * float64(time.Second)),
		Log:           o.Log,
	}
	if !o.Live {
		ing.ReadRate = o.Speed
	}

	runErr := ingest.Run(ctx, ing, ingest.Handlers{
		OnFrame:   r.onFrame,
		OnAudio:   r.onAudio,
		OnSegment: r.buf.Add,
		OnSession: r.onSession,
	})

	calStop()
	if calDone != nil {
		<-calDone
	}

	r.mu.Lock()
	r.engine.Flush(r.getClock())
	r.mu.Unlock()
	close(r.goals)
	results := <-done

	r.clipMu.Lock()
	for i := range results {
		id := results[i].Goal.ID
		if _, gone := r.retracted[id]; gone {
			results[i].Retracted = true
			if p, ok := r.clipPaths[id]; ok {
				results[i].ClipPath = p
			}
		}
	}
	r.clipMu.Unlock()

	if errors.Is(runErr, context.Canceled) {
		runErr = nil
	}
	return results, runErr
}

type DetectOptions struct {
	Input     string
	Cfg       *config.Config
	FFmpeg    string
	Tesseract string
	Log       *slog.Logger
}

func DetectScoreboard(ctx context.Context, d DetectOptions) (*ocr.Detection, error) {
	cfg := d.Cfg
	if cfg == nil {
		cfg = config.Default()
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if d.FFmpeg == "" {
		d.FFmpeg = "ffmpeg"
	}
	if err := ocr.CheckTesseract(d.Tesseract); err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "clipper-calib-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	finder := ocr.NewFinder(d.Tesseract, cfg.Frame.Width, cfg.Frame.Height, cfg.Auto.MinPresence, cfg.Auto.MaxSamples)
	every := cfg.Auto.SampleEverySeconds * 2
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var found *ocr.Detection
	next := 0.0
	onFrame := func(t float64, img image.Image) {
		if found != nil || t < next {
			return
		}
		if t > cfg.Auto.SearchSeconds {
			cancel()
			return
		}
		next = t + every
		if err := finder.AddSample(t, img); err != nil {
			log.Warn("scoreboard search: OCR failed", "err", err)
			return
		}
		n := finder.Count()
		if n >= cfg.Auto.MinSamples {
			if det, err := finder.Resolve(); err == nil {
				found = det
				cancel()
				return
			}
		}
	}

	runErr := ingest.Run(sctx, ingest.Options{
		Input:         d.Input,
		FFmpeg:        d.FFmpeg,
		SegDir:        dir,
		SegSeconds:    2,
		FrameW:        cfg.Frame.Width,
		FrameH:        cfg.Frame.Height,
		FrameInterval: cfg.Frame.IntervalSeconds,
		FramesOnly:    true,
		Log:           log,
	}, ingest.Handlers{OnFrame: onFrame})

	if found != nil {
		return found, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return nil, runErr
	}
	return nil, fmt.Errorf("no scoreboard found in the first %.0f minutes: is it visible, written like 1-0 or 1:0, and does the video start with pre-match coverage? Try a later start", cfg.Auto.SearchSeconds/60)
}

func SavePreview(det *ocr.Detection, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, det.Preview)
}

func announceScoreboard(em *emitter, o Options, det *ocr.Detection, t float64) {
	sb := det.Scoreboard
	msg := fmt.Sprintf("scoreboard found at x=%d y=%d w=%d h=%d, reads %d-%d (seen in %.0f%% of samples)",
		sb.X, sb.Y, sb.W, sb.H, det.Score.Home, det.Score.Away, 100*det.Presence)
	extra := map[string]any{"x": sb.X, "y": sb.Y, "w": sb.W, "h": sb.H, "invert": sb.Invert}
	if !o.NoClips && o.OutDir != "" {
		if err := os.MkdirAll(o.OutDir, 0o755); err == nil {
			p := filepath.Join(o.OutDir, "scoreboard-detected.png")
			if err := SavePreview(det, p); err == nil {
				extra["preview"] = p
				msg += "; preview saved to " + p
			}
		}
	}
	em.emit("scoreboard", t, msg, extra)
}

func (r *runner) calibLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-r.jobs:
			r.calibrate(job)
		}
	}
}

func (r *runner) calibrate(job calibJob) {
	if err := r.finder.AddSample(job.t, job.img); err != nil {
		r.log.Warn("scoreboard search: OCR failed", "err", err)
		return
	}
	n := r.finder.Count()
	if n >= r.cfg.Auto.MinSamples {
		det, err := r.finder.Resolve()
		if err == nil {
			r.finder.Reset()
			r.install(det, job.t)
			return
		}
		r.log.Debug("scoreboard not found yet", "samples", n, "err", err)
	}
	if n >= r.cfg.Auto.MaxSamples {
		r.remind(job.t)
	}
}

func (r *runner) install(det *ocr.Detection, t float64) {
	rd, err := ocr.NewReader(det.Scoreboard, r.o.Tesseract)
	if err != nil {
		r.log.Warn("scoreboard reader failed", "err", err)
		return
	}
	r.mu.Lock()
	r.reader = rd
	if r.tracker == nil {
		r.tracker = ocr.NewTracker(det.Scoreboard.StableFrames)
	}
	r.calibrating = false
	r.searchSlow = false
	r.lastRemind = 0
	r.lastRead = t
	r.engine.SetScoreMode(true)
	r.mu.Unlock()
	announceScoreboard(r.em, r.o, det, t)
}

func (r *runner) remind(t float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.searchSlow = true
	if r.reader != nil || (r.lastRemind != 0 && t-r.lastRemind < r.cfg.Auto.RemindEverySeconds) {
		return
	}
	r.lastRemind = t
	switch {
	case r.cfg.Auto.AudioFallback && r.cfg.Audio.Enabled:
		r.engine.SetScoreMode(false)
		r.em.emit("scoreboard", t, "still looking for the scoreboard; meanwhile goals are detected from crowd noise alone, which is less reliable", nil)
	default:
		r.em.emit("scoreboard", t, "still looking for the scoreboard (pre-match coverage often has none); goals cannot be detected from the score until it appears. Use --audio-only to detect from crowd noise alone", nil)
	}
}

func (r *runner) getClock() float64 { return math.Float64frombits(r.clock.Load()) }

func (r *runner) setClock(t float64) {
	if t > r.getClock() {
		r.clock.Store(math.Float64bits(t))
	}
}

func (r *runner) onSession(session int, offset float64, resumed bool) {
	if !resumed {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.det != nil {
		r.det.Reset()
	}
	if r.tracker != nil {
		r.tracker.Gap(offset)
	}
	r.frozen, r.sameN, r.lastThmb = false, 0, nil
	r.em.emit("stall", offset, "stream restarted: detection resumes, clips will not span the gap", nil)
}

func (r *runner) onFrame(t float64, img image.Image) {
	thumb := thumbnail(img)

	r.mu.Lock()
	frozen := r.updateFreeze(t, thumb)
	reader := r.reader
	sampleNow := r.calibrating && !frozen && t >= r.nextSample
	if sampleNow {
		step := r.cfg.Auto.SampleEverySeconds
		if r.searchSlow {
			step *= 4
		}
		r.nextSample = t + step
	}
	r.mu.Unlock()

	if sampleNow {
		select {
		case r.jobs <- calibJob{t: t, img: img}:
		default:
		}
	}

	var (
		score ocr.Score
		ok    bool
	)
	if reader != nil && !frozen {
		s, good, err := reader.Read(img)
		if err != nil {
			r.log.Warn("scoreboard read failed", "err", err)
		} else {
			score, ok = s, good
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.setClock(t)
	if r.tracker != nil && reader != nil && !frozen {
		if ok {
			r.lastRead = t
			for _, sg := range r.tracker.Observe(t, score, true) {
				r.handleScoreSignal(sg)
			}
			r.engine.SetReviewActive(r.tracker.Ready())
		} else if r.finder != nil && !r.calibrating && t-r.lastRead > r.cfg.Auto.RelostSeconds {
			r.calibrating = true
			r.nextSample = t
			r.em.emit("scoreboard", t, "score unreadable for a long time: looking for the scoreboard again", nil)
		}
	}
	r.engine.Advance(t)
}

func (r *runner) onAudio(t float64, pcm []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setClock(t)
	if r.frozen || r.det == nil {
		return
	}
	r.det.Feed(t, pcm)
	r.engine.Advance(t)
}

func (r *runner) handleScoreSignal(sg signals.Signal) {
	switch sg.Type {
	case signals.ScoreUp:
		r.em.emit("score", sg.Time, fmt.Sprintf("score %d-%d -> %d-%d", sg.PrevHome, sg.PrevAway, sg.Home, sg.Away),
			map[string]any{"home": sg.Home, "away": sg.Away})
	case signals.ScoreDown:
		r.em.emit("score", sg.Time, fmt.Sprintf("score fell %d-%d -> %d-%d (VAR or misread)", sg.PrevHome, sg.PrevAway, sg.Home, sg.Away),
			map[string]any{"home": sg.Home, "away": sg.Away})
	}
	r.engine.Add(sg)
}

func (r *runner) updateFreeze(t float64, thumb []uint8) bool {
	limit := r.cfg.Ingest.FreezeSeconds
	if limit <= 0 {
		return false
	}
	if r.lastThmb != nil && sameThumb(thumb, r.lastThmb) {
		r.sameN++
	} else {
		r.sameN = 0
	}
	r.lastThmb = thumb
	frozen := float64(r.sameN)*r.interval >= limit
	if frozen && !r.frozen {
		r.frozen = true
		if r.det != nil {
			r.det.Reset()
		}
		r.em.emit("stall", t, "picture frozen: detection paused", nil)
	}
	if !frozen && r.frozen {
		r.frozen = false
		if r.tracker != nil {
			r.tracker.Gap(t)
		}
		r.em.emit("resume", t, "picture moving again: detection resumed", nil)
	}
	return r.frozen
}

func (r *runner) onFusionEvent(kind string, t float64, msg string) {
	switch kind {
	case "state":
		r.log.Debug("fusion state", "time", t, "change", msg)
	case "candidate":
		r.em.emit("candidate", t, "GOAL candidate: "+msg, nil)
	case "cancelled":
		r.em.emit("cancelled", t, msg, nil)
	}
}

func (r *runner) onGoal(g fusion.Goal) {
	if g.Review {
		r.em.emit("review", g.EventTime,
			"REVIEW clip: "+g.Reason,
			map[string]any{"confidence": g.Confidence, "detected_at": g.DetectedAt})
		r.goals <- g
		return
	}
	score := "score unknown"
	if g.Home >= 0 {
		score = fmt.Sprintf("%d-%d", g.Home, g.Away)
	}
	r.em.emit("goal", g.EventTime,
		fmt.Sprintf("GOAL %s confirmed (confidence %.2f): %s", score, g.Confidence, g.Reason),
		map[string]any{"home": g.Home, "away": g.Away, "confidence": g.Confidence,
			"detected_at": g.DetectedAt, "during_gap": g.DuringGap})
	r.goals <- g
}

func (r *runner) clipWorker(done chan<- []Result) {
	var pending []fusion.Goal
	var results []Result
	in := r.goals
	closed := false
	tk := time.NewTicker(300 * time.Millisecond)
	defer tk.Stop()

	for {
		select {
		case g, ok := <-in:
			if !ok {
				closed, in = true, nil
			} else {
				pending = append(pending, g)
			}
		case <-tk.C:
		}

		var rest []fusion.Goal
		for _, g := range pending {
			ready := closed || r.buf.LatestEnd() >= g.ClipEnd || r.getClock() >= g.ClipEnd+30
			if !ready {
				rest = append(rest, g)
				continue
			}
			results = append(results, r.cut(g))
		}
		pending = rest

		if closed && len(pending) == 0 {
			done <- results
			return
		}
	}
}

func (r *runner) cut(g fusion.Goal) Result {
	res := Result{Goal: g, ClipStart: g.ClipStart, ClipEnd: g.ClipEnd}
	if r.o.NoClips {
		return res
	}

	sel, ok := r.buf.Window(g.ClipStart, g.ClipEnd, g.EventTime)
	if !ok {
		res.Err = errors.New("no buffered footage covers this goal")
		r.em.emit("error", g.EventTime, "could not clip: "+res.Err.Error(), nil)
		return res
	}

	reencode := r.o.Reencode || r.cfg.Clip.Reencode
	base := clip.BaseName(r.o.Name, g.EventTime, g.Home, g.Away)
	if g.Review {
		base += "_review"
	}
	path := filepath.Join(r.o.OutDir, base+".mp4")

	start, end, err := clip.CutBuffered(context.Background(), r.o.FFmpeg, sel, path, reencode)
	if err != nil {
		res.Err = err
		r.em.emit("error", g.EventTime, "clip failed: "+err.Error(), nil)
		return res
	}
	res.ClipPath, res.ClipStart, res.ClipEnd = path, start, end

	meta := clip.Meta{
		Source: r.o.Input, File: filepath.Base(path),
		EventTime: g.EventTime, DetectedAt: g.DetectedAt, Confidence: g.Confidence,
		Home: g.Home, Away: g.Away, Reason: g.Reason, DuringGap: g.DuringGap,
		ClipStart: start, ClipEnd: end, Trimmed: sel.Trimmed, Reencoded: reencode,
		Signals: g.Signals, Review: g.Review,
	}
	if err := clip.WriteMeta(path, meta); err != nil {
		r.log.Warn("could not write clip metadata", "err", err)
	}

	r.clipMu.Lock()
	r.clipPaths[g.ID] = path
	if reason, gone := r.retracted[g.ID]; gone {
		if np, err := clip.MarkRetracted(path, reason); err == nil {
			r.clipPaths[g.ID] = np
			res.ClipPath, res.Retracted = np, true
		}
	}
	r.clipMu.Unlock()

	r.em.emit("clip", g.EventTime, "saved "+res.ClipPath, map[string]any{"path": res.ClipPath, "trimmed": sel.Trimmed})
	return res
}

func (r *runner) onRetract(g fusion.Goal, down signals.Signal) {
	reason := fmt.Sprintf("score fell back to %d-%d at %s (VAR or disallowed)", down.Home, down.Away, MMSS(down.Time))

	r.clipMu.Lock()
	r.retracted[g.ID] = reason
	if path, ok := r.clipPaths[g.ID]; ok {
		if np, err := clip.MarkRetracted(path, reason); err != nil {
			r.log.Warn("could not mark the clip as retracted", "err", err)
		} else {
			r.clipPaths[g.ID] = np
		}
	}
	r.clipMu.Unlock()

	r.em.emit("retracted", down.Time,
		fmt.Sprintf("GOAL %d-%d RETRACTED: %s", g.Home, g.Away, reason),
		map[string]any{"home": g.Home, "away": g.Away, "reason": reason})
}

type emitter struct {
	mu     sync.Mutex
	w      io.Writer
	asJSON bool
}

func (e *emitter) emit(kind string, t float64, msg string, extra map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.asJSON {
		m := map[string]any{"event": kind, "stream_time": t, "message": msg}
		for k, v := range extra {
			m[k] = v
		}
		b, err := json.Marshal(m)
		if err == nil {
			fmt.Fprintln(e.w, string(b))
		}
		return
	}
	fmt.Fprintf(e.w, "[%s] %s\n", MMSS(t), msg)
}

func MMSS(t float64) string {
	s := int(t)
	if s < 0 {
		s = 0
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

const thumbW, thumbH = 32, 18

func thumbnail(img image.Image) []uint8 {
	b := img.Bounds()
	bw, bh := b.Dx()/thumbW, b.Dy()/thumbH
	out := make([]uint8, thumbW*thumbH)
	if bw < 1 || bh < 1 {
		return out
	}
	for ty := 0; ty < thumbH; ty++ {
		for tx := 0; tx < thumbW; tx++ {
			sum, n := 0, 0
			for y := 0; y < bh; y += 4 {
				for x := 0; x < bw; x += 4 {
					r, g, bl, _ := img.At(b.Min.X+tx*bw+x, b.Min.Y+ty*bh+y).RGBA()
					sum += int((r + g + bl) / 3 >> 8)
					n++
				}
			}
			out[ty*thumbW+tx] = uint8(sum / n)
		}
	}
	return out
}

func sameThumb(a, b []uint8) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	d := 0
	for i := range a {
		x := int(a[i]) - int(b[i])
		if x < 0 {
			x = -x
		}
		d += x
	}
	return float64(d)/float64(len(a)) < 0.75
}
