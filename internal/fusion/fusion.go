package fusion

import (
	"fmt"
	"math"

	"goalclip/internal/config"
	"goalclip/internal/signals"
)

type Goal struct {
	ID         int              `json:"id"`
	EventTime  float64          `json:"event_time"`
	DetectedAt float64          `json:"detected_at"`
	Confidence float64          `json:"confidence"`
	Home       int              `json:"home"`
	Away       int              `json:"away"`
	DuringGap  bool             `json:"during_gap"`
	Review     bool             `json:"review,omitempty"`
	Reason     string           `json:"reason"`
	ClipStart  float64          `json:"clip_start"`
	ClipEnd    float64          `json:"clip_end"`
	Signals    []signals.Signal `json:"signals"`
}

type Hooks struct {
	Goal    func(Goal)
	Event   func(kind string, t float64, msg string)
	Retract func(g Goal, reversal signals.Signal)
}

type candidate struct {
	sig       signals.Signal
	deadline  float64
	audioOnly bool
	review    bool
}

type Engine struct {
	cfg       config.Fusion
	clip      config.Clip
	scoreMode bool
	hooks     Hooks

	audio         []signals.Signal
	confirmed     []Goal
	nextID        int
	pending       []candidate
	cooldownUntil float64
	now           float64
	state         string

	reviewActive bool
	lastReview   float64
	scoreTimes   []float64
	home, away   int
}

func New(cfg config.Fusion, clip config.Clip, scoreMode bool, hooks Hooks) *Engine {
	return &Engine{cfg: cfg, clip: clip, scoreMode: scoreMode, hooks: hooks, state: "idle", lastReview: -1e9, home: -1, away: -1}
}

func (e *Engine) Add(s signals.Signal) {
	switch s.Type {
	case signals.AudioSpike:
		e.audio = append(e.audio, s)
		e.pruneAudio(s.Time)
		if !e.scoreMode && s.Confidence >= e.cfg.AudioOnlyMinConfidence &&
			s.Time >= e.cooldownUntil && !e.hasAudioCandidate() {
			e.pending = append(e.pending, candidate{sig: s, deadline: s.Time, audioOnly: true})
			e.event("candidate", s.Time, fmt.Sprintf("audio spike (%.2f)", s.Confidence))
		}
		if e.scoreMode && e.reviewActive && e.cfg.ReviewClips &&
			s.Confidence >= e.cfg.ReviewMinConfidence && s.Time-e.lastReview >= e.cfg.Cooldown {
			e.lastReview = s.Time
			e.pending = append(e.pending, candidate{sig: s, deadline: s.Time + e.cfg.ReviewWaitSeconds, review: true})
		}
	case signals.ScoreUp:
		if !e.scoreMode {
			return
		}
		e.scoreTimes = append(e.scoreTimes, s.Time)
		e.home, e.away = s.Home, s.Away
		e.pending = append(e.pending, candidate{sig: s, deadline: s.Time + e.cfg.ConfirmDelay})
		e.event("candidate", s.Time, fmt.Sprintf("score %d-%d -> %d-%d, confirming for %.0fs",
			s.PrevHome, s.PrevAway, s.Home, s.Away, e.cfg.ConfirmDelay))
	case signals.ScoreDown:
		e.home, e.away = s.Home, s.Away
		e.cancel(s)
	}
	e.refreshState()
}

func (e *Engine) Advance(now float64) {
	if now > e.now {
		e.now = now
	}
	e.resolve(false)
	e.pruneConfirmed()
}

func (e *Engine) Flush(now float64) {
	if now > e.now {
		e.now = now
	}
	e.resolve(true)
}

func (e *Engine) State() string { return e.state }

func (e *Engine) SetScoreMode(on bool) { e.scoreMode = on }

func (e *Engine) SetReviewActive(on bool) { e.reviewActive = on }

func (e *Engine) resolve(force bool) {
	var rest []candidate
	for _, c := range e.pending {
		if !force && c.deadline > e.now {
			rest = append(rest, c)
			continue
		}
		if c.review && c.deadline > e.now {
			continue
		}
		e.confirm(c)
	}
	e.pending = rest
	e.refreshState()
}

func (e *Engine) confirm(c candidate) {
	var g Goal
	if c.review {
		for _, t := range e.scoreTimes {
			if t >= c.sig.Time-5 {
				return
			}
		}
		g = Goal{
			EventTime:  c.sig.Time,
			Confidence: c.sig.Confidence,
			Home:       e.home,
			Away:       e.away,
			Review:     true,
			Reason:     "crowd roar but the score did not change: possibly a disallowed goal (offside or VAR), or a near miss",
			Signals:    []signals.Signal{c.sig},
		}
	} else if c.audioOnly {
		if c.sig.Time < e.cooldownUntil {
			return
		}
		g = Goal{
			EventTime:  c.sig.Time,
			Confidence: 0.4 + 0.3*c.sig.Confidence,
			Home:       -1, Away: -1,
			Reason:  "audio spike only (no scoreboard)",
			Signals: []signals.Signal{c.sig},
		}
		e.cooldownUntil = c.sig.Time + e.cfg.Cooldown
	} else {
		sig := c.sig
		g = Goal{Home: sig.Home, Away: sig.Away, Signals: []signals.Signal{sig}}
		best, found := e.bestAudio(sig)
		switch {
		case sig.AfterGap:
			g.DuringGap = true
			g.EventTime = sig.Time
			g.Confidence = 0.6
			g.Reason = "score increased right after a stream gap; the moment itself may be missing"
		case found:
			g.EventTime = best.Time
			g.Confidence = 0.7 + 0.3*best.Confidence
			g.Reason = "score change confirmed by audio spike"
			g.Signals = append(g.Signals, best)
		default:
			g.EventTime = math.Max(0, sig.Time-e.cfg.ScoreLag)
			g.Confidence = 0.7
			g.Reason = "score change (no audio spike nearby)"
		}
	}

	pre := e.clip.PreSeconds
	if g.DuringGap {
		pre = 2
	}
	g.ID = e.nextID
	e.nextID++
	g.DetectedAt = e.now
	g.ClipStart = math.Max(0, g.EventTime-pre)
	g.ClipEnd = g.EventTime + e.clip.PostSeconds
	if !g.Review {
		e.confirmed = append(e.confirmed, g)
	}
	if e.hooks.Goal != nil {
		e.hooks.Goal(g)
	}
}

func (e *Engine) bestAudio(sig signals.Signal) (signals.Signal, bool) {
	target := sig.Time - e.cfg.ScoreLag
	var best signals.Signal
	found := false
	bestDist := math.MaxFloat64
	for _, a := range e.audio {
		if a.Time < sig.Time-e.cfg.AudioWindow || a.Time > sig.Time+5 {
			continue
		}
		if d := math.Abs(a.Time - target); d < bestDist {
			best, bestDist, found = a, d, true
		}
	}
	return best, found
}

func (e *Engine) cancel(down signals.Signal) {
	var rest []candidate
	for _, c := range e.pending {
		if !c.audioOnly && c.sig.Home == down.PrevHome && c.sig.Away == down.PrevAway {
			e.event("cancelled", down.Time, fmt.Sprintf("score fell back to %d-%d: goal cancelled (VAR or misread)",
				down.Home, down.Away))
			continue
		}
		rest = append(rest, c)
	}
	e.pending = rest

	var keep []Goal
	for _, g := range e.confirmed {
		late := down.Time - g.EventTime
		if g.Home == down.PrevHome && g.Away == down.PrevAway && late >= 0 && late <= e.cfg.RetractWindowSeconds {
			e.event("retracted", down.Time, fmt.Sprintf("score fell back to %d-%d after the goal was confirmed",
				down.Home, down.Away))
			if e.hooks.Retract != nil {
				e.hooks.Retract(g, down)
			}
			continue
		}
		keep = append(keep, g)
	}
	e.confirmed = keep
}

func (e *Engine) pruneConfirmed() {
	for len(e.scoreTimes) > 0 && e.now-e.scoreTimes[0] > 600 {
		e.scoreTimes = e.scoreTimes[1:]
	}
	i := 0
	for i < len(e.confirmed) && e.now-e.confirmed[i].EventTime > e.cfg.RetractWindowSeconds {
		i++
	}
	e.confirmed = e.confirmed[i:]
}

func (e *Engine) hasAudioCandidate() bool {
	for _, c := range e.pending {
		if c.audioOnly {
			return true
		}
	}
	return false
}

func (e *Engine) pruneAudio(t float64) {
	i := 0
	for i < len(e.audio) && e.audio[i].Time < t-300 {
		i++
	}
	e.audio = e.audio[i:]
}

func (e *Engine) refreshState() {
	next := "idle"
	switch {
	case len(e.pending) > 0:
		next = "candidate"
	case e.now < e.cooldownUntil:
		next = "cooldown"
	}
	if next != e.state {
		e.event("state", e.now, fmt.Sprintf("%s -> %s", e.state, next))
		e.state = next
	}
}

func (e *Engine) event(kind string, t float64, msg string) {
	if e.hooks.Event != nil {
		e.hooks.Event(kind, t, msg)
	}
}
